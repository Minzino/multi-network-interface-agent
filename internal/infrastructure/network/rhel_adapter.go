package network

import (
    "context"
    "fmt"
    "path/filepath"
    "strings"
    "time"

    "strconv"
    "multinic-agent/internal/domain/constants"
    "multinic-agent/internal/domain/entities"
    "multinic-agent/internal/domain/errors"
    "multinic-agent/internal/domain/interfaces"

	"github.com/sirupsen/logrus"
)

// RHELAdapter configures network for RHEL-based OS using direct file modification.
type RHELAdapter struct {
	commandExecutor interfaces.CommandExecutor
	fileSystem      interfaces.FileSystem
	logger          *logrus.Logger
	isContainer     bool // indicates if running in container
}

// NewRHELAdapter creates a new RHELAdapter.
func NewRHELAdapter(
	executor interfaces.CommandExecutor,
	fileSystem interfaces.FileSystem,
	logger *logrus.Logger,
) *RHELAdapter {
	// Check if running in container by checking if /host exists
	isContainer := false
	if _, err := executor.ExecuteWithTimeout(context.Background(), 1*time.Second, "test", "-d", "/host"); err == nil {
		isContainer = true
	}

	return &RHELAdapter{
		commandExecutor: executor,
		fileSystem:      fileSystem,
		logger:          logger,
		isContainer:     isContainer,
	}
}

// GetConfigDir returns the directory path where configuration files are stored
// RHEL uses traditional network-scripts directory for interface configuration
func (a *RHELAdapter) GetConfigDir() string { return constants.NetworkManagerDir }

// execCommand is a helper method to execute commands with nsenter if in container
func (a *RHELAdapter) execCommand(ctx context.Context, command string, args ...string) ([]byte, error) {
	if a.isContainer {
		// In container environment, use nsenter to run in host namespace
		cmdArgs := []string{"--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid", command}
		cmdArgs = append(cmdArgs, args...)
		return a.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, "nsenter", cmdArgs...)
	}
	// Direct execution on host
	return a.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, command, args...)
}

// Configure configures network interface by renaming device and creating ifcfg file.
func (a *RHELAdapter) Configure(ctx context.Context, iface entities.NetworkInterface, name entities.InterfaceName) error {
	ifaceName := name.String()
    macAddress := iface.MacAddress()

	a.logger.WithFields(logrus.Fields{
		"interface": ifaceName,
		"mac":       macAddress,
	}).Info("Starting RHEL interface configuration with device rename approach")

	// 1. Find the actual device name by MAC address
	actualDevice, err := a.findDeviceByMAC(ctx, macAddress)
	if err != nil {
		return errors.NewNetworkError(fmt.Sprintf("Failed to find device with MAC %s", macAddress), err)
	}

	a.logger.WithFields(logrus.Fields{
		"target_name":   ifaceName,
		"actual_device": actualDevice,
		"mac":           macAddress,
	}).Debug("Found actual device for MAC address")

    // 2. Check if device name needs to be changed
    if actualDevice != ifaceName {
		a.logger.WithFields(logrus.Fields{
			"from": actualDevice,
			"to":   ifaceName,
		}).Info("Renaming network interface")

        // Try rename without down first; fallback to down
        if _, err := a.execCommand(ctx, "ip", "link", "set", actualDevice, "name", ifaceName); err != nil {
            _, _ = a.execCommand(ctx, "ip", "link", "set", actualDevice, "down")
            if _, err2 := a.execCommand(ctx, "ip", "link", "set", actualDevice, "name", ifaceName); err2 != nil {
                return errors.NewNetworkError(fmt.Sprintf("Failed to rename interface %s to %s", actualDevice, ifaceName), err2)
            }
            _, _ = a.execCommand(ctx, "ip", "link", "set", ifaceName, "up")
        }

		a.logger.WithField("interface", ifaceName).Info("Interface renamed successfully")
	}

    // 3. Runtime MTU/IP
    if iface.MTU() > 0 { if _, err := a.execCommand(ctx, "ip", "link", "set", ifaceName, "mtu", fmt.Sprintf("%d", iface.MTU())); err != nil { return errors.NewNetworkError("Failed to set MTU", err) } }
    if addr := strings.TrimSpace(iface.Address()); addr != "" && strings.TrimSpace(iface.CIDR()) != "" {
        parts := strings.Split(iface.CIDR(), "/"); if len(parts) == 2 {
            full := fmt.Sprintf("%s/%s", addr, parts[1])
            if _, err := a.execCommand(ctx, "ip", "addr", "replace", full, "dev", ifaceName); err != nil { return errors.NewNetworkError("Failed to set IPv4", err) }
        }
    }
    if _, err := a.execCommand(ctx, "ip", "link", "set", ifaceName, "up"); err != nil { return errors.NewNetworkError("Failed to set link up", err) }

    // 4. Persist files: .link + .nmconnection with 9X prefix
    idx := extractIndexRHEL(ifaceName)
    linkPath := filepath.Join("/etc/systemd/network", fmt.Sprintf("9%d-%s.link", idx, ifaceName))
    nmPath := filepath.Join(a.GetConfigDir(), fmt.Sprintf("9%d-%s.nmconnection", idx, ifaceName))
    linkContent := fmt.Sprintf("[Match]\nMACAddress=%s\n[Link]\nName=%s\n", strings.ToLower(macAddress), ifaceName)
    if err := a.fileSystem.WriteFile(linkPath, []byte(linkContent), 0644); err != nil { return errors.NewSystemError("failed to write .link", err) }
    nmContent := a.generateNMConnection(iface, ifaceName)
    if err := a.fileSystem.WriteFile(nmPath, []byte(nmContent), 0600); err != nil { return errors.NewSystemError("failed to write .nmconnection", err) }
    a.logger.WithFields(logrus.Fields{"link": linkPath, "nmconnection": nmPath}).Info("RHEL persist files written (no immediate reload)")
    return nil
}

// Validate verifies that the configured interface exists.
func (a *RHELAdapter) Validate(ctx context.Context, name entities.InterfaceName) error {
	ifaceName := name.String()
	a.logger.WithField("interface", ifaceName).Debug("Starting interface validation")

	// Check if interface exists using ip command
	output, err := a.execCommand(ctx, "ip", "link", "show", ifaceName)
	if err != nil {
		return errors.NewNetworkError(fmt.Sprintf("Interface %s not found", ifaceName), err)
	}

    // Check if persist files exist
    idx := extractIndexRHEL(ifaceName)
    linkPath := filepath.Join("/etc/systemd/network", fmt.Sprintf("9%d-%s.link", idx, ifaceName))
    nmPath := filepath.Join(a.GetConfigDir(), fmt.Sprintf("9%d-%s.nmconnection", idx, ifaceName))
    if !a.fileSystem.Exists(linkPath) || !a.fileSystem.Exists(nmPath) {
        return errors.NewNetworkError("persist files not found", nil)
    }

	a.logger.WithFields(logrus.Fields{
		"interface": ifaceName,
		"output":    string(output),
	}).Debug("Interface validation successful")

	return nil
}

// Rollback removes interface configuration by deleting the ifcfg file.
func (a *RHELAdapter) Rollback(ctx context.Context, name string) error {
	a.logger.WithField("interface", name).Info("Starting RHEL interface rollback/deletion")

    idx := extractIndexRHEL(name)
    linkPath := filepath.Join("/etc/systemd/network", fmt.Sprintf("9%d-%s.link", idx, name))
    nmPath := filepath.Join(a.GetConfigDir(), fmt.Sprintf("9%d-%s.nmconnection", idx, name))
    if err := a.fileSystem.Remove(linkPath); err != nil {
        a.logger.WithError(err).WithField("link", linkPath).Debug("Error removing .link (ignored)")
    }
    if err := a.fileSystem.Remove(nmPath); err != nil {
        a.logger.WithError(err).WithField("nm", nmPath).Debug("Error removing .nmconnection (ignored)")
    }
    a.logger.WithField("interface", name).Info("RHEL interface rollback (files removed; no immediate reload)")
    return nil
}

// findDeviceByMAC finds the actual device name by MAC address
func (a *RHELAdapter) findDeviceByMAC(ctx context.Context, macAddress string) (string, error) {
	// Get all devices with their general info in one command
	output, err := a.execCommand(ctx, "ip", "link", "show")
	if err != nil {
		return "", fmt.Errorf("failed to list devices: %w", err)
	}

	// Parse ip link show output
	// Format:
	// 2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 ...
	//     link/ether fa:16:3e:00:be:63 brd ff:ff:ff:ff:ff:ff
	lines := strings.Split(string(output), "\n")
	targetMAC := strings.ToLower(macAddress)

	var currentDevice string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Check if this is a device line (starts with number)
		if strings.Contains(line, ":") && len(line) > 0 && line[0] >= '0' && line[0] <= '9' {
			// Extract device name (e.g., "2: eth0:" -> "eth0")
			parts := strings.Split(line, ":")
			if len(parts) >= 2 {
				currentDevice = strings.TrimSpace(parts[1])
			}
		} else if strings.Contains(line, "link/ether") && currentDevice != "" {
			// This line contains MAC address
			fields := strings.Fields(line)
			for i, field := range fields {
				if field == "link/ether" && i+1 < len(fields) {
					mac := strings.ToLower(fields[i+1])
					if mac == targetMAC {
						a.logger.WithFields(logrus.Fields{
							"device": currentDevice,
							"mac":    macAddress,
						}).Info("Found device for MAC address")
						return currentDevice, nil
					}
					break
				}
			}
		}
	}

	return "", fmt.Errorf("no device found with MAC address %s", macAddress)
}

// generateIfcfgContent generates the ifcfg file content
func (a *RHELAdapter) generateNMConnection(iface entities.NetworkInterface, ifaceName string) string {
    b := &strings.Builder{}
    fmt.Fprintf(b, "[connection]\n")
    fmt.Fprintf(b, "id=%s\n", ifaceName)
    fmt.Fprintf(b, "type=ethernet\n")
    fmt.Fprintf(b, "interface-name=%s\nautoconnect=true\n\n", ifaceName)
    fmt.Fprintf(b, "[ethernet]\nmac-address=%s\n", strings.ToLower(iface.MacAddress()))
    if iface.MTU() > 0 { fmt.Fprintf(b, "mtu=%d\n", iface.MTU()) }
    fmt.Fprintf(b, "\n[ipv4]\nmethod=manual\n")
    if iface.Address() != "" && iface.CIDR() != "" {
        parts := strings.Split(iface.CIDR(), "/"); if len(parts) == 2 {
            fmt.Fprintf(b, "address1=%s/%s\n", iface.Address(), parts[1])
        }
    }
    fmt.Fprintf(b, "never-default=true\n\n[ipv6]\nmethod=ignore\n")
    return b.String()
}

func extractIndexRHEL(name string) int {
    if strings.HasPrefix(name, constants.InterfacePrefix) {
        idx := strings.TrimPrefix(name, constants.InterfacePrefix)
        if n, err := strconv.Atoi(idx); err == nil { return n }
    }
    return 0
}
