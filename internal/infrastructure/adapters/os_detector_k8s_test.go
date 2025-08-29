package adapters

import (
    "os"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/apimachinery/pkg/runtime"
    "k8s.io/apimachinery/pkg/runtime/schema"
    dynamicfake "k8s.io/client-go/dynamic/fake"
)

func makeNode(name, osImage string) *unstructured.Unstructured {
    u := &unstructured.Unstructured{
        Object: map[string]interface{}{
            "apiVersion": "v1",
            "kind":       "Node",
            "metadata": map[string]interface{}{
                "name": name,
            },
            "status": map[string]interface{}{
                "nodeInfo": map[string]interface{}{
                    "osImage": osImage,
                },
            },
        },
    }
    u.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Node"})
    return u
}

func TestK8sOSDetector_RHEL(t *testing.T) {
    scheme := runtime.NewScheme()
    dyn := dynamicfake.NewSimpleDynamicClient(scheme, makeNode("node-1", "Red Hat Enterprise Linux 9.4 (Plow)"))

    t.Setenv("NODE_NAME", "node-1")
    d := NewK8sOSDetector(dyn)

    osType, err := d.DetectOS()
    require.NoError(t, err)
    assert.Equal(t, "rhel", string(osType))
}

func TestK8sOSDetector_Ubuntu(t *testing.T) {
    scheme := runtime.NewScheme()
    dyn := dynamicfake.NewSimpleDynamicClient(scheme, makeNode("node-2", "Ubuntu 22.04.4 LTS"))

    os.Unsetenv("NODE_NAME")
    // Hostname path is not controlled here; rely on error-free outcome only if NODE_NAME present
    t.Setenv("NODE_NAME", "node-2")

    d := NewK8sOSDetector(dyn)
    osType, err := d.DetectOS()
    require.NoError(t, err)
    assert.Equal(t, "ubuntu", string(osType))
}

