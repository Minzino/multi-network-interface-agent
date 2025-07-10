package usecases

import (
	"context"
	"fmt"
	"multinic-agent/internal/domain/interfaces"
	"multinic-agent/internal/domain/services"
	"strings"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// DeleteNetworkInput은 네트워크 삭제 유스케이스의 입력 데이터입니다
type DeleteNetworkInput struct {
	NodeName string
}

// DeleteNetworkOutput은 네트워크 삭제 유스케이스의 출력 데이터입니다
type DeleteNetworkOutput struct {
	DeletedInterfaces []string
	TotalDeleted      int
	Errors            []error
}

// DeleteNetworkUseCase는 고아 인터페이스를 감지하고 삭제하는 유스케이스입니다
type DeleteNetworkUseCase struct {
	osDetector    interfaces.OSDetector
	rollbacker    interfaces.NetworkRollbacker
	namingService *services.InterfaceNamingService
	repository    interfaces.NetworkInterfaceRepository
	fileSystem    interfaces.FileSystem
	logger        *logrus.Logger
}

// NewDeleteNetworkUseCase는 새로운 DeleteNetworkUseCase를 생성합니다
func NewDeleteNetworkUseCase(
	osDetector interfaces.OSDetector,
	rollbacker interfaces.NetworkRollbacker,
	namingService *services.InterfaceNamingService,
	repository interfaces.NetworkInterfaceRepository,
	fileSystem interfaces.FileSystem,
	logger *logrus.Logger,
) *DeleteNetworkUseCase {
	return &DeleteNetworkUseCase{
		osDetector:    osDetector,
		rollbacker:    rollbacker,
		namingService: namingService,
		repository:    repository,
		fileSystem:    fileSystem,
		logger:        logger,
	}
}

// Execute는 고아 인터페이스 삭제 유스케이스를 실행합니다
func (uc *DeleteNetworkUseCase) Execute(ctx context.Context, input DeleteNetworkInput) (*DeleteNetworkOutput, error) {
	// 삭제 프로세스 시작 로그는 실제 삭제가 있을 때만 출력

	osType, err := uc.osDetector.DetectOS()
	if err != nil {
		return nil, fmt.Errorf("failed to detect OS: %w", err)
	}

	switch osType {
	case interfaces.OSTypeUbuntu:
		return uc.executeNetplanCleanup(ctx, input)
	case interfaces.OSTypeRHEL:
		return uc.executeNmcliCleanup(ctx, input)
	default:
		uc.logger.WithField("os_type", osType).Warn("Skipping orphaned interface cleanup for unsupported OS type")
		return &DeleteNetworkOutput{}, nil
	}
}

// executeNetplanCleanup은 Netplan (Ubuntu) 환경의 고아 인터페이스를 정리합니다
func (uc *DeleteNetworkUseCase) executeNetplanCleanup(ctx context.Context, input DeleteNetworkInput) (*DeleteNetworkOutput, error) {
	output := &DeleteNetworkOutput{
		DeletedInterfaces: []string{},
		Errors:            []error{},
	}

	orphanedFiles, err := uc.findOrphanedNetplanFiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to find orphaned netplan files: %w", err)
	}

	if len(orphanedFiles) == 0 {
		// 삭제할 파일이 없으면 조용히 종료
		return output, nil
	}

	uc.logger.WithFields(logrus.Fields{
		"node_name":      input.NodeName,
		"orphaned_files": len(orphanedFiles),
	}).Info("Orphaned netplan files detected - starting cleanup process")

	for _, fileName := range orphanedFiles {
		interfaceName := uc.extractInterfaceNameFromFile(fileName)
		if err := uc.deleteNetplanFile(ctx, fileName, interfaceName); err != nil {
			uc.logger.WithFields(logrus.Fields{
				"file_name":      fileName,
				"interface_name": interfaceName,
				"error":          err.Error(),
			}).Error("Failed to delete netplan file")
			output.Errors = append(output.Errors, fmt.Errorf("failed to delete netplan file %s: %w", fileName, err))
		} else {
			output.DeletedInterfaces = append(output.DeletedInterfaces, interfaceName)
			output.TotalDeleted++
		}
	}
	return output, nil
}

// executeNmcliCleanup은 nmcli (RHEL) 환경의 고아 인터페이스를 정리합니다
func (uc *DeleteNetworkUseCase) executeNmcliCleanup(ctx context.Context, input DeleteNetworkInput) (*DeleteNetworkOutput, error) {
	output := &DeleteNetworkOutput{
		DeletedInterfaces: []string{},
		Errors:            []error{},
	}

	connections, err := uc.namingService.ListNmcliConnectionNames(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list nmcli connections: %w", err)
	}

	var orphanedConnections []string
	for _, connName := range connections {
		if !strings.HasPrefix(connName, "multinic") {
			continue
		}

		exists, err := uc.checkInterfaceExists(ctx, connName)
		if err != nil {
			uc.logger.WithFields(logrus.Fields{
				"connection_name": connName,
				"error":           err,
			}).Warn("Error occurred while checking interface existence")
			continue
		}

		if !exists {
			orphanedConnections = append(orphanedConnections, connName)
		}
	}

	if len(orphanedConnections) == 0 {
		uc.logger.Debug("No orphaned nmcli connections to delete")
		return output, nil
	}

	uc.logger.WithFields(logrus.Fields{
		"node_name":            input.NodeName,
		"orphaned_connections": orphanedConnections,
	}).Info("Orphaned nmcli connections detected - starting cleanup process")

	for _, connName := range orphanedConnections {
		if err := uc.rollbacker.Rollback(ctx, connName); err != nil {
			uc.logger.WithFields(logrus.Fields{
				"connection_name": connName,
				"error":           err,
			}).Error("Failed to delete nmcli connection")
			output.Errors = append(output.Errors, fmt.Errorf("failed to delete nmcli connection %s: %w", connName, err))
		} else {
			output.DeletedInterfaces = append(output.DeletedInterfaces, connName)
			output.TotalDeleted++
		}
	}
	return output, nil
}

// findOrphanedNetplanFiles는 DB에 없는 MAC 주소의 netplan 파일을 찾습니다
func (uc *DeleteNetworkUseCase) findOrphanedNetplanFiles(ctx context.Context) ([]string, error) {
	var orphanedFiles []string

	// /etc/netplan 디렉토리에서 multinic 관련 파일 스캔
	netplanDir := "/etc/netplan"
	files, err := uc.namingService.ListNetplanFiles(netplanDir)
	if err != nil {
		return nil, fmt.Errorf("failed to scan netplan directory: %w", err)
	}

	// 현재 노드의 모든 활성 인터페이스 가져오기 (DB에서)
	hostname, err := uc.namingService.GetHostname()
	if err != nil {
		return nil, fmt.Errorf("failed to get hostname: %w", err)
	}

	activeInterfaces, err := uc.repository.GetAllNodeInterfaces(ctx, hostname)
	if err != nil {
		return nil, fmt.Errorf("failed to get active interfaces: %w", err)
	}

	// MAC 주소 맵 생성 (빠른 조회를 위해)
	activeMACAddresses := make(map[string]bool)
	for _, iface := range activeInterfaces {
		activeMACAddresses[strings.ToLower(iface.MacAddress)] = true
	}

	for _, fileName := range files {
		// multinic 파일만 처리 (9*-multinic*.yaml 패턴)
		if !uc.isMultinicNetplanFile(fileName) {
			continue
		}

		// 파일의 MAC 주소 확인
		filePath := fmt.Sprintf("%s/%s", netplanDir, fileName)
		macAddress, err := uc.getMACAddressFromNetplanFile(filePath)
		if err != nil {
			uc.logger.WithFields(logrus.Fields{
				"file_name": fileName,
				"error":     err.Error(),
			}).Warn("Failed to extract MAC address from netplan file")
			continue
		}

		// DB에 해당 MAC 주소가 없으면 고아 파일
		if !activeMACAddresses[strings.ToLower(macAddress)] {
			interfaceName := uc.extractInterfaceNameFromFile(fileName)
			uc.logger.WithFields(logrus.Fields{
				"file_name":      fileName,
				"interface_name": interfaceName,
				"mac_address":    macAddress,
			}).Info("Found orphaned netplan file")
			orphanedFiles = append(orphanedFiles, fileName)
		}
	}

	return orphanedFiles, nil
}

// isMultinicNetplanFile은 파일이 multinic 관련 netplan 파일인지 확인합니다
func (uc *DeleteNetworkUseCase) isMultinicNetplanFile(fileName string) bool {
	// 9*-multinic*.yaml 패턴 매칭
	return strings.Contains(fileName, "multinic") && strings.HasSuffix(fileName, ".yaml") &&
		strings.HasPrefix(fileName, "9") && strings.Contains(fileName, "-")
}

// extractInterfaceNameFromFile은 파일명에서 인터페이스 이름을 추출합니다
func (uc *DeleteNetworkUseCase) extractInterfaceNameFromFile(fileName string) string {
	// 예: "91-multinic1.yaml" -> "multinic1" 또는 "multinic1.yaml" -> "multinic1"
	if !strings.Contains(fileName, "multinic") {
		return ""
	}

	// .yaml 확장자 제거
	nameWithoutExt := strings.TrimSuffix(fileName, ".yaml")

	// "-"로 분할된 경우 (예: "91-multinic1")
	parts := strings.Split(nameWithoutExt, "-")
	for _, part := range parts {
		if strings.HasPrefix(part, "multinic") {
			return part
		}
	}

	// 분할되지 않은 경우 전체가 multinic로 시작하는지 확인 (예: "multinic1")
	if strings.HasPrefix(nameWithoutExt, "multinic") {
		return nameWithoutExt
	}

	return ""
}

// checkInterfaceExists는 해당 인터페이스가 실제 시스템에 존재하는지 확인합니다
func (uc *DeleteNetworkUseCase) checkInterfaceExists(ctx context.Context, interfaceName string) (bool, error) {
	// ip addr show {interface} 명령으로 인터페이스 존재 여부 확인
	_, err := uc.namingService.GetMacAddressForInterface(interfaceName)
	if err != nil {
		// 인터페이스가 존재하지 않으면 에러가 발생
		if strings.Contains(err.Error(), "does not exist") ||
			strings.Contains(err.Error(), "failed to get info") {
			return false, nil
		}
		return false, err
	}

	// MAC 주소를 성공적으로 조회했다면 인터페이스가 존재함
	return true, nil
}

// deleteNetplanFile은 고아 netplan 파일을 삭제하고 netplan을 재적용합니다
func (uc *DeleteNetworkUseCase) deleteNetplanFile(ctx context.Context, fileName, interfaceName string) error {
	uc.logger.WithFields(logrus.Fields{
		"file_name":      fileName,
		"interface_name": interfaceName,
	}).Info("Starting to delete orphaned netplan file")

	// Rollback 호출로 파일 삭제 및 netplan 재적용
	if err := uc.rollbacker.Rollback(ctx, interfaceName); err != nil {
		return fmt.Errorf("failed to rollback netplan file: %w", err)
	}

	uc.logger.WithFields(logrus.Fields{
		"file_name":      fileName,
		"interface_name": interfaceName,
	}).Info("Successfully deleted orphaned netplan file")

	return nil
}

// getMACAddressFromNetplanFile은 netplan 파일에서 MAC 주소를 추출합니다
func (uc *DeleteNetworkUseCase) getMACAddressFromNetplanFile(filePath string) (string, error) {
	content, err := uc.fileSystem.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	// Simple YAML structure for netplan files
	type NetplanConfig struct {
		Network struct {
			Ethernets map[string]struct {
				Match struct {
					Macaddress string `yaml:"macaddress"`
				} `yaml:"match"`
			} `yaml:"ethernets"`
		} `yaml:"network"`
	}

	var config NetplanConfig
	if err := yaml.Unmarshal(content, &config); err != nil {
		return "", fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Extract MAC address from the first ethernet configuration
	for _, eth := range config.Network.Ethernets {
		if eth.Match.Macaddress != "" {
			return eth.Match.Macaddress, nil
		}
	}

	return "", fmt.Errorf("MAC address not found")
}
