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
// RHEL/NetworkManager stores connection profiles in /etc/NetworkManager/system-connections/
func (a *RHELAdapter) GetConfigDir() string {
	return "/etc/NetworkManager/system-connections"
}

// execNmcli is a helper method to execute nmcli commands with nsenter if in container
func (a *RHELAdapter) execNmcli(ctx context.Context, args ...string) ([]byte, error) {
	if a.isContainer {
		// In container environment, use nsenter to run in host namespace
		cmdArgs := []string{"--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid", "nmcli"}
		cmdArgs = append(cmdArgs, args...)
		return a.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, "nsenter", cmdArgs...)
	}
	// Direct execution on host
	return a.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, "nmcli", args...)
}

// Configure configures network interface by directly modifying nmconnection file.
func (a *RHELAdapter) Configure(ctx context.Context, iface entities.NetworkInterface, name entities.InterfaceName) error {
	ifaceName := name.String()
	macAddress := iface.MacAddress

	a.logger.WithFields(logrus.Fields{
		"interface": ifaceName,
		"mac":       macAddress,
	}).Info("Starting RHEL interface configuration with direct file modification")

	// 1. Find the actual device name by MAC address
	actualDevice, err := a.findDeviceByMAC(ctx, macAddress)
	if err != nil {
		return errors.NewNetworkError(fmt.Sprintf("Failed to find device with MAC %s", macAddress), err)
	}

	a.logger.WithFields(logrus.Fields{
		"connection_name": ifaceName,
		"actual_device":   actualDevice,
		"mac":            macAddress,
	}).Debug("Found actual device for MAC address")

	// 2. Generate nmconnection file content
	configPath := filepath.Join(a.GetConfigDir(), ifaceName+".nmconnection")
	content := a.generateNmConnectionContent(iface, ifaceName, actualDevice)

	// 3. Write the configuration file directly
	if err := a.fileSystem.WriteFile(configPath, []byte(content), 0600); err != nil {
		return errors.NewNetworkError(fmt.Sprintf("Failed to write nmconnection file: %s", configPath), err)
	}

	a.logger.WithFields(logrus.Fields{
		"interface":   ifaceName,
		"config_path": configPath,
	}).Debug("nmconnection file written")

	// 4. Reload NetworkManager to apply changes
	if err := a.reloadNetworkManager(ctx); err != nil {
		a.logger.WithError(err).Warn("NetworkManager reload failed, continuing anyway")
	}

	// 5. Verify the connection is working
	time.Sleep(2 * time.Second)
	
	if err := a.Validate(ctx, name); err != nil {
		a.logger.WithError(err).Error("Interface validation failed after configuration")
		// Rollback if validation fails
		if rollbackErr := a.Rollback(ctx, ifaceName); rollbackErr != nil {
			a.logger.WithError(rollbackErr).Warn("Error during rollback after validation failure")
		}
		return errors.NewNetworkError(fmt.Sprintf("Interface validation failed after configuration: %s", ifaceName), err)
	}

	a.logger.WithField("interface", ifaceName).Info("RHEL interface configuration completed")
	return nil
}

// Validate verifies that the configured interface is properly activated.
func (a *RHELAdapter) Validate(ctx context.Context, name entities.InterfaceName) error {
	ifaceName := name.String()
	a.logger.WithField("interface", ifaceName).Info("Starting nmcli interface validation")

	// Check connection status using `nmcli connection show`
	// In RHEL, the device name doesn't change, only the CONNECTION name is multinic0
	output, err := a.execNmcli(ctx, "connection", "show", "--active")
	if err != nil {
		return errors.NewNetworkError(fmt.Sprintf("nmcli connection show execution failed: %s", ifaceName), err)
	}

	// Check if our connection is active
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		// NAME UUID TYPE DEVICE format
		if len(fields) >= 4 && fields[0] == ifaceName {
			// Connection is active
			a.logger.WithFields(logrus.Fields{
				"connection": ifaceName,
				"device":     fields[3],
			}).Info("nmcli connection validation successful")
			return nil
		}
	}

	// If not found in active connections, it might have failed to activate
	// Let's check all connections to see if it exists but is not active
	allOutput, err := a.execNmcli(ctx, "connection", "show")
	if err == nil {
		for _, line := range strings.Split(string(allOutput), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 1 && fields[0] == ifaceName {
				return errors.NewNetworkError(fmt.Sprintf("Connection %s exists but is not active", ifaceName), nil)
			}
		}
	}

	return errors.NewNetworkError(fmt.Sprintf("Connection %s not found", ifaceName), nil)
}

// Rollback removes interface configuration by deleting the nmconnection file.
func (a *RHELAdapter) Rollback(ctx context.Context, name string) error {
	a.logger.WithField("interface", name).Info("Starting RHEL interface rollback/deletion")

	// 1. Delete the configuration file
	configPath := filepath.Join(a.GetConfigDir(), name+".nmconnection")
	if err := a.fileSystem.Remove(configPath); err != nil {
		a.logger.WithError(err).WithField("interface", name).Debug("Error removing nmconnection file (can be ignored)")
	}

	// 2. Reload NetworkManager to apply the removal
	if err := a.reloadNetworkManager(ctx); err != nil {
		a.logger.WithError(err).Warn("NetworkManager reload failed during rollback")
	}

	a.logger.WithField("interface", name).Info("RHEL interface rollback/deletion completed")
	return nil
}

// findDeviceByMAC finds the actual device name by MAC address
func (a *RHELAdapter) findDeviceByMAC(ctx context.Context, macAddress string) (string, error) {
	// Get all devices with their general info in one command
	// Using nmcli device status to get basic device list
	output, err := a.execNmcli(ctx, "device", "status")
	if err != nil {
		return "", fmt.Errorf("failed to list devices: %w", err)
	}

	// First, get a list of all ethernet devices
	// We'll check each one individually for MAC address
	lines := strings.Split(string(output), "\n")
	var devices []string
	
	// Skip header line
	for i := 1; i < len(lines); i++ {
		fields := strings.Fields(lines[i])
		if len(fields) >= 2 && fields[1] == "ethernet" {
			devices = append(devices, fields[0])
		}
	}
	
	// Now check each device for the MAC address
	targetMAC := strings.ToUpper(macAddress)
	
	for _, device := range devices {
		// Get detailed info for this specific device
		// Using proper nmcli syntax without -f flag for device show
		detailOutput, err := a.execNmcli(ctx, "-g", "GENERAL.HWADDR", "device", "show", device)
		if err != nil {
			// Device might have disappeared, continue to next
			a.logger.WithFields(logrus.Fields{
				"device": device,
				"error":  err,
			}).Debug("Failed to get device details, skipping")
			continue
		}
		
		// The output will be just the MAC address with -g (get-values) flag
		// nmcli escapes colons in MAC addresses (e.g., FA\:16\:3E\:BB\:93\:7A)
		hwaddr := strings.ToUpper(strings.TrimSpace(string(detailOutput)))
		hwaddr = strings.ReplaceAll(hwaddr, "\\:", ":")
		
		if hwaddr == targetMAC {
			a.logger.WithFields(logrus.Fields{
				"device": device,
				"mac":    macAddress,
			}).Info("Found disconnected device for MAC address")
			return device, nil
		}
	}
	
	return "", fmt.Errorf("no ethernet device found with MAC address %s", macAddress)
}

// generateNmConnectionContent generates the nmconnection file content
func (a *RHELAdapter) generateNmConnectionContent(iface entities.NetworkInterface, ifaceName, actualDevice string) string {
	// Generate a simple UUID (for demo purposes - in production, use proper UUID library)
	uuid := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", 
		time.Now().Unix(), 
		time.Now().UnixNano()&0xffff,
		time.Now().UnixNano()&0xffff,
		time.Now().UnixNano()&0xffff,
		time.Now().UnixNano()&0xffffffffffff)

	content := fmt.Sprintf(`[connection]
id=%s
uuid=%s
type=ethernet
interface-name=%s
timestamp=%d

[ethernet]
mac-address=%s`, ifaceName, uuid, actualDevice, time.Now().Unix(), strings.ToUpper(iface.MacAddress))

	// Add MTU if specified
	if iface.MTU > 0 {
		content += fmt.Sprintf("\nmtu=%d", iface.MTU)
	}

	// Add IPv4 configuration
	content += "\n\n[ipv4]"
	if iface.Address != "" && iface.CIDR != "" {
		// Extract prefix from CIDR
		parts := strings.Split(iface.CIDR, "/")
		if len(parts) == 2 {
			prefix := parts[1]
			fullAddress := fmt.Sprintf("%s/%s", iface.Address, prefix)
			content += fmt.Sprintf("\nmethod=manual\naddress1=%s", fullAddress)
		} else {
			content += "\nmethod=disabled"
		}
	} else {
		content += "\nmethod=disabled"
	}

	// Always disable IPv6
	content += "\n\n[ipv6]\naddr-gen-mode=default\nmethod=disabled\n\n[proxy]\n"

	return content
}

// reloadNetworkManager reloads NetworkManager to apply configuration changes
func (a *RHELAdapter) reloadNetworkManager(ctx context.Context) error {
	// Try nmcli connection reload first (faster)
	if _, err := a.execNmcli(ctx, "connection", "reload"); err == nil {
		a.logger.Debug("NetworkManager connections reloaded successfully")
		return nil
	}

	// Fallback to systemctl reload (slower but more reliable)
	if a.isContainer {
		// In container, use nsenter to reload on host
		_, err := a.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, 
			"nsenter", "--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid", 
			"systemctl", "reload", "NetworkManager")
		return err
	}
	
	// Direct execution on host
	_, err := a.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, "systemctl", "reload", "NetworkManager")
	return err
}
