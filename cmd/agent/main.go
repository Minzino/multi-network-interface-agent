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

	"multinic-agent/internal/application/usecases"
	"multinic-agent/internal/domain/interfaces"
	"multinic-agent/internal/infrastructure/config"
	"multinic-agent/internal/infrastructure/container"

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

	// 헬스체크 서버 시작
	if err := a.startHealthServer(cfg.Health.Port); err != nil {
		return err
	}

	// 컨텍스트 및 시그널 핸들링 설정
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// 메인 루프 시작
	ticker := time.NewTicker(cfg.Agent.PollInterval)
	defer ticker.Stop()

	a.logger.Info("MultiNIC agent started")

	for {
		select {
		case <-ctx.Done():
			a.logger.Info("Agent shutting down")
			return a.shutdown()

		case <-sigChan:
			a.logger.Info("Received shutdown signal")
			cancel()

		case <-ticker.C:
			if err := a.processNetworkConfigurations(ctx); err != nil {
				a.logger.WithError(err).Error("Failed to process network configurations")
				a.container.GetHealthService().UpdateDBHealth(false, err)
			} else {
				a.container.GetHealthService().UpdateDBHealth(true, nil)
			}
		}
	}
}

// startHealthServer는 헬스체크 서버를 시작합니다
func (a *Application) startHealthServer(port string) error {
	healthService := a.container.GetHealthService()

	a.healthServer = &http.Server{
		Addr:    ":" + port,
		Handler: healthService,
	}

	go func() {
		a.logger.WithField("port", port).Info("Health check server started")
		if err := a.healthServer.ListenAndServe(); err != http.ErrServerClosed {
			a.logger.WithError(err).Error("Health check server failed")
		}
	}()

	return nil
}

// processNetworkConfigurations는 네트워크 설정을 처리합니다
func (a *Application) processNetworkConfigurations(ctx context.Context) error {
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
			"cleaned_hostname": hostname,
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
