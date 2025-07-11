package network

import (
	"context"
	"errors"
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
				// 1. Delete existing (rollback)
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "down", "multinic0").
					Return([]byte(""), errors.New("not found")).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "delete", "multinic0").
					Return([]byte(""), errors.New("not found")).Once()
				
				// 2. Add new connection
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "add", "type", "ethernet", "con-name", "multinic0", "ifname", "multinic0", "mac", "fa:16:3e:00:be:63").
					Return([]byte("Connection successfully added"), nil).Once()
				
				// 3. Disable IPv4
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "modify", "multinic0", "ipv4.method", "disabled").
					Return([]byte(""), nil).Once()
				
				// 4. Disable IPv6
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "modify", "multinic0", "ipv6.method", "disabled").
					Return([]byte(""), nil).Once()
				
				// 5. Activate connection
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "up", "multinic0").
					Return([]byte("Connection successfully activated"), nil).Once()
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
				// 1. Delete existing (rollback)
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "down", "multinic0").
					Return([]byte(""), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "delete", "multinic0").
					Return([]byte(""), nil).Once()
				
				// 2. Add new connection
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "add", "type", "ethernet", "con-name", "multinic0", "ifname", "multinic0", "mac", "fa:16:3e:00:be:63").
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
				
				// 6. Activate connection
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "up", "multinic0").
					Return([]byte("Connection successfully activated"), nil).Once()
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
				// Rollback
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "down", "multinic0").
					Return([]byte(""), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "delete", "multinic0").
					Return([]byte(""), nil).Once()
				
				// Add fails
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "add", "type", "ethernet", "con-name", "multinic0", "ifname", "multinic0", "mac", "fa:16:3e:00:be:63").
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
				// Initial rollback
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "down", "multinic0").
					Return([]byte(""), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "delete", "multinic0").
					Return([]byte(""), nil).Once()
				
				// Add succeeds
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "add", "type", "ethernet", "con-name", "multinic0", "ifname", "multinic0", "mac", "fa:16:3e:00:be:63").
					Return([]byte(""), nil).Once()
				
				// Disable IPv4
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "modify", "multinic0", "ipv4.method", "disabled").
					Return([]byte(""), nil).Once()
				
				// Disable IPv6
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", 
					"connection", "modify", "multinic0", "ipv6.method", "disabled").
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

			adapter := NewRHELAdapter(mockExecutor, logrus.New())
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
				output := `DEVICE     TYPE      STATE      CONNECTION
eth0       ethernet  connected  eth0
multinic0  ethernet  connected  multinic0
lo         loopback  unmanaged  --`
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "device", "status").
					Return([]byte(output), nil).Once()
			},
			wantErr: false,
		},
		{
			name: "인터페이스가 disconnected 상태",
			setupMocks: func(m *MockCommandExecutor) {
				output := `DEVICE     TYPE      STATE         CONNECTION
eth0       ethernet  connected     eth0
multinic0  ethernet  disconnected  --
lo         loopback  unmanaged     --`
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "device", "status").
					Return([]byte(output), nil).Once()
			},
			wantErr: true,
		},
		{
			name: "인터페이스가 목록에 없음",
			setupMocks: func(m *MockCommandExecutor) {
				output := `DEVICE  TYPE      STATE      CONNECTION
eth0    ethernet  connected  eth0
lo      loopback  unmanaged  --`
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "device", "status").
					Return([]byte(output), nil).Once()
			},
			wantErr: true,
		},
		{
			name: "nmcli 명령 실행 실패",
			setupMocks: func(m *MockCommandExecutor) {
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "device", "status").
					Return([]byte(""), errors.New("command failed")).Once()
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExecutor := new(MockCommandExecutor)
			tt.setupMocks(mockExecutor)

			adapter := NewRHELAdapter(mockExecutor, logrus.New())
			
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

			adapter := NewRHELAdapter(mockExecutor, logrus.New())
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
	adapter := NewRHELAdapter(nil, logrus.New())
	assert.Equal(t, "", adapter.GetConfigDir())
}