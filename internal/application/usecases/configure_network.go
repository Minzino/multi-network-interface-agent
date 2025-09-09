package usecases

import (
    "context"
    "fmt"
    "multinic-agent/internal/domain/entities"
    "multinic-agent/internal/domain/errors"
    "multinic-agent/internal/domain/interfaces"
    "multinic-agent/internal/domain/services"
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
    return NewConfigureNetworkUseCaseWithDetector(repo, configurer, rollbacker, naming, fs, osDetector, logger, maxConcurrentTasks, drift, time.Duration(0))
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

    // 2. 워커풀 기반 병렬 처리
    pool := NewWorkerPool[entities.NetworkInterface](maxWorkers, len(allInterfaces))
    stop := pool.Start(ctx, func(pctx context.Context, job entities.NetworkInterface) {
        defer wg.Done()
        // per-interface timeout
        timeout := uc.opTimeout
        if timeout <= 0 { timeout = 30 * time.Second }
        jctx, cancel := context.WithTimeout(pctx, timeout)
        defer cancel()

        if err := uc.processInterfaceWithCheck(jctx, job, osType, &processedCount, &failedCount, &failures, &failuresMu, &results, &resultsMu); err != nil {
            uc.logger.WithError(err).Error("Critical error processing interface")
        }
    })
    for _, iface := range allInterfaces {
        wg.Add(1)
        pool.Submit(iface)
    }
    stop()
    wg.Wait()

    return &ConfigureNetworkOutput{
        ProcessedCount: int(atomic.LoadInt32(&processedCount)),
        FailedCount:    int(atomic.LoadInt32(&failedCount)),
        TotalCount:     len(allInterfaces),
        Failures:       failures,
        Results:        results,
    }, nil
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
