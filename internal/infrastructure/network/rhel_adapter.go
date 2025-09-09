package network

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

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
func (a *RHELAdapter) GetConfigDir() string {
	// RHEL uses /etc/sysconfig/network-scripts/ for ifcfg files
	return "/etc/sysconfig/network-scripts"
}

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

		// Bring interface down
		if _, err := a.execCommand(ctx, "ip", "link", "set", actualDevice, "down"); err != nil {
			return errors.NewNetworkError(fmt.Sprintf("Failed to bring down interface %s", actualDevice), err)
		}

		// Rename interface
		if _, err := a.execCommand(ctx, "ip", "link", "set", actualDevice, "name", ifaceName); err != nil {
			// Try to bring it back up if rename fails
			if _, bringUpErr := a.execCommand(ctx, "ip", "link", "set", actualDevice, "up"); bringUpErr != nil {
				a.logger.WithError(bringUpErr).Warn("Failed to bring interface back up after rename failure")
			}
			return errors.NewNetworkError(fmt.Sprintf("Failed to rename interface %s to %s", actualDevice, ifaceName), err)
		}

		// Bring interface up with new name
		if _, err := a.execCommand(ctx, "ip", "link", "set", ifaceName, "up"); err != nil {
			return errors.NewNetworkError(fmt.Sprintf("Failed to bring up interface %s", ifaceName), err)
		}

		a.logger.WithField("interface", ifaceName).Info("Interface renamed successfully")
	}

	// 3. Generate ifcfg file content
	configPath := filepath.Join(a.GetConfigDir(), "ifcfg-"+ifaceName)
	content := a.generateIfcfgContent(iface, ifaceName)

	a.logger.WithFields(logrus.Fields{
		"interface":   ifaceName,
		"config_path": configPath,
    	"mac_address": iface.MacAddress(),
	}).Info("About to write ifcfg file")

	// Log the full content in debug mode for troubleshooting
	a.logger.WithFields(logrus.Fields{
		"interface": ifaceName,
		"content":   content,
	}).Debug("Full ifcfg file content")

	// 4. Write the configuration file
	if err := a.fileSystem.WriteFile(configPath, []byte(content), 0644); err != nil {
		return errors.NewNetworkError(fmt.Sprintf("Failed to write ifcfg file: %s", configPath), err)
	}

	// Verify file was actually written
	if !a.fileSystem.Exists(configPath) {
		return errors.NewNetworkError(fmt.Sprintf("ifcfg file was not created: %s", configPath), nil)
	}

	a.logger.WithFields(logrus.Fields{
		"interface":   ifaceName,
		"config_path": configPath,
	}).Info("ifcfg file written successfully")

	// 5. Restart NetworkManager to apply changes
	if _, err := a.execCommand(ctx, "systemctl", "restart", "NetworkManager"); err != nil {
		a.logger.WithError(err).Error("NetworkManager restart failed")
		return errors.NewNetworkError("Failed to restart NetworkManager", err)
	}

	a.logger.WithField("interface", ifaceName).Info("NetworkManager restarted successfully")

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

	// Check if ifcfg file exists
	configPath := filepath.Join(a.GetConfigDir(), "ifcfg-"+ifaceName)
	if !a.fileSystem.Exists(configPath) {
		return errors.NewNetworkError(fmt.Sprintf("Configuration file %s not found", configPath), nil)
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

	// 1. Delete the configuration file
	configPath := filepath.Join(a.GetConfigDir(), "ifcfg-"+name)

	if err := a.fileSystem.Remove(configPath); err != nil {
		a.logger.WithError(err).WithField("interface", name).Debug("Error removing ifcfg file (can be ignored)")
	}

	// 2. Restart NetworkManager to apply the removal
	if _, err := a.execCommand(ctx, "systemctl", "restart", "NetworkManager"); err != nil {
		a.logger.WithError(err).Warn("NetworkManager restart failed during rollback")
	}

	a.logger.WithField("interface", name).Info("RHEL interface rollback/deletion completed")
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
func (a *RHELAdapter) generateIfcfgContent(iface entities.NetworkInterface, ifaceName string) string {
	content := fmt.Sprintf(`DEVICE=%s
NAME=%s
TYPE=Ethernet
ONBOOT=yes
BOOTPROTO=none`, ifaceName, ifaceName)

	// Add IP configuration if available
    if iface.Address() != "" && iface.CIDR() != "" {
        // Extract prefix from CIDR
        parts := strings.Split(iface.CIDR(), "/")
        if len(parts) == 2 {
            prefix := parts[1]
            content += fmt.Sprintf("\nIPADDR=%s\nPREFIX=%s", iface.Address(), prefix)
        }
    }

	// Add MTU if specified
    if iface.MTU() > 0 {
        content += fmt.Sprintf("\nMTU=%d", iface.MTU())
    }

	// Always add MAC address
    content += fmt.Sprintf("\nHWADDR=%s", strings.ToLower(iface.MacAddress()))

	return content
}

// GenerateIfcfgContentForTest is a test helper method
func (a *RHELAdapter) GenerateIfcfgContentForTest(iface entities.NetworkInterface, ifaceName string) string {
	return a.generateIfcfgContent(iface, ifaceName)
}
