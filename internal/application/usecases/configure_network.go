package usecases

import (
	"bufio"
	"context"
	"fmt"
	"multinic-agent/internal/domain/entities"
	"multinic-agent/internal/domain/errors"
	"multinic-agent/internal/domain/interfaces"
	"multinic-agent/internal/domain/services"
	"multinic-agent/internal/infrastructure/metrics"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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


// ConfigureNetworkUseCase는 네트워크 설정을 처리하는 유스케이스입니다
type ConfigureNetworkUseCase struct {
	repository         interfaces.NetworkInterfaceRepository
	configurer         interfaces.NetworkConfigurer
	rollbacker         interfaces.NetworkRollbacker
	namingService      *services.InterfaceNamingService
	fileSystem         interfaces.FileSystem // 파일 시스템 의존성 추가
	osDetector         interfaces.OSDetector
	logger             *logrus.Logger
	maxConcurrentTasks int
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
	maxConcurrentTasks int,
) *ConfigureNetworkUseCase {
	return &ConfigureNetworkUseCase{
		repository:         repo,
		configurer:         configurer,
		rollbacker:         rollbacker,
		namingService:      naming,
		fileSystem:         fs,
		osDetector:         osDetector,
		logger:             logger,
		maxConcurrentTasks: maxConcurrentTasks,
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

	// 병렬 처리를 위한 설정
	maxWorkers := uc.maxConcurrentTasks
	if maxWorkers <= 0 {
		maxWorkers = 1 // 최소 1개는 처리
	}
	
	var (
		processedCount int32
		failedCount    int32
		wg             sync.WaitGroup
		semaphore      = make(chan struct{}, maxWorkers) // 동시 실행 제한
	)

	// 2. 각 인터페이스를 병렬로 처리
	for _, iface := range allInterfaces {
		wg.Add(1)
		go func(iface entities.NetworkInterface) {
			defer wg.Done()
			
			// 세마포어 획득 (동시 실행 제한)
			semaphore <- struct{}{}
			
			// 동시 처리 메트릭 업데이트
			currentTasks := float64(len(semaphore))
			metrics.SetConcurrentTasks(currentTasks)
			
			defer func() { 
				<-semaphore 
				metrics.SetConcurrentTasks(float64(len(semaphore)))
			}()
			
			if err := uc.processInterfaceWithCheck(ctx, iface, osType, &processedCount, &failedCount); err != nil {
				uc.logger.WithError(err).Error("Critical error processing interface")
			}
		}(iface)
	}

	// 모든 처리가 완료될 때까지 대기
	wg.Wait()

	return &ConfigureNetworkOutput{
		ProcessedCount: int(atomic.LoadInt32(&processedCount)),
		FailedCount:    int(atomic.LoadInt32(&failedCount)),
		TotalCount:     len(allInterfaces),
	}, nil
}

// processInterface는 개별 인터페이스를 처리합니다
func (uc *ConfigureNetworkUseCase) processInterface(ctx context.Context, iface entities.NetworkInterface, interfaceName entities.InterfaceName) error {
	startTime := time.Now()
	
	// 1. 유효성 검증
	if err := iface.Validate(); err != nil {
		metrics.RecordInterfaceProcessing(interfaceName.String(), "failed", time.Since(startTime).Seconds())
		metrics.RecordError("validation")
		return errors.NewValidationError("Interface validation failed", err)
	}

	uc.logger.WithFields(logrus.Fields{
		"interface_id":   iface.ID,
		"interface_name": interfaceName.String(),
		"mac_address":    iface.MacAddress,
	}).Info("Starting interface configuration")

	// 2. 네트워크 설정 적용
	if err := uc.applyConfiguration(ctx, iface, interfaceName); err != nil {
		metrics.RecordInterfaceProcessing(interfaceName.String(), "failed", time.Since(startTime).Seconds())
		return err
	}

	// 3. 설정 검증
	if err := uc.validateConfiguration(ctx, interfaceName); err != nil {
		metrics.RecordInterfaceProcessing(interfaceName.String(), "failed", time.Since(startTime).Seconds())
		return err
	}

	// 4. 성공 상태로 업데이트
	if err := uc.repository.UpdateInterfaceStatus(ctx, iface.ID, entities.StatusConfigured); err != nil {
		metrics.RecordInterfaceProcessing(interfaceName.String(), "failed", time.Since(startTime).Seconds())
		metrics.RecordError("system")
		return errors.NewSystemError("Failed to update interface status", err)
	}

	uc.logger.WithFields(logrus.Fields{
		"interface_id":   iface.ID,
		"interface_name": interfaceName.String(),
	}).Info("Interface configuration succeeded")

	metrics.RecordInterfaceProcessing(interfaceName.String(), "success", time.Since(startTime).Seconds())
	return nil
}

// applyConfiguration은 네트워크 설정을 적용하고 실패 시 롤백합니다
func (uc *ConfigureNetworkUseCase) applyConfiguration(ctx context.Context, iface entities.NetworkInterface, interfaceName entities.InterfaceName) error {
	if err := uc.configurer.Configure(ctx, iface, interfaceName); err != nil {
		// 롤백 시도
		if rollbackErr := uc.performRollback(ctx, interfaceName.String(), "configuration"); rollbackErr != nil {
			// 롤백도 실패한 경우 더 심각한 상황
			return errors.NewNetworkError(
				fmt.Sprintf("Failed to apply configuration and rollback also failed: %v", rollbackErr),
				err,
			)
		}
		return errors.NewNetworkError("Failed to apply network configuration", err)
	}
	return nil
}

// validateConfiguration은 네트워크 설정을 검증하고 실패 시 롤백합니다
func (uc *ConfigureNetworkUseCase) validateConfiguration(ctx context.Context, interfaceName entities.InterfaceName) error {
	if err := uc.configurer.Validate(ctx, interfaceName); err != nil {
		// 검증 실패 시 롤백
		if rollbackErr := uc.performRollback(ctx, interfaceName.String(), "validation"); rollbackErr != nil {
			return errors.NewNetworkError(
				fmt.Sprintf("Validation failed and rollback also failed: %v", rollbackErr),
				err,
			)
		}
		return errors.NewNetworkError("Network configuration validation failed", err)
	}
	return nil
}

// performRollback은 롤백을 수행하고 결과를 기록합니다
func (uc *ConfigureNetworkUseCase) performRollback(ctx context.Context, interfaceName string, stage string) error {
	err := uc.rollbacker.Rollback(ctx, interfaceName)
	if err != nil {
		uc.logger.WithFields(logrus.Fields{
			"interface_name": interfaceName,
			"stage":          stage,
			"error":          err,
		}).Error("Rollback failed")
		return err
	}
	
	uc.logger.WithFields(logrus.Fields{
		"interface_name": interfaceName,
		"stage":          stage,
	}).Info("Rollback completed successfully")
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

	netplanData, err := uc.parseNetplanFile(content)
	if err != nil {
		uc.logger.WithError(err).WithField("file", configPath).Warn("Failed to parse Netplan YAML, treating as configuration mismatch")
		return true
	}

	// Netplan 파일에서 설정 추출
	fileConfig := uc.extractNetplanConfig(netplanData)
	
	// MAC 주소 검증
	if fileConfig.macAddress != dbIface.MacAddress {
		uc.logger.WithFields(logrus.Fields{
			"db_mac":   dbIface.MacAddress,
			"file_mac": fileConfig.macAddress,
		}).Warn("MAC address mismatch, treating as configuration change")
		return true
	}

	// 드리프트 체크
	return uc.checkConfigDrift(dbIface, fileConfig)
}

// netplanFileConfig는 Netplan 파일에서 추출한 설정을 담는 구조체입니다
type netplanFileConfig struct {
	macAddress   string
	address      string
	cidr         string
	mtu          int
	hasAddresses bool
}

// parseNetplanFile은 Netplan YAML을 파싱합니다
func (uc *ConfigureNetworkUseCase) parseNetplanFile(content []byte) (*NetplanYAML, error) {
	var netplanData NetplanYAML
	if err := yaml.Unmarshal(content, &netplanData); err != nil {
		return nil, err
	}
	return &netplanData, nil
}

// extractNetplanConfig는 Netplan 데이터에서 설정을 추출합니다
func (uc *ConfigureNetworkUseCase) extractNetplanConfig(netplanData *NetplanYAML) netplanFileConfig {
	config := netplanFileConfig{}
	
	for _, eth := range netplanData.Network.Ethernets {
		config.macAddress = eth.Match.MACAddress
		config.hasAddresses = len(eth.Addresses) > 0
		config.mtu = eth.MTU
		
		if config.hasAddresses {
			// Parse the full CIDR from the file
			ip, ipNet, err := net.ParseCIDR(eth.Addresses[0])
			if err == nil {
				config.address = ip.String()      // Get the actual IP address
				config.cidr = ipNet.String()      // Get the network CIDR
			} else {
				// If parsing fails, use the raw address
				config.address = eth.Addresses[0]
				config.cidr = ""
			}
		}
		break // Assuming one ethernet per file
	}
	
	return config
}

// checkConfigDrift는 DB와 파일 설정 간의 드리프트를 체크합니다
func (uc *ConfigureNetworkUseCase) checkConfigDrift(dbIface entities.NetworkInterface, fileConfig netplanFileConfig) bool {
	// 드리프트 감지 - 간단한 OR 조건으로 유지
	isDrifted := (!fileConfig.hasAddresses && dbIface.Address != "") ||
		(dbIface.Address != fileConfig.address) ||
		(dbIface.CIDR != fileConfig.cidr) ||
		(dbIface.MTU != fileConfig.mtu)

	if isDrifted {
		uc.logDriftDetails("netplan", dbIface, logrus.Fields{
			"file_address":   fileConfig.address,
			"file_cidr":      fileConfig.cidr,
			"file_mtu":       fileConfig.mtu,
			"config_change_1": (!fileConfig.hasAddresses && dbIface.Address != ""),
			"config_change_2": (dbIface.Address != fileConfig.address),
			"config_change_3": (dbIface.CIDR != fileConfig.cidr),
			"config_change_4": (dbIface.MTU != fileConfig.mtu),
		})
		
		// 드리프트 타입별 메트릭 기록
		if !fileConfig.hasAddresses && dbIface.Address != "" {
			metrics.RecordDrift("missing_address")
		}
		if dbIface.Address != fileConfig.address {
			metrics.RecordDrift("ip_address")
		}
		if dbIface.CIDR != fileConfig.cidr {
			metrics.RecordDrift("cidr")
		}
		if dbIface.MTU != fileConfig.mtu {
			metrics.RecordDrift("mtu")
		}
	}

	return isDrifted
}


// findIfcfgFile는 해당 인터페이스의 ifcfg 파일을 찾습니다
func (uc *ConfigureNetworkUseCase) findIfcfgFile(interfaceName string) string {
	configDir := uc.configurer.GetConfigDir()
	fileName := "ifcfg-" + interfaceName
	filePath := filepath.Join(configDir, fileName)
	
	if uc.fileSystem.Exists(filePath) {
		return filePath
	}
	
	return ""
}

// isIfcfgDrifted는 ifcfg 파일과 DB 데이터 간의 드리프트를 감지합니다
func (uc *ConfigureNetworkUseCase) isIfcfgDrifted(ctx context.Context, dbIface entities.NetworkInterface, configPath string) bool {
	content, err := uc.fileSystem.ReadFile(configPath)
	if err != nil {
		uc.logger.WithError(err).WithField("file", configPath).Warn("Failed to read ifcfg file, treating as configuration mismatch")
		return true
	}

	// ifcfg 파일 파싱
	fileConfig := uc.parseIfcfgFile(content)
	
	// MAC 주소 검증
	if fileConfig.macAddress != strings.ToLower(dbIface.MacAddress) {
		uc.logger.WithFields(logrus.Fields{
			"db_mac":   dbIface.MacAddress,
			"file_mac": fileConfig.macAddress,
		}).Warn("MAC address mismatch in ifcfg file")
		return true
	}
	
	// 드리프트 체크
	return uc.checkIfcfgDrift(dbIface, fileConfig)
}

// ifcfgFileConfig는 ifcfg 파일에서 추출한 설정을 담는 구조체입니다
type ifcfgFileConfig struct {
	macAddress string
	ipAddress  string
	prefix     string
	mtu        int
}

// parseIfcfgFile은 ifcfg 파일을 파싱합니다
func (uc *ConfigureNetworkUseCase) parseIfcfgFile(content []byte) ifcfgFileConfig {
	config := ifcfgFileConfig{}
	
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		
		switch key {
		case "HWADDR":
			config.macAddress = strings.ToLower(value)
		case "IPADDR":
			config.ipAddress = value
		case "PREFIX":
			config.prefix = value
		case "MTU":
			if mtu, err := strconv.Atoi(value); err == nil {
				config.mtu = mtu
			}
		}
	}
	
	return config
}

// checkIfcfgDrift는 DB와 ifcfg 파일 설정 간의 드리프트를 체크합니다
func (uc *ConfigureNetworkUseCase) checkIfcfgDrift(dbIface entities.NetworkInterface, fileConfig ifcfgFileConfig) bool {
	// 드리프트 체크 - 각 항목을 확인
	var dbPrefix string
	if dbIface.CIDR != "" {
		if parts := strings.Split(dbIface.CIDR, "/"); len(parts) == 2 {
			dbPrefix = parts[1]
		}
	}
	
	isDrifted := (dbIface.Address != fileConfig.ipAddress) ||
		(dbPrefix != "" && fileConfig.prefix != "" && dbPrefix != fileConfig.prefix) ||
		(dbIface.MTU != fileConfig.mtu)
	
	if isDrifted {
		uc.logDriftDetails("ifcfg", dbIface, logrus.Fields{
			"file_address":    fileConfig.ipAddress,
			"file_prefix":     fileConfig.prefix,
			"file_mtu":        fileConfig.mtu,
		})
	}
	
	return isDrifted
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

// processInterfaceWithCheck는 개별 인터페이스를 처리하기 전에 필요성을 검사합니다
func (uc *ConfigureNetworkUseCase) processInterfaceWithCheck(ctx context.Context, iface entities.NetworkInterface, osType interfaces.OSType, processedCount, failedCount *int32) error {
	// 인터페이스 이름 생성 (기존에 할당된 이름이 있다면 재사용)
	interfaceName, err := uc.namingService.GenerateNextNameForMAC(iface.MacAddress)
	if err != nil {
		uc.handleInterfaceError("interface name generation", iface.ID, iface.MacAddress, err)
		atomic.AddInt32(failedCount, 1)
		return nil // 다음 인터페이스 처리를 위해 에러 반환하지 않음
	}

	// OS별로 처리 필요성 검사
	shouldProcess, configPath := uc.checkNeedProcessing(ctx, iface, interfaceName, osType)
	
	if shouldProcess {
		uc.logger.WithFields(logrus.Fields{
			"interface_id":   iface.ID,
			"interface_name": interfaceName.String(),
			"mac_address":    iface.MacAddress,
			"status":         iface.Status,
			"os_type":        osType,
			"config_path":    configPath,
		}).Debug("Processing interface")
		
		if err := uc.processInterface(ctx, iface, interfaceName); err != nil {
			uc.handleProcessingError(ctx, iface, interfaceName, err)
			atomic.AddInt32(failedCount, 1)
		} else {
			atomic.AddInt32(processedCount, 1)
		}
	}
	
	return nil
}

// checkNeedProcessing는 인터페이스 처리 필요성을 검사합니다
func (uc *ConfigureNetworkUseCase) checkNeedProcessing(ctx context.Context, iface entities.NetworkInterface, interfaceName entities.InterfaceName, osType interfaces.OSType) (bool, string) {
	if osType == interfaces.OSTypeRHEL {
		return uc.checkRHELNeedProcessing(ctx, iface, interfaceName)
	}
	return uc.checkNetplanNeedProcessing(ctx, iface, interfaceName)
}

// checkRHELNeedProcessing는 RHEL 시스템에서 인터페이스 처리 필요성을 검사합니다
func (uc *ConfigureNetworkUseCase) checkRHELNeedProcessing(ctx context.Context, iface entities.NetworkInterface, interfaceName entities.InterfaceName) (bool, string) {
	configPath := uc.findIfcfgFile(interfaceName.String())
	fileExists := configPath != ""
	
	isDrifted := false
	if fileExists {
		isDrifted = uc.isIfcfgDrifted(ctx, iface, configPath)
	}
	
	// 파일이 없거나, 드리프트가 있거나, 아직 설정되지 않은 경우 처리
	shouldProcess := !fileExists || isDrifted || iface.Status == entities.StatusPending
	return shouldProcess, configPath
}

// checkNetplanNeedProcessing는 Ubuntu 시스템에서 인터페이스 처리 필요성을 검사합니다
func (uc *ConfigureNetworkUseCase) checkNetplanNeedProcessing(ctx context.Context, iface entities.NetworkInterface, interfaceName entities.InterfaceName) (bool, string) {
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
	shouldProcess := !fileExists || isDrifted || iface.Status == entities.StatusPending
	return shouldProcess, configPath
}

// extractInterfaceIndex는 인터페이스 이름에서 인덱스를 추출합니다
// logDriftDetails는 드리프트 상세 정보를 로깅합니다
func (uc *ConfigureNetworkUseCase) logDriftDetails(configType string, dbIface entities.NetworkInterface, fileFields logrus.Fields) {
	fields := logrus.Fields{
		"interface_id":   dbIface.ID,
		"mac_address":    dbIface.MacAddress,
		"db_address":     dbIface.Address,
		"db_cidr":        dbIface.CIDR,
		"db_mtu":         dbIface.MTU,
	}
	
	// 파일 필드 추가
	for k, v := range fileFields {
		fields[k] = v
	}
	
	uc.logger.WithFields(fields).Debug(configType + " configuration drift detected")
}

// handleInterfaceError는 인터페이스 처리 에러를 기록합니다
func (uc *ConfigureNetworkUseCase) handleInterfaceError(operation string, interfaceID int, macAddress string, err error) {
	fields := logrus.Fields{
		"operation":    operation,
		"interface_id": interfaceID,
		"mac_address":  macAddress,
		"error":        err,
	}
	
	// 에러 타입에 따른 로그 레벨 조정
	switch {
	case errors.IsValidationError(err):
		uc.logger.WithFields(fields).Warn("Validation error")
	case errors.IsNetworkError(err):
		uc.logger.WithFields(fields).Error("Network error")
	case errors.IsTimeoutError(err):
		uc.logger.WithFields(fields).Error("Timeout error")
	default:
		uc.logger.WithFields(fields).Error("Operation failed")
	}
}

// handleProcessingError는 인터페이스 처리 중 발생한 에러를 처리합니다
func (uc *ConfigureNetworkUseCase) handleProcessingError(ctx context.Context, iface entities.NetworkInterface, interfaceName entities.InterfaceName, err error) {
	uc.logger.WithFields(logrus.Fields{
		"interface_id":   iface.ID,
		"interface_name": interfaceName.String(),
		"error_type":     uc.getErrorType(err),
		"error":          err,
	}).Error("Failed to configure/sync interface")

	// 실패 상태로 업데이트
	if updateErr := uc.repository.UpdateInterfaceStatus(ctx, iface.ID, entities.StatusFailed); updateErr != nil {
		uc.logger.WithError(updateErr).Error("Failed to update interface status")
	}
}

// getErrorType는 에러 타입을 반환합니다
func (uc *ConfigureNetworkUseCase) getErrorType(err error) string {
	switch {
	case errors.IsValidationError(err):
		return "validation"
	case errors.IsNetworkError(err):
		return "network"
	case errors.IsTimeoutError(err):
		return "timeout"
	case errors.IsSystemError(err):
		return "system"
	default:
		return "unknown"
	}
}

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

