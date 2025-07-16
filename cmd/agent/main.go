package main

import (
	"context"
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

	// 폴링 컨트롤러 생성
	pollingController := polling.NewPollingController(strategy, a.logger)

	a.logger.Info("MultiNIC agent started")

	// 시그널 처리를 위한 goroutine
	go func() {
		<-sigChan
		a.logger.Info("Received shutdown signal")
		cancel()
	}()

	// 폴링 시작
	return pollingController.Start(ctx, func(ctx context.Context) error {
		err := a.processNetworkConfigurations(ctx)
		if err != nil {
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

// processNetworkConfigurations는 네트워크 설정을 처리합니다
func (a *Application) processNetworkConfigurations(ctx context.Context) error {
	startTime := time.Now()

	// 호스트네임 가져오기
	hostname, err := os.Hostname()
	if err != nil {
		return err
	}

	// .novalocal 또는 다른 도메인 접미사 제거
	originalHostname := hostname
	if idx := strings.Index(hostname, "."); idx != -1 {
		hostname = hostname[:idx]
	}

	// 호스트명 변경사항 디버그 로그
	if originalHostname != hostname {
		a.logger.WithFields(logrus.Fields{
			"original_hostname": originalHostname,
			"cleaned_hostname":  hostname,
		}).Debug("Hostname domain suffix removed")
	}

	// 1. 네트워크 설정 유스케이스 실행 (생성/수정)
	configInput := usecases.ConfigureNetworkInput{
		NodeName: hostname,
	}

	configOutput, err := a.configureUseCase.Execute(ctx, configInput)
	if err != nil {
		return err
	}

	// 2. 네트워크 삭제 유스케이스 실행 (고아 인터페이스 정리)
	deleteInput := usecases.DeleteNetworkInput{
		NodeName: hostname,
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

	// 헬스체크 통계 업데이트 (설정 관련)
	healthService := a.container.GetHealthService()
	for i := 0; i < configOutput.ProcessedCount; i++ {
		healthService.IncrementProcessedVMs()
	}
	for i := 0; i < configOutput.FailedCount; i++ {
		healthService.IncrementFailedConfigs()
	}

	// 실제로 처리된 것이 있을 때만 로그 출력
	if configOutput.ProcessedCount > 0 || configOutput.FailedCount > 0 || (deleteOutput != nil && deleteOutput.TotalDeleted > 0) {
		deletedTotal := 0
		deleteErrors := 0
		if deleteOutput != nil {
			deletedTotal = deleteOutput.TotalDeleted
			deleteErrors = len(deleteOutput.Errors)
		}

		a.logger.WithFields(logrus.Fields{
			"config_processed": configOutput.ProcessedCount,
			"config_failed":    configOutput.FailedCount,
			"config_total":     configOutput.TotalCount,
			"deleted_total":    deletedTotal,
			"delete_errors":    deleteErrors,
		}).Info("Network processing completed")
	}

	// 삭제 에러가 있다면 별도로 로깅
	if len(deleteOutput.Errors) > 0 {
		for _, delErr := range deleteOutput.Errors {
			a.logger.WithError(delErr).Warn("Error occurred during interface deletion")
		}
	}

	// 폴링 사이클 메트릭 기록
	metrics.RecordPollingCycle(time.Since(startTime).Seconds())

	return nil
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
