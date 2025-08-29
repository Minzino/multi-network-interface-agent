package persistence

import (
    "context"
    "fmt"
    "multinic-agent/internal/domain/entities"
    "multinic-agent/internal/domain/errors"
    "multinic-agent/internal/domain/interfaces"

    "github.com/sirupsen/logrus"
)

// NodeConfig represents a simplified view of MultiNicNodeConfig spec for a node
type NodeConfig struct {
    NodeName   string
    Interfaces []NodeInterface
}

// NodeInterface represents a single interface entry from the node CR spec
type NodeInterface struct {
    ID         int    `yaml:"id"`
    PortID     string `yaml:"portId"`
    MacAddress string `yaml:"macAddress"`
    Address    string `yaml:"address"`
    CIDR       string `yaml:"cidr"`
    MTU        int    `yaml:"mtu"`
}

// NodeConfigSource abstracts how to obtain a node's CR spec (K8s, file, etc.)
type NodeConfigSource interface {
    GetNodeConfig(ctx context.Context, nodeName string) (*NodeConfig, error)
}

// NodeCRRepository implements NetworkInterfaceRepository backed by MultiNicNodeConfig CR
// Note: Agent는 CR의 status를 직접 업데이트하지 않습니다. UpdateInterfaceStatus는 no-op입니다.
type NodeCRRepository struct {
    source NodeConfigSource
    logger *logrus.Logger
}

// NewNodeCRRepository creates a new NodeCRRepository
func NewNodeCRRepository(source NodeConfigSource, logger *logrus.Logger) interfaces.NetworkInterfaceRepository {
    return &NodeCRRepository{source: source, logger: logger}
}

// GetPendingInterfaces returns interfaces from the node config (all treated as pending desired state)
func (r *NodeCRRepository) GetPendingInterfaces(ctx context.Context, nodeName string) ([]entities.NetworkInterface, error) {
    return r.loadAll(ctx, nodeName)
}

// GetConfiguredInterfaces returns empty since Agent does not own status in NodeCR model
func (r *NodeCRRepository) GetConfiguredInterfaces(ctx context.Context, nodeName string) ([]entities.NetworkInterface, error) {
    // In node-CR architecture, configured state is tracked by controller in CR.status
    // The Agent does not persist configured state; return empty slice.
    return []entities.NetworkInterface{}, nil
}

// UpdateInterfaceStatus is a no-op for node-CR architecture (controller updates CR.status)
func (r *NodeCRRepository) UpdateInterfaceStatus(ctx context.Context, interfaceID int, status entities.InterfaceStatus) error {
    r.logger.WithFields(logrus.Fields{
        "interface_id": interfaceID,
        "status":       status,
    }).Debug("NodeCRRepository: UpdateInterfaceStatus no-op (handled by controller)")
    return nil
}

// GetInterfaceByID finds an interface by ID from node config
func (r *NodeCRRepository) GetInterfaceByID(ctx context.Context, id int) (*entities.NetworkInterface, error) {
    // Without nodeName context, scan across all known nodes is not supported by File source
    // This repository is used per-node by the Agent; we cannot determine nodeName here.
    return nil, errors.NewNotFoundError("GetInterfaceByID not supported without nodeName context in NodeCRRepository")
}

// GetActiveInterfaces returns all declared interfaces for the node
func (r *NodeCRRepository) GetActiveInterfaces(ctx context.Context, nodeName string) ([]entities.NetworkInterface, error) {
    return r.loadAll(ctx, nodeName)
}

// GetAllNodeInterfaces returns all declared interfaces for the node
func (r *NodeCRRepository) GetAllNodeInterfaces(ctx context.Context, nodeName string) ([]entities.NetworkInterface, error) {
    return r.loadAll(ctx, nodeName)
}

func (r *NodeCRRepository) loadAll(ctx context.Context, nodeName string) ([]entities.NetworkInterface, error) {
    cfg, err := r.source.GetNodeConfig(ctx, nodeName)
    if err != nil {
        return nil, errors.NewSystemError("failed to get node config", err)
    }
    if cfg == nil {
        return nil, errors.NewNotFoundError(fmt.Sprintf("node config not found for node %s", nodeName))
    }

    var out []entities.NetworkInterface
    for i, ni := range cfg.Interfaces {
        id := ni.ID
        if id == 0 {
            id = i + 1
        }
        out = append(out, entities.NetworkInterface{
            ID:               id,
            MacAddress:       ni.MacAddress,
            AttachedNodeName: cfg.NodeName,
            Status:           entities.StatusPending,
            Address:          ni.Address,
            CIDR:             ni.CIDR,
            MTU:              ni.MTU,
        })
    }
    return out, nil
}
