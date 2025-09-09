package network

import (
	"context"
	"fmt"
	"multinic-agent/internal/domain/constants"
	"multinic-agent/internal/domain/entities"
	"multinic-agent/internal/domain/errors"
	"multinic-agent/internal/domain/interfaces"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// NetplanAdapter is a NetworkConfigurer and NetworkRollbacker implementation using Ubuntu Netplan
type NetplanAdapter struct {
	commandExecutor interfaces.CommandExecutor
	fileSystem      interfaces.FileSystem
	logger          *logrus.Logger
	configDir       string
}

// NewNetplanAdapter creates a new NetplanAdapter
func NewNetplanAdapter(
	executor interfaces.CommandExecutor,
	fs interfaces.FileSystem,
	logger *logrus.Logger,
) *NetplanAdapter {
	return &NetplanAdapter{
		commandExecutor: executor,
		fileSystem:      fs,
		logger:          logger,
		configDir:       constants.NetplanConfigDir,
	}
}

// GetConfigDir returns the directory path where configuration files are stored
func (a *NetplanAdapter) GetConfigDir() string {
	return a.configDir
}

// Configure configures a network interface
func (a *NetplanAdapter) Configure(ctx context.Context, iface entities.NetworkInterface, name entities.InterfaceName) error {
	// Generate configuration file path
	index := extractInterfaceIndex(name.String())
	configPath := filepath.Join(a.configDir, fmt.Sprintf("9%d-%s.yaml", index, name.String()))

	// Backup logic removed - overwrite existing configuration file if it exists

    // Decide rename policy: allow set-name only if the MAC-bound interface is not UP (or not present)
    renameAllowed := true
    if curName, isUp, found := a.findInterfaceByMAC(ctx, iface.MacAddress()); found && isUp {
        renameAllowed = false
        a.logger.WithFields(logrus.Fields{"mac": iface.MacAddress(), "current_name": curName}).Debug("Interface is UP - skip set-name to avoid rename")
    }

    // Generate Netplan configuration
    config := a.generateNetplanConfig(iface, name.String(), renameAllowed)
	configData, err := yaml.Marshal(config)
	if err != nil {
		return errors.NewSystemError("failed to marshal Netplan configuration", err)
	}

    // Save configuration file with restrictive permissions to satisfy netplan security requirements
    // Netplan warns/fails when files are group/world-readable. Use 0600.
    if err := a.fileSystem.WriteFile(configPath, configData, 0600); err != nil {
        return errors.NewSystemError("failed to save Netplan configuration file", err)
    }

	a.logger.WithFields(logrus.Fields{
		"interface":   name.String(),
		"config_path": configPath,
	}).Info("Netplan configuration file created")

    // Test Netplan (try). revert 오류 계열이면 apply로 폴백하여 안정성 확보
    if err := a.testNetplan(ctx); err != nil {
        errStr := err.Error()
        if strings.Contains(errStr, "File exists: '/run/systemd/network'") ||
           strings.Contains(errStr, "reverting config") ||
           strings.Contains(errStr, "exit status 255") {
            a.logger.WithError(err).Warn("netplan try failed; attempting fallback to 'netplan apply'")
            if applyErr := a.applyNetplan(ctx); applyErr != nil {
                if removeErr := a.fileSystem.Remove(configPath); removeErr != nil {
                    a.logger.WithError(removeErr).WithField("config_path", configPath).Error("Failed to remove config after apply fallback failure")
                }
                return errors.NewNetworkError("Netplan apply fallback failed", applyErr)
            }
        } else {
            if removeErr := a.fileSystem.Remove(configPath); removeErr != nil {
                a.logger.WithError(removeErr).WithField("config_path", configPath).Error("Failed to remove config file after Netplan test failure")
            }
            return errors.NewNetworkError("Netplan configuration test failed", err)
        }
    }

	// Apply Netplan
	if err := a.applyNetplan(ctx); err != nil {
		// Rollback on failure
		if rollbackErr := a.Rollback(ctx, name.String()); rollbackErr != nil {
			a.logger.WithError(rollbackErr).Error("Rollback failed")
		}
		return errors.NewNetworkError("failed to apply Netplan configuration", err)
	}

	return nil
}

// Validate verifies that the configured interface is working properly
func (a *NetplanAdapter) Validate(ctx context.Context, name entities.InterfaceName) error {
	// Check if interface exists
	interfacePath := fmt.Sprintf("/sys/class/net/%s", name.String())
	if !a.fileSystem.Exists(interfacePath) {
		return errors.NewValidationError("network interface does not exist", nil)
	}

	// Check if interface is UP
	_, err := a.commandExecutor.ExecuteWithTimeout(ctx, 10*time.Second, "ip", "link", "show", name.String(), "up")
	if err != nil {
		return errors.NewValidationError("network interface is not UP", err)
	}

	return nil
}

// Rollback reverts the interface configuration to the previous state
func (a *NetplanAdapter) Rollback(ctx context.Context, name string) error {
	index := extractInterfaceIndex(name)
	configPath := filepath.Join(a.configDir, fmt.Sprintf("9%d-%s.yaml", index, name))

	// Remove configuration file
	if a.fileSystem.Exists(configPath) {
		if err := a.fileSystem.Remove(configPath); err != nil {
			return errors.NewSystemError("failed to remove configuration file", err)
		}
	}

	// Backup restore logic removed - simply remove configuration file

	// Reapply Netplan
	if err := a.applyNetplan(ctx); err != nil {
		return errors.NewNetworkError("failed to apply Netplan after rollback", err)
	}

	a.logger.WithField("interface", name).Info("network configuration rollback completed")
	return nil
}

// testNetplan tests the configuration with netplan try command
func (a *NetplanAdapter) testNetplan(ctx context.Context) error {
	// In container environment, use nsenter to run in host namespace
	_, err := a.commandExecutor.ExecuteWithTimeout(
		ctx,
		120*time.Second,
		"nsenter", "--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid",
		"netplan", "try", "--timeout=120",
	)
	return err
}

// applyNetplan applies the configuration with netplan apply command
func (a *NetplanAdapter) applyNetplan(ctx context.Context) error {
	// In container environment, use nsenter to run in host namespace
	_, err := a.commandExecutor.ExecuteWithTimeout(
		ctx,
		30*time.Second,
		"nsenter", "--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid",
		"netplan", "apply",
	)
	return err
}

// generateNetplanConfig generates Netplan configuration
func (a *NetplanAdapter) generateNetplanConfig(iface entities.NetworkInterface, interfaceName string, setName bool) map[string]interface{} {
    ethernetConfig := map[string]interface{}{
        "match": map[string]interface{}{
            "macaddress": iface.MacAddress(),
        },
    }
    if setName {
        ethernetConfig["set-name"] = interfaceName
    }

	// Static IP configuration: Both Address and CIDR must be present
    if iface.Address() != "" && iface.CIDR() != "" {
        // Extract prefix from CIDR (e.g., "10.0.0.0/24" -> "24")
        parts := strings.Split(iface.CIDR(), "/")
        if len(parts) == 2 {
            prefix := parts[1]
            fullAddress := fmt.Sprintf("%s/%s", iface.Address(), prefix)

            ethernetConfig["dhcp4"] = false
            ethernetConfig["addresses"] = []string{fullAddress}
            if iface.MTU() > 0 {
                ethernetConfig["mtu"] = iface.MTU()
            }
        } else {
            a.logger.WithFields(logrus.Fields{
                "address": iface.Address(),
                "cidr":    iface.CIDR(),
            }).Warn("Invalid CIDR format, skipping IP configuration")
        }
    }

	config := map[string]interface{}{
		"network": map[string]interface{}{
			"version": 2,
			"ethernets": map[string]interface{}{
				interfaceName: ethernetConfig,
			},
		},
	}

	return config
}

// extractInterfaceIndex extracts the index from interface name
func extractInterfaceIndex(name string) int {
	// multinic0 -> 0, multinic1 -> 1 etc
	if strings.HasPrefix(name, constants.InterfacePrefix) {
		indexStr := strings.TrimPrefix(name, constants.InterfacePrefix)
		if index, err := strconv.Atoi(indexStr); err == nil {
			return index
		}
	}
	return 0
}

// findInterfaceByMAC returns the interface name, UP state, and whether found, for the given MAC.
func (a *NetplanAdapter) findInterfaceByMAC(ctx context.Context, mac string) (name string, up bool, found bool) {
    macLower := strings.ToLower(strings.TrimSpace(mac))
    out, err := a.commandExecutor.ExecuteWithTimeout(ctx, 5*time.Second, "ip", "-o", "link", "show")
    if err != nil { return "", false, false }
    for _, line := range strings.Split(string(out), "\n") {
        if strings.Contains(strings.ToLower(line), macLower) {
            parts := strings.SplitN(line, ":", 3)
            if len(parts) >= 2 {
                n := strings.TrimSpace(parts[1])
                isUp := strings.Contains(line, "state UP") || (strings.Contains(line, ",UP,") && strings.Contains(line, "LOWER_UP"))
                return n, isUp, true
            }
        }
    }
    return "", false, false
}
