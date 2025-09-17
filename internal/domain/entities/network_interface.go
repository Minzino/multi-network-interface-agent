package entities

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"multinic-agent/internal/domain/constants"
	domainErrors "multinic-agent/internal/domain/errors"
)

// NetworkInterface is a domain entity for network interface
type NetworkInterface struct {
	id            *InterfaceIndex
	macAddress    *MACAddress
	nodeName      *NodeName
	status        InterfaceStatus
	ipAddress     *IPAddress
	cidr          *CIDR
	mtu           *MTU
	interfaceName *InterfaceName
}

// NewNetworkInterface creates a new NetworkInterface with validatio
func NewNetworkInterface(id int, macAddr, nodeNameStr, ipAddr, cidrStr string, mtuValue int) (*NetworkInterface, error) {
	// Validate and create value objects
	interfaceIndex, err := NewInterfaceIndex(id)
	if err != nil {
		return nil, err
	}

	macAddress, err := NewMACAddress(macAddr)
	if err != nil {
		return nil, err
	}

	nodeName, err := NewNodeName(nodeNameStr)
	if err != nil {
		return nil, err
	}

	ipAddress, err := NewIPAddress(ipAddr)
	if err != nil {
		return nil, err
	}

	cidr, err := NewCIDR(cidrStr)
	if err != nil {
		return nil, err
	}

	mtu, err := NewMTU(mtuValue)
	if err != nil {
		return nil, err
	}

	interfaceName, err := NewInterfaceName(interfaceIndex.ToInterfaceName())
	if err != nil {
		return nil, err
	}

	// Validate business rules
	if !cidr.Contains(ipAddress) {
		return nil, domainErrors.NewValidationErrorWithCode("VAL015",
			fmt.Sprintf("IP address %s is not within CIDR %s", ipAddr, cidrStr), nil)
	}

	return &NetworkInterface{
		id:            interfaceIndex,
		macAddress:    macAddress,
		nodeName:      nodeName,
		status:        StatusPending,
		ipAddress:     ipAddress,
		cidr:          cidr,
		mtu:           mtu,
		interfaceName: interfaceName,
	}, nil
}

// Getters
func (ni *NetworkInterface) ID() int {
	return ni.id.Value()
}

func (ni *NetworkInterface) MacAddress() string {
	return ni.macAddress.String()
}

func (ni *NetworkInterface) AttachedNodeName() string {
	return ni.nodeName.String()
}

func (ni *NetworkInterface) Status() InterfaceStatus {
	return ni.status
}

func (ni *NetworkInterface) Address() string {
	return ni.ipAddress.String()
}

func (ni *NetworkInterface) CIDR() string {
	return ni.cidr.String()
}

func (ni *NetworkInterface) MTU() int {
	return ni.mtu.Value()
}

func (ni *NetworkInterface) InterfaceName() string {
	return ni.interfaceName.String()
}

// Business methods
func (ni *NetworkInterface) MarkAsConfigured() {
	ni.status = StatusConfigured
}

func (ni *NetworkInterface) MarkAsFailed() {
	ni.status = StatusFailed
}

func (ni *NetworkInterface) IsConfigured() bool {
	return ni.status == StatusConfigured
}

func (ni *NetworkInterface) IsFailed() bool {
	return ni.status == StatusFailed
}

func (ni *NetworkInterface) IsPending() bool {
	return ni.status == StatusPending
}

// CanApplyTo checks if this interface can be applied to the given node
func (ni *NetworkInterface) CanApplyTo(targetNodeName string) bool {
	targetNode, err := NewNodeName(targetNodeName)
	if err != nil {
		return false
	}
	return ni.nodeName.Equals(targetNode)
}

// UpdateMTU updates the MTU value with validation
func (ni *NetworkInterface) UpdateMTU(newMTU int) error {
	mtu, err := NewMTU(newMTU)
	if err != nil {
		return err
	}
	ni.mtu = mtu
	return nil
}

// IsJumboFrame checks if this interface uses jumbo frames
func (ni *NetworkInterface) IsJumboFrame() bool {
	return ni.mtu.IsJumboFrame()
}

// InterfaceStatus represents the state of an interface
type InterfaceStatus int

const (
	StatusPending InterfaceStatus = iota
	StatusConfigured
	StatusFailed
)

// InterfaceName is a value object representing multinic interface name
type InterfaceName struct {
	value string
	index int
}

// interfaceNamePattern은 유효한 인터페이스 이름 패턴입니다
var interfaceNamePattern = regexp.MustCompile(`^` + constants.InterfacePrefix + `([0-9]+)$`)

// NewInterfaceName creates a new interface name
func NewInterfaceName(name string) (*InterfaceName, error) {
	if name == "" {
		return nil, domainErrors.NewValidationErrorWithCode("VAL016", "interface name cannot be empty", nil)
	}

	matches := interfaceNamePattern.FindStringSubmatch(name)
	if len(matches) != 2 {
		return nil, domainErrors.NewValidationErrorWithCode("VAL017",
			fmt.Sprintf("invalid interface name format: %s (expected: %s<number>)", name, constants.InterfacePrefix), nil)
	}

	index, err := strconv.Atoi(matches[1])
	if err != nil {
		return nil, domainErrors.NewValidationErrorWithCode("VAL018",
			fmt.Sprintf("invalid interface index in name: %s", name), err)
	}

	if index < 0 || index >= constants.MaxInterfaces {
		return nil, domainErrors.NewValidationErrorWithCode("VAL019",
			fmt.Sprintf("interface index out of range: %d (0-%d)", index, constants.MaxInterfaces-1), nil)
	}

	return &InterfaceName{
		value: name,
		index: index,
	}, nil
}

// String returns the string representation of interface name
func (n *InterfaceName) String() string {
	return n.value
}

// Index returns the interface index
func (n *InterfaceName) Index() int {
	return n.index
}

// Equals checks if two interface names are equal
func (n *InterfaceName) Equals(other *InterfaceName) bool {
	if other == nil {
		return false
	}
	return n.value == other.value
}

var (
	// Deprecated: Use domain errors instead
	ErrInvalidMacAddress    = errors.New("invalid MAC address format")
	ErrInvalidInterfaceName = errors.New("invalid interface name")
	ErrInvalidNodeName      = errors.New("invalid node name")
)

// Validate verifies the validity of NetworkInterface
func (ni *NetworkInterface) Validate() error {
	if !isValidMacAddress(ni.MacAddress()) {
		return ErrInvalidMacAddress
	}
	if ni.AttachedNodeName() == "" {
		return ErrInvalidNodeName
	}
	return nil
}

// isValidMacAddress validates MAC address format
func isValidMacAddress(mac string) bool {
	macRegex := regexp.MustCompile(`^([0-9A-Fa-f]{2}[:-]){5}([0-9A-Fa-f]{2})$`)
	return macRegex.MatchString(mac)
}

// isValidInterfaceName validates interface name format
func isValidInterfaceName(name string) bool {
	matched, _ := regexp.MatchString(`^multinic[0-9]$`, name)
	return matched
}
