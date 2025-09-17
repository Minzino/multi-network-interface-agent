package usecases

import (
	"context"
	"fmt"
	"testing"

	"multinic-agent/internal/domain/entities"
	"multinic-agent/internal/domain/interfaces"
	"multinic-agent/internal/domain/services"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// All mock types are declared in configure_network_test.go

func TestDeleteNetworkUseCase_Execute(t *testing.T) {
	tests := []struct {
		name           string
		input          DeleteNetworkInput
		osType         interfaces.OSType
		osError        error
		setupMocks     func(*MockOSDetector, *MockNetworkRollbacker, *MockFileSystem, *MockCommandExecutor, *MockNetworkInterfaceRepository)
		expectedOutput *DeleteNetworkOutput
		wantError      bool
		errorContains  string
	}{
		{
			name: "Ubuntu_고아_인터페이스_정리_성공",
			input: DeleteNetworkInput{
				NodeName: "test-node",
			},
			osType:  interfaces.OSTypeUbuntu,
			osError: nil,
			setupMocks: func(osDetector *MockOSDetector, rollbacker *MockNetworkRollbacker, fs *MockFileSystem, executor *MockCommandExecutor, repo *MockNetworkInterfaceRepository) {
				osDetector.On("DetectOS").Return(interfaces.OSTypeUbuntu, nil)
				
				// 호스트명 조회 - GetHostname()에서 호출
				executor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "hostname").Return([]byte("test-node\n"), nil)
				
				// netplan 파일 목록 조회
				fs.On("ListFiles", "/etc/netplan").Return([]string{
					"91-multinic0.yaml", "92-multinic1.yaml", "93-multinic2.yaml",
				}, nil)
				
				// DB에서 활성 인터페이스 조회 (GetAllNodeInterfaces 사용)
				activeInterfacePointers := []*entities.NetworkInterface{
					createTestInterface(0, "test-node", "00:11:22:33:44:00", "10.10.10.10", "10.10.10.0/24", 1500),
					createTestInterface(1, "test-node", "00:11:22:33:44:01", "10.10.10.11", "10.10.10.0/24", 1500),
				}
				// Mock에서 기대하는 타입으로 변환
				activeInterfaces := make([]entities.NetworkInterface, len(activeInterfacePointers))
				for i, iface := range activeInterfacePointers {
					activeInterfaces[i] = *iface
				}
				repo.On("GetAllNodeInterfaces", mock.Anything, "test-node").Return(activeInterfaces, nil)
				
				// multinic2는 고아 인터페이스이므로 삭제 - MAC 주소가 파일에 있어야 함
				fs.On("ReadFile", "/etc/netplan/91-multinic0.yaml").Return([]byte(`network:
  version: 2
  ethernets:
    multinic0:
      addresses: [10.10.10.10/24]
      match:
        macaddress: "00:11:22:33:44:00"`), nil).Maybe()
				fs.On("ReadFile", "/etc/netplan/92-multinic1.yaml").Return([]byte(`network:
  version: 2
  ethernets:
    multinic1:
      addresses: [10.10.10.11/24]
      match:
        macaddress: "00:11:22:33:44:01"`), nil).Maybe()
				fs.On("ReadFile", "/etc/netplan/93-multinic2.yaml").Return([]byte(`network:
  version: 2
  ethernets:
    multinic2:
      addresses: [10.10.10.12/24]
      match:
        macaddress: "00:11:22:33:44:02"`), nil)
				
				// 네트워크 인터페이스 롤백 (삭제된 인터페이스에 대해) - Rollback이 파일 삭제와 netplan apply를 모두 처리
				rollbacker.On("Rollback", mock.Anything, "multinic2").Return(nil)
			},
			expectedOutput: &DeleteNetworkOutput{
				DeletedInterfaces: []string{"multinic2"},
				TotalDeleted:      1,
				Errors:            []error{},
			},
			wantError: false,
		},
		{
			name: "RHEL_고아_인터페이스_정리_성공",
			input: DeleteNetworkInput{
				NodeName: "test-node",
			},
			osType:  interfaces.OSTypeRHEL,
			osError: nil,
			setupMocks: func(osDetector *MockOSDetector, rollbacker *MockNetworkRollbacker, fs *MockFileSystem, executor *MockCommandExecutor, repo *MockNetworkInterfaceRepository) {
				osDetector.On("DetectOS").Return(interfaces.OSTypeRHEL, nil)
				
				// 호스트명 조회 - GetHostname()에서 호출
				executor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "hostname").Return([]byte("test-node\n"), nil)
				
				// ifcfg 파일 목록 조회
				fs.On("ListFiles", "/etc/sysconfig/network-scripts").Return([]string{
					"ifcfg-multinic0", "ifcfg-multinic1", "ifcfg-multinic2",
				}, nil)
				
				// DB에서 활성 인터페이스 조회 (multinic0만 활성)
				activeInterfacePointers := []*entities.NetworkInterface{
					createTestInterface(0, "test-node", "00:11:22:33:44:00", "10.10.10.10", "10.10.10.0/24", 1500),
				}
				// Mock에서 기대하는 타입으로 변환
				activeInterfaces := make([]entities.NetworkInterface, len(activeInterfacePointers))
				for i, iface := range activeInterfacePointers {
					activeInterfaces[i] = *iface
				}
				repo.On("GetAllNodeInterfaces", mock.Anything, "test-node").Return(activeInterfaces, nil)
				
				// ifcfg 파일들의 MAC 주소 정보 - MAC 주소를 통한 고아 인터페이스 감지
				fs.On("ReadFile", "/etc/sysconfig/network-scripts/ifcfg-multinic0").Return([]byte(`HWADDR="00:11:22:33:44:00"
BOOTPROTO=static
IPADDR=10.10.10.10
NETMASK=255.255.255.0
ONBOOT=yes`), nil)
				fs.On("ReadFile", "/etc/sysconfig/network-scripts/ifcfg-multinic1").Return([]byte(`HWADDR="00:11:22:33:44:01"
BOOTPROTO=static
IPADDR=10.10.10.11
NETMASK=255.255.255.0
ONBOOT=yes`), nil)
				fs.On("ReadFile", "/etc/sysconfig/network-scripts/ifcfg-multinic2").Return([]byte(`HWADDR="00:11:22:33:44:02"
BOOTPROTO=static
IPADDR=10.10.10.12
NETMASK=255.255.255.0
ONBOOT=yes`), nil)
				
				// 네트워크 인터페이스 롤백 (삭제된 인터페이스에 대해) - Rollback이 파일 삭제와 NetworkManager 재시작을 모두 처리
				rollbacker.On("Rollback", mock.Anything, "multinic1").Return(nil)
				rollbacker.On("Rollback", mock.Anything, "multinic2").Return(nil)
			},
			expectedOutput: &DeleteNetworkOutput{
				DeletedInterfaces: []string{"multinic1", "multinic2"},
				TotalDeleted:      2,
				Errors:            []error{},
			},
			wantError: false,
		},
		{
			name: "전체_정리_모드_Ubuntu",
			input: DeleteNetworkInput{
				NodeName:    "test-node",
				FullCleanup: true,
			},
			osType:  interfaces.OSTypeUbuntu,
			osError: nil,
			setupMocks: func(osDetector *MockOSDetector, rollbacker *MockNetworkRollbacker, fs *MockFileSystem, executor *MockCommandExecutor, repo *MockNetworkInterfaceRepository) {
				osDetector.On("DetectOS").Return(interfaces.OSTypeUbuntu, nil)
				
				// 전체 netplan 파일 삭제
				fs.On("ListFiles", "/etc/netplan").Return([]string{
					"91-multinic0.yaml", "92-multinic1.yaml", "93-multinic2.yaml",
				}, nil)
				
				// 네트워크 인터페이스 롤백 (전체 정리이므로 모든 인터페이스 롤백) - Rollback이 파일 삭제와 netplan apply를 모두 처리
				rollbacker.On("Rollback", mock.Anything, "multinic0").Return(nil)
				rollbacker.On("Rollback", mock.Anything, "multinic1").Return(nil)
				rollbacker.On("Rollback", mock.Anything, "multinic2").Return(nil)
			},
			expectedOutput: &DeleteNetworkOutput{
				DeletedInterfaces: []string{"multinic0", "multinic1", "multinic2"},
				TotalDeleted:      3,
				Errors:            []error{},
			},
			wantError: false,
		},
		{
			name: "OS_감지_실패",
			input: DeleteNetworkInput{
				NodeName: "test-node",
			},
			osType:  "",
			osError: fmt.Errorf("OS detection failed"),
			setupMocks: func(osDetector *MockOSDetector, rollbacker *MockNetworkRollbacker, fs *MockFileSystem, executor *MockCommandExecutor, repo *MockNetworkInterfaceRepository) {
				osDetector.On("DetectOS").Return(interfaces.OSType(""), fmt.Errorf("OS detection failed"))
			},
			expectedOutput: nil,
			wantError:      true,
			errorContains:  "failed to detect OS",
		},
		{
			name: "지원하지_않는_OS_타입",
			input: DeleteNetworkInput{
				NodeName: "test-node",
			},
			osType:  "unsupported",
			osError: nil,
			setupMocks: func(osDetector *MockOSDetector, rollbacker *MockNetworkRollbacker, fs *MockFileSystem, executor *MockCommandExecutor, repo *MockNetworkInterfaceRepository) {
				osDetector.On("DetectOS").Return(interfaces.OSType("unsupported"), nil)
			},
			expectedOutput: &DeleteNetworkOutput{
				DeletedInterfaces: []string{},
				TotalDeleted:      0,
				Errors:            []error{},
			},
			wantError: false,
		},
		{
			name: "DB_조회_실패",
			input: DeleteNetworkInput{
				NodeName: "test-node",
			},
			osType:  interfaces.OSTypeUbuntu,
			osError: nil,
			setupMocks: func(osDetector *MockOSDetector, rollbacker *MockNetworkRollbacker, fs *MockFileSystem, executor *MockCommandExecutor, repo *MockNetworkInterfaceRepository) {
				osDetector.On("DetectOS").Return(interfaces.OSTypeUbuntu, nil)
				
				executor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "hostname").Return([]byte("test-node\n"), nil)
				fs.On("ListFiles", "/etc/netplan").Return([]string{}, nil)
				
				repo.On("GetAllNodeInterfaces", mock.Anything, "test-node").Return([]entities.NetworkInterface{}, fmt.Errorf("database connection failed"))
			},
			expectedOutput: nil,
			wantError:      true,
			errorContains:  "database connection failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mock 객체들 생성
			mockOSDetector := new(MockOSDetector)
			mockRollbacker := new(MockNetworkRollbacker)
			mockFS := new(MockFileSystem)
			mockExecutor := new(MockCommandExecutor)
			mockRepo := new(MockNetworkInterfaceRepository)

			// 테스트별 Mock 설정을 먼저 호출 (우선순위 보장)
			tt.setupMocks(mockOSDetector, mockRollbacker, mockFS, mockExecutor, mockRepo)

			// 기본 mocks 설정 (InterfaceNamingService용) - Maybe()로 설정하여 충돌 방지
			// 기본 컨테이너 환경 체크 설정
			mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "test", "-d", "/host").Return([]byte{}, fmt.Errorf("not in container")).Maybe()
			// RHEL nmcli 명령어 mocks (naming service에서 사용)
			mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "nmcli", "-t", "-f", "NAME", "c", "show").Return([]byte(""), nil).Maybe()

			// ReserveNamesForInterfaces가 호출될 수도 있으므로 파일시스템 mock 추가
			for i := 0; i < 10; i++ {
				mockFS.On("Exists", fmt.Sprintf("/sys/class/net/multinic%d", i)).Return(false).Maybe()
			}

			// 네이밍 서비스 생성
			namingService := services.NewInterfaceNamingService(mockFS, mockExecutor)

			// 로거 생성
			logger := logrus.New()
			logger.SetLevel(logrus.FatalLevel)

			// 유스케이스 생성
			useCase := NewDeleteNetworkUseCase(
				mockOSDetector,
				mockRollbacker,
				namingService,
				mockRepo,
				mockFS,
				logger,
			)

			// 실행
			result, err := useCase.Execute(context.Background(), tt.input)

			// 검증
			if tt.wantError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				require.NotNil(t, result)
				assert.Equal(t, tt.expectedOutput.TotalDeleted, result.TotalDeleted)
				assert.Equal(t, len(tt.expectedOutput.DeletedInterfaces), len(result.DeletedInterfaces))
				
				// 삭제된 인터페이스 목록 검증 (순서 무관)
				for _, expectedInterface := range tt.expectedOutput.DeletedInterfaces {
					assert.Contains(t, result.DeletedInterfaces, expectedInterface)
				}
				
				assert.Equal(t, len(tt.expectedOutput.Errors), len(result.Errors))
			}

			// Mock 호출 검증
			mockOSDetector.AssertExpectations(t)
			mockRollbacker.AssertExpectations(t)
			mockFS.AssertExpectations(t)
			mockExecutor.AssertExpectations(t)
			mockRepo.AssertExpectations(t)
		})
	}
}

// 고아 인터페이스 감지 로직 테스트 (개별 메서드)
func TestDeleteNetworkUseCase_OrphanDetection(t *testing.T) {
	tests := []struct {
		name              string
		netplanFiles      []string
		activeInterfaces  []*entities.NetworkInterface
		expectedOrphans   []string
	}{
		{
			name: "모든_인터페이스_활성_상태",
			netplanFiles: []string{"91-multinic0.yaml", "92-multinic1.yaml"},
			activeInterfaces: []*entities.NetworkInterface{
				createTestInterface(0, "test-node", "00:11:22:33:44:00", "10.10.10.10", "10.10.10.0/24", 1500),
				createTestInterface(1, "test-node", "00:11:22:33:44:01", "10.10.10.11", "10.10.10.0/24", 1500),
			},
			expectedOrphans: []string{},
		},
		{
			name: "일부_인터페이스_고아_상태",
			netplanFiles: []string{"91-multinic0.yaml", "92-multinic1.yaml", "93-multinic2.yaml"},
			activeInterfaces: []*entities.NetworkInterface{
				createTestInterface(0, "test-node", "00:11:22:33:44:00", "10.10.10.10", "10.10.10.0/24", 1500),
			},
			expectedOrphans: []string{"multinic1", "multinic2"},
		},
		{
			name: "모든_인터페이스_고아_상태",
			netplanFiles: []string{"91-multinic0.yaml", "92-multinic1.yaml"},
			activeInterfaces: []*entities.NetworkInterface{},
			expectedOrphans: []string{"multinic0", "multinic1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 파일명에서 인터페이스명 추출
			var fileInterfaces []string
			for _, filename := range tt.netplanFiles {
				if len(filename) > 5 && filename[2] == '-' && filename[0] == '9' {
					// "91-multinic0.yaml" -> "multinic0"
					interfaceName := filename[3 : len(filename)-5] // 9X- 제거, .yaml 제거
					fileInterfaces = append(fileInterfaces, interfaceName)
				}
			}

			// 활성 인터페이스명 추출
			activeInterfaceNames := make(map[string]bool)
			for _, iface := range tt.activeInterfaces {
				activeInterfaceNames[iface.InterfaceName()] = true
			}

			// 고아 인터페이스 감지
			var actualOrphans []string
			for _, interfaceName := range fileInterfaces {
				if !activeInterfaceNames[interfaceName] {
					actualOrphans = append(actualOrphans, interfaceName)
				}
			}

			// 검증
			assert.Equal(t, len(tt.expectedOrphans), len(actualOrphans))
			for _, expectedOrphan := range tt.expectedOrphans {
				assert.Contains(t, actualOrphans, expectedOrphan)
			}
		})
	}
}