package services

import (
	"context"
	"fmt"
	"multinic-agent/internal/domain/entities"
	"multinic-agent/internal/domain/interfaces"
	"regexp"
	"strings"
	"sync"
	"time"
)

// InterfaceNamingService는 네트워크 인터페이스 이름을 관리하는 도메인 서비스입니다
type InterfaceNamingService struct {
	fileSystem      interfaces.FileSystem
	commandExecutor interfaces.CommandExecutor
	isContainer     bool       // indicates if running in container
	namingMutex     sync.Mutex // 인터페이스 이름 생성 동시성 제어
	// 사전 배정용 상태(프로세스 수명 동안만 유지)
	reservedByMac map[string]string // mac(lower) -> name(multinicX)
	reservedNames map[string]bool   // name(multinicX) -> taken
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
		reservedByMac:   make(map[string]string),
		reservedNames:   make(map[string]bool),
	}
}

// GenerateNextName은 사용 가능한 다음 인터페이스 이름을 생성합니다
func (s *InterfaceNamingService) GenerateNextName() (*entities.InterfaceName, error) {
	s.namingMutex.Lock()
	defer s.namingMutex.Unlock()

	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("multinic%d", i)

		// 실제 인터페이스로 존재하는지 확인
		if s.isInterfaceInUse(name) {
			continue
		}

		// 사용 가능한 이름 발견
		return entities.NewInterfaceName(name)
	}

	return nil, fmt.Errorf("사용 가능한 인터페이스 이름이 없습니다 (multinic0-9 모두 사용 중)")
}

// GenerateNextNameForMAC은 특정 MAC 주소에 대한 인터페이스 이름을 생성합니다
// 이미 해당 MAC 주소로 설정된 인터페이스가 있다면 해당 이름을 재사용합니다
func (s *InterfaceNamingService) GenerateNextNameForMAC(macAddress string) (*entities.InterfaceName, error) {
	s.namingMutex.Lock()
	defer s.namingMutex.Unlock()

	macLower := strings.ToLower(macAddress)

	// 이미 사전 배정된 이름이 있으면 그걸 사용
	if name, ok := s.reservedByMac[macLower]; ok {
		return entities.NewInterfaceName(name)
	}

	// 먼저 해당 MAC 주소로 이미 설정된 인터페이스가 있는지 확인
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("multinic%d", i)

		// ip 명령어로 MAC 주소 확인
		if s.isNameTaken(name) {
			// 해당 인터페이스의 MAC 주소 확인
			existingMAC, err := s.GetMacAddressForInterface(name)
			if err == nil && strings.EqualFold(existingMAC, macAddress) {
				// 동일한 MAC 주소를 가진 인터페이스 발견
				s.reservedByMac[macLower] = name
				s.reservedNames[name] = true
				return entities.NewInterfaceName(name)
			}
		}
	}

	// 기존에 할당된 이름이 없으면 새로운 이름 생성 (이미 락이 걸린 상태이므로 내부 함수 호출)
	name, err := s.generateNextNameInternal()
	if err == nil {
		s.reservedByMac[macLower] = name.String()
		s.reservedNames[name.String()] = true
	}
	return name, err
}

// generateNextNameInternal은 락이 이미 걸린 상태에서 호출되는 내부 함수입니다
func (s *InterfaceNamingService) generateNextNameInternal() (*entities.InterfaceName, error) {
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("multinic%d", i)

		// 실제 인터페이스로 존재하는지 확인
		if s.isNameTaken(name) {
			continue
		}

		// 사용 가능한 이름 발견
		return entities.NewInterfaceName(name)
	}

	return nil, fmt.Errorf("사용 가능한 인터페이스 이름이 없습니다 (multinic0-9 모두 사용 중)")
}

// isInterfaceInUse는 인터페이스가 이미 사용 중인지 확인합니다
func (s *InterfaceNamingService) isInterfaceInUse(name string) bool {
	// /sys/class/net 디렉토리에서 인터페이스 확인
	return s.fileSystem.Exists(fmt.Sprintf("/sys/class/net/%s", name))
}

// isNameTaken은 시스템에 존재하거나 사전 배정된 이름인지 확인합니다
func (s *InterfaceNamingService) isNameTaken(name string) bool {
	if s.isInterfaceInUse(name) {
		return true
	}
	if s.reservedNames[name] {
		return true
	}
	return false
}

// ReserveNamesForInterfaces는 주어진 인터페이스들에 대해 고유한 multinicX 이름을 사전 배정합니다.
// - 이미 시스템에 존재하며 MAC이 일치하는 multinicX는 재사용
// - 나머지는 비어있는 가장 낮은 번호로 배정
func (s *InterfaceNamingService) ReserveNamesForInterfaces(ifaces []entities.NetworkInterface) (map[string]entities.InterfaceName, error) {
	s.namingMutex.Lock()
	defer s.namingMutex.Unlock()

	result := make(map[string]entities.InterfaceName)

	// 1) 기존 multinicX 중 MAC이 일치하는 경우 재사용
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("multinic%d", i)
		if !s.isInterfaceInUse(name) {
			continue
		}
		mac, err := s.GetMacAddressForInterface(name)
		if err != nil || mac == "" {
			continue
		}
		macLower := strings.ToLower(mac)
		// 요청 목록에 포함되는 MAC만 배정
		for _, iface := range ifaces {
			if strings.EqualFold(iface.MacAddress(), macLower) || strings.EqualFold(strings.ToLower(iface.MacAddress()), macLower) {
				if _, exists := s.reservedByMac[macLower]; !exists {
					if en, err := entities.NewInterfaceName(name); err == nil {
						s.reservedByMac[macLower] = name
						s.reservedNames[name] = true
						result[macLower] = *en
					}
				}
			}
		}
	}

	// 2) 남은 MAC들에 대해 비어있는 이름을 순차 배정
	for _, iface := range ifaces {
		macLower := strings.ToLower(iface.MacAddress())
		if _, ok := s.reservedByMac[macLower]; ok {
			continue
		}
		// 다음 가용 이름 찾기
		var chosen string
		for i := 0; i < 10; i++ {
			candidate := fmt.Sprintf("multinic%d", i)
			if !s.isNameTaken(candidate) {
				chosen = candidate
				break
			}
		}
		if chosen == "" {
			return nil, fmt.Errorf("사용 가능한 인터페이스 이름이 없습니다 (multinic0-9 모두 사용/예약됨)")
		}
		s.reservedByMac[macLower] = chosen
		s.reservedNames[chosen] = true
		if en, err := entities.NewInterfaceName(chosen); err == nil {
			result[macLower] = *en
		}
	}

	return result, nil
}

// GetCurrentMultinicInterfaces는 현재 시스템에 존재하는 모든 multinic 인터페이스를 반환합니다
// ip a 명령어를 통해 실제 네트워크 인터페이스를 확인합니다
func (s *InterfaceNamingService) GetCurrentMultinicInterfaces() []entities.InterfaceName {
	var interfaces []entities.InterfaceName

	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("multinic%d", i)
		if s.isInterfaceInUse(name) {
			if interfaceName, err := entities.NewInterfaceName(name); err == nil {
				interfaces = append(interfaces, *interfaceName)
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

// GetAltNames는 인터페이스의 대체 이름(altname) 목록을 반환합니다.
func (s *InterfaceNamingService) GetAltNames(interfaceName string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := s.commandExecutor.ExecuteWithTimeout(ctx, 5*time.Second, "ip", "link", "show", interfaceName)
	if err != nil {
		return nil, fmt.Errorf("failed to read interface %s: %w", interfaceName, err)
	}
	var alts []string
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "altname ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				alts = append(alts, parts[1])
			}
		}
	}
	return alts, nil
}

// RenameInterface는 인터페이스 이름을 변경합니다.
func (s *InterfaceNamingService) RenameInterface(oldName, newName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.commandExecutor.ExecuteWithTimeout(ctx, 5*time.Second, "ip", "link", "set", "dev", oldName, "name", newName)
	if err != nil {
		return fmt.Errorf("failed to rename %s to %s: %w", oldName, newName, err)
	}
	return nil
}

// InterfaceExists는 지정 이름의 인터페이스가 존재하는지 확인합니다.
func (s *InterfaceNamingService) InterfaceExists(name string) bool {
	return s.isInterfaceInUse(name)
}

// FindInterfaceNameByMAC는 시스템 전체 인터페이스 중 주어진 MAC을 가진 인터페이스 이름을 찾습니다.
// ip -o link show 출력에서 라인 단위로 파싱하여 인터페이스명과 MAC을 매칭합니다.
func (s *InterfaceNamingService) FindInterfaceNameByMAC(macAddress string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	output, err := s.commandExecutor.ExecuteWithTimeout(ctx, 10*time.Second, "ip", "-o", "link", "show")
	if err != nil {
		return "", fmt.Errorf("시스템 인터페이스 나열 실패: %w", err)
	}

	macLower := strings.ToLower(strings.TrimSpace(macAddress))
	// 예: "2: ens3: <...> mtu ... qdisc ... state ... link/ether fa:16:3e:e8:ae:9d brd ..."
	lineRe := regexp.MustCompile(`^\s*\d+:\s+([^:]+):.*link/ether\s+([0-9A-Fa-f:]{17})`) // 인터페이스명, MAC
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		m := lineRe.FindStringSubmatch(line)
		if len(m) == 3 {
			name := m[1]
			mac := strings.ToLower(m[2])
			if mac == macLower {
				return name, nil
			}
		}
	}
	return "", fmt.Errorf("MAC %s 를 가진 인터페이스를 찾지 못했습니다", macAddress)
}

// IsMacPresent는 시스템에 해당 MAC을 가진 인터페이스가 존재하는지 여부를 반환합니다.
func (s *InterfaceNamingService) IsMacPresent(macAddress string) bool {
	if name, err := s.FindInterfaceNameByMAC(macAddress); err == nil && name != "" {
		return true
	}
	return false
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

// GetInterfaceMTU는 특정 인터페이스의 MTU 값을 반환합니다
func (s *InterfaceNamingService) GetInterfaceMTU(interfaceName string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := s.commandExecutor.ExecuteWithTimeout(ctx, 5*time.Second, "ip", "link", "show", interfaceName)
	if err != nil {
		return 0, err
	}
	re := regexp.MustCompile(`\bmtu\s+(\d+)\b`)
	m := re.FindStringSubmatch(string(output))
	if len(m) < 2 {
		return 0, fmt.Errorf("failed to parse MTU for %s", interfaceName)
	}
	// parse int
	var mtu int
	_, convErr := fmt.Sscanf(m[1], "%d", &mtu)
	if convErr != nil {
		return 0, convErr
	}
	return mtu, nil
}

// GetIPv4WithPrefix는 "A.B.C.D/P" 형태의 IPv4 주소를 반환합니다
func (s *InterfaceNamingService) GetIPv4WithPrefix(interfaceName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := s.commandExecutor.ExecuteWithTimeout(ctx, 5*time.Second, "ip", "-o", "-4", "addr", "show", "dev", interfaceName)
	if err != nil {
		return "", err
	}
	re := regexp.MustCompile(`\binet\s+(\d+\.\d+\.\d+\.\d+/\d+)`)
	m := re.FindStringSubmatch(string(output))
	if len(m) < 2 {
		return "", fmt.Errorf("failed to parse IPv4 for %s", interfaceName)
	}
	return m[1], nil
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
