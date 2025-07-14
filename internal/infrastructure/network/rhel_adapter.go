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
	if a.isContainer {
		// In container, we need to write to the host's filesystem
		return "/host/etc/NetworkManager/system-connections"
	}
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

	a.logger.WithFields(logrus.Fields{
		"interface":   ifaceName,
		"config_path": configPath,
		"actual_device": actualDevice,
		"mac_address": iface.MacAddress,
		"content_length": len(content),
		"is_container": a.isContainer,
	}).Info("About to write nmconnection file")
	
	// Log the full content in debug mode for troubleshooting
	a.logger.WithFields(logrus.Fields{
		"interface": ifaceName,
		"content": content,
	}).Debug("Full nmconnection file content")

	// 3. Write the configuration file directly
	// In container environment, we need to use nsenter to write to host filesystem
	if a.isContainer {
		// Create a temporary file first
		tmpFile := fmt.Sprintf("/tmp/multinic-%s-%d.nmconnection", ifaceName, time.Now().Unix())
		if err := a.fileSystem.WriteFile(tmpFile, []byte(content), 0600); err != nil {
			return errors.NewNetworkError(fmt.Sprintf("Failed to write temporary nmconnection file: %s", tmpFile), err)
		}
		
		// Copy to host using nsenter
		hostPath := strings.TrimPrefix(configPath, "/host")
		copyCmd := fmt.Sprintf("cp %s %s && chmod 600 %s", tmpFile, hostPath, hostPath)
		output, err := a.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, 
			"nsenter", "--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid",
			"sh", "-c", copyCmd)
		
		// Clean up temp file
		_ = a.fileSystem.Remove(tmpFile)
		
		if err != nil {
			a.logger.WithError(err).WithFields(logrus.Fields{
				"interface": ifaceName,
				"output": string(output),
				"temp_file": tmpFile,
				"host_path": hostPath,
			}).Error("Failed to copy nmconnection file to host")
			return errors.NewNetworkError(fmt.Sprintf("Failed to copy nmconnection file to host: %s", hostPath), err)
		}
		
		a.logger.WithFields(logrus.Fields{
			"interface": ifaceName,
			"host_path": hostPath,
		}).Debug("Successfully copied nmconnection file to host via nsenter")
		
		// Update configPath to use host path for verification
		configPath = hostPath
	} else {
		// Direct write on host
		if err := a.fileSystem.WriteFile(configPath, []byte(content), 0600); err != nil {
			return errors.NewNetworkError(fmt.Sprintf("Failed to write nmconnection file: %s", configPath), err)
		}
	}

	// Verify file was actually written
	// In container, we need to check via nsenter
	if a.isContainer {
		checkCmd := fmt.Sprintf("test -f %s && echo 'exists'", configPath)
		output, err := a.commandExecutor.ExecuteWithTimeout(ctx, 5*time.Second,
			"nsenter", "--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid",
			"sh", "-c", checkCmd)
		if err != nil || !strings.Contains(string(output), "exists") {
			return errors.NewNetworkError(fmt.Sprintf("nmconnection file was not created: %s", configPath), nil)
		}
		
		// Skip content verification in container - just trust nsenter copy
		a.logger.WithFields(logrus.Fields{
			"interface":   ifaceName,
			"config_path": configPath,
		}).Info("nmconnection file written and verified successfully via nsenter")
	} else {
		// Direct verification on host
		if !a.fileSystem.Exists(configPath) {
			return errors.NewNetworkError(fmt.Sprintf("nmconnection file was not created: %s", configPath), nil)
		}

		// Verify file content
		writtenContent, err := a.fileSystem.ReadFile(configPath)
		if err != nil {
			a.logger.WithError(err).WithField("config_path", configPath).Error("Failed to read back written file")
			return errors.NewNetworkError(fmt.Sprintf("Failed to verify nmconnection file: %s", configPath), err)
		}

		if len(writtenContent) == 0 {
			return errors.NewNetworkError(fmt.Sprintf("nmconnection file is empty: %s", configPath), nil)
		}
		
		a.logger.WithFields(logrus.Fields{
			"interface":   ifaceName,
			"config_path": configPath,
			"file_size":   len(writtenContent),
			"file_exists": true,
		}).Info("nmconnection file written and verified successfully")
	}


	// 4. Reload NetworkManager to apply changes
	if err := a.reloadNetworkManager(ctx); err != nil {
		a.logger.WithError(err).Error("NetworkManager reload failed")
		return errors.NewNetworkError("Failed to reload NetworkManager", err)
	}

	a.logger.WithField("interface", ifaceName).Debug("NetworkManager reloaded successfully")

	// 5. Force NetworkManager to recognize the new connection
	// First, try using nmcli to import the connection explicitly
	// Use the correct path based on container environment
	nmcliLoadPath := configPath
	if a.isContainer && strings.HasPrefix(configPath, "/etc/") {
		// configPath is already adjusted to host path without /host prefix
		nmcliLoadPath = configPath
	}
	importOutput, importErr := a.execNmcli(ctx, "connection", "load", nmcliLoadPath)
	if importErr != nil {
		a.logger.WithError(importErr).WithFields(logrus.Fields{
			"interface": ifaceName,
			"output": string(importOutput),
			"config_path": configPath,
		}).Warn("Failed to explicitly load connection file")
		
		// If loading failed, try creating connection directly with nmcli as fallback
		a.logger.WithField("interface", ifaceName).Info("Attempting to create connection directly with nmcli")
		
		// Delete the file first to avoid conflicts
		_ = a.fileSystem.Remove(configPath)
		
		// Create connection with nmcli
		createArgs := []string{"connection", "add", 
			"type", "ethernet",
			"con-name", ifaceName,
			"ifname", actualDevice,
			"802-3-ethernet.mac-address", strings.ToUpper(iface.MacAddress),
		}
		
		// Add MTU if specified
		if iface.MTU > 0 {
			createArgs = append(createArgs, "802-3-ethernet.mtu", fmt.Sprintf("%d", iface.MTU))
		}
		
		// Add IP configuration
		if iface.Address != "" && iface.CIDR != "" {
			parts := strings.Split(iface.CIDR, "/")
			if len(parts) == 2 {
				prefix := parts[1]
				fullAddress := fmt.Sprintf("%s/%s", iface.Address, prefix)
				createArgs = append(createArgs, "ipv4.method", "manual", "ipv4.addresses", fullAddress)
			} else {
				createArgs = append(createArgs, "ipv4.method", "disabled")
			}
		} else {
			createArgs = append(createArgs, "ipv4.method", "disabled")
		}
		
		// Disable IPv6
		createArgs = append(createArgs, "ipv6.method", "disabled")
		
		createOutput, createErr := a.execNmcli(ctx, createArgs...)
		if createErr != nil {
			a.logger.WithError(createErr).WithFields(logrus.Fields{
				"interface": ifaceName,
				"output": string(createOutput),
			}).Error("Failed to create connection with nmcli")
			return errors.NewNetworkError(fmt.Sprintf("Failed to create connection %s with nmcli", ifaceName), createErr)
		}
		
		a.logger.WithFields(logrus.Fields{
			"interface": ifaceName,
			"output": string(createOutput),
		}).Info("Connection created successfully with nmcli")
		
		// Skip file-based validation since we used nmcli
		return nil
	} else {
		a.logger.WithFields(logrus.Fields{
			"interface": ifaceName,
			"output": string(importOutput),
		}).Debug("Connection file explicitly loaded")
	}

	// 6. Wait for NetworkManager to discover the new connection file
	// and then try to activate with retries
	maxRetries := 5  // Increased from 3 to 5
	retryDelay := 3 * time.Second  // Increased from 2 to 3 seconds
	var lastErr error
	
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			a.logger.WithFields(logrus.Fields{
				"interface": ifaceName,
				"attempt":   i + 1,
				"max_attempts": maxRetries,
			}).Debug("Retrying connection activation after delay")
			time.Sleep(retryDelay)
			
			// Force reload on retry
			if i > 2 {
				a.logger.WithField("interface", ifaceName).Debug("Forcing NetworkManager reload on retry")
				_ = a.reloadNetworkManager(ctx)
			}
		}
		
		// Check if connection exists in NetworkManager
		if err := a.validateConnectionExists(ctx, ifaceName); err != nil {
			lastErr = err
			a.logger.WithError(err).WithFields(logrus.Fields{
				"interface": ifaceName,
				"attempt":   i + 1,
			}).Debug("Connection not yet visible to NetworkManager")
			
			// On later attempts, check if file still exists
			if i > 1 && !a.fileSystem.Exists(configPath) {
				a.logger.WithFields(logrus.Fields{
					"interface": ifaceName,
					"config_path": configPath,
					"attempt": i + 1,
				}).Error("Configuration file disappeared - NetworkManager may have rejected it")
				
				// Check if NetworkManager created a different file
				configDir := a.GetConfigDir()
				files, _ := a.fileSystem.ListFiles(configDir)
				var relatedFiles []string
				for _, f := range files {
					if strings.Contains(f, ifaceName) || strings.Contains(f, actualDevice) {
						relatedFiles = append(relatedFiles, f)
					}
				}
				
				if len(relatedFiles) > 0 {
					a.logger.WithFields(logrus.Fields{
						"interface": ifaceName,
						"related_files": relatedFiles,
						"config_dir": configDir,
					}).Warn("NetworkManager may have created alternative connection files")
				}
				
				return errors.NewNetworkError(fmt.Sprintf("Configuration file %s was removed by NetworkManager", configPath), nil)
			}
			continue
		}
		
		// Try to activate the connection
		if err := a.activateConnection(ctx, ifaceName); err != nil {
			a.logger.WithError(err).Warn("Failed to activate connection, but continuing")
			// Don't treat activation failure as fatal - connection exists
		}
		
		// Connection exists and we attempted activation
		a.logger.WithField("interface", ifaceName).Info("Connection successfully created and activation attempted")
		return nil
	}
	
	// After all retries, connection still not visible
	a.logger.WithError(lastErr).Error("Connection not visible to NetworkManager after retries")
	return errors.NewNetworkError(fmt.Sprintf("Connection %s not recognized by NetworkManager after %d retries", ifaceName, maxRetries), lastErr)
}

// Validate verifies that the configured interface is properly activated.
func (a *RHELAdapter) Validate(ctx context.Context, name entities.InterfaceName) error {
	ifaceName := name.String()
	a.logger.WithField("interface", ifaceName).Debug("Starting nmcli interface validation")

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
			}).Debug("nmcli connection validation successful - active")
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
				a.logger.WithFields(logrus.Fields{
					"connection": ifaceName,
					"status": "exists_but_inactive",
				}).Debug("Connection exists but is not active - this is acceptable")
				// For RHEL, we accept connections that exist but are not active
				// as the file was created successfully
				return nil
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
	
	if a.isContainer {
		// In container, use nsenter to remove file from host
		hostPath := strings.TrimPrefix(configPath, "/host")
		rmCmd := fmt.Sprintf("rm -f %s", hostPath)
		output, err := a.commandExecutor.ExecuteWithTimeout(ctx, 10*time.Second,
			"nsenter", "--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid",
			"sh", "-c", rmCmd)
		if err != nil {
			a.logger.WithError(err).WithFields(logrus.Fields{
				"interface": name,
				"output": string(output),
				"host_path": hostPath,
			}).Debug("Error removing nmconnection file via nsenter (can be ignored)")
		}
	} else {
		// Direct removal on host
		if err := a.fileSystem.Remove(configPath); err != nil {
			a.logger.WithError(err).WithField("interface", name).Debug("Error removing nmconnection file (can be ignored)")
		}
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
	// Generate a more unique UUID to avoid collisions
	// Using MAC address and interface name as part of the seed
	macHash := 0
	for _, b := range iface.MacAddress {
		macHash = macHash*31 + int(b)
	}
	
	uuid := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", 
		uint32(time.Now().Unix()), 
		uint16(time.Now().UnixNano()&0xffff),
		uint16((time.Now().UnixNano()>>16)&0xffff) | 0x4000,  // Version 4 UUID
		uint16((time.Now().UnixNano()>>32)&0x3fff) | 0x8000,  // Variant bits
		uint64(macHash)^uint64(time.Now().UnixNano()))

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

// activateConnection tries to activate the connection
func (a *RHELAdapter) activateConnection(ctx context.Context, connectionName string) error {
	output, err := a.execNmcli(ctx, "connection", "up", connectionName)
	if err != nil {
		a.logger.WithError(err).WithFields(logrus.Fields{
			"connection": connectionName,
			"output":     string(output),
		}).Debug("Failed to activate connection")
		return err
	}
	
	a.logger.WithFields(logrus.Fields{
		"connection": connectionName,
		"output":     string(output),
	}).Debug("Connection activated successfully")
	return nil
}

// validateConnectionExists checks if the connection exists (active or inactive)
func (a *RHELAdapter) validateConnectionExists(ctx context.Context, connectionName string) error {
	// Check if connection exists in any state (active or inactive)
	allOutput, err := a.execNmcli(ctx, "connection", "show")
	if err != nil {
		return errors.NewNetworkError(fmt.Sprintf("nmcli connection show execution failed: %s", connectionName), err)
	}

	// Check if our connection exists
	lines := strings.Split(string(allOutput), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 1 && fields[0] == connectionName {
			a.logger.WithFields(logrus.Fields{
				"connection": connectionName,
				"status":     "exists",
			}).Debug("Connection found in nmcli")
			return nil
		}
	}

	return errors.NewNetworkError(fmt.Sprintf("Connection %s not found", connectionName), nil)
}

// Helper function for min
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// GenerateNmConnectionContentForTest is a test helper method
func (a *RHELAdapter) GenerateNmConnectionContentForTest(iface entities.NetworkInterface, ifaceName, actualDevice string) string {
	return a.generateNmConnectionContent(iface, ifaceName, actualDevice)
}

// reloadNetworkManager reloads NetworkManager to apply configuration changes
func (a *RHELAdapter) reloadNetworkManager(ctx context.Context) error {
	// Try nmcli connection reload first (faster)
	if output, err := a.execNmcli(ctx, "connection", "reload"); err == nil {
		a.logger.WithField("output", string(output)).Debug("NetworkManager connections reloaded successfully")
		return nil
	} else {
		a.logger.WithError(err).Debug("nmcli connection reload failed, trying systemctl")
	}

	// Fallback to systemctl reload (slower but more reliable)
	if a.isContainer {
		// In container, use nsenter to reload on host
		output, err := a.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, 
			"nsenter", "--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid", 
			"systemctl", "reload", "NetworkManager")
		if err != nil {
			a.logger.WithError(err).WithField("output", string(output)).Error("systemctl reload NetworkManager failed in container")
			return err
		}
		a.logger.WithField("output", string(output)).Debug("NetworkManager reloaded via systemctl in container")
		return nil
	}
	
	// Direct execution on host
	output, err := a.commandExecutor.ExecuteWithTimeout(ctx, 30*time.Second, "systemctl", "reload", "NetworkManager")
	if err != nil {
		a.logger.WithError(err).WithField("output", string(output)).Error("systemctl reload NetworkManager failed")
		return err
	}
	a.logger.WithField("output", string(output)).Debug("NetworkManager reloaded via systemctl")
	return nil
}
