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
}

// NewRHELAdapter creates a new RHELAdapter.
func NewRHELAdapter(
	executor interfaces.CommandExecutor,
	logger *logrus.Logger,
) *RHELAdapter {
	return &RHELAdapter{
		commandExecutor: executor,
		logger:          logger,
	}
}

// GetConfigDir returns the directory path where configuration files are stored
// RHEL uses nmcli so returns empty string instead of actual file path
func (a *RHELAdapter) GetConfigDir() string {
	return ""
}

// Configure configures network interface using nmcli.
func (a *RHELAdapter) Configure(ctx context.Context, iface entities.NetworkInterface, name entities.InterfaceName) error {
	ifaceName := name.String()
	macAddress := iface.MacAddress

	a.logger.WithFields(logrus.Fields{
		"interface": ifaceName,
		"mac":       macAddress,
	}).Info("Starting RHEL interface configuration with nmcli")

	// 1. Delete existing connection if any
	_ = a.Rollback(ctx, ifaceName)

	// 2. Add new connection
	addCmd := []string{
		"connection", "add", "type", "ethernet", "con-name", ifaceName, "ifname", ifaceName, "mac", macAddress,
	}
	if _, err := a.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, "nmcli", addCmd...); err != nil {
		return errors.NewNetworkError(fmt.Sprintf("nmcli connection add failed: %s", ifaceName), err)
	}

	// 3. Configure IP settings
	if iface.Address != "" && iface.CIDR != "" {
		// Static IP configuration
		// Extract prefix from CIDR (e.g., "10.0.0.0/24" -> "24")
		parts := strings.Split(iface.CIDR, "/")
		if len(parts) == 2 {
			prefix := parts[1]
			fullAddress := fmt.Sprintf("%s/%s", iface.Address, prefix)
			
			setIPCmd := []string{"connection", "modify", ifaceName, "ipv4.method", "manual", "ipv4.addresses", fullAddress}
			if _, err := a.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, "nmcli", setIPCmd...); err != nil {
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
		if _, err := a.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, "nmcli", disableIPv4Cmd...); err != nil {
			return errors.NewNetworkError(fmt.Sprintf("nmcli ipv4.method disabled failed: %s", ifaceName), err)
		}
	}
	
	// Always disable IPv6
	disableIPv6Cmd := []string{"connection", "modify", ifaceName, "ipv6.method", "disabled"}
	if _, err := a.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, "nmcli", disableIPv6Cmd...); err != nil {
		return errors.NewNetworkError(fmt.Sprintf("nmcli ipv6.method disabled failed: %s", ifaceName), err)
	}
	
	// 4. Set MTU if specified
	if iface.MTU > 0 {
		setMTUCmd := []string{"connection", "modify", ifaceName, "ethernet.mtu", fmt.Sprintf("%d", iface.MTU)}
		if _, err := a.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, "nmcli", setMTUCmd...); err != nil {
			return errors.NewNetworkError(fmt.Sprintf("nmcli MTU configuration failed: %s", ifaceName), err)
		}
		
		a.logger.WithFields(logrus.Fields{
			"interface": ifaceName,
			"mtu":       iface.MTU,
		}).Debug("MTU configured")
	}

	// 5. Activate connection
	upCmd := []string{"connection", "up", ifaceName}
	if _, err := a.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, "nmcli", upCmd...); err != nil {
		// Rollback on activation failure
		if rollbackErr := a.Rollback(ctx, ifaceName); rollbackErr != nil {
			a.logger.WithError(rollbackErr).Warn("Error during rollback after nmcli connection up failure")
		}
		return errors.NewNetworkError(fmt.Sprintf("nmcli connection up failed: %s", ifaceName), err)
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
	output, err := a.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, "nmcli", "device", "status")
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
	_, err := a.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, "nmcli", downCmd...)
	if err != nil {
		// Not treating as error if connection doesn't exist or already down
		a.logger.WithError(err).WithField("interface", name).Debug("Error during nmcli connection down (can be ignored)")
	}

	// Delete connection
	deleteCmd := []string{"connection", "delete", name}
	_, err = a.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, "nmcli", deleteCmd...)
	if err != nil {
		// Not treating as error if connection doesn't exist
		a.logger.WithError(err).WithField("interface", name).Debug("Error during nmcli connection delete (can be ignored)")
		return nil // Purpose of rollback is removal, so consider success even if already gone
	}

	a.logger.WithField("interface", name).Info("nmcli interface rollback/deletion completed")
	return nil
}
