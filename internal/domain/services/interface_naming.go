package services

import (
	"context"
	"fmt"
	"multinic-agent/internal/domain/entities"
	"multinic-agent/internal/domain/interfaces"
	"regexp"
	"strings"
	"time"
)

// InterfaceNamingService는 네트워크 인터페이스 이름을 관리하는 도메인 서비스입니다
type InterfaceNamingService struct {
	fileSystem      interfaces.FileSystem
	commandExecutor interfaces.CommandExecutor
	isContainer     bool // indicates if running in container
}

// NewInterfaceNamingService는 새로운 InterfaceNamingService를 생성합니다
func NewInterfaceNamingService(fs interfaces.FileSystem, executor interfaces.CommandExecutor) *InterfaceNamingService {
	// Check if running in container by checking if /host exists
	isContainer := false
	if _, err := executor.ExecuteWithTimeout(context.Background(), 1*time.Second, "test", "-d", "/host"); err == nil {
		isContainer = true
	}

	return &InterfaceNamingService{
		fileSystem:      fs,
		commandExecutor: executor,
		isContainer:     isContainer,
	}
}

// GenerateNextName은 사용 가능한 다음 인터페이스 이름을 생성합니다
func (s *InterfaceNamingService) GenerateNextName() (entities.InterfaceName, error) {
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("multinic%d", i)

		// 실제 인터페이스로 존재하는지 확인
		if s.isInterfaceInUse(name) {
			continue
		}

		// 사용 가능한 이름 발견
		return entities.NewInterfaceName(name)
	}

	return entities.InterfaceName{}, fmt.Errorf("사용 가능한 인터페이스 이름이 없습니다 (multinic0-9 모두 사용 중)")
}

// GenerateNextNameForMAC은 특정 MAC 주소에 대한 인터페이스 이름을 생성합니다
// 이미 해당 MAC 주소로 설정된 인터페이스가 있다면 해당 이름을 재사용합니다
func (s *InterfaceNamingService) GenerateNextNameForMAC(macAddress string) (entities.InterfaceName, error) {
	// 먼저 해당 MAC 주소로 이미 설정된 인터페이스가 있는지 확인
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("multinic%d", i)

		// ip 명령어로 MAC 주소 확인
		if s.isInterfaceInUse(name) {
			// 해당 인터페이스의 MAC 주소 확인
			existingMAC, err := s.GetMacAddressForInterface(name)
			if err == nil && strings.EqualFold(existingMAC, macAddress) {
				// 동일한 MAC 주소를 가진 인터페이스 발견
				return entities.NewInterfaceName(name)
			}
		}
	}

	// 기존에 할당된 이름이 없으면 새로운 이름 생성
	return s.GenerateNextName()
}

// isInterfaceInUse는 인터페이스가 이미 사용 중인지 확인합니다
func (s *InterfaceNamingService) isInterfaceInUse(name string) bool {
	// /sys/class/net 디렉토리에서 인터페이스 확인
	return s.fileSystem.Exists(fmt.Sprintf("/sys/class/net/%s", name))
}

// GetCurrentMultinicInterfaces는 현재 시스템에 존재하는 모든 multinic 인터페이스를 반환합니다
// ip a 명령어를 통해 실제 네트워크 인터페이스를 확인합니다
func (s *InterfaceNamingService) GetCurrentMultinicInterfaces() []entities.InterfaceName {
	var interfaces []entities.InterfaceName

	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("multinic%d", i)
		if s.isInterfaceInUse(name) {
			if interfaceName, err := entities.NewInterfaceName(name); err == nil {
				interfaces = append(interfaces, interfaceName)
			}
		}
	}

	return interfaces
}

// GetMacAddressForInterface는 특정 인터페이스의 MAC 주소를 ip 명령어로 조회합니다
func (s *InterfaceNamingService) GetMacAddressForInterface(interfaceName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// ip addr show 명령어로 특정 인터페이스 정보 조회
	output, err := s.commandExecutor.ExecuteWithTimeout(ctx, 10*time.Second, "ip", "addr", "show", interfaceName)
	if err != nil {
		return "", fmt.Errorf("인터페이스 %s 정보 조회 실패: %w", interfaceName, err)
	}

	// MAC 주소 추출 (예: "link/ether fa:16:3e:00:be:63 brd ff:ff:ff:ff:ff:ff")
	macRegex := regexp.MustCompile(`link/ether\s+([a-fA-F0-9:]{17})`)
	matches := macRegex.FindStringSubmatch(string(output))
	if len(matches) < 2 {
		return "", fmt.Errorf("인터페이스 %s에서 MAC 주소를 찾을 수 없습니다", interfaceName)
	}

	return matches[1], nil
}

// IsInterfaceUp은 특정 인터페이스가 UP 상태인지 확인합니다
func (s *InterfaceNamingService) IsInterfaceUp(interfaceName string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// ip link show 명령어로 특정 인터페이스 정보 조회
	output, err := s.commandExecutor.ExecuteWithTimeout(ctx, 10*time.Second, "ip", "link", "show", interfaceName)
	if err != nil {
		return false, fmt.Errorf("인터페이스 %s 상태 조회 실패: %w", interfaceName, err)
	}

	outputStr := string(output)
	// UP, LOWER_UP 상태 확인 (예: "state UP" 또는 "<BROADCAST,MULTICAST,UP,LOWER_UP>")
	return strings.Contains(outputStr, "state UP") || 
		   (strings.Contains(outputStr, ",UP,") && strings.Contains(outputStr, "LOWER_UP")), nil
}

// ListNetplanFiles는 지정된 디렉토리의 netplan 파일 목록을 반환합니다
func (s *InterfaceNamingService) ListNetplanFiles(dir string) ([]string, error) {
	files, err := s.fileSystem.ListFiles(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to list files in directory %s: %w", dir, err)
	}

	return files, nil
}

// GetHostname은 시스템의 호스트네임을 반환합니다
func (s *InterfaceNamingService) GetHostname() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	output, err := s.commandExecutor.ExecuteWithTimeout(ctx, 5*time.Second, "hostname")
	if err != nil {
		return "", fmt.Errorf("failed to get hostname: %w", err)
	}

	hostname := strings.TrimSpace(string(output))
	if hostname == "" {
		return "", fmt.Errorf("hostname is empty")
	}

	// .novalocal 또는 다른 도메인 접미사 제거 (main.go와 동일한 로직)
	if idx := strings.Index(hostname, "."); idx != -1 {
		hostname = hostname[:idx]
	}

	return hostname, nil
}
