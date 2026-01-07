package network

import (
	"multinic-agent/internal/domain/errors"
	"multinic-agent/internal/domain/interfaces"

	"github.com/sirupsen/logrus"
)

// NetworkManagerFactory is a factory that creates appropriate network managers based on OS
type NetworkManagerFactory struct {
	osDetector      interfaces.OSDetector
	commandExecutor interfaces.CommandExecutor
	fileSystem      interfaces.FileSystem
	logger          *logrus.Logger
	opts            Options
}

// NewNetworkManagerFactory creates a new NetworkManagerFactory
func NewNetworkManagerFactory(
	osDetector interfaces.OSDetector,
	executor interfaces.CommandExecutor,
	fs interfaces.FileSystem,
	logger *logrus.Logger,
	opts Options,
) *NetworkManagerFactory {
	return &NetworkManagerFactory{
		osDetector:      osDetector,
		commandExecutor: executor,
		fileSystem:      fs,
		logger:          logger,
		opts:            opts.normalize(),
	}
}

// CreateNetworkConfigurer creates appropriate NetworkConfigurer based on OS
func (f *NetworkManagerFactory) CreateNetworkConfigurer() (interfaces.NetworkConfigurer, error) {
	osType, err := f.osDetector.DetectOS()
	if err != nil {
		return nil, errors.NewSystemError("failed to detect OS", err)
	}

	f.logger.WithField("os_type", osType).Debug("OS type detected")

	switch osType {
	case interfaces.OSTypeUbuntu:
		return NewNetplanAdapterWithOptions(
			f.commandExecutor,
			f.fileSystem,
			f.logger,
			f.opts,
		), nil

	case interfaces.OSTypeRHEL:
		return NewRHELAdapterWithOptions(
			f.commandExecutor,
			f.fileSystem,
			f.logger,
			f.opts,
		), nil

	default:
		return nil, errors.NewSystemError("unsupported OS type", nil)
	}
}

// CreateNetworkRollbacker creates appropriate NetworkRollbacker based on OS
func (f *NetworkManagerFactory) CreateNetworkRollbacker() (interfaces.NetworkRollbacker, error) {
	// Return same instance as NetworkConfigurer (same implementation implements both interfaces)
	configurer, err := f.CreateNetworkConfigurer()
	if err != nil {
		return nil, err
	}

	// Convert to NetworkRollbacker through type assertion
	if rollbacker, ok := configurer.(interfaces.NetworkRollbacker); ok {
		return rollbacker, nil
	}

	return nil, errors.NewSystemError("network manager does not support rollback functionality", nil)
}
