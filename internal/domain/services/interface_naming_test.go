package services

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockFileSystem은 FileSystem 인터페이스의 목 구현체입니다
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

// MockCommandExecutor는 CommandExecutor 인터페이스의 목 구현체입니다
type MockCommandExecutor struct {
	mock.Mock
}

func (m *MockCommandExecutor) Execute(ctx context.Context, command string, args ...string) ([]byte, error) {
	mockArgs := m.Called(ctx, command, args)
	return mockArgs.Get(0).([]byte), mockArgs.Error(1)
}

func (m *MockCommandExecutor) ExecuteWithTimeout(ctx context.Context, timeout time.Duration, command string, args ...string) ([]byte, error) {
	// Convert variadic args to []interface{} for mock.Called
	callArgs := []interface{}{ctx, timeout, command}
	for _, arg := range args {
		callArgs = append(callArgs, arg)
	}
	mockArgs := m.Called(callArgs...)
	return mockArgs.Get(0).([]byte), mockArgs.Error(1)
}

func TestInterfaceNamingService_GenerateNextName(t *testing.T) {
	tests := []struct {
		name           string
		existingIfaces []string
		expectedName   string
		wantError      bool
	}{
		{
			name:           "모든 인터페이스가 비어있음 - multinic0 반환",
			existingIfaces: []string{},
			expectedName:   "multinic0",
			wantError:      false,
		},
		{
			name:           "multinic0이 사용 중 - multinic1 반환",
			existingIfaces: []string{"multinic0"},
			expectedName:   "multinic1",
			wantError:      false,
		},
		{
			name:           "multinic0, multinic1이 사용 중 - multinic2 반환",
			existingIfaces: []string{"multinic0", "multinic1"},
			expectedName:   "multinic2",
			wantError:      false,
		},
		{
			name:           "일부 인터페이스가 건너뛰어짐 - 가장 작은 번호 반환",
			existingIfaces: []string{"multinic0", "multinic2", "multinic4"},
			expectedName:   "multinic1",
			wantError:      false,
		},
		{
			name: "모든 인터페이스가 사용 중 - 에러 반환",
			existingIfaces: []string{
				"multinic0", "multinic1", "multinic2", "multinic3", "multinic4",
				"multinic5", "multinic6", "multinic7", "multinic8", "multinic9",
			},
			expectedName: "",
			wantError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockFS := new(MockFileSystem)

			// 순차적으로 호출될 것으로 예상되는 인터페이스들만 Mock 설정
			if tt.wantError {
				// 모든 인터페이스가 사용 중인 경우 - 모든 호출이 true
				for i := 0; i < 10; i++ {
					interfacePath := fmt.Sprintf("/sys/class/net/multinic%d", i)
					mockFS.On("Exists", interfacePath).Return(true).Once()
				}
			} else {
				// 첫 번째 사용 가능한 인터페이스까지만 호출됨
				expectedIndex := 0
				if tt.expectedName == "multinic1" {
					expectedIndex = 1
				} else if tt.expectedName == "multinic2" {
					expectedIndex = 2
				}

				// 0부터 expectedIndex까지 순차적으로 호출
				for i := 0; i <= expectedIndex; i++ {
					interfacePath := fmt.Sprintf("/sys/class/net/multinic%d", i)
					interfaceName := fmt.Sprintf("multinic%d", i)

					// 기존 인터페이스 목록에 있으면 true, 없으면 false
					exists := false
					for _, existing := range tt.existingIfaces {
						if existing == interfaceName {
							exists = true
							break
						}
					}
					mockFS.On("Exists", interfacePath).Return(exists).Once()
				}
			}

			mockExecutor := new(MockCommandExecutor)
			// 기본 컨테이너 환경 체크 설정
			mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "test", "-d", "/host").Return([]byte{}, fmt.Errorf("not in container")).Maybe()
			// RHEL nmcli 명령어 mocks (naming service에서 사용)
			mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "nmcli", "-t", "-f", "NAME", "c", "show").Return([]byte(""), nil).Maybe()
			// 컨테이너 환경에서 nsenter 사용하는 경우도 대비
			mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "nsenter", "--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid", "nmcli", "-t", "-f", "NAME", "c", "show").Return([]byte(""), nil).Maybe()
			service := NewInterfaceNamingService(mockFS, mockExecutor)
			result, err := service.GenerateNextName()

			if tt.wantError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "사용 가능한 인터페이스 이름이 없습니다")
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedName, result.String())
			}

			mockFS.AssertExpectations(t)
		})
	}
}

func TestInterfaceNamingService_isInterfaceInUse(t *testing.T) {
	tests := []struct {
		name          string
		interfaceName string
		exists        bool
		expected      bool
	}{
		{
			name:          "인터페이스가 존재함",
			interfaceName: "multinic0",
			exists:        true,
			expected:      true,
		},
		{
			name:          "인터페이스가 존재하지 않음",
			interfaceName: "multinic1",
			exists:        false,
			expected:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockFS := new(MockFileSystem)
			mockExecutor := new(MockCommandExecutor)
			// 기본 컨테이너 환경 체크 설정
			mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "test", "-d", "/host").Return([]byte{}, fmt.Errorf("not in container")).Maybe()
			// RHEL nmcli 명령어 mocks
			mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "nmcli", "-t", "-f", "NAME", "c", "show").Return([]byte(""), nil).Maybe()
			mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "nsenter", "--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid", "nmcli", "-t", "-f", "NAME", "c", "show").Return([]byte(""), nil).Maybe()
			expectedPath := fmt.Sprintf("/sys/class/net/%s", tt.interfaceName)
			mockFS.On("Exists", expectedPath).Return(tt.exists)

			service := NewInterfaceNamingService(mockFS, mockExecutor)
			result := service.isInterfaceInUse(tt.interfaceName)

			assert.Equal(t, tt.expected, result)
			mockFS.AssertExpectations(t)
		})
	}
}

func TestInterfaceNamingService_GetCurrentMultinicInterfaces_SystemBased(t *testing.T) {
	tests := []struct {
		name          string
		setupMock     func(*MockFileSystem)
		expectedCount int
		expectedNames []string
	}{
		{
			name: "시스템에 multinic0과 multinic2가 존재하는 경우",
			setupMock: func(mockFS *MockFileSystem) {
				// multinic0과 multinic2가 시스템에 존재
				mockFS.On("Exists", "/sys/class/net/multinic0").Return(true)
				mockFS.On("Exists", "/sys/class/net/multinic1").Return(false)
				mockFS.On("Exists", "/sys/class/net/multinic2").Return(true)

				// 나머지 인터페이스들은 존재하지 않음
				for i := 3; i < 10; i++ {
					mockFS.On("Exists", fmt.Sprintf("/sys/class/net/multinic%d", i)).Return(false)
				}
			},
			expectedCount: 2,
			expectedNames: []string{"multinic0", "multinic2"},
		},
		{
			name: "시스템에 multinic1만 존재하는 경우",
			setupMock: func(mockFS *MockFileSystem) {
				// multinic1만 시스템에 존재
				for i := 0; i < 10; i++ {
					if i == 1 {
						mockFS.On("Exists", fmt.Sprintf("/sys/class/net/multinic%d", i)).Return(true)
					} else {
						mockFS.On("Exists", fmt.Sprintf("/sys/class/net/multinic%d", i)).Return(false)
					}
				}
			},
			expectedCount: 1,
			expectedNames: []string{"multinic1"},
		},
		{
			name: "시스템에 multinic 인터페이스가 없는 경우",
			setupMock: func(mockFS *MockFileSystem) {
				// 모든 multinic 인터페이스가 시스템에 존재하지 않음
				for i := 0; i < 10; i++ {
					mockFS.On("Exists", fmt.Sprintf("/sys/class/net/multinic%d", i)).Return(false)
				}
			},
			expectedCount: 0,
			expectedNames: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockFS := new(MockFileSystem)
			mockExecutor := new(MockCommandExecutor)
			// 기본 컨테이너 환경 체크 설정
			mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "test", "-d", "/host").Return([]byte{}, fmt.Errorf("not in container")).Maybe()
			// RHEL nmcli 명령어 mocks
			mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "nmcli", "-t", "-f", "NAME", "c", "show").Return([]byte(""), nil).Maybe()
			mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "nsenter", "--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid", "nmcli", "-t", "-f", "NAME", "c", "show").Return([]byte(""), nil).Maybe()
			tt.setupMock(mockFS)

			service := NewInterfaceNamingService(mockFS, mockExecutor)
			interfaces := service.GetCurrentMultinicInterfaces()

			assert.Equal(t, tt.expectedCount, len(interfaces))

			actualNames := make([]string, len(interfaces))
			for i, iface := range interfaces {
				actualNames[i] = iface.String()
			}

			assert.ElementsMatch(t, tt.expectedNames, actualNames)
			mockFS.AssertExpectations(t)
		})
	}
}

func TestInterfaceNamingService_GetMacAddressForInterface_FromIPCommand(t *testing.T) {
	tests := []struct {
		name          string
		interfaceName string
		setupMock     func(*MockFileSystem, *MockCommandExecutor)
		expectedMac   string
		expectError   bool
	}{
		{
			name:          "ip 명령어로 MAC 주소 추출 성공",
			interfaceName: "multinic0",
			setupMock: func(mockFS *MockFileSystem, mockExecutor *MockCommandExecutor) {
				ipOutput := `2: multinic0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1450 qdisc fq_codel state UP group default qlen 1000
    link/ether fa:16:3e:b1:29:8f brd ff:ff:ff:ff:ff:ff
    inet6 fe80::f816:3eff:feb1:298f/64 scope link
       valid_lft forever preferred_lft forever`
				mockExecutor.On("ExecuteWithTimeout",
					mock.AnythingOfType("*context.timerCtx"),
					mock.AnythingOfType("time.Duration"),
					"ip",
					"addr", "show", "multinic0").Return([]byte(ipOutput), nil)
			},
			expectedMac: "fa:16:3e:b1:29:8f",
			expectError: false,
		},
		{
			name:          "인터페이스가 존재하지 않는 경우",
			interfaceName: "multinic9",
			setupMock: func(mockFS *MockFileSystem, mockExecutor *MockCommandExecutor) {
				mockExecutor.On("ExecuteWithTimeout",
					mock.AnythingOfType("*context.timerCtx"),
					mock.AnythingOfType("time.Duration"),
					"ip",
					"addr", "show", "multinic9").Return([]byte(""), fmt.Errorf("Device \"multinic9\" does not exist"))
			},
			expectedMac: "",
			expectError: true,
		},
		{
			name:          "MAC 주소를 찾을 수 없는 경우",
			interfaceName: "multinic1",
			setupMock: func(mockFS *MockFileSystem, mockExecutor *MockCommandExecutor) {
				ipOutput := `3: multinic1: <BROADCAST,MULTICAST> mtu 1500 qdisc noop state DOWN group default
    link/loopback 00:00:00:00:00:00 brd 00:00:00:00:00:00`
				mockExecutor.On("ExecuteWithTimeout",
					mock.AnythingOfType("*context.timerCtx"),
					mock.AnythingOfType("time.Duration"),
					"ip",
					"addr", "show", "multinic1").Return([]byte(ipOutput), nil)
			},
			expectedMac: "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockFS := new(MockFileSystem)
			mockExecutor := new(MockCommandExecutor)
			// 기본 컨테이너 환경 체크 설정
			mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "test", "-d", "/host").Return([]byte{}, fmt.Errorf("not in container")).Maybe()
			// RHEL nmcli 명령어 mocks
			mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "nmcli", "-t", "-f", "NAME", "c", "show").Return([]byte(""), nil).Maybe()
			mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "nsenter", "--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid", "nmcli", "-t", "-f", "NAME", "c", "show").Return([]byte(""), nil).Maybe()
			tt.setupMock(mockFS, mockExecutor)

			service := NewInterfaceNamingService(mockFS, mockExecutor)
			mac, err := service.GetMacAddressForInterface(tt.interfaceName)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedMac, mac)
			}
			mockFS.AssertExpectations(t)
		})
	}
}

func TestInterfaceNamingService_GetHostname(t *testing.T) {
	tests := []struct {
		name           string
		hostnameOutput string
		expectError    bool
		expectedHost   string
	}{
		{
			name:           "도메인 접미사가 있는 호스트네임",
			hostnameOutput: "biz-master1.novalocal",
			expectError:    false,
			expectedHost:   "biz-master1",
		},
		{
			name:           "도메인 접미사가 없는 호스트네임",
			hostnameOutput: "worker-node",
			expectError:    false,
			expectedHost:   "worker-node",
		},
		{
			name:           "여러 도메인 레벨",
			hostnameOutput: "test.example.com",
			expectError:    false,
			expectedHost:   "test",
		},
		{
			name:           "빈 호스트네임",
			hostnameOutput: "",
			expectError:    true,
			expectedHost:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockFS := new(MockFileSystem)
			mockExecutor := new(MockCommandExecutor)

			// 컨테이너 환경 체크 Mock 추가
			mockExecutor.On("ExecuteWithTimeout", mock.Anything, 1*time.Second, "test", "-d", "/host").
				Return([]byte(""), fmt.Errorf("not found")).Once()

			if tt.expectError && tt.hostnameOutput == "" {
				mockExecutor.On("ExecuteWithTimeout", mock.Anything, 5*time.Second, "hostname").
					Return([]byte(""), nil).Once()
			} else {
				mockExecutor.On("ExecuteWithTimeout", mock.Anything, 5*time.Second, "hostname").
					Return([]byte(tt.hostnameOutput), nil).Once()
			}

			service := NewInterfaceNamingService(mockFS, mockExecutor)
			hostname, err := service.GetHostname()

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedHost, hostname)
			}
			mockExecutor.AssertExpectations(t)
		})
	}
}
