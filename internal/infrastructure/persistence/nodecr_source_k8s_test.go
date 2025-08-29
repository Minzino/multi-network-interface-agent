package persistence

import (
    "context"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/apimachinery/pkg/runtime"
    "k8s.io/apimachinery/pkg/runtime/schema"
    dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestK8sNodeConfigSource_GetNodeConfig_ParsesSpec(t *testing.T) {
    scheme := runtime.NewScheme()
    gvk := schema.GroupVersionKind{Group: "multinic.io", Version: "v1alpha1", Kind: "MultiNicNodeConfig"}

    u := &unstructured.Unstructured{
        Object: map[string]interface{}{
            "apiVersion": "multinic.io/v1alpha1",
            "kind":       "MultiNicNodeConfig",
            "metadata": map[string]interface{}{
                "name":      "worker-node-01",
                "namespace": "multinic-system",
            },
            "spec": map[string]interface{}{
                "nodeName": "worker-node-01",
                "interfaces": []interface{}{
                    map[string]interface{}{
                        "id":         int64(1),
                        "macAddress": "02:00:00:00:01:01",
                        "address":    "192.168.100.10",
                        "cidr":       "192.168.100.10/24",
                        "mtu":        int64(1500),
                    },
                    map[string]interface{}{
                        "id":         int64(2),
                        "macAddress": "02:00:00:00:01:02",
                        "address":    "192.168.200.10",
                        "cidr":       "192.168.200.10/24",
                        "mtu":        int64(1500),
                    },
                },
            },
        },
    }
    u.SetGroupVersionKind(gvk)
    u.SetNamespace("multinic-system")

    dyn := dynamicfake.NewSimpleDynamicClient(scheme, u)

    src := NewK8sNodeConfigSource(dyn, "multinic-system")

    cfg, err := src.GetNodeConfig(context.Background(), "worker-node-01")
    require.NoError(t, err)
    require.NotNil(t, cfg)
    assert.Equal(t, "worker-node-01", cfg.NodeName)
    require.Len(t, cfg.Interfaces, 2)
    assert.Equal(t, "02:00:00:00:01:01", cfg.Interfaces[0].MacAddress)
    assert.Equal(t, "192.168.200.10", cfg.Interfaces[1].Address)
}
