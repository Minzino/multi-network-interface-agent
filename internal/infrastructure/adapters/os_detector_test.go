package adapters

import (
	"os"
	"testing"

	"multinic-agent/internal/domain/interfaces"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockFileSystemForOSDetector는 OS 감지용 Mock FileSystem입니다
type MockFileSystemForOSDetector struct {
	mock.Mock
}

func (m *MockFileSystemForOSDetector) ReadFile(path string) ([]byte, error) {
	args := m.Called(path)
	// Return value can be nil if the mock is set up to return an error.
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]byte), args.Error(1)
}

func (m *MockFileSystemForOSDetector) WriteFile(path string, data []byte, perm os.FileMode) error {
	args := m.Called(path, data, perm)
	return args.Error(0)
}

func (m *MockFileSystemForOSDetector) Exists(path string) bool {
	args := m.Called(path)
	return args.Bool(0)
}

func (m *MockFileSystemForOSDetector) MkdirAll(path string, perm os.FileMode) error {
	args := m.Called(path, perm)
	return args.Error(0)
}

func (m *MockFileSystemForOSDetector) Remove(path string) error {
	args := m.Called(path)
	return args.Error(0)
}

func (m *MockFileSystemForOSDetector) ListFiles(path string) ([]string, error) {
	args := m.Called(path)
	return args.Get(0).([]string), args.Error(1)
}

// TestDetectOS_Ubuntu_Simple는 Ubuntu 감지 로직만 독립적으로 테스트합니다.
func TestDetectOS_Ubuntu_Simple(t *testing.T) {
	mockFS := new(MockFileSystemForOSDetector)
	detector := NewRealOSDetector(mockFS)

	// 모의 설정: /etc/os-release 파일만 읽도록 설정
	osReleaseContent := "NAME=Ubuntu\nID=ubuntu"
	mockFS.On("ReadFile", "/host/etc/os-release").Return([]byte(osReleaseContent), nil).Once()

	// 테스트 실행
	osType, err := detector.DetectOS()

	// 결과 검증
	assert.NoError(t, err)
	assert.Equal(t, interfaces.OSTypeUbuntu, osType)

	// 모의 객체의 모든 예상 호출이 충족되었는지 확인
	mockFS.AssertExpectations(t)
}

func TestRealOSDetector_DetectOS(t *testing.T) {
	tests := []struct {
		name             string
		osReleaseContent string
		osReleaseError   error
		expectedOS       interfaces.OSType
		expectError      bool
	}{
		{
			name:             "os-release에서 Ubuntu 감지",
			osReleaseContent: "NAME=Ubuntu\nID=ubuntu",
			expectedOS:       interfaces.OSTypeUbuntu,
		},
		{
			name:             "os-release에서 SUSE 감지",
			osReleaseContent: "NAME=\"SUSE Linux Enterprise Server\"\nID=suse",
			expectedOS:       interfaces.OSTypeSUSE,
		},
		{
			name:             "os-release에서 RHEL 감지 (ID 필드)",
			osReleaseContent: "NAME=\"Red Hat Enterprise Linux\"\nID=rhel",
			expectedOS:       interfaces.OSTypeRHEL,
		},
		{
			name:             "os-release에서 RHEL 계열 감지 (ID_LIKE 필드)",
			osReleaseContent: "NAME=\"SUSE Liberty Linux\"\nID=sll\nID_LIKE=fedora",
			expectedOS:       interfaces.OSTypeRHEL,
		},
		{
			name:           "모든 파일 읽기 실패",
			osReleaseError: os.ErrNotExist,
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockFS := new(MockFileSystemForOSDetector)

			mockFS.On("ReadFile", "/host/etc/os-release").Return([]byte(tt.osReleaseContent), tt.osReleaseError).Once()

			detector := NewRealOSDetector(mockFS)
			result, err := detector.DetectOS()

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedOS, result)
			}

			mockFS.AssertExpectations(t)
		})
	}
}