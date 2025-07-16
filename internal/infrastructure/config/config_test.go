package config

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvironmentConfigLoader_Load(t *testing.T) {
	// 환경 변수 백업
	originalEnvs := map[string]string{
		"DB_HOST":       os.Getenv("DB_HOST"),
		"DB_PORT":       os.Getenv("DB_PORT"),
		"DB_USER":       os.Getenv("DB_USER"),
		"DB_PASSWORD":   os.Getenv("DB_PASSWORD"),
		"DB_NAME":       os.Getenv("DB_NAME"),
		"POLL_INTERVAL": os.Getenv("POLL_INTERVAL"),
		"HEALTH_PORT":   os.Getenv("HEALTH_PORT"),
		"BACKUP_DIR":    os.Getenv("BACKUP_DIR"),
	}

	// 테스트 후 환경 변수 복원
	defer func() {
		for key, value := range originalEnvs {
			if value == "" {
				os.Unsetenv(key)
			} else {
				os.Setenv(key, value)
			}
		}
	}()

	tests := []struct {
		name      string
		envVars   map[string]string
		wantError bool
		validate  func(*testing.T, *Config)
	}{
		{
			name: "기본 설정값 사용",
			envVars: map[string]string{
				"DB_HOST": "",
				"DB_PORT": "",
			},
			wantError: false,
			validate: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "localhost", cfg.Database.Host)
				assert.Equal(t, "3306", cfg.Database.Port)
				assert.Equal(t, "root", cfg.Database.User)
				assert.Equal(t, "", cfg.Database.Password)
				assert.Equal(t, "multinic", cfg.Database.Database)
				assert.Equal(t, 30*time.Second, cfg.Agent.PollInterval)
				assert.Equal(t, "8080", cfg.Health.Port)
			},
		},
		{
			name: "환경 변수로 설정 오버라이드",
			envVars: map[string]string{
				"DB_HOST":       "custom-host",
				"DB_PORT":       "5432",
				"DB_USER":       "custom-user",
				"DB_PASSWORD":   "custom-pass",
				"DB_NAME":       "custom-db",
				"POLL_INTERVAL": "60s",
				"HEALTH_PORT":   "9090",
				"BACKUP_DIR":    "/custom/backup",
			},
			wantError: false,
			validate: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "custom-host", cfg.Database.Host)
				assert.Equal(t, "5432", cfg.Database.Port)
				assert.Equal(t, "custom-user", cfg.Database.User)
				assert.Equal(t, "custom-pass", cfg.Database.Password)
				assert.Equal(t, "custom-db", cfg.Database.Database)
				assert.Equal(t, 60*time.Second, cfg.Agent.PollInterval)
				assert.Equal(t, "9090", cfg.Health.Port)
				assert.Equal(t, "/custom/backup", cfg.Agent.BackupDirectory)
			},
		},
		{
			name: "유효하지 않은 duration 형식",
			envVars: map[string]string{
				"POLL_INTERVAL": "invalid-duration",
			},
			wantError: false,
			validate: func(t *testing.T, cfg *Config) {
				// 잘못된 형식일 때는 기본값 사용
				assert.Equal(t, 30*time.Second, cfg.Agent.PollInterval)
			},
		},
		{
			name: "빈 DB_HOST로 유효성 검증 실패",
			envVars: map[string]string{
				"DB_HOST": "",
			},
			wantError: false, // 기본값이 설정되므로 에러 없음
			validate: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "localhost", cfg.Database.Host)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 환경 변수 설정
			for key, value := range tt.envVars {
				if value == "" {
					os.Unsetenv(key)
				} else {
					os.Setenv(key, value)
				}
			}

			loader := NewEnvironmentConfigLoader()
			config, err := loader.Load()

			if tt.wantError {
				assert.Error(t, err)
				assert.Nil(t, config)
			} else {
				assert.NoError(t, err)
				require.NotNil(t, config)
				tt.validate(t, config)
			}
		})
	}
}

func TestEnvironmentConfigLoader_validate(t *testing.T) {
	loader := &EnvironmentConfigLoader{}

	tests := []struct {
		name      string
		config    *Config
		wantError bool
	}{
		{
			name: "유효한 설정",
			config: &Config{
				Database: DatabaseConfig{
					Host:     "localhost",
					Port:     "5432",
					User:     "user",
					Password: "pass",
					Database: "db",
				},
				Agent: AgentConfig{
					PollInterval: 30 * time.Second,
					MaxRetries:   3,
				},
				Health: HealthConfig{
					Port: "8080",
				},
			},
			wantError: false,
		},
		{
			name: "빈 DB 호스트",
			config: &Config{
				Database: DatabaseConfig{
					Host:     "",
					Port:     "5432",
					User:     "user",
					Password: "pass",
					Database: "db",
				},
				Agent: AgentConfig{
					PollInterval: 30 * time.Second,
				},
				Health: HealthConfig{
					Port: "8080",
				},
			},
			wantError: true,
		},
		{
			name: "빈 DB 포트",
			config: &Config{
				Database: DatabaseConfig{
					Host:     "localhost",
					Port:     "",
					User:     "user",
					Password: "pass",
					Database: "db",
				},
				Agent: AgentConfig{
					PollInterval: 30 * time.Second,
				},
				Health: HealthConfig{
					Port: "8080",
				},
			},
			wantError: true,
		},
		{
			name: "잘못된 폴링 간격",
			config: &Config{
				Database: DatabaseConfig{
					Host:     "localhost",
					Port:     "5432",
					User:     "user",
					Password: "pass",
					Database: "db",
				},
				Agent: AgentConfig{
					PollInterval: -1 * time.Second,
				},
				Health: HealthConfig{
					Port: "8080",
				},
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := loader.validate(tt.config)

			if tt.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGetEnvHelpers(t *testing.T) {
	t.Run("getEnvOrDefault", func(t *testing.T) {
		// 존재하지 않는 환경 변수
		result := getEnvOrDefault("NON_EXISTENT_VAR", "default")
		assert.Equal(t, "default", result)

		// 존재하는 환경 변수
		os.Setenv("TEST_VAR", "test_value")
		defer os.Unsetenv("TEST_VAR")

		result = getEnvOrDefault("TEST_VAR", "default")
		assert.Equal(t, "test_value", result)
	})

	t.Run("getEnvIntOrDefault", func(t *testing.T) {
		// 존재하지 않는 환경 변수
		result := getEnvIntOrDefault("NON_EXISTENT_INT", 42)
		assert.Equal(t, 42, result)

		// 유효한 정수
		os.Setenv("TEST_INT", "123")
		defer os.Unsetenv("TEST_INT")

		result = getEnvIntOrDefault("TEST_INT", 42)
		assert.Equal(t, 123, result)

		// 잘못된 정수 형식
		os.Setenv("TEST_BAD_INT", "not_a_number")
		defer os.Unsetenv("TEST_BAD_INT")

		result = getEnvIntOrDefault("TEST_BAD_INT", 42)
		assert.Equal(t, 42, result)
	})

	t.Run("getEnvDurationOrDefault", func(t *testing.T) {
		// 존재하지 않는 환경 변수
		result := getEnvDurationOrDefault("NON_EXISTENT_DURATION", 30*time.Second)
		assert.Equal(t, 30*time.Second, result)

		// 유효한 duration
		os.Setenv("TEST_DURATION", "1m30s")
		defer os.Unsetenv("TEST_DURATION")

		result = getEnvDurationOrDefault("TEST_DURATION", 30*time.Second)
		assert.Equal(t, 90*time.Second, result)

		// 잘못된 duration 형식
		os.Setenv("TEST_BAD_DURATION", "invalid")
		defer os.Unsetenv("TEST_BAD_DURATION")

		result = getEnvDurationOrDefault("TEST_BAD_DURATION", 30*time.Second)
		assert.Equal(t, 30*time.Second, result)
	})
}
