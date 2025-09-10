package persistence

import (
    "context"
    "testing"

    "github.com/sirupsen/logrus"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

type stubNodeSource struct{ cfg *NodeConfig }

func (s *stubNodeSource) GetNodeConfig(ctx context.Context, nodeName string) (*NodeConfig, error) {
    return s.cfg, nil
}

func TestNodeCRRepository_MapsInterfaces_FromSource(t *testing.T) {
    t.Parallel()

    src := &stubNodeSource{cfg: &NodeConfig{
        NodeName: "worker-node-01",
        Interfaces: []NodeInterface{
            {ID: 1, MacAddress: "02:00:00:00:01:01", Address: "192.168.100.10", CIDR: "192.168.100.10/24", MTU: 1500},
            {ID: 2, MacAddress: "02:00:00:00:01:02", Address: "192.168.200.10", CIDR: "192.168.200.10/24", MTU: 1500},
        },
    }}
    logger := logrus.New()
    repo := NewNodeCRRepository(src, logger)

    ctx := context.Background()
    nodeName := "worker-node-01"

    ifaces, err := repo.GetAllNodeInterfaces(ctx, nodeName)
    require.NoError(t, err)
    require.Len(t, ifaces, 2)

    first := ifaces[0]
    assert.Equal(t, nodeName, first.AttachedNodeName())
    assert.Equal(t, "02:00:00:00:01:01", first.MacAddress())
    assert.Equal(t, "192.168.100.10", first.Address())
    assert.Equal(t, "192.168.100.10/24", first.CIDR())
    assert.Equal(t, 1500, first.MTU())

    second := ifaces[1]
    assert.Equal(t, "02:00:00:00:01:02", second.MacAddress())
    assert.Equal(t, "192.168.200.10", second.Address())
    assert.Equal(t, "192.168.200.10/24", second.CIDR())
    assert.Equal(t, 1500, second.MTU())
}

func TestNodeCRRepository_UpdateInterfaceStatus_NoOp(t *testing.T) {
    t.Parallel()

    src := &stubNodeSource{cfg: &NodeConfig{NodeName: "n1"}}
    logger := logrus.New()
    repo := NewNodeCRRepository(src, logger)

    err := repo.UpdateInterfaceStatus(context.Background(), 1, 1)
    assert.NoError(t, err)
}
