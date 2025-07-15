package config

import (
	"multinic-agent/internal/domain/constants"
	"multinic-agent/internal/domain/errors"
	"os"
	"strconv"
	"time"
)

// Config is a struct that holds application configuration
type Config struct {
	Database DatabaseConfig
	Agent    AgentConfig
	Health   HealthConfig
}

// DatabaseConfig is a struct that holds database configuration
type DatabaseConfig struct {
	Host         string
	Port         string
	User         string
	Password     string
	Database     string
	MaxOpenConns int
	MaxIdleConns int
	MaxLifetime  time.Duration
}

// AgentConfig is a struct that holds agent configuration
type AgentConfig struct {
	PollInterval       time.Duration
	MaxRetries         int
	RetryDelay         time.Duration
	CommandTimeout     time.Duration
	BackupDirectory    string
	Backoff            BackoffConfig
	MaxConcurrentTasks int // 동시에 처리할 최대 인터페이스 수
}

// BackoffConfig is a struct that holds backoff configuration
type BackoffConfig struct {
	Enabled     bool
	MaxInterval time.Duration
	Multiplier  float64
}

// HealthConfig is a struct that holds health check configuration
type HealthConfig struct {
	Port string
}

// ConfigLoader is an interface for loading configuration
type ConfigLoader interface {
	Load() (*Config, error)
}

// EnvironmentConfigLoader is an implementation that loads configuration from environment variables
type EnvironmentConfigLoader struct{}

// NewEnvironmentConfigLoader creates a new EnvironmentConfigLoader
func NewEnvironmentConfigLoader() ConfigLoader {
	return &EnvironmentConfigLoader{}
}

// Load loads configuration from environment variables
func (l *EnvironmentConfigLoader) Load() (*Config, error) {
	config := &Config{
		Database: DatabaseConfig{
			Host:         getEnvOrDefault("DB_HOST", constants.DefaultDBHost),
			Port:         getEnvOrDefault("DB_PORT", constants.DefaultDBPort),
			User:         getEnvOrDefault("DB_USER", "root"), // TODO: 기본값 제거, 환경변수 필수로 변경
			Password:     getEnvOrDefault("DB_PASSWORD", ""), // 보안: 기본값 제거
			Database:     getEnvOrDefault("DB_NAME", constants.DefaultDBName),
			MaxOpenConns: getEnvIntOrDefault("DB_MAX_OPEN_CONNS", 10),
			MaxIdleConns: getEnvIntOrDefault("DB_MAX_IDLE_CONNS", 5),
			MaxLifetime:  getEnvDurationOrDefault("DB_MAX_LIFETIME", 5*time.Minute),
		},
		Agent: AgentConfig{
			PollInterval:       getEnvDurationOrDefault("POLL_INTERVAL", 30*time.Second),
			MaxRetries:         getEnvIntOrDefault("MAX_RETRIES", 3),
			RetryDelay:         getEnvDurationOrDefault("RETRY_DELAY", 2*time.Second),
			CommandTimeout:     getEnvDurationOrDefault("COMMAND_TIMEOUT", 30*time.Second),
			BackupDirectory:    getEnvOrDefault("BACKUP_DIR", constants.DefaultBackupDir),
			MaxConcurrentTasks: getEnvIntOrDefault("MAX_CONCURRENT_TASKS", 5),
			Backoff: BackoffConfig{
				Enabled:     getEnvBoolOrDefault("BACKOFF_ENABLED", true),
				MaxInterval: getEnvDurationOrDefault("BACKOFF_MAX_INTERVAL", getEnvDurationOrDefault("POLL_INTERVAL", 30*time.Second)*10),
				Multiplier:  getEnvFloatOrDefault("BACKOFF_MULTIPLIER", 2.0),
			},
		},
		Health: HealthConfig{
			Port: getEnvOrDefault("HEALTH_PORT", constants.DefaultHealthPort),
		},
	}

	// Validate configuration
	if err := l.validate(config); err != nil {
		return nil, err
	}

	return config, nil
}

// validate validates the configuration
func (l *EnvironmentConfigLoader) validate(config *Config) error {
	// Validate database configuration
	if config.Database.Host == "" {
		return errors.NewValidationError("database host not configured", nil)
	}
	if config.Database.Port == "" {
		return errors.NewValidationError("database port not configured", nil)
	}
	if config.Database.User == "" {
		return errors.NewValidationError("database user not configured", nil)
	}
	if config.Database.Database == "" {
		return errors.NewValidationError("database name not configured", nil)
	}

	// Validate agent configuration
	if config.Agent.PollInterval <= 0 {
		return errors.NewValidationError("invalid polling interval", nil)
	}
	if config.Agent.MaxRetries < 0 {
		return errors.NewValidationError("invalid max retry count", nil)
	}

	// Validate health check configuration
	if config.Health.Port == "" {
		return errors.NewValidationError("health check port not configured", nil)
	}

	return nil
}

// Environment variable helper functions

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvIntOrDefault(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getEnvDurationOrDefault(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
	}
	return defaultValue
}

func getEnvBoolOrDefault(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolValue, err := strconv.ParseBool(value); err == nil {
			return boolValue
		}
	}
	return defaultValue
}

func getEnvFloatOrDefault(key string, defaultValue float64) float64 {
	if value := os.Getenv(key); value != "" {
		if floatValue, err := strconv.ParseFloat(value, 64); err == nil {
			return floatValue
		}
	}
	return defaultValue
}
