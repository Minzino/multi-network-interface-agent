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
	// RHEL의 경우 nmcli 연결 목록도 확인
	ctx := context.Background()
	nmcliConnections, _ := s.ListNmcliConnectionNames(ctx)
	
	return s.GenerateNextNameForOS(nmcliConnections)
}

// GenerateNextNameForOS는 OS에 따라 사용 가능한 다음 이름을 생성합니다
func (s *InterfaceNamingService) GenerateNextNameForOS(nmcliConnections []string) (entities.InterfaceName, error) {
	// nmcli connection 이름을 맵으로 변환하여 빠른 조회
	connMap := make(map[string]bool)
	for _, conn := range nmcliConnections {
		connMap[conn] = true
	}
	
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("multinic%d", i)
		
		// nmcli connection에 있는지 확인
		if connMap[name] {
			continue
		}
		
		// 실제 인터페이스로도 존재하는지 확인 (Ubuntu의 경우)
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
	// OS 타입을 확인하여 RHEL인 경우 nmcli connection을 확인
	ctx := context.Background()
	nmcliConnections, _ := s.ListNmcliConnectionNames(ctx)
	
	// 먼저 해당 MAC 주소로 이미 설정된 connection이 있는지 확인
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("multinic%d", i)
		
		// RHEL의 경우 nmcli connection 목록에서 확인
		isUsedInNmcli := false
		for _, conn := range nmcliConnections {
			if conn == name {
				isUsedInNmcli = true
				// 해당 connection의 MAC 주소 확인
				existingMAC, err := s.GetNmcliConnectionMAC(ctx, name)
				if err == nil && strings.EqualFold(existingMAC, macAddress) {
					// 동일한 MAC 주소를 가진 connection 발견
					return entities.NewInterfaceName(name)
				}
				break
			}
		}
		
		// Ubuntu의 경우 기존 방식대로 확인
		if !isUsedInNmcli && s.isInterfaceInUse(name) {
			// 해당 인터페이스의 MAC 주소 확인
			existingMAC, err := s.GetMacAddressForInterface(name)
			if err == nil && strings.EqualFold(existingMAC, macAddress) {
				// 동일한 MAC 주소를 가진 인터페이스 발견
				return entities.NewInterfaceName(name)
			}
		}
	}
	
	// 기존에 할당된 이름이 없으면 새로운 이름 생성
	return s.GenerateNextNameForOS(nmcliConnections)
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

// execNmcli is a helper method to execute nmcli commands with nsenter if in container
func (s *InterfaceNamingService) execNmcli(ctx context.Context, args ...string) ([]byte, error) {
	if s.isContainer {
		// In container environment, use nsenter to run in host namespace
		cmdArgs := []string{"--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid", "nmcli"}
		cmdArgs = append(cmdArgs, args...)
		return s.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, "nsenter", cmdArgs...)
	}
	// Direct execution on host
	return s.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, "nmcli", args...)
}

// ListNmcliConnectionNames는 nmcli에 설정된 모든 연결 프로파일 이름을 반환합니다.
func (s *InterfaceNamingService) ListNmcliConnectionNames(ctx context.Context) ([]string, error) {
	output, err := s.execNmcli(ctx, "-t", "-f", "NAME", "c", "show")
	if err != nil {
		return nil, fmt.Errorf("failed to execute nmcli connection show: %w", err)
	}

	lines := strings.Split(string(output), "\n")
	var names []string
	for _, line := range lines {
		name := strings.TrimSpace(line)
		if name != "" {
			names = append(names, name)
		}
	}

	return names, nil
}

// ListNetplanFiles는 지정된 디렉토리의 netplan 파일 목록을 반환합니다
func (s *InterfaceNamingService) ListNetplanFiles(dir string) ([]string, error) {
	files, err := s.fileSystem.ListFiles(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to list files in directory %s: %w", dir, err)
	}

	return files, nil
}

// GetNmcliConnectionMAC는 nmcli connection에서 MAC 주소를 조회합니다
func (s *InterfaceNamingService) GetNmcliConnectionMAC(ctx context.Context, connName string) (string, error) {
	// nmcli -t -f 802-3-ethernet.mac-address connection show {connName}
	output, err := s.execNmcli(ctx, "-t", "-f", "802-3-ethernet.mac-address", "connection", "show", connName)
	if err != nil {
		return "", fmt.Errorf("failed to get MAC address for connection %s: %w", connName, err)
	}
	
	// Output format: "802-3-ethernet.mac-address:FA:16:3E:00:BE:63"
	outputStr := strings.TrimSpace(string(output))
	parts := strings.Split(outputStr, ":")
	if len(parts) >= 7 {
		// Extract MAC address part (last 6 parts)
		macParts := parts[len(parts)-6:]
		return strings.Join(macParts, ":"), nil
	}
	
	return "", fmt.Errorf("unexpected output format from nmcli: %s", outputStr)
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

	return hostname, nil
}
