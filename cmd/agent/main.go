package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"multinic-agent/internal/application/polling"
	"multinic-agent/internal/application/usecases"
	"multinic-agent/internal/domain/interfaces"
	"multinic-agent/internal/infrastructure/config"
	"multinic-agent/internal/infrastructure/container"
	"multinic-agent/internal/infrastructure/metrics"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
)

func main() {
	// 로거 초기화
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})

	// LOG_LEVEL 환경 변수 설정
	logLevelStr := os.Getenv("LOG_LEVEL")
	if logLevelStr != "" {
		logLevel, err := logrus.ParseLevel(logLevelStr)
		if err != nil {
			logger.WithError(err).Warnf("Unknown LOG_LEVEL value: %s. Using default Info level.", logLevelStr)
			logger.SetLevel(logrus.InfoLevel) // Fallback to Info
		} else {
			logger.SetLevel(logLevel)
		}
	} else {
		logger.SetLevel(logrus.InfoLevel) // Default to Info if not set
	}

	// 설정 로드
	configLoader := config.NewEnvironmentConfigLoader()
	cfg, err := configLoader.Load()
	if err != nil {
		logger.WithError(err).Fatal("Failed to load configuration")
	}

	// 의존성 주입 컨테이너 생성
	appContainer, err := container.NewContainer(cfg, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create dependency injection container")
	}
	defer func() {
		if err := appContainer.Close(); err != nil {
			logger.WithError(err).Error("Failed to cleanup container")
		}
	}()

	// 애플리케이션 시작
	app := NewApplication(appContainer, logger)
	if err := app.Run(); err != nil {
		logger.WithError(err).Fatal("Failed to run application")
	}
}

// Application은 메인 애플리케이션 구조체입니다
type Application struct {
	container        *container.Container
	logger           *logrus.Logger
	configureUseCase *usecases.ConfigureNetworkUseCase
	deleteUseCase    *usecases.DeleteNetworkUseCase
	healthServer     *http.Server
	osType           interfaces.OSType
}

// NewApplication은 새로운 Application을 생성합니다
func NewApplication(container *container.Container, logger *logrus.Logger) *Application {
	return &Application{
		container:        container,
		logger:           logger,
		configureUseCase: container.GetConfigureNetworkUseCase(),
		deleteUseCase:    container.GetDeleteNetworkUseCase(),
	}
}

// Run은 애플리케이션을 실행합니다
func (a *Application) Run() error {
	cfg := a.container.GetConfig()

	// OS 타입 감지 및 Info 로그 출력
	osDetector := a.container.GetOSDetector()
	osType, err := osDetector.DetectOS()
	if err != nil {
		return fmt.Errorf("failed to detect OS type: %w", err)
	}
	a.osType = osType
	a.logger.WithField("os_type", osType).Info("Operating system detected")

	// 에이전트 정보 메트릭 설정
	hostname, _ := os.Hostname()
	metrics.SetAgentInfo("0.5.0", string(osType), hostname)

	// 헬스체크 서버 시작
	if err := a.startHealthServer(cfg.Health.Port); err != nil {
		return err
	}

	// 컨텍스트 및 시그널 핸들링 설정
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// 폴링 전략 설정
	var strategy polling.Strategy
	if cfg.Agent.Backoff.Enabled {
		// 지수 백오프 전략 사용
		strategy = polling.NewExponentialBackoffStrategy(
			cfg.Agent.PollInterval,        // 기본 간격
			cfg.Agent.Backoff.MaxInterval, // 최대 간격
			cfg.Agent.Backoff.Multiplier,  // 지수 계수
			a.logger,
		)
		a.logger.WithFields(logrus.Fields{
			"base_interval": cfg.Agent.PollInterval,
			"max_interval":  cfg.Agent.Backoff.MaxInterval,
			"multiplier":    cfg.Agent.Backoff.Multiplier,
		}).Info("Exponential backoff polling enabled")
	} else {
		// 고정 간격 폴링 (기존 방식)
		strategy = &fixedIntervalStrategy{interval: cfg.Agent.PollInterval}
		a.logger.WithField("interval", cfg.Agent.PollInterval).Info("Fixed interval polling enabled")
	}

	// RUN_MODE=job: 한 번 처리 후 종료
	if cfg.Agent.RunMode == "job" {
		a.logger.Info("MultiNIC agent started (run mode: job)")
		// optional cleanup action
		action := os.Getenv("AGENT_ACTION")
		if strings.EqualFold(action, "cleanup") {
			// 실행: 삭제 유스케이스만 수행
			deleteInput := usecases.DeleteNetworkInput{NodeName: hostname}
			if _, err := a.deleteUseCase.Execute(ctx, deleteInput); err != nil {
				a.logger.WithError(err).Error("Failed to cleanup network (job mode)")
				a.delayJobExitIfNeeded()
				return err
			}
			a.delayJobExitIfNeeded()
			return nil
		}
		if err := a.processNetworkConfigurations(ctx); err != nil {
			a.logger.WithError(err).Error("Failed to process network configurations (job mode)")
			a.delayJobExitIfNeeded()
			return err
		}
		a.delayJobExitIfNeeded()
		return nil
	}

	// 서비스 모드: 폴링 컨트롤러 시작
	pollingController := polling.NewPollingController(strategy, a.logger)
	a.logger.Info("MultiNIC agent started")
	go func() { <-sigChan; a.logger.Info("Received shutdown signal"); cancel() }()
	return pollingController.Start(ctx, func(ctx context.Context) error {
		if err := a.processNetworkConfigurations(ctx); err != nil {
			a.logger.WithError(err).Error("Failed to process network configurations")
			a.container.GetHealthService().UpdateDBHealth(false, err)
			metrics.SetDBConnectionStatus(false)
			return err
		}
		a.container.GetHealthService().UpdateDBHealth(true, nil)
		metrics.SetDBConnectionStatus(true)
		return nil
	})
}

// startHealthServer는 헬스체크 서버를 시작합니다
func (a *Application) startHealthServer(port string) error {
	healthService := a.container.GetHealthService()

	// HTTP 핸들러 설정
	mux := http.NewServeMux()
	mux.Handle("/", healthService)
	mux.Handle("/metrics", promhttp.Handler())

	a.healthServer = &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	go func() {
		a.logger.WithField("port", port).Info("Health check server started (with /metrics)")
		if err := a.healthServer.ListenAndServe(); err != http.ErrServerClosed {
			a.logger.WithError(err).Error("Health check server failed")
		}
	}()

	return nil
}

// delayJobExitIfNeeded는 Job 모드에서 종료 전 대기 시간을 적용합니다.
func (a *Application) delayJobExitIfNeeded() {
	// JOB_EXIT_DELAY_SECONDS 환경변수 (기본 5초)
	delayStr := os.Getenv("JOB_EXIT_DELAY_SECONDS")
	if strings.TrimSpace(delayStr) == "" {
		delayStr = "5"
	}
	d, err := time.ParseDuration(delayStr + "s")
	if err != nil || d <= 0 {
		return
	}
	a.logger.WithField("delay", d.String()).Info("Delaying job exit for inspection")
	time.Sleep(d)
}

// processNetworkConfigurations는 네트워크 설정을 처리합니다
func (a *Application) processNetworkConfigurations(ctx context.Context) error {
	startTime := time.Now()

	// 노드 이름 결정: NODE_NAME(env) -> cleaned hostname
	hostname, err := resolveNodeName(a.logger)
	if err != nil {
		return err
	}
    // 설정 조회가 필요하면 사용 (현재 FullCleanup은 일반 적용 Job에서 사용하지 않음)

	// 1. 네트워크 삭제 유스케이스 실행 (고아 인터페이스 선정리)
	//    - 이전 테스트에서 남은 multinic* netplan/ifcfg 파일을 먼저 정리하여
	//      드리프트 경고 및 이름 충돌 가능성을 낮춥니다.
    deleteInput := usecases.DeleteNetworkInput{
        NodeName:    hostname,
        // 일반 적용 Job에서는 고아만 정리(FullCleanup 금지). 전체 정리는 cleanup Job에서만.
        FullCleanup: false,
    }

	deleteOutput, err := a.deleteUseCase.Execute(ctx, deleteInput)
	if err != nil {
		a.logger.WithError(err).Error("Failed to process orphaned interface deletion")
		// 삭제 실패는 치명적이지 않으므로 빈 결과로 초기화
		deleteOutput = &usecases.DeleteNetworkOutput{
			TotalDeleted: 0,
			Errors:       []error{err},
		}
	}

	// 2. 네트워크 설정 유스케이스 실행 (생성/수정)
	configInput := usecases.ConfigureNetworkInput{
		NodeName: hostname,
	}

	configOutput, err := a.configureUseCase.Execute(ctx, configInput)
	if err != nil {
		return err
	}

	// 헬스체크 통계 업데이트 (설정 관련)
	healthService := a.container.GetHealthService()
	for i := 0; i < configOutput.ProcessedCount; i++ {
		healthService.IncrementProcessedVMs()
	}
	for i := 0; i < configOutput.FailedCount; i++ {
		healthService.IncrementFailedConfigs()
	}

	// 실패 여부 선계산 및 부분 실패 처리 정책 적용
	var resultErr error
	if configOutput != nil && configOutput.FailedCount > 0 {
		// 부분 실패 정책: 일부 성공 + 일부 실패인 경우에도 Job을 성공(0)으로 처리할지
		completeOnPartial := strings.EqualFold(strings.TrimSpace(os.Getenv("JOB_COMPLETE_ON_PARTIAL_FAILURE")), "true") || os.Getenv("JOB_COMPLETE_ON_PARTIAL_FAILURE") == ""
		allFailed := (configOutput.ProcessedCount == 0)
		partialFailed := (configOutput.ProcessedCount > 0 && configOutput.FailedCount > 0)
		if allFailed {
			resultErr = fmt.Errorf("network configuration failed for %d/%d interfaces", configOutput.FailedCount, configOutput.TotalCount)
		} else if partialFailed {
			if !completeOnPartial {
				resultErr = fmt.Errorf("network configuration partially failed: %d/%d interfaces", configOutput.FailedCount, configOutput.TotalCount)
			}
		} else {
			// shouldn't happen (only failed without processed counted), keep error
			resultErr = fmt.Errorf("network configuration failed for %d/%d interfaces", configOutput.FailedCount, configOutput.TotalCount)
		}
	}

	// 실제로 처리된 것이 있을 때만 로그 출력
	if configOutput.ProcessedCount > 0 || configOutput.FailedCount > 0 || (deleteOutput != nil && deleteOutput.TotalDeleted > 0) {
		deletedTotal := 0
		deleteErrors := 0
		if deleteOutput != nil {
			deletedTotal = deleteOutput.TotalDeleted
			deleteErrors = len(deleteOutput.Errors)
		}

		fields := logrus.Fields{
			"config_processed": configOutput.ProcessedCount,
			"config_failed":    configOutput.FailedCount,
			"config_total":     configOutput.TotalCount,
			"deleted_total":    deletedTotal,
			"delete_errors":    deleteErrors,
		}
		if resultErr != nil {
			a.logger.WithFields(fields).Error("Network processing completed with failures")
		} else {
			a.logger.WithFields(fields).Info("Network processing completed")
		}

		// 종료 메시지(termination log)에 요약 정보 기록 (Controller가 읽어 로그로 표출 가능)
		// 포맷: JSON {node, processed, failed, total, failures[], deleted_total, delete_errors, timestamp}
		// 노드 이름은 위에서 resolveNodeName으로 구함
		summary := map[string]any{
			"node":          hostname,
			"processed":     configOutput.ProcessedCount,
			"failed":        configOutput.FailedCount,
			"total":         configOutput.TotalCount,
			"failures":      configOutput.Failures,
			"deleted_total": deletedTotal,
			"delete_errors": deleteErrors,
			"timestamp":     time.Now().Format(time.RFC3339),
		}
		if b, err := json.Marshal(summary); err == nil {
			// Kubernetes는 /dev/termination-log 내용을 컨테이너 종료 메시지로 노출
			_ = os.WriteFile("/dev/termination-log", b, 0644)
		} else {
			a.logger.WithError(err).Warn("Failed to marshal termination summary JSON")
		}
	}

	// 삭제 에러가 있다면 별도로 로깅
	if len(deleteOutput.Errors) > 0 {
		for _, delErr := range deleteOutput.Errors {
			a.logger.WithError(delErr).Warn("Error occurred during interface deletion")
		}
	}

	// 폴링 사이클 메트릭 기록
	metrics.RecordPollingCycle(time.Since(startTime).Seconds())

	// 설정 실패가 하나라도 있으면 에러 반환 (job 모드에서 비정상 종료 유도)
	return resultErr
}

// shutdown은 애플리케이션을 정리하고 종료합니다
func (a *Application) shutdown() error {
	// 헬스체크 서버 정리
	if a.healthServer != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()

		if err := a.healthServer.Shutdown(shutdownCtx); err != nil {
			a.logger.WithError(err).Error("Failed to shutdown health check server")
		}
	}

	return nil
}

// fixedIntervalStrategy는 고정 간격 폴링 전략입니다
type fixedIntervalStrategy struct {
	interval time.Duration
}

func (s *fixedIntervalStrategy) NextInterval(success bool) time.Duration {
	return s.interval
}

func (s *fixedIntervalStrategy) Reset() {
	// 고정 간격이므로 리셋할 것이 없음
}

// resolveNodeName은 환경변수 NODE_NAME(Downward API의 spec.nodeName 주입)을 우선 사용하고,
// 없으면 os.Hostname()에서 도메인 접미사를 제거해 반환합니다.
func resolveNodeName(logger *logrus.Logger) (string, error) {
	if v := os.Getenv("NODE_NAME"); strings.TrimSpace(v) != "" {
		return v, nil
	}
	// 보조 키도 확인 (환경에 따라 이름이 다를 수 있음)
	if v := os.Getenv("MY_NODE_NAME"); strings.TrimSpace(v) != "" {
		return v, nil
	}
	hn, err := os.Hostname()
	if err != nil {
		return "", err
	}
	cleaned := cleanHostnameDomainSuffix(hn)
	if hn != cleaned {
		logger.WithFields(logrus.Fields{
			"original_hostname": hn,
			"cleaned_hostname":  cleaned,
		}).Debug("Hostname domain suffix removed")
	}
	return cleaned, nil
}

func cleanHostnameDomainSuffix(h string) string {
	if idx := strings.Index(h, "."); idx != -1 {
		return h[:idx]
	}
	return h
}
