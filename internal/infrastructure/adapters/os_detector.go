package adapters

import (
	"bufio"
	"fmt"
	"multinic-agent/internal/domain/errors"
	"multinic-agent/internal/domain/interfaces"
	"strings"
)

// RealOSDetector is an OSDetector implementation that detects the actual OS
type RealOSDetector struct {
	fileSystem interfaces.FileSystem
}

// NewRealOSDetector creates a new RealOSDetector
func NewRealOSDetector(fs interfaces.FileSystem) interfaces.OSDetector {
	return &RealOSDetector{
		fileSystem: fs,
	}
}

// DetectOS returns the current operating system type
func (d *RealOSDetector) DetectOS() (interfaces.OSType, error) {
	// Try /etc/os-release file first
	releaseInfo, err := d.parseOSRelease()
	if err != nil {
		return "", errors.NewSystemError("OS detection failed: cannot read /etc/os-release file", err)
	}

	id, ok := releaseInfo["ID"]
	if !ok {
		return "", errors.NewSystemError("OS detection failed: no ID field in /etc/os-release file", nil)
	}

	idLike, _ := releaseInfo["ID_LIKE"]

	// OS type determination logic
	if id == "ubuntu" {
		return interfaces.OSTypeUbuntu, nil
	} else if id == "rhel" || id == "centos" || id == "rocky" || id == "almalinux" || id == "oracle" || strings.Contains(idLike, "rhel") || strings.Contains(idLike, "fedora") {
		return interfaces.OSTypeRHEL, nil
	}

	// Return error if doesn't match known IDs
	return "", errors.NewSystemError(fmt.Sprintf("unsupported OS type. ID: '%s', ID_LIKE: '%s'", id, idLike), nil)
}

// parseOSRelease parses /etc/os-release file and returns it as a map.
func (d *RealOSDetector) parseOSRelease() (map[string]string, error) {
	content, err := d.fileSystem.ReadFile("/host/etc/os-release")
	if err != nil {
		return nil, err
	}

	releaseInfo := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "=") {
			parts := strings.SplitN(line, "=", 2)
			key := strings.TrimSpace(parts[0])
			value := strings.Trim(strings.TrimSpace(parts[1]), "\"")
			releaseInfo[key] = value
		}
	}

	return releaseInfo, nil
}
