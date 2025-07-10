package config

import (
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
	PollInterval    time.Duration
	MaxRetries      int
	RetryDelay      time.Duration
	CommandTimeout  time.Duration
	BackupDirectory string
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
			Host:         getEnvOrDefault("DB_HOST", "192.168.34.79"),
			Port:         getEnvOrDefault("DB_PORT", "30305"),
			User:         getEnvOrDefault("DB_USER", "root"),
			Password:     getEnvOrDefault("DB_PASSWORD", "cloud1234"),
			Database:     getEnvOrDefault("DB_NAME", "multinic"),
			MaxOpenConns: getEnvIntOrDefault("DB_MAX_OPEN_CONNS", 10),
			MaxIdleConns: getEnvIntOrDefault("DB_MAX_IDLE_CONNS", 5),
			MaxLifetime:  getEnvDurationOrDefault("DB_MAX_LIFETIME", 5*time.Minute),
		},
		Agent: AgentConfig{
			PollInterval:    getEnvDurationOrDefault("POLL_INTERVAL", 30*time.Second),
			MaxRetries:      getEnvIntOrDefault("MAX_RETRIES", 3),
			RetryDelay:      getEnvDurationOrDefault("RETRY_DELAY", 2*time.Second),
			CommandTimeout:  getEnvDurationOrDefault("COMMAND_TIMEOUT", 30*time.Second),
			BackupDirectory: getEnvOrDefault("BACKUP_DIR", "/var/lib/multinic/backups"),
		},
		Health: HealthConfig{
			Port: getEnvOrDefault("HEALTH_PORT", "8080"),
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
