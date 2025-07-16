package usecases

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"multinic-agent/internal/domain/entities"
	"multinic-agent/internal/domain/interfaces"
	"multinic-agent/internal/domain/services"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// All mock types are declared in configure_network_test.go

func TestDeleteNetworkUseCase_Execute_NetplanFileCleanup_Success(t *testing.T) {
	// Arrange
	mockOSDetector := new(MockOSDetector)
	mockRollbacker := new(MockNetworkRollbacker)
	mockFileSystem := new(MockFileSystem)
	mockExecutor := new(MockCommandExecutor)
	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	mockRepository := new(MockNetworkInterfaceRepository)
	// 기본 컨테이너 환경 체크 설정
	mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "test", "-d", "/host").Return([]byte{}, fmt.Errorf("not in container")).Maybe()
	// RHEL nmcli 명령어 mocks (naming service에서 사용)
	mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "nmcli", "-t", "-f", "NAME", "c", "show").Return([]byte(""), nil).Maybe()
	namingService := services.NewInterfaceNamingService(mockFileSystem, mockExecutor)
	useCase := NewDeleteNetworkUseCase(mockOSDetector, mockRollbacker, namingService, mockRepository, mockFileSystem, logger)

	ctx := context.Background()
	input := DeleteNetworkInput{NodeName: "test-node"}

	mockOSDetector.On("DetectOS").Return(interfaces.OSTypeUbuntu, nil)

	// Setup hostname
	mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "hostname", mock.Anything).Return([]byte("test-node\n"), nil)

	// Setup netplan files
	netplanFiles := []string{"91-multinic1.yaml", "92-multinic2.yaml"}
	mockFileSystem.On("ListFiles", "/etc/netplan").Return(netplanFiles, nil)

	// Setup active interfaces from DB (only multinic1 is active)
	activeInterfaces := []entities.NetworkInterface{
		{ID: 1, MacAddress: "fa:16:3e:11:11:11", AttachedNodeName: "test-node"},
	}
	mockRepository.On("GetAllNodeInterfaces", ctx, "test-node").Return(activeInterfaces, nil)

	// Setup netplan file content for multinic1 (active)
	multinic1Content := `network:
  ethernets:
    multinic1:
      match:
        macaddress: fa:16:3e:11:11:11
      dhcp4: false
  version: 2`
	mockFileSystem.On("ReadFile", "/etc/netplan/91-multinic1.yaml").Return([]byte(multinic1Content), nil)

	// Setup netplan file content for multinic2 (orphan)
	multinic2Content := `network:
  ethernets:
    multinic2:
      match:
        macaddress: fa:16:3e:22:22:22
      dhcp4: false
  version: 2`
	mockFileSystem.On("ReadFile", "/etc/netplan/92-multinic2.yaml").Return([]byte(multinic2Content), nil)

	mockRollbacker.On("Rollback", ctx, "multinic2").Return(nil)

	// Act
	output, err := useCase.Execute(ctx, input)

	// Assert
	assert.NoError(t, err)
	assert.NotNil(t, output)
	assert.Equal(t, 1, output.TotalDeleted)
	assert.Equal(t, []string{"multinic2"}, output.DeletedInterfaces)
	mockRepository.AssertExpectations(t)
	mockFileSystem.AssertExpectations(t)
	mockRollbacker.AssertExpectations(t)
}

func TestDeleteNetworkUseCase_Execute_NmcliCleanup_Success(t *testing.T) {
	t.Skip("RHEL now uses ifcfg files, not nmcli connections")
	// Arrange
	mockOSDetector := new(MockOSDetector)
	mockRollbacker := new(MockNetworkRollbacker)
	mockFileSystem := new(MockFileSystem)
	mockExecutor := new(MockCommandExecutor)
	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	mockRepository := new(MockNetworkInterfaceRepository)
	// 기본 컨테이너 환경 체크 설정
	mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "test", "-d", "/host").Return([]byte{}, fmt.Errorf("not in container")).Maybe()
	// RHEL nmcli 명령어 mocks (naming service에서 사용)
	mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "nmcli", "-t", "-f", "NAME", "c", "show").Return([]byte(""), nil).Maybe()
	namingService := services.NewInterfaceNamingService(mockFileSystem, mockExecutor)
	useCase := NewDeleteNetworkUseCase(mockOSDetector, mockRollbacker, namingService, mockRepository, mockFileSystem, logger)

	ctx := context.Background()
	input := DeleteNetworkInput{NodeName: "rhel-node"}

	mockOSDetector.On("DetectOS").Return(interfaces.OSTypeRHEL, nil)

	// Setup hostname
	mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "hostname").Return([]byte("rhel-node\n"), nil)

	// Mock GetActiveInterfaces for RHEL nmcli cleanup
	activeInterfaces := []entities.NetworkInterface{
		{
			ID:               1,
			MacAddress:       "00:11:22:33:44:55",
			AttachedNodeName: "rhel-node",
			Status:           entities.StatusConfigured,
		},
	}
	mockRepository.On("GetActiveInterfaces", ctx, "rhel-node").Return(activeInterfaces, nil)

	// Mock nmcli connection list
	nmcliConnections := []string{"multinic0", "multinic1"}
	mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "nmcli", "-t", "-f", "NAME", "c", "show").Return([]byte(strings.Join(nmcliConnections, "\n")), nil)

	// Mock MAC address retrieval from nmcli
	// multinic0 has the MAC that exists in DB
	mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "nmcli", "-t", "-f", "802-3-ethernet.mac-address", "connection", "show", "multinic0").
		Return([]byte("802-3-ethernet.mac-address:00:11:22:33:44:55"), nil)

	// multinic1 has a MAC that doesn't exist in DB (orphan)
	mockExecutor.On("ExecuteWithTimeout", mock.Anything, mock.Anything, "nmcli", "-t", "-f", "802-3-ethernet.mac-address", "connection", "show", "multinic1").
		Return([]byte("802-3-ethernet.mac-address:AA:BB:CC:DD:EE:FF"), nil)

	mockRollbacker.On("Rollback", ctx, "multinic1").Return(nil)

	// Act
	output, err := useCase.Execute(ctx, input)

	// Assert
	assert.NoError(t, err)
	assert.NotNil(t, output)
	assert.Equal(t, 1, output.TotalDeleted)
	assert.Equal(t, []string{"multinic1"}, output.DeletedInterfaces)
	mockExecutor.AssertExpectations(t)
	mockRollbacker.AssertExpectations(t)
}
