package entities

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"multinic-agent/internal/domain/constants"
	"multinic-agent/internal/domain/errors"
)

// IPAddress는 IP 주소를 나타내는 값 객체입니다
type IPAddress struct {
	value string
	ip    net.IP
}

// NewIPAddress는 새로운 IPAddress를 생성합니다
func NewIPAddress(value string) (*IPAddress, error) {
	if value == "" {
		return nil, errors.NewValidationErrorWithCode("VAL002", "IP address cannot be empty", nil)
	}

	ip := net.ParseIP(value)
	if ip == nil {
		return nil, errors.NewValidationErrorWithCode("VAL003", fmt.Sprintf("invalid IP address format: %s", value), nil)
	}

	return &IPAddress{
		value: value,
		ip:    ip,
	}, nil
}

// String은 IP 주소의 문자열 표현을 반환합니다
func (ip *IPAddress) String() string {
	return ip.value
}

// IsIPv4는 IPv4 주소인지 확인합니다
func (ip *IPAddress) IsIPv4() bool {
	return ip.ip.To4() != nil
}

// IsIPv6는 IPv6 주소인지 확인합니다
func (ip *IPAddress) IsIPv6() bool {
	return ip.ip.To4() == nil && ip.ip.To16() != nil
}

// Equals는 두 IP 주소가 같은지 비교합니다
func (ip *IPAddress) Equals(other *IPAddress) bool {
	if other == nil {
		return false
	}
	return ip.ip.Equal(other.ip)
}

// MACAddress는 MAC 주소를 나타내는 값 객체입니다
type MACAddress struct {
	value string
	mac   net.HardwareAddr
}

// NewMACAddress는 새로운 MACAddress를 생성합니다
func NewMACAddress(value string) (*MACAddress, error) {
	if value == "" {
		return nil, errors.NewValidationErrorWithCode("VAL004", "MAC address cannot be empty", nil)
	}

	mac, err := net.ParseMAC(value)
	if err != nil {
		return nil, errors.NewValidationErrorWithCode("VAL005", fmt.Sprintf("invalid MAC address format: %s", value), err)
	}

	return &MACAddress{
		value: value,
		mac:   mac,
	}, nil
}

// String은 MAC 주소의 문자열 표현을 반환합니다
func (mac *MACAddress) String() string {
	return mac.value
}

// Canonical은 정규화된 MAC 주소를 반환합니다 (소문자, 콜론 구분)
func (mac *MACAddress) Canonical() string {
	return mac.mac.String()
}

// Equals는 두 MAC 주소가 같은지 비교합니다
func (mac *MACAddress) Equals(other *MACAddress) bool {
	if other == nil {
		return false
	}
	return strings.EqualFold(mac.mac.String(), other.mac.String())
}

// CIDR은 CIDR 표기법을 나타내는 값 객체입니다
type CIDR struct {
	value   string
	network *net.IPNet
}

// NewCIDR은 새로운 CIDR을 생성합니다
func NewCIDR(value string) (*CIDR, error) {
	if value == "" {
		return nil, errors.NewValidationErrorWithCode("VAL006", "CIDR cannot be empty", nil)
	}

	_, network, err := net.ParseCIDR(value)
	if err != nil {
		return nil, errors.NewValidationErrorWithCode("VAL007", fmt.Sprintf("invalid CIDR format: %s", value), err)
	}

	return &CIDR{
		value:   value,
		network: network,
	}, nil
}

// String은 CIDR의 문자열 표현을 반환합니다
func (c *CIDR) String() string {
	return c.value
}

// Contains는 주어진 IP가 이 CIDR 범위에 포함되는지 확인합니다
func (c *CIDR) Contains(ip *IPAddress) bool {
	if ip == nil {
		return false
	}
	return c.network.Contains(ip.ip)
}

// NetworkAddress는 네트워크 주소를 반환합니다
func (c *CIDR) NetworkAddress() *IPAddress {
	addr, _ := NewIPAddress(c.network.IP.String())
	return addr
}

// MTU는 MTU 값을 나타내는 값 객체입니다
type MTU struct {
	value int
}

// NewMTU는 새로운 MTU를 생성합니다
func NewMTU(value int) (*MTU, error) {
	if value < 68 {
		return nil, errors.NewValidationErrorWithCode("VAL008", fmt.Sprintf("MTU too small: %d (minimum: 68)", value), nil)
	}
	if value > 65536 {
		return nil, errors.NewValidationErrorWithCode("VAL009", fmt.Sprintf("MTU too large: %d (maximum: 65536)", value), nil)
	}

	return &MTU{value: value}, nil
}

// Value는 MTU 값을 반환합니다
func (m *MTU) Value() int {
	return m.value
}

// String은 MTU의 문자열 표현을 반환합니다
func (m *MTU) String() string {
	return strconv.Itoa(m.value)
}

// IsJumboFrame은 점보 프레임인지 확인합니다 (MTU > 1500)
func (m *MTU) IsJumboFrame() bool {
	return m.value > 1500
}

// InterfaceIndex는 인터페이스 인덱스를 나타내는 값 객체입니다
type InterfaceIndex struct {
	value int
}

// NewInterfaceIndex는 새로운 InterfaceIndex를 생성합니다
func NewInterfaceIndex(value int) (*InterfaceIndex, error) {
	if value < 0 {
		return nil, errors.NewValidationErrorWithCode("VAL010", fmt.Sprintf("interface index cannot be negative: %d", value), nil)
	}
	if value >= constants.MaxInterfaces {
		return nil, errors.NewValidationErrorWithCode("VAL011", fmt.Sprintf("interface index too large: %d (maximum: %d)", value, constants.MaxInterfaces-1), nil)
	}

	return &InterfaceIndex{value: value}, nil
}

// Value는 인터페이스 인덱스 값을 반환합니다
func (idx *InterfaceIndex) Value() int {
	return idx.value
}

// String은 인터페이스 인덱스의 문자열 표현을 반환합니다
func (idx *InterfaceIndex) String() string {
	return strconv.Itoa(idx.value)
}

// ToInterfaceName은 인터페이스 이름으로 변환합니다
func (idx *InterfaceIndex) ToInterfaceName() string {
	return fmt.Sprintf("%s%d", constants.InterfacePrefix, idx.value)
}

// NodeName은 노드 이름을 나타내는 값 객체입니다
type NodeName struct {
	value string
}

// nodeNamePattern은 유효한 노드 이름 패턴입니다
var nodeNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// NewNodeName은 새로운 NodeName을 생성합니다
func NewNodeName(value string) (*NodeName, error) {
	if value == "" {
		return nil, errors.NewValidationErrorWithCode("VAL012", "node name cannot be empty", nil)
	}
	
	if len(value) > 253 {
		return nil, errors.NewValidationErrorWithCode("VAL013", fmt.Sprintf("node name too long: %d characters (maximum: 253)", len(value)), nil)
	}

	if !nodeNamePattern.MatchString(value) {
		return nil, errors.NewValidationErrorWithCode("VAL014", fmt.Sprintf("invalid node name format: %s", value), nil)
	}

	return &NodeName{value: value}, nil
}

// String은 노드 이름의 문자열 표현을 반환합니다
func (n *NodeName) String() string {
	return n.value
}

// Equals는 두 노드 이름이 같은지 비교합니다
func (n *NodeName) Equals(other *NodeName) bool {
	if other == nil {
		return false
	}
	return n.value == other.value
}