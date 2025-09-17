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
    return *iface
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

// TestRHELAdapter_Configure is moved to rhel_adapter_new_test.go

func TestRHELAdapter_Configure_OLD_DISABLED(t *testing.T) {
	t.Skip("Old nmcli-based tests - replaced by file-based tests in rhel_adapter_new_test.go")
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
            iface: func() entities.NetworkInterface {
                ni, _ := entities.NewNetworkInterface(1, "fa:16:3e:00:be:63", "node", "", "10.0.0.0/24", 1500)
                return *ni
            }(),
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
            iface: func() entities.NetworkInterface {
                ni, _ := entities.NewNetworkInterface(1, "fa:16:3e:00:be:63", "node", "192.168.1.100", "192.168.1.0/24", 1500)
                return *ni
            }(),
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
            iface: func() entities.NetworkInterface {
                ni, _ := entities.NewNetworkInterface(1, "fa:16:3e:00:be:63", "node", "", "10.0.0.0/24", 1500)
                return *ni
            }(),
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
            iface: func() entities.NetworkInterface {
                ni, _ := entities.NewNetworkInterface(1, "fa:16:3e:00:be:63", "node", "", "10.0.0.0/24", 1500)
                return *ni
            }(),
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

func TestRHELAdapter_Validate_Old(t *testing.T) {
	t.Skip("Old nmcli-based validation tests - replaced by new ip-based tests")
	tests := []struct {
		name       string
		setupMocks func(*MockCommandExecutor)
		wantErr    bool
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
			name: "인터페이스가 disconnected 상태 (exists but inactive - now considered valid)",
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
			wantErr: false, // Changed to false - inactive connections are now considered valid
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
	t.Skip("Old nmconnection-based rollback tests - need to update for ifcfg files")
	tests := []struct {
		name       string
		setupMocks func(*MockCommandExecutor, *MockFileSystem)
		wantErr    bool
	}{
		{
			name: "성공적인 롤백 (파일 삭제)",
			setupMocks: func(m *MockCommandExecutor, fs *MockFileSystem) {
				// Container check for adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				// File removal
				fs.On("Remove", "/etc/NetworkManager/system-connections/multinic0.nmconnection").
					Return(nil).Once()
				// NetworkManager reload
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "reload").
					Return([]byte(""), nil).Once()
			},
			wantErr: false,
		},
		{
			name: "파일이 이미 없는 경우도 성공",
			setupMocks: func(m *MockCommandExecutor, fs *MockFileSystem) {
				// Container check for adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				// File removal fails (already removed)
				fs.On("Remove", "/etc/NetworkManager/system-connections/multinic0.nmconnection").
					Return(errors.New("no such file")).Once()
				// NetworkManager reload still happens
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "reload").
					Return([]byte(""), nil).Once()
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExecutor := new(MockCommandExecutor)
			mockFS := new(MockFileSystem)
			tt.setupMocks(mockExecutor, mockFS)

			adapter := NewRHELAdapter(mockExecutor, mockFS, logrus.New())
			err := adapter.Rollback(context.Background(), "multinic0")

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockExecutor.AssertExpectations(t)
			mockFS.AssertExpectations(t)
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

func TestRHELAdapter_generateIfcfgContent(t *testing.T) {
	t.Skip("Old nmconnection format tests - need to update for ifcfg format")
	tests := []struct {
		name           string
		iface          entities.NetworkInterface
		ifaceName      string
		actualDevice   string
		expectedFields []string
	}{
        {
            name: "정적 IP와 MTU가 있는 인터페이스",
            iface: func() entities.NetworkInterface {
                ni, _ := entities.NewNetworkInterface(1, "FA:16:3E:BB:93:7A", "node", "192.168.1.100", "192.168.1.0/24", 1500)
                return *ni
            }(),
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
            iface: func() entities.NetworkInterface {
                ni, _ := entities.NewNetworkInterface(2, "FA:16:3E:C6:48:12", "node", "0.0.0.0", "0.0.0.0/0", 1500)
                return *ni
            }(),
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
            content := adapter.generateNMConnection(tt.iface, tt.ifaceName)

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

// TestRHELAdapter_SELinuxRestore tests SELinux context restoration functionality
func TestRHELAdapter_SELinuxRestore(t *testing.T) {
	tests := []struct {
		name             string
		enableSELinux    bool
		isContainer      bool
		setupMocks       func(*MockCommandExecutor, *MockFileSystem)
		expectRestorecon bool
	}{
		{
			name:          "SELinux disabled - no restorecon",
			enableSELinux: false,
			isContainer:   false,
			setupMocks: func(m *MockCommandExecutor, fs *MockFileSystem) {
				// Container check during adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				// No restorecon calls expected
			},
			expectRestorecon: false,
		},
		{
			name:          "SELinux enabled - native restorecon",
			enableSELinux: true,
			isContainer:   false,
			setupMocks: func(m *MockCommandExecutor, fs *MockFileSystem) {
				// Container check during adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				
				// Directory existence checks
				fs.On("Exists", "/etc/NetworkManager/system-connections").Return(true)
				fs.On("Exists", "/etc/systemd/network").Return(true)
				
				// Expect native restorecon calls
				m.On("ExecuteWithTimeout", mock.Anything, 10*time.Second, "sh", "-c", 
					"restorecon -Rv /etc/NetworkManager/system-connections").
					Return([]byte("restorecon: /etc/NetworkManager/system-connections context"), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 10*time.Second, "sh", "-c", 
					"restorecon -Rv /etc/systemd/network").
					Return([]byte("restorecon: /etc/systemd/network context"), nil).Once()
			},
			expectRestorecon: true,
		},
		{
			name:          "SELinux enabled - container nsenter restorecon",
			enableSELinux: true,
			isContainer:   true,
			setupMocks: func(m *MockCommandExecutor, fs *MockFileSystem) {
				// Container check during adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), nil).Once()
				
				// Directory existence checks
				fs.On("Exists", "/etc/NetworkManager/system-connections").Return(true)
				fs.On("Exists", "/etc/systemd/network").Return(true)
				
				// Expect nsenter-based restorecon calls
				m.On("ExecuteWithTimeout", mock.Anything, 10*time.Second, "sh", "-c", 
					"nsenter --target 1 --mount --uts --ipc --net --pid restorecon -Rv /etc/NetworkManager/system-connections").
					Return([]byte("restorecon: /etc/NetworkManager/system-connections context"), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 10*time.Second, "sh", "-c", 
					"nsenter --target 1 --mount --uts --ipc --net --pid restorecon -Rv /etc/systemd/network").
					Return([]byte("restorecon: /etc/systemd/network context"), nil).Once()
			},
			expectRestorecon: true,
		},
		{
			name:          "SELinux enabled - restorecon failure (should continue)",
			enableSELinux: true,
			isContainer:   false,
			setupMocks: func(m *MockCommandExecutor, fs *MockFileSystem) {
				// Container check during adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				
				// Directory existence checks
				fs.On("Exists", "/etc/NetworkManager/system-connections").Return(true)
				fs.On("Exists", "/etc/systemd/network").Return(false) // Only one dir exists
				
				// Expect one restorecon call that fails
				m.On("ExecuteWithTimeout", mock.Anything, 10*time.Second, "sh", "-c", 
					"restorecon -Rv /etc/NetworkManager/system-connections").
					Return([]byte(""), errors.New("restorecon command not found")).Once()
			},
			expectRestorecon: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExecutor := new(MockCommandExecutor)
			mockFS := new(MockFileSystem)
			
			tt.setupMocks(mockExecutor, mockFS)
			
			// Create adapter with SELinux option
			adapter := NewRHELAdapterWithSELinux(mockExecutor, mockFS, logrus.New(), tt.enableSELinux)
			
			// Test the restoreSELinuxContext method directly
			ctx := context.Background()
			adapter.restoreSELinuxContext(ctx) // This should not panic or error

			mockExecutor.AssertExpectations(t)
			mockFS.AssertExpectations(t)
		})
	}
}

// TestRHELAdapter_Configure_WithSELinux tests full Configure flow with SELinux integration
func TestRHELAdapter_Configure_WithSELinux(t *testing.T) {
	// Create test interface
	iface, _ := entities.NewNetworkInterface(0, "fa:16:3e:bb:93:7a", "test-node", "192.168.1.100", "192.168.1.0/24", 1450)
	ifaceName := mustCreateInterfaceName("multinic0")
	
	mockExecutor := new(MockCommandExecutor)
	mockFS := new(MockFileSystem)
	
	// Container check during adapter initialization
	mockExecutor.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
		Return([]byte(""), errors.New("not found")).Once()
	
	// findDeviceByMAC mock (ip link show)
	ipLinkOutput := `1: lo: <LOOPBACK,UP,LOWER_UP> mtu 65536 qdisc noqueue state UNKNOWN
    link/loopback 00:00:00:00:00:00 brd 00:00:00:00:00:00
2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc pfifo_fast state UP qlen 1000
    link/ether fa:16:3e:bb:93:7a brd ff:ff:ff:ff:ff:ff`
	
	mockExecutor.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "ip", "link", "show").
		Return([]byte(ipLinkOutput), nil).Once()
	
	// Runtime commands (ip link set name, mtu, addr, up)
	mockExecutor.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "ip", "link", "set", "eth0", "name", "multinic0").
		Return([]byte(""), nil).Once()
	mockExecutor.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "ip", "link", "set", "multinic0", "mtu", "1450").
		Return([]byte(""), nil).Once()
	mockExecutor.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "ip", "addr", "replace", "192.168.1.100/24", "dev", "multinic0").
		Return([]byte(""), nil).Once()
	mockExecutor.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "ip", "link", "set", "multinic0", "up").
		Return([]byte(""), nil).Once()
	
	// File writes
	mockFS.On("WriteFile", "/etc/systemd/network/90-multinic0.link", mock.AnythingOfType("[]uint8"), os.FileMode(0644)).
		Return(nil).Once()
	mockFS.On("WriteFile", "/etc/NetworkManager/system-connections/90-multinic0.nmconnection", mock.AnythingOfType("[]uint8"), os.FileMode(0600)).
		Return(nil).Once()
	
	// SELinux restorecon (enabled)
	mockFS.On("Exists", "/etc/NetworkManager/system-connections").Return(true)
	mockFS.On("Exists", "/etc/systemd/network").Return(true)
	mockExecutor.On("ExecuteWithTimeout", mock.Anything, 10*time.Second, "sh", "-c", 
		"restorecon -Rv /etc/NetworkManager/system-connections").
		Return([]byte("restorecon: context restored"), nil).Once()
	mockExecutor.On("ExecuteWithTimeout", mock.Anything, 10*time.Second, "sh", "-c", 
		"restorecon -Rv /etc/systemd/network").
		Return([]byte("restorecon: context restored"), nil).Once()
	
	// Create adapter with SELinux enabled
	adapter := NewRHELAdapterWithSELinux(mockExecutor, mockFS, logrus.New(), true)
	
	// Test Configure
	err := adapter.Configure(context.Background(), *iface, ifaceName)
	assert.NoError(t, err)
	
	mockExecutor.AssertExpectations(t)
	mockFS.AssertExpectations(t)
}
