package persistence

import (
    "context"
    "fmt"

    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/runtime/schema"
    "k8s.io/client-go/dynamic"
)

// K8sNodeConfigSource fetches MultiNicNodeConfig via Kubernetes Dynamic client
type K8sNodeConfigSource struct {
    client    dynamic.Interface
    namespace string
    gvr       schema.GroupVersionResource
}

// NewK8sNodeConfigSource creates a K8s-backed NodeConfig source
func NewK8sNodeConfigSource(client dynamic.Interface, namespace string) *K8sNodeConfigSource {
    return &K8sNodeConfigSource{
        client:    client,
        namespace: namespace,
        gvr: schema.GroupVersionResource{
            Group:    "multinic.io",
            Version:  "v1alpha1",
            Resource: "multinicnodeconfigs",
        },
    }
}

func (s *K8sNodeConfigSource) GetNodeConfig(ctx context.Context, nodeName string) (*NodeConfig, error) {
    u, err := s.client.Resource(s.gvr).Namespace(s.namespace).Get(ctx, nodeName, metav1.GetOptions{})
    if err != nil {
        return nil, fmt.Errorf("failed to get MultiNicNodeConfig %s/%s: %w", s.namespace, nodeName, err)
    }

    return unstructuredToNodeConfig(u), nil
}

func unstructuredToNodeConfig(u *unstructured.Unstructured) *NodeConfig {
    cfg := &NodeConfig{}
    // default node name from metadata.name
    cfg.NodeName = u.GetName()

    spec, _, _ := unstructured.NestedMap(u.Object, "spec")
    if v, ok := spec["nodeName"].(string); ok && v != "" {
        cfg.NodeName = v
    }

    ifaces, _, _ := unstructured.NestedSlice(u.Object, "spec", "interfaces")
    for _, it := range ifaces {
        m, ok := it.(map[string]any)
        if !ok {
            continue
        }
        ni := NodeInterface{}
        if v, ok := m["id"].(int64); ok {
            ni.ID = int(v)
        } else if v, ok := m["id"].(int); ok {
            ni.ID = v
        }
        if v, ok := m["portId"].(string); ok {
            ni.PortID = v
        }
        if v, ok := m["name"].(string); ok {
            ni.Name = v
        }
        if v, ok := m["macAddress"].(string); ok {
            ni.MacAddress = v
        }
        if v, ok := m["address"].(string); ok {
            ni.Address = v
        }
        if v, ok := m["cidr"].(string); ok {
            ni.CIDR = v
        }
        if v, ok := m["mtu"].(int64); ok {
            ni.MTU = int(v)
        } else if v, ok := m["mtu"].(int); ok {
            ni.MTU = v
        }
        cfg.Interfaces = append(cfg.Interfaces, ni)
    }
    return cfg
}
