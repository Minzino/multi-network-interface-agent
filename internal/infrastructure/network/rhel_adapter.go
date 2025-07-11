package network

import (
	"context"
	"fmt"
	"strings"
	"time"

	"multinic-agent/internal/domain/entities"
	"multinic-agent/internal/domain/errors"
	"multinic-agent/internal/domain/interfaces"

	"github.com/sirupsen/logrus"
)

// RHELAdapter configures network for RHEL-based OS using nmcli.
type RHELAdapter struct {
	commandExecutor interfaces.CommandExecutor
	logger          *logrus.Logger
	isContainer     bool // indicates if running in container
}

// NewRHELAdapter creates a new RHELAdapter.
func NewRHELAdapter(
	executor interfaces.CommandExecutor,
	logger *logrus.Logger,
) *RHELAdapter {
	// Check if running in container by checking if /host exists
	isContainer := false
	if _, err := executor.ExecuteWithTimeout(context.Background(), 1*time.Second, "test", "-d", "/host"); err == nil {
		isContainer = true
	}
	
	return &RHELAdapter{
		commandExecutor: executor,
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

// Configure configures network interface using nmcli.
func (a *RHELAdapter) Configure(ctx context.Context, iface entities.NetworkInterface, name entities.InterfaceName) error {
	ifaceName := name.String()
	macAddress := iface.MacAddress

	a.logger.WithFields(logrus.Fields{
		"interface": ifaceName,
		"mac":       macAddress,
	}).Info("Starting RHEL interface configuration with nmcli")

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

	// 2. Delete existing connection if any
	_ = a.Rollback(ctx, ifaceName)

	// Helper function to execute nmcli commands
	execNmcli := func(args ...string) error {
		_, err := a.execNmcli(ctx, args...)
		return err
	}

	// 3. Add new connection
	// Use the actual device name found by MAC address
	addCmd := []string{
		"connection", "add", "type", "ethernet", "con-name", ifaceName, "ifname", actualDevice,
	}
	if err := execNmcli(addCmd...); err != nil {
		return errors.NewNetworkError(fmt.Sprintf("nmcli connection add failed: %s", ifaceName), err)
	}

	// 4. Configure IP settings
	if iface.Address != "" && iface.CIDR != "" {
		// Static IP configuration
		// Extract prefix from CIDR (e.g., "10.0.0.0/24" -> "24")
		parts := strings.Split(iface.CIDR, "/")
		if len(parts) == 2 {
			prefix := parts[1]
			fullAddress := fmt.Sprintf("%s/%s", iface.Address, prefix)
			
			setIPCmd := []string{"connection", "modify", ifaceName, "ipv4.method", "manual", "ipv4.addresses", fullAddress}
			if err := execNmcli(setIPCmd...); err != nil {
				return errors.NewNetworkError(fmt.Sprintf("nmcli ipv4.addresses configuration failed: %s", ifaceName), err)
			}
			
			a.logger.WithFields(logrus.Fields{
				"interface": ifaceName,
				"address":   fullAddress,
			}).Debug("Static IP configured")
		} else {
			a.logger.WithFields(logrus.Fields{
				"interface": ifaceName,
				"cidr":      iface.CIDR,
			}).Warn("Invalid CIDR format, skipping IP configuration")
		}
	} else {
		// No IP configuration - disable IP assignment
		disableIPv4Cmd := []string{"connection", "modify", ifaceName, "ipv4.method", "disabled"}
		if err := execNmcli(disableIPv4Cmd...); err != nil {
			return errors.NewNetworkError(fmt.Sprintf("nmcli ipv4.method disabled failed: %s", ifaceName), err)
		}
	}
	
	// Always disable IPv6
	disableIPv6Cmd := []string{"connection", "modify", ifaceName, "ipv6.method", "disabled"}
	if err := execNmcli(disableIPv6Cmd...); err != nil {
		return errors.NewNetworkError(fmt.Sprintf("nmcli ipv6.method disabled failed: %s", ifaceName), err)
	}
	
	// 4. Set MTU if specified
	if iface.MTU > 0 {
		setMTUCmd := []string{"connection", "modify", ifaceName, "ethernet.mtu", fmt.Sprintf("%d", iface.MTU)}
		if err := execNmcli(setMTUCmd...); err != nil {
			return errors.NewNetworkError(fmt.Sprintf("nmcli MTU configuration failed: %s", ifaceName), err)
		}
		
		a.logger.WithFields(logrus.Fields{
			"interface": ifaceName,
			"mtu":       iface.MTU,
		}).Debug("MTU configured")
	}

	// 5. Test configuration before activation (optional reload)
	// This ensures the configuration is valid
	reloadCmd := []string{"connection", "reload"}
	if err := execNmcli(reloadCmd...); err != nil {
		a.logger.WithError(err).Warn("nmcli connection reload failed, continuing anyway")
	}

	// 6. Activate connection
	upCmd := []string{"connection", "up", ifaceName}
	if err := execNmcli(upCmd...); err != nil {
		// Rollback on activation failure
		if rollbackErr := a.Rollback(ctx, ifaceName); rollbackErr != nil {
			a.logger.WithError(rollbackErr).Warn("Error during rollback after nmcli connection up failure")
		}
		return errors.NewNetworkError(fmt.Sprintf("nmcli connection up failed: %s", ifaceName), err)
	}

	// 7. Verify the connection is actually working
	// Give it a moment to establish
	time.Sleep(2 * time.Second)
	
	if err := a.Validate(ctx, name); err != nil {
		a.logger.WithError(err).Error("Interface validation failed after activation")
		// Rollback if validation fails
		if rollbackErr := a.Rollback(ctx, ifaceName); rollbackErr != nil {
			a.logger.WithError(rollbackErr).Warn("Error during rollback after validation failure")
		}
		return errors.NewNetworkError(fmt.Sprintf("Interface validation failed after activation: %s", ifaceName), err)
	}

	a.logger.WithField("interface", ifaceName).Info("nmcli interface configuration completed")
	return nil
}

// Validate verifies that the configured interface is properly activated.
func (a *RHELAdapter) Validate(ctx context.Context, name entities.InterfaceName) error {
	ifaceName := name.String()
	a.logger.WithField("interface", ifaceName).Info("Starting nmcli interface validation")

	// Check status using `nmcli device status`
	// Output example: DEVICE  TYPE      STATE      CONNECTION
	//           eth0    ethernet  connected  eth0
	//           multinic0 ethernet  connected  multinic0
	output, err := a.execNmcli(ctx, "device", "status")
	if err != nil {
		return errors.NewNetworkError(fmt.Sprintf("nmcli device status execution failed: %s", ifaceName), err)
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, ifaceName) {
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[2] == "connected" {
				a.logger.WithField("interface", ifaceName).Info("nmcli interface validation successful")
				return nil
			}
			return errors.NewNetworkError(fmt.Sprintf("Interface %s state is not 'connected': %s", ifaceName, line), nil)
		}
	}

	return errors.NewNetworkError(fmt.Sprintf("Interface %s not found in nmcli device status output", ifaceName), nil)
}

// Rollback removes interface configuration using nmcli.
func (a *RHELAdapter) Rollback(ctx context.Context, name string) error {
	a.logger.WithField("interface", name).Info("Starting nmcli interface rollback/deletion")

	// Deactivate connection
	downCmd := []string{"connection", "down", name}
	_, err := a.execNmcli(ctx, downCmd...)
	if err != nil {
		// Not treating as error if connection doesn't exist or already down
		a.logger.WithError(err).WithField("interface", name).Debug("Error during nmcli connection down (can be ignored)")
	}

	// Delete connection
	deleteCmd := []string{"connection", "delete", name}
	_, err = a.execNmcli(ctx, deleteCmd...)
	if err != nil {
		// Not treating as error if connection doesn't exist
		a.logger.WithError(err).WithField("interface", name).Debug("Error during nmcli connection delete (can be ignored)")
		return nil // Purpose of rollback is removal, so consider success even if already gone
	}

	a.logger.WithField("interface", name).Info("nmcli interface rollback/deletion completed")
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
