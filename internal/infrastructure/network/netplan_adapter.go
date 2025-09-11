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

// exec is a small helper wrapping command execution with a sensible timeout
func (a *NetplanAdapter) exec(ctx context.Context, cmd string, args ...string) ([]byte, error) {
    // default 30s per op; callers use context if they need stricter bounds
    return a.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, cmd, args...)
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
    // 1) Runtime apply via ip: rename/mtu/address/link-up
    target := name.String()
    curName, wasUp, found := a.findInterfaceByMAC(ctx, iface.MacAddress())
    if !found || strings.TrimSpace(curName) == "" {
        return errors.NewNetworkError("MAC not found on system for runtime apply", fmt.Errorf("mac=%s", iface.MacAddress()))
    }

    // Rename if needed (attempt without down first to reduce disruption; fallback to down)
    if curName != target {
        if _, err := a.exec(ctx, "ip", "link", "set", curName, "name", target); err != nil {
            a.logger.WithFields(logrus.Fields{"from": curName, "to": target, "err": err}).Debug("rename without down failed; retry with down")
            // bring down, rename, then restore up if previously up
            _, _ = a.exec(ctx, "ip", "link", "set", curName, "down")
            if _, err2 := a.exec(ctx, "ip", "link", "set", curName, "name", target); err2 != nil {
                return errors.NewNetworkError("failed to rename interface", err2)
            }
            if wasUp {
                _, _ = a.exec(ctx, "ip", "link", "set", target, "up")
            }
        }
    }

    // MTU
    if iface.MTU() > 0 {
        if _, err := a.exec(ctx, "ip", "link", "set", target, "mtu", fmt.Sprintf("%d", iface.MTU())); err != nil {
            return errors.NewNetworkError("failed to set MTU", err)
        }
    }

    // IPv4
    if addr := strings.TrimSpace(iface.Address()); addr != "" && strings.TrimSpace(iface.CIDR()) != "" {
        parts := strings.Split(iface.CIDR(), "/")
        if len(parts) == 2 {
            full := fmt.Sprintf("%s/%s", addr, parts[1])
            if _, err := a.exec(ctx, "ip", "addr", "replace", full, "dev", target); err != nil {
                return errors.NewNetworkError("failed to set IPv4 address", err)
            }
        } else {
            a.logger.WithFields(logrus.Fields{"address": addr, "cidr": iface.CIDR()}).Warn("invalid CIDR; skipping ip addr replace")
        }
    }

    // Ensure link up
    if _, err := a.exec(ctx, "ip", "link", "set", target, "up"); err != nil {
        return errors.NewNetworkError("failed to set link up", err)
    }

    // 2) Persist via Netplan YAML (write-only, no apply)
    index := extractInterfaceIndex(target)
    configPath := filepath.Join(a.configDir, fmt.Sprintf("9%d-%s.yaml", index, target))
    config := a.generateNetplanConfig(iface, target)
    data, err := yaml.Marshal(config)
    if err != nil { return errors.NewSystemError("failed to marshal Netplan configuration", err) }
    if err := a.fileSystem.WriteFile(configPath, data, 0600); err != nil {
        return errors.NewSystemError("failed to save Netplan configuration file", err)
    }
    a.logger.WithFields(logrus.Fields{"interface": target, "config_path": configPath}).Info("Netplan configuration file created (persist-only)")

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

    a.logger.WithField("interface", name).Info("network configuration rollback completed")
    return nil
}

// testNetplan tests the configuration with netplan try command
func (a *NetplanAdapter) testNetplan(ctx context.Context) error {
    // In container environment, use nsenter to run in host namespace
    // Disabled in runtime-ip mode (persist-only). Keep stub for compatibility.
    return nil
}

// applyNetplan applies the configuration with netplan apply command
func (a *NetplanAdapter) applyNetplan(ctx context.Context) error {
    // In container environment, use nsenter to run in host namespace
    // Disabled in runtime-ip mode (persist-only). Keep stub for compatibility.
    return nil
}

// generateNetplanConfig generates Netplan configuration
func (a *NetplanAdapter) generateNetplanConfig(iface entities.NetworkInterface, interfaceName string) map[string]interface{} {
    ethernetConfig := map[string]interface{}{
        "match": map[string]interface{}{
            "macaddress": iface.MacAddress(),
        },
    }
    // Always include set-name for persistent rename on Ubuntu/Debian
    ethernetConfig["set-name"] = interfaceName

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
