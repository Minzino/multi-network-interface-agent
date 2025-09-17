package usecases

import (
	"context"
	"fmt"
	"multinic-agent/internal/domain/interfaces"
	"multinic-agent/internal/domain/services"
	"multinic-agent/internal/infrastructure/metrics"
	"os"
	"strings"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// DeleteNetworkInput은 네트워크 삭제 유스케이스의 입력 데이터입니다
type DeleteNetworkInput struct {
	NodeName string
	// FullCleanup이 true이면, 고아 판정 없이 multinic 관련 파일 전체를 정리합니다.
	// (예: Job 시작 시 깨끗한 상태를 보장하기 위해 사용)
	FullCleanup bool
}

// DeleteNetworkOutput은 네트워크 삭제 유스케이스의 출력 데이터입니다
type DeleteNetworkOutput struct {
	DeletedInterfaces []string
	TotalDeleted      int
	Errors            []error
}

// DeleteNetworkUseCase는 고아 인터페이스를 감지하고 삭제하는 유스케이스입니다
type DeleteNetworkUseCase struct {
	osDetector         interfaces.OSDetector
	rollbacker         interfaces.NetworkRollbacker
	namingService      *services.InterfaceNamingService
	repository         interfaces.NetworkInterfaceRepository
	fileSystem         interfaces.FileSystem
	logger             *logrus.Logger
	routingCoordinator *services.RoutingCoordinator // 라우팅 전역 직렬화
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
		osDetector:         osDetector,
		rollbacker:         rollbacker,
		namingService:      namingService,
		repository:         repository,
		fileSystem:         fileSystem,
		logger:             logger,
		routingCoordinator: services.NewRoutingCoordinator(logger), // 라우팅 코디네이터 초기화
	}
}

// Execute는 고아 인터페이스 삭제 유스케이스를 실행합니다
func (uc *DeleteNetworkUseCase) Execute(ctx context.Context, input DeleteNetworkInput) (*DeleteNetworkOutput, error) {
	osType, err := uc.osDetector.DetectOS()
	if err != nil {
		return nil, fmt.Errorf("failed to detect OS: %w", err)
	}

	// 전체 정리 모드: 입력 플래그 또는 환경변수(AGENT_ACTION=cleanup)로 트리거
	if input.FullCleanup || os.Getenv("AGENT_ACTION") == "cleanup" {
		uc.logger.Info("Cleanup mode: removing all multinic interface files")
		switch osType {
		case interfaces.OSTypeUbuntu:
			return uc.executeFullNetplanCleanup(ctx, input)
		case interfaces.OSTypeRHEL:
			return uc.executeFullIfcfgCleanup(ctx, input)
		default:
			uc.logger.WithField("os_type", osType).Warn("Skipping cleanup for unsupported OS type")
			return &DeleteNetworkOutput{}, nil
		}
	}

	// 일반 모드: DB 기반 고아 인터페이스 감지
	switch osType {
	case interfaces.OSTypeUbuntu:
		return uc.executeNetplanCleanup(ctx, input)
	case interfaces.OSTypeRHEL:
		return uc.executeIfcfgCleanup(ctx, input)
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
			metrics.OrphanedInterfacesDeleted.Inc()
		}
	}
	return output, nil
}

// executeIfcfgCleanup은 ifcfg (RHEL) 환경의 고아 인터페이스를 정리합니다
func (uc *DeleteNetworkUseCase) executeIfcfgCleanup(ctx context.Context, input DeleteNetworkInput) (*DeleteNetworkOutput, error) {
	output := &DeleteNetworkOutput{
		DeletedInterfaces: []string{},
		Errors:            []error{},
	}

	// ifcfg 파일 디렉토리
	ifcfgDir := "/etc/sysconfig/network-scripts"

    // 디렉토리의 파일 목록 가져오기 (RHEL9+ 환경에서는 디렉토리가 없을 수 있음 → 비치명적으로 취급)
    files, err := uc.namingService.ListNetplanFiles(ifcfgDir)
    if err != nil {
        // 디렉토리 부재는 정상 시나리오로 간주하고 조용히 종료
        if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file or directory") {
            uc.logger.WithField("dir", ifcfgDir).Info("ifcfg directory not present - skipping full cleanup")
            // 추가: 시스템에 남아있는 multinicX 인터페이스 이름 정리 (DOWN 상태만 대상)
            uc.cleanupMultinicInterfaceNames(ctx)
            return output, nil
        }
        return nil, fmt.Errorf("failed to list ifcfg files: %w", err)
    }

	// 고아 파일 찾기
	orphanedFiles, err := uc.findOrphanedIfcfgFiles(ctx, files, ifcfgDir)
	if err != nil {
		return nil, fmt.Errorf("failed to find orphaned ifcfg files: %w", err)
	}

	if len(orphanedFiles) == 0 {
		uc.logger.Debug("No orphaned ifcfg files to delete")
		return output, nil
	}

	uc.logger.WithFields(logrus.Fields{
		"node_name":      input.NodeName,
		"orphaned_files": orphanedFiles,
	}).Info("Orphaned ifcfg files detected - starting cleanup process")

	// 고아 파일 삭제
	for _, fileName := range orphanedFiles {
		interfaceName := uc.extractInterfaceNameFromIfcfgFile(fileName)
		if interfaceName == "" {
			continue
		}

		if err := uc.rollbacker.Rollback(ctx, interfaceName); err != nil {
			uc.logger.WithFields(logrus.Fields{
				"file_name":      fileName,
				"interface_name": interfaceName,
				"error":          err,
			}).Error("Failed to delete ifcfg file")
			output.Errors = append(output.Errors, fmt.Errorf("failed to delete ifcfg file %s: %w", fileName, err))
		} else {
			output.DeletedInterfaces = append(output.DeletedInterfaces, interfaceName)
			output.TotalDeleted++
			metrics.OrphanedInterfacesDeleted.Inc()
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
		activeMACAddresses[strings.ToLower(iface.MacAddress())] = true
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

// isMultinicIfcfgFile은 파일이 multinic 관련 ifcfg 파일인지 확인합니다
func (uc *DeleteNetworkUseCase) isMultinicIfcfgFile(fileName string) bool {
	// ifcfg-multinic* 패턴 매칭
	return strings.HasPrefix(fileName, "ifcfg-multinic")
}

// extractInterfaceNameFromIfcfgFile은 ifcfg 파일명에서 인터페이스 이름을 추출합니다
func (uc *DeleteNetworkUseCase) extractInterfaceNameFromIfcfgFile(fileName string) string {
	// 예: "ifcfg-multinic0" -> "multinic0"
	if strings.HasPrefix(fileName, "ifcfg-") {
		return strings.TrimPrefix(fileName, "ifcfg-")
	}
	return ""
}

// getMACAddressFromIfcfgFile은 ifcfg 파일에서 MAC 주소를 추출합니다
func (uc *DeleteNetworkUseCase) getMACAddressFromIfcfgFile(filePath string) (string, error) {
	content, err := uc.fileSystem.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	// HWADDR 필드 찾기
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "HWADDR=") {
			// HWADDR=fa:16:3e:00:be:63 형식에서 MAC 주소 추출
			macAddress := strings.TrimPrefix(line, "HWADDR=")
			macAddress = strings.Trim(macAddress, "\"'")
			return macAddress, nil
		}
	}

	return "", fmt.Errorf("HWADDR not found in ifcfg file")
}

// findOrphanedIfcfgFiles는 DB에 없는 MAC 주소의 ifcfg 파일을 찾습니다
func (uc *DeleteNetworkUseCase) findOrphanedIfcfgFiles(ctx context.Context, files []string, ifcfgDir string) ([]string, error) {
	var orphanedFiles []string

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
	var activeMACList []string
	for _, iface := range activeInterfaces {
		macLower := strings.ToLower(iface.MacAddress())
		activeMACAddresses[macLower] = true
		activeMACList = append(activeMACList, macLower)
	}

	uc.logger.WithFields(logrus.Fields{
		"node_name":       hostname,
		"active_macs":     activeMACList,
		"interface_count": len(activeInterfaces),
	}).Debug("Active MAC addresses from database for orphan detection")

	for _, fileName := range files {
		// ifcfg-multinic* 파일만 처리
		if !uc.isMultinicIfcfgFile(fileName) {
			continue
		}

		// 파일의 MAC 주소 확인
		filePath := fmt.Sprintf("%s/%s", ifcfgDir, fileName)
		macAddress, err := uc.getMACAddressFromIfcfgFile(filePath)
		if err != nil {
			uc.logger.WithFields(logrus.Fields{
				"file_name": fileName,
				"error":     err.Error(),
			}).Warn("Failed to extract MAC address from ifcfg file")
			continue
		}

		uc.logger.WithFields(logrus.Fields{
			"file_name": fileName,
			"file_mac":  strings.ToLower(macAddress),
			"is_active": activeMACAddresses[strings.ToLower(macAddress)],
		}).Debug("Checking ifcfg file for orphan detection")

		// DB에 해당 MAC 주소가 없으면 고아 파일
		if !activeMACAddresses[strings.ToLower(macAddress)] {
			interfaceName := uc.extractInterfaceNameFromIfcfgFile(fileName)
			uc.logger.WithFields(logrus.Fields{
				"file_name":      fileName,
				"interface_name": interfaceName,
				"mac_address":    macAddress,
			}).Info("Found orphaned ifcfg file")
			orphanedFiles = append(orphanedFiles, fileName)
		} else {
			// DB에 있는 MAC 주소 - 정상 파일이므로 로그만 출력
			uc.logger.WithFields(logrus.Fields{
				"file_name":   fileName,
				"mac_address": macAddress,
			}).Debug("ifcfg file belongs to active interface - keeping it")
		}
	}

	return orphanedFiles, nil
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

// executeFullNetplanCleanup는 모든 multinic netplan 파일을 정리합니다 (cleanup 모드용)
func (uc *DeleteNetworkUseCase) executeFullNetplanCleanup(ctx context.Context, input DeleteNetworkInput) (*DeleteNetworkOutput, error) {
	output := &DeleteNetworkOutput{
		DeletedInterfaces: []string{},
		Errors:            []error{},
	}

	// /etc/netplan 디렉토리에서 모든 multinic 관련 파일 스캔
	netplanDir := "/etc/netplan"
	files, err := uc.namingService.ListNetplanFiles(netplanDir)
	if err != nil {
		return nil, fmt.Errorf("failed to scan netplan directory: %w", err)
	}

	var multinicFiles []string
	for _, fileName := range files {
		// multinic 파일만 처리 (9*-multinic*.yaml 패턴)
		if uc.isMultinicNetplanFile(fileName) {
			multinicFiles = append(multinicFiles, fileName)
		}
	}

	if len(multinicFiles) == 0 {
		uc.logger.Info("No multinic netplan files found - cleanup complete")
		// 추가: 시스템에 남아있는 multinicX 인터페이스 이름 정리 (DOWN 상태만 대상)
		uc.cleanupMultinicInterfaceNames(ctx)

		return output, nil
	}

	uc.logger.WithFields(logrus.Fields{
		"node_name":      input.NodeName,
		"multinic_files": multinicFiles,
	}).Info("Found multinic netplan files - starting full cleanup")

	for _, fileName := range multinicFiles {
		interfaceName := uc.extractInterfaceNameFromFile(fileName)
		if err := uc.deleteNetplanFile(ctx, fileName, interfaceName); err != nil {
			uc.logger.WithFields(logrus.Fields{
				"file_name":      fileName,
				"interface_name": interfaceName,
				"error":          err.Error(),
			}).Error("Failed to delete multinic netplan file")
			output.Errors = append(output.Errors, fmt.Errorf("failed to delete netplan file %s: %w", fileName, err))
		} else {
			output.DeletedInterfaces = append(output.DeletedInterfaces, interfaceName)
			output.TotalDeleted++
			metrics.OrphanedInterfacesDeleted.Inc()
		}
	}

	return output, nil
}

// executeFullIfcfgCleanup는 모든 multinic ifcfg 파일을 정리합니다 (cleanup 모드용)
func (uc *DeleteNetworkUseCase) executeFullIfcfgCleanup(ctx context.Context, input DeleteNetworkInput) (*DeleteNetworkOutput, error) {
	output := &DeleteNetworkOutput{
		DeletedInterfaces: []string{},
		Errors:            []error{},
	}

	// ifcfg 파일 디렉토리
	ifcfgDir := "/etc/sysconfig/network-scripts"

	// 디렉토리의 파일 목록 가져오기
	files, err := uc.namingService.ListNetplanFiles(ifcfgDir)
	if err != nil {
		return nil, fmt.Errorf("failed to list ifcfg files: %w", err)
	}

	var multinicFiles []string
	for _, fileName := range files {
		// ifcfg-multinic* 파일만 처리
		if uc.isMultinicIfcfgFile(fileName) {
			multinicFiles = append(multinicFiles, fileName)
		}
	}

	if len(multinicFiles) == 0 {
		uc.logger.Info("No multinic ifcfg files found - cleanup complete")
		// 추가: 시스템에 남아있는 multinicX 인터페이스 이름 정리 (DOWN 상태만 대상)
		uc.cleanupMultinicInterfaceNames(ctx)
		return output, nil
	}

	uc.logger.WithFields(logrus.Fields{
		"node_name":      input.NodeName,
		"multinic_files": multinicFiles,
	}).Info("Found multinic ifcfg files - starting full cleanup")

	// 모든 multinic 파일 삭제
	for _, fileName := range multinicFiles {
		interfaceName := uc.extractInterfaceNameFromIfcfgFile(fileName)
		if interfaceName == "" {
			continue
		}

		if err := uc.rollbacker.Rollback(ctx, interfaceName); err != nil {
			uc.logger.WithFields(logrus.Fields{
				"file_name":      fileName,
				"interface_name": interfaceName,
				"error":          err,
			}).Error("Failed to delete multinic ifcfg file")
			output.Errors = append(output.Errors, fmt.Errorf("failed to delete ifcfg file %s: %w", fileName, err))
		} else {
			output.DeletedInterfaces = append(output.DeletedInterfaces, interfaceName)
			output.TotalDeleted++
			metrics.OrphanedInterfacesDeleted.Inc()
		}
	}

	return output, nil
}

// cleanupMultinicInterfaceNames는 남아있는 multinicX 인터페이스를 안전하게 이름 해제합니다.
// - 대상: 이름 패턴이 multinic0~9 이고, 인터페이스가 DOWN 상태인 경우만
// - 방법: altname(ens*/enp*)가 있으면 altname으로 rename 시도, 없으면 건너뜀
func (uc *DeleteNetworkUseCase) cleanupMultinicInterfaceNames(ctx context.Context) {
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("multinic%d", i)
		if !uc.namingService.InterfaceExists(name) {
			continue
		}
		if up, err := uc.namingService.IsInterfaceUp(name); err != nil {
			uc.logger.WithFields(logrus.Fields{"interface_name": name, "error": err}).Debug("Failed to check interface UP status; skip renaming for safety")
			continue
		} else if up {
			uc.logger.WithField("interface_name", name).Warn("Skip renaming UP interface (keep current name)")
			continue
		}
		alts, err := uc.namingService.GetAltNames(name)
		if err != nil {
			uc.logger.WithFields(logrus.Fields{"interface_name": name, "error": err}).Debug("Failed to get alt names for interface")
			continue
		}
		// altname 중 사용 중이지 않은 첫 번째 후보 선택
		var target string
		for _, alt := range alts {
			if !uc.namingService.InterfaceExists(alt) {
				target = alt
				break
			}
		}
		if target == "" {
			// 사용 가능한 altname이 없다면 건너뜀 (임의 이름은 위험)
			uc.logger.WithField("interface_name", name).Debug("No available altname to rename; skipping")
			continue
		}
		if err := uc.namingService.RenameInterface(name, target); err != nil {
			uc.logger.WithFields(logrus.Fields{"from": name, "to": target, "error": err}).Warn("Failed to rename interface")
		} else {
			uc.logger.WithFields(logrus.Fields{"from": name, "to": target}).Info("Renamed leftover multinic interface to altname")
		}
	}
}
