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

func TestRHELAdapter_Configure_DirectFileModification(t *testing.T) {
	tests := []struct {
		name          string
		iface         entities.NetworkInterface
		interfaceName entities.InterfaceName
		setupMocks    func(*MockCommandExecutor, *MockFileSystem)
		wantErr       bool
		errorType     error
	}{
		{
			name: "성공적인 인터페이스 설정 - 직접 파일 수정 방식",
			iface: entities.NetworkInterface{
				MacAddress: "fa:16:3e:00:be:63",
				Address:    "192.168.1.100",
				CIDR:       "192.168.1.0/24",
				MTU:        1500,
			},
			interfaceName: mustCreateInterfaceName("multinic0"),
			setupMocks: func(m *MockCommandExecutor, fs *MockFileSystem) {
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
				
				// File write operation (NEW)
				fs.On("WriteFile", "/etc/NetworkManager/system-connections/multinic0.nmconnection", mock.AnythingOfType("[]uint8"), os.FileMode(0600)).
					Return(nil).Once()
				
				// Reload NetworkManager
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "reload").
					Return([]byte(""), nil).Once()
				
				// Try to activate connection
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "up", "multinic0").
					Return([]byte("Connection successfully activated"), nil).Once()
				
				// Validate connection exists (any state)
				validationOutput := `NAME      UUID                                  TYPE      DEVICE
multinic0 12345678-1234-1234-1234-123456789012  ethernet  eth1
eth0      abcdefgh-abcd-abcd-abcd-abcdefghijkl  ethernet  eth0`
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "show").
					Return([]byte(validationOutput), nil).Once()
			},
			wantErr: false,
		},
		{
			name: "파일 쓰기 실패",
			iface: entities.NetworkInterface{
				MacAddress: "fa:16:3e:00:be:63",
				Address:    "192.168.1.100",
				CIDR:       "192.168.1.0/24",
				MTU:        1500,
			},
			interfaceName: mustCreateInterfaceName("multinic0"),
			setupMocks: func(m *MockCommandExecutor, fs *MockFileSystem) {
				// Container check for adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				
				// Find device by MAC
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
				
				// File write fails
				fs.On("WriteFile", "/etc/NetworkManager/system-connections/multinic0.nmconnection", mock.AnythingOfType("[]uint8"), os.FileMode(0600)).
					Return(errors.New("permission denied")).Once()
			},
			wantErr:   true,
			errorType: &multinicErrors.DomainError{Type: multinicErrors.ErrorTypeNetwork},
		},
		{
			name: "NetworkManager reload 실패",
			iface: entities.NetworkInterface{
				MacAddress: "fa:16:3e:00:be:63",
			},
			interfaceName: mustCreateInterfaceName("multinic0"),
			setupMocks: func(m *MockCommandExecutor, fs *MockFileSystem) {
				// Container check for adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				
				// Find device by MAC
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
				
				// File write succeeds
				fs.On("WriteFile", "/etc/NetworkManager/system-connections/multinic0.nmconnection", mock.AnythingOfType("[]uint8"), os.FileMode(0600)).
					Return(nil).Once()
				
				// Reload fails
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "reload").
					Return([]byte(""), errors.New("reload failed")).Once()
				// Fallback to systemctl also fails
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "systemctl", "reload", "NetworkManager").
					Return([]byte(""), errors.New("systemctl failed")).Once()
			},
			wantErr:   true,
			errorType: &multinicErrors.DomainError{Type: multinicErrors.ErrorTypeNetwork},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExecutor := new(MockCommandExecutor)
			mockFS := new(MockFileSystem)
			tt.setupMocks(mockExecutor, mockFS)

			adapter := NewRHELAdapter(mockExecutor, mockFS, logrus.New())
			
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
			mockFS.AssertExpectations(t)
		})
	}
}

func TestRHELAdapter_ValidateConnectionExists(t *testing.T) {
	tests := []struct {
		name          string
		connectionName string
		setupMocks    func(*MockCommandExecutor)
		wantErr       bool
	}{
		{
			name:           "연결이 존재함",
			connectionName: "multinic0",
			setupMocks: func(m *MockCommandExecutor) {
				// Container check for adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				
				output := `NAME      UUID                                  TYPE      DEVICE
multinic0 12345678-1234-1234-1234-123456789012  ethernet  eth1
eth0      abcdefgh-abcd-abcd-abcd-abcdefghijkl  ethernet  eth0`
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "show").
					Return([]byte(output), nil).Once()
			},
			wantErr: false,
		},
		{
			name:           "연결이 존재하지 않음",
			connectionName: "multinic0",
			setupMocks: func(m *MockCommandExecutor) {
				// Container check for adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				
				output := `NAME      UUID                                  TYPE      DEVICE
eth0      abcdefgh-abcd-abcd-abcd-abcdefghijkl  ethernet  eth0`
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "nmcli", "connection", "show").
					Return([]byte(output), nil).Once()
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExecutor := new(MockCommandExecutor)
			mockFS := new(MockFileSystem)
			tt.setupMocks(mockExecutor)

			adapter := NewRHELAdapter(mockExecutor, mockFS, logrus.New())
			
			err := adapter.validateConnectionExists(context.Background(), tt.connectionName)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockExecutor.AssertExpectations(t)
		})
	}
}