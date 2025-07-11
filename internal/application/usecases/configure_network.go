package usecases

import (
	"bufio"
	"context"
	"fmt"
	"multinic-agent/internal/domain/entities"
	"multinic-agent/internal/domain/errors"
	"multinic-agent/internal/domain/interfaces"
	"multinic-agent/internal/domain/services"
	"net"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// NetplanYAML represents the Netplan configuration structure
type NetplanYAML struct {
	Network struct {
		Ethernets map[string]struct {
			DHCP4     bool   `yaml:"dhcp4"`
			MTU       int    `yaml:"mtu,omitempty"`
			Addresses []string `yaml:"addresses,omitempty"`
			Match     struct {
				MACAddress string `yaml:"macaddress"`
			} `yaml:"match"`
			SetName string `yaml:"set-name"`
		} `yaml:"ethernets"`
		Version int `yaml:"version"`
	} `yaml:"network"`
}

// NmConnectionConfig represents parsed nmconnection file data
type NmConnectionConfig struct {
	MacAddress string
	MTU        int
	Addresses  []string // IPv4 addresses in CIDR format
	Method     string   // manual, disabled, etc.
}

// ConfigureNetworkUseCase는 네트워크 설정을 처리하는 유스케이스입니다
type ConfigureNetworkUseCase struct {
	repository    interfaces.NetworkInterfaceRepository
	configurer    interfaces.NetworkConfigurer
	rollbacker    interfaces.NetworkRollbacker
	namingService *services.InterfaceNamingService
	fileSystem    interfaces.FileSystem // 파일 시스템 의존성 추가
	osDetector    interfaces.OSDetector
	logger        *logrus.Logger
}

// NewConfigureNetworkUseCase는 새로운 ConfigureNetworkUseCase를 생성합니다
func NewConfigureNetworkUseCase(
	repo interfaces.NetworkInterfaceRepository,
	configurer interfaces.NetworkConfigurer,
	rollbacker interfaces.NetworkRollbacker,
	naming *services.InterfaceNamingService,
	fs interfaces.FileSystem, // 파일 시스템 의존성 추가
	osDetector interfaces.OSDetector,
	logger *logrus.Logger,
) *ConfigureNetworkUseCase {
	return &ConfigureNetworkUseCase{
		repository:    repo,
		configurer:    configurer,
		rollbacker:    rollbacker,
		namingService: naming,
		fileSystem:    fs,
		osDetector:    osDetector,
		logger:        logger,
	}
}

// ConfigureNetworkInput은 유스케이스의 입력 파라미터입니다
type ConfigureNetworkInput struct {
	NodeName string
}

// ConfigureNetworkOutput은 유스케이스의 출력 결과입니다
type ConfigureNetworkOutput struct {
	ProcessedCount int
	FailedCount    int
	TotalCount     int
}

// Execute는 네트워크 설정 유스케이스를 실행합니다
func (uc *ConfigureNetworkUseCase) Execute(ctx context.Context, input ConfigureNetworkInput) (*ConfigureNetworkOutput, error) {
	// OS 타입 감지
	osType, err := uc.osDetector.DetectOS()
	if err != nil {
		return nil, errors.NewSystemError("failed to detect OS type", err)
	}

	// 1. 해당 노드의 모든 활성 인터페이스 조회 (netplan_success 상태 무관)
	allInterfaces, err := uc.repository.GetAllNodeInterfaces(ctx, input.NodeName)
	if err != nil {
		return nil, errors.NewSystemError("failed to get node interfaces", err)
	}

	uc.logger.WithFields(logrus.Fields{
		"node_name": input.NodeName,
		"interface_count": len(allInterfaces),
		"os_type": osType,
	}).Debug("Retrieved interfaces from database")

	// 실제로 처리할 인터페이스가 있을 때만 로그 출력하도록 나중에 확인

	var processedCount, failedCount int

	// 2. 각 인터페이스 처리 (생성/수정/동기화)
	for _, iface := range allInterfaces {
		// 인터페이스 이름 생성 (기존에 할당된 이름이 있다면 재사용)
		interfaceName, err := uc.namingService.GenerateNextNameForMAC(iface.MacAddress)
		if err != nil {
			uc.logger.WithError(err).WithField("mac_address", iface.MacAddress).Error("Failed to generate interface name")
			failedCount++
			continue
		}

		// OS 타입에 따라 다른 처리
		var shouldProcess bool
		
		if osType == interfaces.OSTypeRHEL {
			// RHEL: nmcli connection 존재 여부와 드리프트 감지
			connectionExists := uc.isNmcliConnectionExists(ctx, interfaceName.String())
			isDrifted := false
			if connectionExists {
				isDrifted = uc.isNmcliConnectionDrifted(ctx, iface, interfaceName.String())
			}
			shouldProcess = !connectionExists || isDrifted || iface.Status == entities.StatusPending
		} else {
			// Ubuntu: Netplan 파일 기반 처리
			configPath := uc.findNetplanFileForInterface(interfaceName.String())
			if configPath == "" {
				// 파일이 없으면 새로 생성할 경로 설정
				configPath = filepath.Join(uc.configurer.GetConfigDir(), fmt.Sprintf("9%d-%s.yaml", extractInterfaceIndex(interfaceName.String()), interfaceName.String()))
			}

			// 파일이 존재하지 않거나, 드리프트가 발생했거나, 아직 설정되지 않은 경우 처리
			fileExists := uc.fileSystem.Exists(configPath)
			isDrifted := false
			if fileExists {
				isDrifted = uc.isDrifted(ctx, iface, configPath)
			}
			shouldProcess = !fileExists || isDrifted || iface.Status == entities.StatusPending
		}
		
		if shouldProcess {
			uc.logger.WithFields(logrus.Fields{
				"interface_id":   iface.ID,
				"interface_name": interfaceName.String(),
				"mac_address":    iface.MacAddress,
				"should_process": shouldProcess,
				"status":         iface.Status,
				"os_type":        osType,
			}).Debug("Processing interface")
			
			if err := uc.processInterface(ctx, iface, interfaceName); err != nil {
				uc.logger.WithFields(logrus.Fields{
					"interface_id":   iface.ID,
					"interface_name": interfaceName.String(),
					"error":          err,
				}).Error("Failed to configure/sync interface")
				failedCount++

				// 실패 상태로 업데이트
				if updateErr := uc.repository.UpdateInterfaceStatus(ctx, iface.ID, entities.StatusFailed); updateErr != nil {
					uc.logger.WithError(updateErr).Error("Failed to update interface status")
				}
			} else {
				processedCount++
			}
		}
	}

	return &ConfigureNetworkOutput{
		ProcessedCount: processedCount,
		FailedCount:    failedCount,
		TotalCount:     len(allInterfaces),
	}, nil
}

// processInterface는 개별 인터페이스를 처리합니다
func (uc *ConfigureNetworkUseCase) processInterface(ctx context.Context, iface entities.NetworkInterface, interfaceName entities.InterfaceName) error {
	// 1. 유효성 검증
	if err := iface.Validate(); err != nil {
		return errors.NewValidationError("Interface validation failed", err)
	}

	uc.logger.WithFields(logrus.Fields{
		"interface_id":   iface.ID,
		"interface_name": interfaceName.String(),
		"mac_address":    iface.MacAddress,
	}).Info("Starting interface configuration")

	// 2. 네트워크 설정 적용
	if err := uc.configurer.Configure(ctx, iface, interfaceName); err != nil {
		// 롤백 시도
		if rollbackErr := uc.rollbacker.Rollback(ctx, interfaceName.String()); rollbackErr != nil {
			uc.logger.WithError(rollbackErr).Error("Rollback failed")
		}
		return errors.NewNetworkError("Failed to apply network configuration", err)
	}

	// 3. 설정 검증
	if err := uc.configurer.Validate(ctx, interfaceName); err != nil {
		// 검증 실패 시 롤백
		if rollbackErr := uc.rollbacker.Rollback(ctx, interfaceName.String()); rollbackErr != nil {
			uc.logger.WithError(rollbackErr).Error("Rollback failed")
		}
		return errors.NewNetworkError("Network configuration validation failed", err)
	}

	// 4. 성공 상태로 업데이트
	if err := uc.repository.UpdateInterfaceStatus(ctx, iface.ID, entities.StatusConfigured); err != nil {
		return errors.NewSystemError("Failed to update interface status", err)
	}

	uc.logger.WithFields(logrus.Fields{
		"interface_id":   iface.ID,
		"interface_name": interfaceName.String(),
	}).Info("Interface configuration succeeded")

	return nil
}

// isDrifted는 Netplan 설정 파일과 DB 데이터 간의 드리프트를 감지합니다.
func (uc *ConfigureNetworkUseCase) isDrifted(ctx context.Context, dbIface entities.NetworkInterface, configPath string) bool {
	// 파일이 존재하지 않으면 드리프트로 간주 (새로 생성해야 함)
	if !uc.fileSystem.Exists(configPath) {
		uc.logger.WithFields(logrus.Fields{
			"interface_id":   dbIface.ID,
			"mac_address":    dbIface.MacAddress,
			"config_path":    configPath,
		}).Debug("Configuration file not found, detected as configuration change")
		return true
	}

	content, err := uc.fileSystem.ReadFile(configPath)
	if err != nil {
		uc.logger.WithError(err).WithField("file", configPath).Warn("Failed to read Netplan file, treating as configuration mismatch")
		return true // 파일 읽기 실패 시 드리프트로 간주하여 재설정 시도
	}

	var netplanData NetplanYAML
	if err := yaml.Unmarshal(content, &netplanData); err != nil {
		uc.logger.WithError(err).WithField("file", configPath).Warn("Failed to parse Netplan YAML, treating as configuration mismatch")
		return true // YAML 파싱 실패 시 드리프트로 간주하여 재설정 시도
	}

	var fileMAC, fileAddress, fileCIDR string
	var fileMTU int
	var hasAddresses bool

	for _, eth := range netplanData.Network.Ethernets {
		fileMAC = eth.Match.MACAddress
		hasAddresses = len(eth.Addresses) > 0
		if hasAddresses {
			// Parse the full CIDR from the file
			ip, ipNet, err := net.ParseCIDR(eth.Addresses[0])
			if err == nil {
				fileAddress = ip.String()      // Get the actual IP address (e.g., "1.1.1.146")
				fileCIDR = ipNet.String()      // Get the network CIDR (e.g., "1.1.1.0/24")
			} else {
				// If parsing fails, use the raw address and an empty CIDR
				fileAddress = eth.Addresses[0]
				fileCIDR = "" // Or some default/error value
			}
		} else {
			// addresses 필드가 없는 경우 (구형 포맷)
			fileAddress = ""
			fileCIDR = ""
		}
		fileMTU = eth.MTU
		break // Assuming one ethernet per file
	}

	// MAC 주소가 일치하지 않으면 심각한 문제이므로 드리프트로 간주
	if fileMAC != dbIface.MacAddress {
		uc.logger.WithFields(logrus.Fields{
			"db_mac":   dbIface.MacAddress,
			"file_mac": fileMAC,
		}).Warn("MAC address mismatch, treating as configuration change")
		return true
	}

	// 드리프트 감지 조건
	isDrifted := (!hasAddresses && dbIface.Address != "") ||
		(dbIface.Address != fileAddress) ||
		(dbIface.CIDR != fileCIDR) ||
		(dbIface.MTU != fileMTU)

	if isDrifted {
		uc.logger.WithFields(logrus.Fields{
			"interface_id":   dbIface.ID,
			"mac_address":    dbIface.MacAddress,
			"db_address":     dbIface.Address,
			"file_address":   fileAddress,
			"db_cidr":        dbIface.CIDR,
			"file_cidr":      fileCIDR,
			"db_mtu":         dbIface.MTU,
			"file_mtu":       fileMTU,
			"config_change_1": (!hasAddresses && dbIface.Address != ""),
			"config_change_2": (dbIface.Address != fileAddress),
			"config_change_3": (dbIface.CIDR != fileCIDR),
			"config_change_4": (dbIface.MTU != fileMTU),
		}).Debug("Configuration changes detected")
	}

	return isDrifted
}

// isNmcliConnectionDrifted는 nmconnection 파일과 DB 데이터 간의 드리프트를 감지합니다
func (uc *ConfigureNetworkUseCase) isNmcliConnectionDrifted(ctx context.Context, dbIface entities.NetworkInterface, connectionName string) bool {
	configPath := uc.findNmConnectionFile(connectionName)
	if configPath == "" {
		uc.logger.WithFields(logrus.Fields{
			"interface_id":    dbIface.ID,
			"connection_name": connectionName,
		}).Debug("nmconnection file not found, detected as configuration change")
		return true
	}

	nmConfig, err := uc.parseNmConnectionFile(configPath)
	if err != nil {
		uc.logger.WithError(err).WithField("file", configPath).Warn("Failed to parse nmconnection file, treating as configuration mismatch")
		return true
	}

	// MAC 주소 검증
	if !strings.EqualFold(nmConfig.MacAddress, dbIface.MacAddress) {
		uc.logger.WithFields(logrus.Fields{
			"interface_id":   dbIface.ID,
			"db_mac":         dbIface.MacAddress,
			"file_mac":       nmConfig.MacAddress,
			"connection_name": connectionName,
		}).Warn("MAC address mismatch in nmconnection file, treating as configuration change")
		return true
	}

	// IP 주소 및 설정 드리프트 감지
	var fileAddress, fileCIDR string
	hasAddresses := len(nmConfig.Addresses) > 0
	
	if hasAddresses {
		// Parse the first address
		ip, ipNet, err := net.ParseCIDR(nmConfig.Addresses[0])
		if err == nil {
			fileAddress = ip.String()
			fileCIDR = ipNet.String()
		}
	}

	// 드리프트 감지 조건
	isDrifted := (!hasAddresses && dbIface.Address != "") ||
		(dbIface.Address != fileAddress) ||
		(dbIface.CIDR != fileCIDR) ||
		(dbIface.MTU != nmConfig.MTU)

	if isDrifted {
		uc.logger.WithFields(logrus.Fields{
			"interface_id":    dbIface.ID,
			"connection_name": connectionName,
			"db_address":      dbIface.Address,
			"file_address":    fileAddress,
			"db_cidr":         dbIface.CIDR,
			"file_cidr":       fileCIDR,
			"db_mtu":          dbIface.MTU,
			"file_mtu":        nmConfig.MTU,
			"config_change_1": (!hasAddresses && dbIface.Address != ""),
			"config_change_2": (dbIface.Address != fileAddress),
			"config_change_3": (dbIface.CIDR != fileCIDR),
			"config_change_4": (dbIface.MTU != nmConfig.MTU),
		}).Debug("nmconnection configuration changes detected")
	}

	return isDrifted
}

// parseNmConnectionFile는 nmconnection 파일을 파싱합니다
func (uc *ConfigureNetworkUseCase) parseNmConnectionFile(filePath string) (*NmConnectionConfig, error) {
	content, err := uc.fileSystem.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	config := &NmConnectionConfig{
		Method: "disabled", // 기본값
	}

	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "mac-address":
			config.MacAddress = value
		case "mtu":
			if mtu, err := strconv.Atoi(value); err == nil {
				config.MTU = mtu
			}
		case "method":
			config.Method = value
		case "address1", "addresses":
			// Handle multiple address formats
			addresses := strings.Split(value, ";")
			for _, addr := range addresses {
				addr = strings.TrimSpace(addr)
				if addr != "" {
					config.Addresses = append(config.Addresses, addr)
				}
			}
		}
	}

	return config, scanner.Err()
}

// findNmConnectionFile는 해당 connection의 nmconnection 파일을 찾습니다
func (uc *ConfigureNetworkUseCase) findNmConnectionFile(connectionName string) string {
	configDir := uc.configurer.GetConfigDir()
	fileName := connectionName + ".nmconnection"
	filePath := filepath.Join(configDir, fileName)
	
	if uc.fileSystem.Exists(filePath) {
		return filePath
	}
	
	return ""
}

// isNmcliConnectionExists는 nmcli connection이 존재하는지 확인합니다
func (uc *ConfigureNetworkUseCase) isNmcliConnectionExists(ctx context.Context, connectionName string) bool {
	connections, err := uc.namingService.ListNmcliConnectionNames(ctx)
	if err != nil {
		uc.logger.WithError(err).Debug("Failed to list nmcli connections")
		return false
	}
	
	for _, conn := range connections {
		if conn == connectionName {
			return true
		}
	}
	
	return false
}

// findNetplanFileForInterface는 해당 인터페이스의 실제 netplan 파일을 찾습니다
func (uc *ConfigureNetworkUseCase) findNetplanFileForInterface(interfaceName string) string {
	configDir := uc.configurer.GetConfigDir()
	files, err := uc.fileSystem.ListFiles(configDir)
	if err != nil {
		uc.logger.WithError(err).Warn("Failed to scan Netplan directory")
		return ""
	}

	// 해당 인터페이스 이름을 포함하는 파일 찾기
	for _, file := range files {
		if strings.Contains(file, interfaceName) && strings.HasSuffix(file, ".yaml") {
			return filepath.Join(configDir, file)
		}
	}

	return ""
}

// extractInterfaceIndex는 인터페이스 이름에서 인덱스를 추출합니다
func extractInterfaceIndex(name string) int {
	// multinic0 -> 0, multinic1 -> 1 등
	if strings.HasPrefix(name, "multinic") {
		indexStr := strings.TrimPrefix(name, "multinic")
		if index, err := strconv.Atoi(indexStr); err == nil {
			return index
		}
	}
	return 0
}

