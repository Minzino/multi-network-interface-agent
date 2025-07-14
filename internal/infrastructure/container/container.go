package container

import (
	"database/sql"
	"multinic-agent/internal/application/usecases"
	"multinic-agent/internal/domain/interfaces"
	"multinic-agent/internal/domain/services"
	"multinic-agent/internal/infrastructure/adapters"
	"multinic-agent/internal/infrastructure/config"
	"multinic-agent/internal/infrastructure/health"
	"multinic-agent/internal/infrastructure/network"
	"multinic-agent/internal/infrastructure/persistence"

	_ "github.com/go-sql-driver/mysql"
	"github.com/sirupsen/logrus"
)

// Container는 의존성 주입을 관리하는 컨테이너입니다
type Container struct {
	config *config.Config
	logger *logrus.Logger

	// 인프라스트럭처 어댑터들
	fileSystem      interfaces.FileSystem
	commandExecutor interfaces.CommandExecutor
	clock           interfaces.Clock
	osDetector      interfaces.OSDetector

	// 서비스들
	healthService  *health.HealthService
	namingService  *services.InterfaceNamingService
	networkFactory *network.NetworkManagerFactory

	// 레포지토리
	repository interfaces.NetworkInterfaceRepository

	// 유스케이스
	configureNetworkUseCase *usecases.ConfigureNetworkUseCase
	deleteNetworkUseCase    *usecases.DeleteNetworkUseCase

	// 데이터베이스
	db *sql.DB
}

// NewContainer는 새로운 Container를 생성합니다
func NewContainer(cfg *config.Config, logger *logrus.Logger) (*Container, error) {
	container := &Container{
		config: cfg,
		logger: logger,
	}

	if err := container.initializeInfrastructure(); err != nil {
		return nil, err
	}

	if err := container.initializeServices(); err != nil {
		return nil, err
	}

	if err := container.initializeUseCases(); err != nil {
		return nil, err
	}

	return container, nil
}

// initializeInfrastructure는 인프라스트럭처 컴포넌트들을 초기화합니다
func (c *Container) initializeInfrastructure() error {
	// 기본 어댑터들 초기화
	c.fileSystem = adapters.NewRealFileSystem()
	c.commandExecutor = adapters.NewRealCommandExecutor()
	c.clock = adapters.NewRealClock()
	c.osDetector = adapters.NewRealOSDetector(c.fileSystem)

	// 데이터베이스 연결
	dsn := c.buildDSN()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}

	// 연결 풀 설정
	db.SetMaxOpenConns(c.config.Database.MaxOpenConns)
	db.SetMaxIdleConns(c.config.Database.MaxIdleConns)
	db.SetConnMaxLifetime(c.config.Database.MaxLifetime)

	// 연결 테스트
	if err := db.Ping(); err != nil {
		return err
	}

	c.db = db

	// 레포지토리 초기화
	c.repository = persistence.NewMySQLRepository(c.db, c.logger)

	return nil
}

// initializeServices는 서비스들을 초기화합니다
func (c *Container) initializeServices() error {
	// 헬스 서비스
	c.healthService = health.NewHealthService(c.clock, c.logger)

	// 인터페이스 네이밍 서비스
	c.namingService = services.NewInterfaceNamingService(c.fileSystem, c.commandExecutor)

	// 네트워크 관리자 팩토리
	c.networkFactory = network.NewNetworkManagerFactory(
		c.osDetector,
		c.commandExecutor,
		c.fileSystem,
		c.logger,
	)

	return nil
}

// initializeUseCases는 유스케이스들을 초기화합니다
func (c *Container) initializeUseCases() error {
	// 네트워크 설정자 생성
	configurer, err := c.networkFactory.CreateNetworkConfigurer()
	if err != nil {
		return err
	}

	// 롤백 관리자 생성
	rollbacker, err := c.networkFactory.CreateNetworkRollbacker()
	if err != nil {
		return err
	}

	// 네트워크 설정 유스케이스
		c.configureNetworkUseCase = usecases.NewConfigureNetworkUseCase(
		c.repository,
		configurer,
		rollbacker,
		c.namingService,
		c.fileSystem,
		c.osDetector,
		c.logger,
	)

	// 네트워크 삭제 유스케이스
	c.deleteNetworkUseCase = usecases.NewDeleteNetworkUseCase(
		c.osDetector,
		rollbacker,
		c.namingService,
		c.repository,
		c.fileSystem,
		c.logger,
	)

	return nil
}

// buildDSN은 데이터베이스 연결 문자열을 생성합니다
func (c *Container) buildDSN() string {
	cfg := c.config.Database
	return cfg.User + ":" + cfg.Password + "@tcp(" + cfg.Host + ":" + cfg.Port + ")/" + cfg.Database + "?parseTime=true"
}

// GetConfig는 설정을 반환합니다
func (c *Container) GetConfig() *config.Config {
	return c.config
}

// GetHealthService는 헬스 서비스를 반환합니다
func (c *Container) GetHealthService() *health.HealthService {
	return c.healthService
}

// GetConfigureNetworkUseCase는 네트워크 설정 유스케이스를 반환합니다
func (c *Container) GetConfigureNetworkUseCase() *usecases.ConfigureNetworkUseCase {
	return c.configureNetworkUseCase
}

// GetDeleteNetworkUseCase는 네트워크 삭제 유스케이스를 반환합니다
func (c *Container) GetDeleteNetworkUseCase() *usecases.DeleteNetworkUseCase {
	return c.deleteNetworkUseCase
}

// Close는 컨테이너를 정리합니다
func (c *Container) Close() error {
	if c.db != nil {
		return c.db.Close()
	}
	return nil
}
