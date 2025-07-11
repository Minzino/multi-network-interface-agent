package usecases

import (
	"context"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"multinic-agent/internal/domain/entities"
	"multinic-agent/internal/domain/services"
)

func TestConfigureNetworkUseCase_ParseNmConnectionFile(t *testing.T) {
	tests := []struct {
		name           string
		fileContent    string
		expectedConfig *NmConnectionConfig
		expectError    bool
	}{
		{
			name: "정적 IP가 설정된 nmconnection 파일 파싱",
			fileContent: `[connection]
id=multinic0
uuid=12345678-1234-1234-1234-123456789abc
type=ethernet

[ethernet]
mac-address=FA:16:3E:BB:93:7A
mtu=1400

[ipv4]
method=manual
address1=192.168.1.100/24
`,
			expectedConfig: &NmConnectionConfig{
				MacAddress: "FA:16:3E:BB:93:7A",
				MTU:        1400,
				Addresses:  []string{"192.168.1.100/24"},
				Method:     "manual",
			},
			expectError: false,
		},
		{
			name: "IP 없는 nmconnection 파일 파싱",
			fileContent: `[connection]
id=multinic1
type=ethernet

[ethernet]
mac-address=FA:16:3E:C6:48:12
mtu=1500

[ipv4]
method=disabled
`,
			expectedConfig: &NmConnectionConfig{
				MacAddress: "FA:16:3E:C6:48:12",
				MTU:        1500,
				Addresses:  []string{},
				Method:     "disabled",
			},
			expectError: false,
		},
		{
			name: "여러 IP 주소가 있는 nmconnection 파일 파싱",
			fileContent: `[connection]
id=multinic2
type=ethernet

[ethernet]
mac-address=FA:16:3E:AA:BB:CC
mtu=1400

[ipv4]
method=manual
address1=192.168.1.100/24
address2=10.0.0.100/16
`,
			expectedConfig: &NmConnectionConfig{
				MacAddress: "FA:16:3E:AA:BB:CC",
				MTU:        1400,
				Addresses:  []string{"192.168.1.100/24", "10.0.0.100/16"},
				Method:     "manual",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mocks
			mockFS := &MockFileSystem{}
			mockRepo := &MockNetworkInterfaceRepository{}
			mockConfigurer := &MockNetworkConfigurer{}
			mockRollbacker := &MockNetworkRollbacker{}
			mockOSDetector := &MockOSDetector{}
			mockCommandExecutor := &MockCommandExecutor{}

			// Mock container environment check (for InterfaceNamingService initialization)
			mockCommandExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "test", "-d", "/host").Return([]byte{}, assert.AnError).Maybe()
			
			// Create real InterfaceNamingService with mocks
			realNaming := services.NewInterfaceNamingService(mockFS, mockCommandExecutor)
			
			uc := NewConfigureNetworkUseCase(
				mockRepo, mockConfigurer, mockRollbacker, 
				realNaming, mockFS, mockOSDetector, logrus.New(),
			)

			// Mock file read
			mockFS.On("ReadFile", "/test/file.nmconnection").Return([]byte(tt.fileContent), nil)

			// Test parsing
			config, err := uc.parseNmConnectionFile("/test/file.nmconnection")

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedConfig.MacAddress, config.MacAddress)
				assert.Equal(t, tt.expectedConfig.MTU, config.MTU)
				assert.Equal(t, tt.expectedConfig.Method, config.Method)
				assert.ElementsMatch(t, tt.expectedConfig.Addresses, config.Addresses)
			}

			mockFS.AssertExpectations(t)
		})
	}
}

func TestConfigureNetworkUseCase_IsNmcliConnectionDrifted(t *testing.T) {
	tests := []struct {
		name           string
		dbInterface    entities.NetworkInterface
		connectionName string
		fileContent    string
		fileExists     bool
		expectedDrift  bool
	}{
		{
			name: "IP 주소 드리프트 감지",
			dbInterface: entities.NetworkInterface{
				ID:         1,
				MacAddress: "fa:16:3e:bb:93:7a",
				Address:    "192.168.1.100",
				CIDR:       "192.168.1.0/24",
				MTU:        1500,
			},
			connectionName: "multinic0",
			fileContent: `[ethernet]
mac-address=FA:16:3E:BB:93:7A
mtu=1500

[ipv4]
method=manual
address1=192.168.1.200/24
`,
			fileExists:    true,
			expectedDrift: true, // IP가 다름 (100 vs 200)
		},
		{
			name: "MTU 드리프트 감지",
			dbInterface: entities.NetworkInterface{
				ID:         1,
				MacAddress: "fa:16:3e:bb:93:7a",
				Address:    "192.168.1.100",
				CIDR:       "192.168.1.0/24",
				MTU:        1500,
			},
			connectionName: "multinic0",
			fileContent: `[ethernet]
mac-address=FA:16:3E:BB:93:7A
mtu=1400

[ipv4]
method=manual
address1=192.168.1.100/24
`,
			fileExists:    true,
			expectedDrift: true, // MTU가 다름 (1500 vs 1400)
		},
		{
			name: "설정 일치 - 드리프트 없음",
			dbInterface: entities.NetworkInterface{
				ID:         1,
				MacAddress: "fa:16:3e:bb:93:7a",
				Address:    "192.168.1.100",
				CIDR:       "192.168.1.0/24",
				MTU:        1500,
			},
			connectionName: "multinic0",
			fileContent: `[ethernet]
mac-address=FA:16:3E:BB:93:7A
mtu=1500

[ipv4]
method=manual
address1=192.168.1.100/24
`,
			fileExists:    true,
			expectedDrift: false, // 모든 설정이 일치
		},
		{
			name: "파일이 존재하지 않는 경우",
			dbInterface: entities.NetworkInterface{
				ID:         1,
				MacAddress: "fa:16:3e:bb:93:7a",
				Address:    "192.168.1.100",
				CIDR:       "192.168.1.0/24",
				MTU:        1500,
			},
			connectionName: "multinic0",
			fileContent:    "",
			fileExists:     false,
			expectedDrift:  true, // 파일이 없으면 드리프트
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mocks
			mockFS := &MockFileSystem{}
			mockRepo := &MockNetworkInterfaceRepository{}
			mockConfigurer := &MockNetworkConfigurer{}
			mockRollbacker := &MockNetworkRollbacker{}
			mockOSDetector := &MockOSDetector{}
			mockCommandExecutor := &MockCommandExecutor{}

			// Mock container environment check (for InterfaceNamingService initialization)
			mockCommandExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "test", "-d", "/host").Return([]byte{}, assert.AnError).Maybe()
			
			// Create real InterfaceNamingService with mocks
			realNaming := services.NewInterfaceNamingService(mockFS, mockCommandExecutor)
			
			uc := NewConfigureNetworkUseCase(
				mockRepo, mockConfigurer, mockRollbacker, 
				realNaming, mockFS, mockOSDetector, logrus.New(),
			)

			// Mock configurer.GetConfigDir()
			mockConfigurer.On("GetConfigDir").Return("/etc/NetworkManager/system-connections")

			if tt.fileExists {
				// Mock file exists and read
				filePath := "/etc/NetworkManager/system-connections/" + tt.connectionName + ".nmconnection"
				mockFS.On("Exists", filePath).Return(true)
				mockFS.On("ReadFile", filePath).Return([]byte(tt.fileContent), nil)
			} else {
				// Mock file doesn't exist
				filePath := "/etc/NetworkManager/system-connections/" + tt.connectionName + ".nmconnection"
				mockFS.On("Exists", filePath).Return(false)
			}

			// Test drift detection
			ctx := context.Background()
			isDrifted := uc.isNmcliConnectionDrifted(ctx, tt.dbInterface, tt.connectionName)

			assert.Equal(t, tt.expectedDrift, isDrifted)
			mockFS.AssertExpectations(t)
			mockConfigurer.AssertExpectations(t)
		})
	}
}