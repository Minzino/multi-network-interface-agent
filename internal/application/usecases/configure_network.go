package usecases

import (
    "context"
    "fmt"
    "multinic-agent/internal/domain/entities"
    "multinic-agent/internal/domain/errors"
    "multinic-agent/internal/domain/interfaces"
    "multinic-agent/internal/domain/services"
    domconst "multinic-agent/internal/domain/constants"
    "multinic-agent/internal/infrastructure/metrics"
    "strings"
    "sync"
    "sync/atomic"
    "time"

    "github.com/sirupsen/logrus"
)

// ConfigureNetworkUseCase는 네트워크 설정을 처리하는 유스케이스입니다
type ConfigureNetworkUseCase struct {
    repository         interfaces.NetworkInterfaceRepository
    configurer         interfaces.NetworkConfigurer
    rollbacker         interfaces.NetworkRollbacker
    namingService      *services.InterfaceNamingService
    fileSystem         interfaces.FileSystem // 파일 시스템 의존성 추가
    osDetector         interfaces.OSDetector
    logger             *logrus.Logger
    maxConcurrentTasks int
    driftDetector      *services.DriftDetector
    // sub usecases
    applier    *ApplyUseCase
    validator  *ValidateUseCase
    processor  *ProcessingUseCase
    // unified per-interface operation timeout
    opTimeout time.Duration
    // retry settings
    maxRetries       int
    backoffMultiplier float64
}

// NewConfigureNetworkUseCase는 새로운 ConfigureNetworkUseCase를 생성합니다
// NewConfigureNetworkUseCase creates a use case with an internally constructed DriftDetector
func NewConfigureNetworkUseCase(
    repo interfaces.NetworkInterfaceRepository,
    configurer interfaces.NetworkConfigurer,
    rollbacker interfaces.NetworkRollbacker,
    naming *services.InterfaceNamingService,
    fs interfaces.FileSystem, // 파일 시스템 의존성 추가
    osDetector interfaces.OSDetector,
    logger *logrus.Logger,
    maxConcurrentTasks int,
) *ConfigureNetworkUseCase {
    // Backward-compatible constructor: build a default DriftDetector
    drift := services.NewDriftDetector(fs, logger, naming)
    return NewConfigureNetworkUseCaseWithDetector(
        repo, configurer, rollbacker, naming, fs, osDetector, logger,
        maxConcurrentTasks, drift, time.Duration(0),
        domconst.DefaultMaxRetries, domconst.DefaultBackoffMultiplier,
    )
}

// NewConfigureNetworkUseCaseWithDetector allows injecting a custom DriftDetector
func NewConfigureNetworkUseCaseWithDetector(
    repo interfaces.NetworkInterfaceRepository,
    configurer interfaces.NetworkConfigurer,
    rollbacker interfaces.NetworkRollbacker,
    naming *services.InterfaceNamingService,
    fs interfaces.FileSystem,
    osDetector interfaces.OSDetector,
    logger *logrus.Logger,
    maxConcurrentTasks int,
    drift *services.DriftDetector,
    opTimeout time.Duration,
    maxRetries int,
    backoffMultiplier float64,
) *ConfigureNetworkUseCase {
    uc := &ConfigureNetworkUseCase{
        repository:         repo,
        configurer:         configurer,
        rollbacker:         rollbacker,
        namingService:      naming,
        fileSystem:         fs,
        osDetector:         osDetector,
        logger:             logger,
        maxConcurrentTasks: maxConcurrentTasks,
        driftDetector:      drift,
        opTimeout:          opTimeout,
        maxRetries:         maxRetries,
        backoffMultiplier:  backoffMultiplier,
    }
    // wire sub usecases
    uc.applier = &ApplyUseCase{parent: uc}
    uc.validator = &ValidateUseCase{parent: uc}
    uc.processor = &ProcessingUseCase{parent: uc, applier: uc.applier, validator: uc.validator}
    return uc
}

// ConfigureNetworkInput은 유스케이스의 입력 파라미터입니다
type ConfigureNetworkInput struct {
	NodeName string
}

// ConfigureNetworkOutput은 유스케이스의 출력 결과입니다
type ConfigureNetworkOutput struct {
    ProcessedCount int
    FailedCount    int
    TotalCount     int
    Failures       []InterfaceFailure
    Results        []InterfaceResult
}

// InterfaceFailure는 실패한 인터페이스에 대한 요약 정보를 담습니다
type InterfaceFailure struct {
	ID        int    `json:"id"`
	MAC       string `json:"mac"`
	Name      string `json:"name"`
	ErrorType string `json:"errorType"`
	Reason    string `json:"reason"`
}

// InterfaceResult는 성공/처리된 인터페이스에 대한 요약 정보를 담습니다
type InterfaceResult struct {
    ID     int    `json:"id"`
    MAC    string `json:"mac"`
    Name   string `json:"name"`
    Status string `json:"status"` // e.g., Configured
}

// Execute는 네트워크 설정 유스케이스를 실행합니다
func (uc *ConfigureNetworkUseCase) Execute(ctx context.Context, input ConfigureNetworkInput) (*ConfigureNetworkOutput, error) {
	// OS 타입 감지
	osType, err := uc.osDetector.DetectOS()
	if err != nil {
		return nil, errors.NewSystemError("failed to detect OS type", err)
	}

	// 1. 해당 노드의 모든 활성 인터페이스 조회 (netplan_success 상태 무관)
	allInterfaces, err := uc.repository.GetAllNodeInterfaces(ctx, input.NodeName)
	if err != nil {
		return nil, errors.NewSystemError("failed to get node interfaces", err)
	}

	uc.logger.WithFields(logrus.Fields{
		"node_name":       input.NodeName,
		"interface_count": len(allInterfaces),
		"os_type":         osType,
	}).Debug("Retrieved interfaces from database")

	// 1-1. 이름 사전 배정: 고유 multinicX 이름을 미리 예약하여 중복 배정/레이스 방지
	if _, err := uc.namingService.ReserveNamesForInterfaces(allInterfaces); err != nil {
		uc.logger.WithError(err).Warn("Failed to reserve names for interfaces - proceeding without preallocation")
	}

	// 병렬 처리를 위한 설정
	maxWorkers := uc.maxConcurrentTasks
	if maxWorkers <= 0 {
		maxWorkers = 1 // 최소 1개는 처리
	}

    var (
        processedCount int32
        failedCount    int32
        wg             sync.WaitGroup
        failuresMu     sync.Mutex
        failures       []InterfaceFailure
        resultsMu      sync.Mutex
        results        []InterfaceResult
    )

    // 2. 워커풀 기반 병렬 처리 (리트라이/메트릭/패닉복구 포함)
    queueSize := len(allInterfaces)
    pool := NewWorkerPool[entities.NetworkInterface](
        maxWorkers,
        queueSize,
        WithPoolName[entities.NetworkInterface]("configure"),
        WithRetryPolicy[entities.NetworkInterface](uc.retryPolicy()),
        WithPanicHandler[entities.NetworkInterface](func(job entities.NetworkInterface, r any) {
            uc.logger.WithField("interface_id", job.ID()).Errorf("panic recovered: %v", r)
        }),
        WithAfterHook[entities.NetworkInterface](func(job entities.NetworkInterface, status string, _ time.Duration, _ int, lastErr error) {
            // 최종 상태에서만 카운팅/결과 집계
            if status == "success" {
                atomic.AddInt32(&processedCount, 1)
                wg.Done()
            } else {
                atomic.AddInt32(&failedCount, 1)
                // 실패 상세 수집 (이 시점에는 이름이 생성되었을 수 있으나, 최소 정보 보장)
                name, _ := uc.namingService.GenerateNextNameForMAC(job.MacAddress())
                // 상태 업데이트: 실패로 마킹
                _ = uc.repository.UpdateInterfaceStatus(context.Background(), job.ID(), entities.StatusFailed)
                failuresMu.Lock()
                failure := InterfaceFailure{
                    ID:        job.ID(),
                    MAC:       job.MacAddress(),
                    Name:      func() string { if name != nil { return name.String() }; return "" }(),
                    ErrorType: func() string { if lastErr != nil { return uc.getErrorType(lastErr) }; return "unknown" }(),
                    Reason:    func() string { if lastErr != nil { return lastErr.Error() }; return "final failure" }(),
                }
                failures = append(failures, failure)
                failuresMu.Unlock()
                wg.Done()
            }
        }),
    )

    stop := pool.StartE(ctx, func(pctx context.Context, job entities.NetworkInterface) error {
        // per-interface timeout
        timeout := uc.opTimeout
        if timeout <= 0 { timeout = 30 * time.Second }
        jctx, cancel := context.WithTimeout(pctx, timeout)
        defer cancel()

        // Preflight Guard: do NOT apply if system check fails (avoid link flap)
        if err := uc.preflightCheck(jctx, job); err != nil {
            // return validation error; after-hook will record failure and no apply attempt is made
            return err
        }

        // 이름/처리 필요성 검사 후 실제 처리. 실패는 에러로 반환하여 워커풀이 재시도 여부를 판단하게 함.
        // 성공 시 결과 집계는 after-hook에서 최종적으로 처리.
        interfaceName, err := uc.namingService.GenerateNextNameForMAC(job.MacAddress())
        if err != nil {
            uc.handleInterfaceError("interface name generation", job.ID(), job.MacAddress(), err)
            return err
        }
        shouldProcess, _ := uc.checkNeedProcessing(jctx, job, *interfaceName, osType)
        if !shouldProcess {
            // 처리할 필요가 없으면 성공으로 간주하고 결과 집계
            resultsMu.Lock()
            results = append(results, InterfaceResult{ID: job.ID(), MAC: job.MacAddress(), Name: interfaceName.String(), Status: "Configured"})
            resultsMu.Unlock()
            return nil
        }

        if err := uc.processor.Process(jctx, job, *interfaceName); err != nil {
            // 오류는 그대로 반환(재시도 판단은 RetryPolicy). 최종 실패 시 after-hook에서 카운팅.
            return err
        }
        // 성공: 결과 수집
        resultsMu.Lock()
        results = append(results, InterfaceResult{ID: job.ID(), MAC: job.MacAddress(), Name: interfaceName.String(), Status: "Configured"})
        resultsMu.Unlock()
        return nil
    })
    for _, iface := range allInterfaces {
        wg.Add(1)
        pool.Submit(iface)
    }
    wg.Wait()
    // 모든 작업의 최종 상태가 완료되었음을 보장한 후 채널을 닫는다
    stop()

    return &ConfigureNetworkOutput{
        ProcessedCount: int(atomic.LoadInt32(&processedCount)),
        FailedCount:    int(atomic.LoadInt32(&failedCount)),
        TotalCount:     len(allInterfaces),
        Failures:       failures,
        Results:        results,
    }, nil
}

// preflightCheck validates system state before any apply to avoid link flaps.
// - Ensures MAC is present on the node
// - Ensures target interface is not UP (dangerous to modify)
func (uc *ConfigureNetworkUseCase) preflightCheck(ctx context.Context, iface entities.NetworkInterface) error {
    // 1) MAC presence
    foundName, err := uc.namingService.FindInterfaceNameByMAC(iface.MacAddress())
    // Block when MAC is not present or lookup failed to ensure safety (avoid apply+rollback link flaps)
    if err != nil || strings.TrimSpace(foundName) == "" {
        return errors.NewValidationError("preflight: MAC not present on system", err)
    }
    // Default: block if interface is UP. Allow existing multinic* during tests/roll-forward scenarios.
    if !strings.HasPrefix(foundName, "multinic") && uc.isInterfaceUp(ctx, foundName) {
        return errors.NewValidationError("preflight: target interface is UP", fmt.Errorf("interface %s is up", foundName))
    }
    return nil
}

// retryPolicy는 도메인 에러 타입에 따라 재시도 여부와 백오프를 결정합니다.
func (uc *ConfigureNetworkUseCase) retryPolicy() RetryPolicy[entities.NetworkInterface] {
    maxRetries := uc.maxRetries
    if maxRetries <= 0 { maxRetries = domconst.DefaultMaxRetries }
    multiplier := uc.backoffMultiplier
    if multiplier <= 0 { multiplier = domconst.DefaultBackoffMultiplier }
    base := 100 * time.Millisecond
    return func(job entities.NetworkInterface, err error, attempt int) (bool, time.Duration) {
        // 재시도 횟수 제한
        if attempt >= maxRetries { return false, 0 }
        // 에러 타입 판별
        var derr *errors.DomainError
        if errors2, ok := any(err).(*errors.DomainError); ok {
            derr = errors2
        }
        if derr == nil { return false, 0 }
        switch derr.Type {
        case errors.ErrorTypeTimeout, errors.ErrorTypeNetwork, errors.ErrorTypeSystem, errors.ErrorTypeResource:
            return true, time.Duration(float64(base) * pow(multiplier, attempt))
        default:
            return false, 0
        }
    }
}

func pow(a float64, n int) float64 {
    p := 1.0
    for i := 0; i < n; i++ { p *= a }
    return p
}

// processInterface는 개별 인터페이스를 처리합니다
func (uc *ConfigureNetworkUseCase) processInterface(ctx context.Context, iface entities.NetworkInterface, interfaceName entities.InterfaceName) error {
    // Keep logging here for continuity; ProcessingUseCase also records metrics and updates status
    uc.logger.WithFields(logrus.Fields{
        "interface_id":   iface.ID(),
        "interface_name": interfaceName.String(),
        "mac_address":    iface.MacAddress(),
    }).Info("Starting interface configuration")

    // Basic entity validation remains here
    if err := iface.Validate(); err != nil {
        metrics.RecordError("validation")
        return errors.NewValidationError("Interface validation failed", err)
    }
    return uc.processor.Process(ctx, iface, interfaceName)
}

// applyConfiguration은 네트워크 설정을 적용하고 실패 시 롤백합니다
func (uc *ConfigureNetworkUseCase) applyConfiguration(ctx context.Context, iface entities.NetworkInterface, interfaceName entities.InterfaceName) error {
	if err := uc.configurer.Configure(ctx, iface, interfaceName); err != nil {
		// 롤백 시도
		if rollbackErr := uc.performRollback(ctx, interfaceName.String(), "configuration"); rollbackErr != nil {
			// 롤백도 실패한 경우 더 심각한 상황
			return errors.NewNetworkError(
				fmt.Sprintf("Failed to apply configuration and rollback also failed: %v", rollbackErr),
				err,
			)
		}
		return errors.NewNetworkError("Failed to apply network configuration", err)
	}
	return nil
}

// validateConfiguration은 네트워크 설정을 검증하고 실패 시 롤백합니다
func (uc *ConfigureNetworkUseCase) validateConfiguration(ctx context.Context, iface entities.NetworkInterface, interfaceName entities.InterfaceName) error {
    // 3-1. MAC 기반 검증: 시스템 전체에서 해당 MAC을 가진 인터페이스 탐색
    foundName, macErr := uc.namingService.FindInterfaceNameByMAC(iface.MacAddress())
    if macErr != nil || strings.TrimSpace(foundName) == "" {
        // 시스템 어디에도 해당 MAC이 없으면 롤백
        if rollbackErr := uc.performRollback(ctx, interfaceName.String(), "validation"); rollbackErr != nil {
            return errors.NewNetworkError(
                fmt.Sprintf("System MAC presence check failed and rollback also failed: %v", rollbackErr),
                macErr,
            )
        }
        return errors.NewNetworkError("System MAC presence check failed", macErr)
    }

    // 3-2. UP 상태 확인: foundName 기준으로 확인
    if !uc.isInterfaceUp(ctx, foundName) {
        if rollbackErr := uc.performRollback(ctx, interfaceName.String(), "validation"); rollbackErr != nil {
            return errors.NewNetworkError(
                fmt.Sprintf("Interface not UP and rollback also failed"),
                fmt.Errorf("interface %s not UP", foundName),
            )
        }
        return errors.NewNetworkError("Interface is not UP after apply", fmt.Errorf("interface %s not UP", foundName))
    }

	// MTU/IPv4는 시스템 환경/외부 요인으로 즉시 반영이 지연될 수 있어, 성공 판정은 MAC/존재/UP 기준으로 제한합니다.
	return nil
}

// performRollback은 롤백을 수행하고 결과를 기록합니다
func (uc *ConfigureNetworkUseCase) performRollback(ctx context.Context, interfaceName string, stage string) error {
	err := uc.rollbacker.Rollback(ctx, interfaceName)
	if err != nil {
		uc.logger.WithFields(logrus.Fields{
			"interface_name": interfaceName,
			"stage":          stage,
			"error":          err,
		}).Error("Rollback failed")
		return err
	}

	uc.logger.WithFields(logrus.Fields{
		"interface_name": interfaceName,
		"stage":          stage,
	}).Info("Rollback completed successfully")
	return nil
}

// processInterfaceWithCheck는 개별 인터페이스를 처리하기 전에 필요성을 검사합니다
func (uc *ConfigureNetworkUseCase) processInterfaceWithCheck(
    ctx context.Context,
    iface entities.NetworkInterface,
    osType interfaces.OSType,
    processedCount, failedCount *int32,
    failures *[]InterfaceFailure,
    failuresMu *sync.Mutex,
    results *[]InterfaceResult,
    resultsMu *sync.Mutex,
) error {
	// 인터페이스 이름 생성 (기존에 할당된 이름이 있다면 재사용)
	interfaceName, err := uc.namingService.GenerateNextNameForMAC(iface.MacAddress())
	if err != nil {
		uc.handleInterfaceError("interface name generation", iface.ID(), iface.MacAddress(), err)
		atomic.AddInt32(failedCount, 1)
		return nil // 다음 인터페이스 처리를 위해 에러 반환하지 않음
	}

	// OS별로 처리 필요성 검사
	shouldProcess, configPath := uc.checkNeedProcessing(ctx, iface, *interfaceName, osType)

	if shouldProcess {
		uc.logger.WithFields(logrus.Fields{
			"interface_id":   iface.ID(),
			"interface_name": interfaceName.String(),
			"mac_address":    iface.MacAddress(),
			"status":         iface.Status,
			"os_type":        osType,
			"config_path":    configPath,
		}).Debug("Processing interface")

		if err := uc.processInterface(ctx, iface, *interfaceName); err != nil {
			uc.handleProcessingError(ctx, iface, *interfaceName, err)
			atomic.AddInt32(failedCount, 1)
			// 실패 상세 수집
			failuresMu.Lock()
			*failures = append(*failures, InterfaceFailure{
				ID:        iface.ID(),
				MAC:       iface.MacAddress(),
				Name:      interfaceName.String(),
				ErrorType: uc.getErrorType(err),
				Reason:    err.Error(),
			})
			failuresMu.Unlock()
        } else {
            atomic.AddInt32(processedCount, 1)
            // 성공 결과 수집 (실제 사용된 이름 기준)
            resultsMu.Lock()
            *results = append(*results, InterfaceResult{ID: iface.ID(), MAC: iface.MacAddress(), Name: interfaceName.String(), Status: "Configured"})
            resultsMu.Unlock()
        }
	}

    return nil
}
