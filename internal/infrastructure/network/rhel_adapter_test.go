package network

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"multinic-agent/internal/domain/entities"
	multinicErrors "multinic-agent/internal/domain/errors"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// mustCreateInterfaceName는 테스트용 InterfaceName을 생성합니다
func mustCreateInterfaceName(name string) entities.InterfaceName {
	iface, err := entities.NewInterfaceName(name)
	if err != nil {
		panic(err)
	}
	return iface
}

// MockCommandExecutor는 테스트용 Mock CommandExecutor입니다
type MockCommandExecutor struct {
	mock.Mock
}

func (m *MockCommandExecutor) Execute(ctx context.Context, command string, args ...string) ([]byte, error) {
	argList := []interface{}{ctx, command}
	for _, arg := range args {
		argList = append(argList, arg)
	}
	mockArgs := m.Called(argList...)
	return mockArgs.Get(0).([]byte), mockArgs.Error(1)
}

func (m *MockCommandExecutor) ExecuteWithTimeout(ctx context.Context, timeout time.Duration, command string, args ...string) ([]byte, error) {
	argList := []interface{}{ctx, timeout, command}
	for _, arg := range args {
		argList = append(argList, arg)
	}
	mockArgs := m.Called(argList...)
	return mockArgs.Get(0).([]byte), mockArgs.Error(1)
}

// MockFileSystem for testing
type MockFileSystem struct {
	mock.Mock
}

func (m *MockFileSystem) ReadFile(path string) ([]byte, error) {
	args := m.Called(path)
	return args.Get(0).([]byte), args.Error(1)
}

func (m *MockFileSystem) WriteFile(path string, data []byte, perm os.FileMode) error {
	args := m.Called(path, data, perm)
	return args.Error(0)
}

func (m *MockFileSystem) Exists(path string) bool {
	args := m.Called(path)
	return args.Bool(0)
}

func (m *MockFileSystem) MkdirAll(path string, perm os.FileMode) error {
	args := m.Called(path, perm)
	return args.Error(0)
}

func (m *MockFileSystem) Remove(path string) error {
	args := m.Called(path)
	return args.Error(0)
}

func (m *MockFileSystem) ListFiles(path string) ([]string, error) {
	args := m.Called(path)
	return args.Get(0).([]string), args.Error(1)
}

func TestRHELAdapter_Configure(t *testing.T) {
	tests := []struct {
		name          string
		iface         entities.NetworkInterface
		interfaceName entities.InterfaceName
		setupMocks    func(*MockCommandExecutor)
		wantErr       bool
		errorType     error
	}{
		{
			name: "성공적인 인터페이스 설정 - IP 없음",
			iface: entities.NetworkInterface{
				MacAddress: "fa:16:3e:00:be:63",
			},
			interfaceName: mustCreateInterfaceName("multinic0"),
			setupMocks: func(m *MockCommandExecutor) {
				// Container check for adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				
				// Find device by MAC - first get device list, then check MAC addresses
				deviceStatusOutput := `DEVICE     TYPE      STATE      CONNECTION
eth0       ethernet  connected  eth0
eth1       ethernet  disconnected  --
lo         loopback  unmanaged  --`
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "device", "status").
					Return([]byte(deviceStatusOutput), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "-g", "GENERAL.HWADDR", "device", "show", "eth0").
					Return([]byte("11:22:33:44:55:66\n"), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "-g", "GENERAL.HWADDR", "device", "show", "eth1").
					Return([]byte("fa:16:3e:00:be:63\n"), nil).Once()
				
				// 1. Delete existing (rollback)
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "down", "multinic0").
					Return([]byte(""), errors.New("not found")).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "delete", "multinic0").
					Return([]byte(""), errors.New("not found")).Once()
				
				// 2. Add new connection (use eth1 which has the matching MAC)
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "add", "type", "ethernet", "con-name", "multinic0", "ifname", "eth1").
					Return([]byte("Connection successfully added"), nil).Once()
				
				// 3. Disable IPv4
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "modify", "multinic0", "ipv4.method", "disabled").
					Return([]byte(""), nil).Once()
				
				// 4. Disable IPv6
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "modify", "multinic0", "ipv6.method", "disabled").
					Return([]byte(""), nil).Once()
				
				// 5. Reload connections
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "reload").
					Return([]byte(""), nil).Once()
				
				// 6. Activate connection
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "up", "multinic0").
					Return([]byte("Connection successfully activated"), nil).Once()
				
				// 7. Validate connection
				validationOutput := `NAME      UUID                                  TYPE      DEVICE
multinic0 12345678-1234-1234-1234-123456789012  ethernet  eth1
eth0      abcdefgh-abcd-abcd-abcd-abcdefghijkl  ethernet  eth0`
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "show", "--active").
					Return([]byte(validationOutput), nil).Once()
			},
			wantErr: false,
		},
		{
			name: "성공적인 인터페이스 설정 - 정적 IP",
			iface: entities.NetworkInterface{
				MacAddress: "fa:16:3e:00:be:63",
				Address:    "192.168.1.100",
				CIDR:       "192.168.1.0/24",
				MTU:        1500,
			},
			interfaceName: mustCreateInterfaceName("multinic0"),
			setupMocks: func(m *MockCommandExecutor) {
				// Container check for adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				
				// Find device by MAC - first get device list, then check MAC addresses
				deviceStatusOutput := `DEVICE     TYPE      STATE      CONNECTION
eth0       ethernet  connected  eth0
eth1       ethernet  disconnected  --
lo         loopback  unmanaged  --`
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "device", "status").
					Return([]byte(deviceStatusOutput), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "-g", "GENERAL.HWADDR", "device", "show", "eth0").
					Return([]byte("11:22:33:44:55:66\n"), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "-g", "GENERAL.HWADDR", "device", "show", "eth1").
					Return([]byte("fa:16:3e:00:be:63\n"), nil).Once()
				
				// 1. Delete existing (rollback)
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "down", "multinic0").
					Return([]byte(""), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "delete", "multinic0").
					Return([]byte(""), nil).Once()
				
				// 2. Add new connection (use eth1 which has the matching MAC)
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "add", "type", "ethernet", "con-name", "multinic0", "ifname", "eth1").
					Return([]byte("Connection successfully added"), nil).Once()
				
				// 3. Set static IP
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "modify", "multinic0", "ipv4.method", "manual", "ipv4.addresses", "192.168.1.100/24").
					Return([]byte(""), nil).Once()
				
				// 4. Disable IPv6
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "modify", "multinic0", "ipv6.method", "disabled").
					Return([]byte(""), nil).Once()
				
				// 5. Set MTU
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "modify", "multinic0", "ethernet.mtu", "1500").
					Return([]byte(""), nil).Once()
				
				// 6. Reload connections
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "reload").
					Return([]byte(""), nil).Once()
				
				// 7. Activate connection
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "up", "multinic0").
					Return([]byte("Connection successfully activated"), nil).Once()
				
				// 8. Validate connection
				validationOutput := `NAME      UUID                                  TYPE      DEVICE
multinic0 12345678-1234-1234-1234-123456789012  ethernet  eth1
eth0      abcdefgh-abcd-abcd-abcd-abcdefghijkl  ethernet  eth0`
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "show", "--active").
					Return([]byte(validationOutput), nil).Once()
			},
			wantErr: false,
		},
		{
			name: "connection add 실패",
			iface: entities.NetworkInterface{
				MacAddress: "fa:16:3e:00:be:63",
			},
			interfaceName: mustCreateInterfaceName("multinic0"),
			setupMocks: func(m *MockCommandExecutor) {
				// Container check for adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				
				// Find device by MAC - first get device list, then check MAC addresses
				deviceStatusOutput := `DEVICE     TYPE      STATE      CONNECTION
eth0       ethernet  connected  eth0
eth1       ethernet  disconnected  --
lo         loopback  unmanaged  --`
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "device", "status").
					Return([]byte(deviceStatusOutput), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "-g", "GENERAL.HWADDR", "device", "show", "eth0").
					Return([]byte("11:22:33:44:55:66\n"), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "-g", "GENERAL.HWADDR", "device", "show", "eth1").
					Return([]byte("fa:16:3e:00:be:63\n"), nil).Once()
				
				// Rollback
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "down", "multinic0").
					Return([]byte(""), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "delete", "multinic0").
					Return([]byte(""), nil).Once()
				
				// Add fails (use eth1 which has the matching MAC)
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "add", "type", "ethernet", "con-name", "multinic0", "ifname", "eth1").
					Return([]byte(""), errors.New("nmcli error")).Once()
			},
			wantErr:   true,
			errorType: &multinicErrors.DomainError{Type: multinicErrors.ErrorTypeNetwork},
		},
		{
			name: "connection up 실패시 롤백",
			iface: entities.NetworkInterface{
				MacAddress: "fa:16:3e:00:be:63",
			},
			interfaceName: mustCreateInterfaceName("multinic0"),
			setupMocks: func(m *MockCommandExecutor) {
				// Container check for adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				
				// Find device by MAC - first get device list, then check MAC addresses
				deviceStatusOutput := `DEVICE     TYPE      STATE      CONNECTION
eth0       ethernet  connected  eth0
eth1       ethernet  disconnected  --
lo         loopback  unmanaged  --`
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "device", "status").
					Return([]byte(deviceStatusOutput), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "-g", "GENERAL.HWADDR", "device", "show", "eth0").
					Return([]byte("11:22:33:44:55:66\n"), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "-g", "GENERAL.HWADDR", "device", "show", "eth1").
					Return([]byte("fa:16:3e:00:be:63\n"), nil).Once()
				
				// Initial rollback
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "down", "multinic0").
					Return([]byte(""), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "delete", "multinic0").
					Return([]byte(""), nil).Once()
				
				// Add succeeds (use eth1 which has the matching MAC)
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "add", "type", "ethernet", "con-name", "multinic0", "ifname", "eth1").
					Return([]byte(""), nil).Once()
				
				// Disable IPv4
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "modify", "multinic0", "ipv4.method", "disabled").
					Return([]byte(""), nil).Once()
				
				// Disable IPv6
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "modify", "multinic0", "ipv6.method", "disabled").
					Return([]byte(""), nil).Once()
				
				// Reload connections
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "reload").
					Return([]byte(""), nil).Once()
				
				// Activate fails
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "up", "multinic0").
					Return([]byte(""), errors.New("activation failed")).Once()
				
				// Rollback after failure
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "down", "multinic0").
					Return([]byte(""), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "delete", "multinic0").
					Return([]byte(""), nil).Once()
			},
			wantErr:   true,
			errorType: &multinicErrors.DomainError{Type: multinicErrors.ErrorTypeNetwork},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExecutor := new(MockCommandExecutor)
			tt.setupMocks(mockExecutor)

			adapter := NewRHELAdapter(mockExecutor, &MockFileSystem{}, logrus.New())
			// Interface name is already set in test case
			
			err := adapter.Configure(context.Background(), tt.iface, tt.interfaceName)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errorType != nil {
					assert.IsType(t, tt.errorType, err)
				}
			} else {
				assert.NoError(t, err)
			}

			mockExecutor.AssertExpectations(t)
		})
	}
}

func TestRHELAdapter_Validate(t *testing.T) {
	tests := []struct {
		name          string
		setupMocks    func(*MockCommandExecutor)
		wantErr       bool
	}{
		{
			name: "인터페이스가 connected 상태",
			setupMocks: func(m *MockCommandExecutor) {
				// Container check for adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				output := `NAME      UUID                                  TYPE      DEVICE
multinic0 12345678-1234-1234-1234-123456789012  ethernet  eth1
eth0      abcdefgh-abcd-abcd-abcd-abcdefghijkl  ethernet  eth0`
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "show", "--active").
					Return([]byte(output), nil).Once()
			},
			wantErr: false,
		},
		{
			name: "인터페이스가 disconnected 상태",
			setupMocks: func(m *MockCommandExecutor) {
				// Container check for adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				// multinic0 is not in active connections (disconnected)
				output := `NAME      UUID                                  TYPE      DEVICE
eth0      abcdefgh-abcd-abcd-abcd-abcdefghijkl  ethernet  eth0`
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "show", "--active").
					Return([]byte(output), nil).Once()
				// Check all connections to see if multinic0 exists but inactive
				allOutput := `NAME      UUID                                  TYPE      DEVICE
multinic0 12345678-1234-1234-1234-123456789012  ethernet  --
eth0      abcdefgh-abcd-abcd-abcd-abcdefghijkl  ethernet  eth0`
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "show").
					Return([]byte(allOutput), nil).Once()
			},
			wantErr: true,
		},
		{
			name: "인터페이스가 목록에 없음",
			setupMocks: func(m *MockCommandExecutor) {
				// Container check for adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				// multinic0 is not in active connections
				output := `NAME      UUID                                  TYPE      DEVICE
eth0      abcdefgh-abcd-abcd-abcd-abcdefghijkl  ethernet  eth0`
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "show", "--active").
					Return([]byte(output), nil).Once()
				// multinic0 is also not in all connections list
				allOutput := `NAME      UUID                                  TYPE      DEVICE
eth0      abcdefgh-abcd-abcd-abcd-abcdefghijkl  ethernet  eth0`
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "show").
					Return([]byte(allOutput), nil).Once()
			},
			wantErr: true,
		},
		{
			name: "nmcli 명령 실행 실패",
			setupMocks: func(m *MockCommandExecutor) {
				// Container check for adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "show", "--active").
					Return([]byte(""), errors.New("command failed")).Once()
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExecutor := new(MockCommandExecutor)
			tt.setupMocks(mockExecutor)

			adapter := NewRHELAdapter(mockExecutor, &MockFileSystem{}, logrus.New())
			
			interfaceName := mustCreateInterfaceName("multinic0")
			err := adapter.Validate(context.Background(), interfaceName)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockExecutor.AssertExpectations(t)
		})
	}
}

func TestRHELAdapter_Rollback(t *testing.T) {
	tests := []struct {
		name       string
		setupMocks func(*MockCommandExecutor)
		wantErr    bool
	}{
		{
			name: "성공적인 롤백",
			setupMocks: func(m *MockCommandExecutor) {
				// Container check for adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "down", "multinic0").
					Return([]byte("Connection successfully deactivated"), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "delete", "multinic0").
					Return([]byte("Connection successfully deleted"), nil).Once()
			},
			wantErr: false,
		},
		{
			name: "connection이 이미 없는 경우도 성공",
			setupMocks: func(m *MockCommandExecutor) {
				// Container check for adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "down", "multinic0").
					Return([]byte(""), errors.New("no such connection")).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "delete", "multinic0").
					Return([]byte(""), errors.New("no such connection")).Once()
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExecutor := new(MockCommandExecutor)
			tt.setupMocks(mockExecutor)

			adapter := NewRHELAdapter(mockExecutor, &MockFileSystem{}, logrus.New())
			err := adapter.Rollback(context.Background(), "multinic0")

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockExecutor.AssertExpectations(t)
		})
	}
}

func TestRHELAdapter_GetConfigDir(t *testing.T) {
	mockExecutor := new(MockCommandExecutor)
	// isContainer check
	mockExecutor.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
		Return([]byte(""), errors.New("not found")).Once()
	
	adapter := NewRHELAdapter(mockExecutor, &MockFileSystem{}, logrus.New())
	assert.Equal(t, "/etc/NetworkManager/system-connections", adapter.GetConfigDir())
}

func TestRHELAdapter_generateNmConnectionContent(t *testing.T) {
	tests := []struct {
		name           string
		iface          entities.NetworkInterface
		ifaceName      string
		actualDevice   string
		expectedFields []string
	}{
		{
			name: "정적 IP와 MTU가 있는 인터페이스",
			iface: entities.NetworkInterface{
				MacAddress: "FA:16:3E:BB:93:7A",
				Address:    "192.168.1.100",
				CIDR:       "192.168.1.0/24",
				MTU:        1500,
			},
			ifaceName:    "multinic0",
			actualDevice: "ens7",
			expectedFields: []string{
				"id=multinic0",
				"interface-name=ens7",
				"mac-address=FA:16:3E:BB:93:7A",
				"mtu=1500",
				"method=manual",
				"address1=192.168.1.100/24",
				"method=disabled", // IPv6
			},
		},
		{
			name: "IP 없는 인터페이스",
			iface: entities.NetworkInterface{
				MacAddress: "FA:16:3E:C6:48:12",
				Address:    "",
				CIDR:       "",
				MTU:        0,
			},
			ifaceName:    "multinic1",
			actualDevice: "ens8",
			expectedFields: []string{
				"id=multinic1",
				"interface-name=ens8",
				"mac-address=FA:16:3E:C6:48:12",
				"method=disabled", // IPv4
				"method=disabled", // IPv6
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExecutor := &MockCommandExecutor{}
			mockFS := &MockFileSystem{}
			logger := logrus.New()

			// Mock container check
			mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "test", "-d", "/host").
				Return([]byte{}, assert.AnError).Maybe()

			adapter := NewRHELAdapter(mockExecutor, mockFS, logger)
			content := adapter.generateNmConnectionContent(tt.iface, tt.ifaceName, tt.actualDevice)

			// Verify all expected fields are present
			for _, field := range tt.expectedFields {
				assert.Contains(t, content, field, "Expected field %s not found in content", field)
			}

			// Verify basic structure
			assert.Contains(t, content, "[connection]")
			assert.Contains(t, content, "[ethernet]")
			assert.Contains(t, content, "[ipv4]")
			assert.Contains(t, content, "[ipv6]")
			assert.Contains(t, content, "[proxy]")
		})
	}
}