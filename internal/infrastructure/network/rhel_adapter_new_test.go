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
				// Container check for adapter initialization - now always returns false
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
				
				// Rename device operations
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "ip", "link", "set", "eth1", "down").
					Return([]byte(""), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "ip", "link", "set", "eth1", "name", "multinic0").
					Return([]byte(""), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "ip", "link", "set", "multinic0", "up").
					Return([]byte(""), nil).Once()
				
				// File write operation
				fs.On("WriteFile", "/etc/sysconfig/network-scripts/ifcfg-multinic0", mock.AnythingOfType("[]uint8"), os.FileMode(0644)).
					Return(nil).Once()
				
				// Verify file exists after write
				fs.On("Exists", "/etc/sysconfig/network-scripts/ifcfg-multinic0").
					Return(true).Once()
				
				// No NetworkManager restart needed
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
				// Container check for adapter initialization - now always returns false
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
				
				// Rename operations succeed
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "ip", "link", "set", "eth1", "down").
					Return([]byte(""), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "ip", "link", "set", "eth1", "name", "multinic0").
					Return([]byte(""), nil).Once()
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "ip", "link", "set", "multinic0", "up").
					Return([]byte(""), nil).Once()
				
				// File write fails
				fs.On("WriteFile", "/etc/sysconfig/network-scripts/ifcfg-multinic0", mock.AnythingOfType("[]uint8"), os.FileMode(0644)).
					Return(errors.New("permission denied")).Once()
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

func TestRHELAdapter_Validate(t *testing.T) {
	tests := []struct {
		name          string
		interfaceName entities.InterfaceName
		setupMocks    func(*MockCommandExecutor, *MockFileSystem)
		wantErr       bool
	}{
		{
			name:           "인터페이스가 존재함",
			interfaceName: mustCreateInterfaceName("multinic0"),
			setupMocks: func(m *MockCommandExecutor, fs *MockFileSystem) {
				// Container check for adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				
				// Interface exists
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "ip", "link", "show", "multinic0").
					Return([]byte("3: multinic0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500"), nil).Once()
				
				// Config file exists
				fs.On("Exists", "/etc/sysconfig/network-scripts/ifcfg-multinic0").
					Return(true).Once()
			},
			wantErr: false,
		},
		{
			name:           "인터페이스가 존재하지 않음",
			interfaceName: mustCreateInterfaceName("multinic0"),
			setupMocks: func(m *MockCommandExecutor, fs *MockFileSystem) {
				// Container check for adapter initialization
				m.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
					Return([]byte(""), errors.New("not found")).Once()
				
				// Interface doesn't exist
				m.On("ExecuteWithTimeout", mock.Anything, 30*time.Second, "ip", "link", "show", "multinic0").
					Return([]byte(""), errors.New("Device \"multinic0\" does not exist")).Once()
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExecutor := new(MockCommandExecutor)
			mockFS := new(MockFileSystem)
			tt.setupMocks(mockExecutor, mockFS)

			adapter := NewRHELAdapter(mockExecutor, mockFS, logrus.New())
			
			err := adapter.Validate(context.Background(), tt.interfaceName)

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