package services

import (
    "bufio"
    "context"
    "multinic-agent/internal/domain/entities"
    "multinic-agent/internal/domain/interfaces"
    "multinic-agent/internal/infrastructure/metrics"
    "net"
    "path/filepath"
    "strconv"
    "strings"

    "github.com/sirupsen/logrus"
    "gopkg.in/yaml.v3"
)

type DriftDetector struct {
    fs      interfaces.FileSystem
    logger  *logrus.Logger
    naming  *InterfaceNamingService
}

func NewDriftDetector(fs interfaces.FileSystem, logger *logrus.Logger, naming *InterfaceNamingService) *DriftDetector {
    return &DriftDetector{fs: fs, logger: logger, naming: naming}
}

// NetplanYAML represents the Netplan configuration structure
type NetplanYAML struct {
    Network struct {
        Ethernets map[string]struct {
            DHCP4     bool     `yaml:"dhcp4"`
            MTU       int      `yaml:"mtu,omitempty"`
            Addresses []string `yaml:"addresses,omitempty"`
            Match     struct {
                MACAddress string `yaml:"macaddress"`
            } `yaml:"match"`
            SetName string `yaml:"set-name"`
        } `yaml:"ethernets"`
        Version int `yaml:"version"`
    } `yaml:"network"`
}

type netplanFileConfig struct {
    macAddress   string
    address      string
    cidr         string
    mtu          int
    hasAddresses bool
}

type ifcfgFileConfig struct {
    macAddress string
    ipAddress  string
    prefix     string
    mtu        int
}

// Public API
func (d *DriftDetector) IsNetplanDrift(ctx context.Context, dbIface entities.NetworkInterface, configPath string) bool {
    if !d.fs.Exists(configPath) {
        d.logger.WithFields(logrus.Fields{
            "interface_id": dbIface.ID(),
            "mac_address":  dbIface.MacAddress(),
            "config_path":  configPath,
        }).Debug("Configuration file not found, detected as configuration change")
        return true
    }

    content, err := d.fs.ReadFile(configPath)
    if err != nil {
        d.logger.WithError(err).WithField("file", configPath).Warn("Failed to read Netplan file, treating as configuration mismatch")
        return true
    }

    netplanData, err := d.parseNetplanFile(content)
    if err != nil {
        d.logger.WithError(err).WithField("file", configPath).Warn("Failed to parse Netplan YAML, treating as configuration mismatch")
        return true
    }
    fileConfig := d.extractNetplanConfig(netplanData)

    if fileConfig.macAddress != dbIface.MacAddress() {
        d.logger.WithFields(logrus.Fields{
            "db_mac":   dbIface.MacAddress(),
            "file_mac": fileConfig.macAddress,
        }).Warn("MAC address mismatch, treating as configuration change")
        return true
    }

    // Validate against system state
    interfaceName := d.extractInterfaceNameFromPath(configPath)
    if interfaceName != "" {
        if d.checkSystemInterfaceDrift(ctx, dbIface, interfaceName) {
            return true
        }
    }

    return d.checkConfigDrift(dbIface, fileConfig)
}

func (d *DriftDetector) IsIfcfgDrift(ctx context.Context, dbIface entities.NetworkInterface, configPath string) bool {
    content, err := d.fs.ReadFile(configPath)
    if err != nil {
        d.logger.WithError(err).WithField("file", configPath).Warn("Failed to read ifcfg file, treating as configuration mismatch")
        return true
    }
    fileConfig := d.parseIfcfgFile(content)
    if fileConfig.macAddress != strings.ToLower(dbIface.MacAddress()) {
        d.logger.WithFields(logrus.Fields{
            "db_mac":   dbIface.MacAddress(),
            "file_mac": fileConfig.macAddress,
        }).Warn("MAC address mismatch in ifcfg file")
        return true
    }
    interfaceName := d.extractInterfaceNameFromPath(configPath)
    if interfaceName != "" {
        if d.checkSystemInterfaceDrift(ctx, dbIface, interfaceName) {
            return true
        }
    }
    return d.checkIfcfgDrift(dbIface, fileConfig)
}

func (d *DriftDetector) FindNetplanFileForInterface(configDir, interfaceName string) string {
    files, err := d.fs.ListFiles(configDir)
    if err != nil {
        d.logger.WithError(err).Warn("Failed to scan Netplan directory")
        return ""
    }
    for _, file := range files {
        if strings.Contains(file, interfaceName) && strings.HasSuffix(file, ".yaml") {
            return filepath.Join(configDir, file)
        }
    }
    return ""
}

func (d *DriftDetector) FindIfcfgFile(configDir, interfaceName string) string {
    fileName := "ifcfg-" + interfaceName
    filePath := filepath.Join(configDir, fileName)
    if d.fs.Exists(filePath) {
        return filePath
    }
    return ""
}

// Internals
func (d *DriftDetector) parseNetplanFile(content []byte) (*NetplanYAML, error) {
    var netplanData NetplanYAML
    if err := yaml.Unmarshal(content, &netplanData); err != nil {
        return nil, err
    }
    return &netplanData, nil
}

func (d *DriftDetector) extractNetplanConfig(netplanData *NetplanYAML) netplanFileConfig {
    config := netplanFileConfig{}
    for _, eth := range netplanData.Network.Ethernets {
        config.macAddress = eth.Match.MACAddress
        config.hasAddresses = len(eth.Addresses) > 0
        config.mtu = eth.MTU
        if config.hasAddresses {
            ip, ipNet, err := net.ParseCIDR(eth.Addresses[0])
            if err == nil {
                config.address = ip.String()
                config.cidr = ipNet.String()
            } else {
                config.address = eth.Addresses[0]
                config.cidr = ""
            }
        }
        break
    }
    return config
}

func (d *DriftDetector) checkConfigDrift(dbIface entities.NetworkInterface, fileConfig netplanFileConfig) bool {
    isDrifted := (!fileConfig.hasAddresses && dbIface.Address() != "") ||
        (dbIface.Address() != fileConfig.address) ||
        (dbIface.CIDR() != fileConfig.cidr) ||
        (dbIface.MTU() != fileConfig.mtu)

    if isDrifted {
        d.logger.WithFields(logrus.Fields{
            "interface_id": dbIface.ID(),
            "mac_address":  dbIface.MacAddress(),
            "db_address":   dbIface.Address(),
            "db_cidr":      dbIface.CIDR(),
            "db_mtu":       dbIface.MTU(),
            "file_address": fileConfig.address,
            "file_cidr":    fileConfig.cidr,
            "file_mtu":     fileConfig.mtu,
        }).Debug("netplan configuration drift detected")

        if !fileConfig.hasAddresses && dbIface.Address() != "" { metrics.RecordDrift("missing_address") }
        if dbIface.Address() != fileConfig.address { metrics.RecordDrift("ip_address") }
        if dbIface.CIDR() != fileConfig.cidr { metrics.RecordDrift("cidr") }
        if dbIface.MTU() != fileConfig.mtu { metrics.RecordDrift("mtu") }
    }
    return isDrifted
}

func (d *DriftDetector) extractInterfaceNameFromPath(configPath string) string {
    fileName := filepath.Base(configPath)
    if strings.HasSuffix(fileName, ".yaml") && strings.Contains(fileName, "-") {
        parts := strings.Split(strings.TrimSuffix(fileName, ".yaml"), "-")
        if len(parts) >= 2 { return parts[len(parts)-1] }
    }
    if strings.HasPrefix(fileName, "ifcfg-") {
        return strings.TrimPrefix(fileName, "ifcfg-")
    }
    return ""
}

func (d *DriftDetector) parseIfcfgFile(content []byte) ifcfgFileConfig {
    config := ifcfgFileConfig{}
    scanner := bufio.NewScanner(strings.NewReader(string(content)))
    for scanner.Scan() {
        line := strings.TrimSpace(scanner.Text())
        if line == "" || strings.HasPrefix(line, "#") { continue }
        parts := strings.SplitN(line, "=", 2)
        if len(parts) != 2 { continue }
        key := strings.TrimSpace(parts[0])
        value := strings.TrimSpace(parts[1])
        switch key {
        case "HWADDR": config.macAddress = strings.ToLower(value)
        case "IPADDR": config.ipAddress = value
        case "PREFIX": config.prefix = value
        case "MTU": if mtu, err := strconv.Atoi(value); err == nil { config.mtu = mtu }
        }
    }
    return config
}

func (d *DriftDetector) checkIfcfgDrift(dbIface entities.NetworkInterface, fileConfig ifcfgFileConfig) bool {
    var dbPrefix string
    if dbIface.CIDR() != "" {
        if parts := strings.Split(dbIface.CIDR(), "/"); len(parts) == 2 { dbPrefix = parts[1] }
    }
    isDrifted := (dbIface.Address() != fileConfig.ipAddress) ||
        (dbPrefix != "" && fileConfig.prefix != "" && dbPrefix != fileConfig.prefix) ||
        (dbIface.MTU() != fileConfig.mtu)
    if isDrifted {
        d.logger.WithFields(logrus.Fields{
            "interface_id": dbIface.ID(),
            "mac_address":  dbIface.MacAddress(),
            "db_address":   dbIface.Address(),
            "db_cidr":      dbIface.CIDR(),
            "db_mtu":       dbIface.MTU(),
            "file_address": fileConfig.ipAddress,
            "file_prefix":  fileConfig.prefix,
            "file_mtu":     fileConfig.mtu,
        }).Debug("ifcfg configuration drift detected")
    }
    return isDrifted
}

// System checks
func (d *DriftDetector) checkSystemInterfaceDrift(ctx context.Context, dbIface entities.NetworkInterface, interfaceName string) bool {
    foundName, err := d.naming.FindInterfaceNameByMAC(dbIface.MacAddress())
    if err != nil || strings.TrimSpace(foundName) == "" {
        d.logger.WithFields(logrus.Fields{
            "interface_name": interfaceName,
            "cr_mac":         dbIface.MacAddress(),
            "error":          err,
        }).Warn("System MAC presence validation failed (not found)")
        return true
    }
    if d.isInterfaceUp(foundName) {
        d.logger.WithFields(logrus.Fields{ "interface_name": foundName, "mac_address": strings.ToLower(dbIface.MacAddress()) }).Warn("Target interface is UP - potentially dangerous to modify")
        return true
    }
    d.logger.WithFields(logrus.Fields{ "interface_name": foundName, "mac_address": strings.ToLower(dbIface.MacAddress()) }).Debug("System MAC presence validation passed")
    return false
}

func (d *DriftDetector) isInterfaceUp(interfaceName string) bool {
    isUp, err := d.naming.IsInterfaceUp(interfaceName)
    if err != nil {
        d.logger.WithFields(logrus.Fields{"interface_name": interfaceName, "error": err}).Debug("Failed to check interface UP status, assuming safe to modify")
        return false
    }
    return isUp
}
