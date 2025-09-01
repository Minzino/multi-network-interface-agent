package adapters

import (
    "context"
    "os"
    "strings"

    "multinic-agent/internal/domain/errors"
    "multinic-agent/internal/domain/interfaces"

    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/runtime/schema"
    "k8s.io/client-go/dynamic"
)

// K8sOSDetector detects OS type by reading Node.status.nodeInfo.osImage via Kube API
type K8sOSDetector struct {
    client dynamic.Interface
}

func NewK8sOSDetector(client dynamic.Interface) interfaces.OSDetector {
    return &K8sOSDetector{client: client}
}

func (d *K8sOSDetector) DetectOS() (interfaces.OSType, error) {
    nodeName := os.Getenv("NODE_NAME")
    if strings.TrimSpace(nodeName) == "" {
        // Best-effort fallback to hostname without domain
        hn, err := os.Hostname()
        if err != nil {
            return "", errors.NewSystemError("failed to obtain node name", err)
        }
        if idx := strings.Index(hn, "."); idx != -1 {
            nodeName = hn[:idx]
        } else {
            nodeName = hn
        }
    }

    gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "nodes"}
    u, err := d.client.Resource(gvr).Get(context.Background(), nodeName, metav1.GetOptions{})
    if err != nil {
        return "", errors.NewSystemError("failed to query Node from Kubernetes", err)
    }

    osImage := readString(u, "status", "nodeInfo", "osImage")
    idLike := strings.ToLower(osImage)

    if strings.Contains(idLike, "ubuntu") {
        return interfaces.OSTypeUbuntu, nil
    }
    if strings.Contains(idLike, "red hat") || strings.Contains(idLike, "rhel") || strings.Contains(idLike, "centos") || strings.Contains(idLike, "rocky") || strings.Contains(idLike, "alma") || strings.Contains(idLike, "oracle") {
        return interfaces.OSTypeRHEL, nil
    }

    return "", errors.NewSystemError("unsupported OS type from Node osImage: "+osImage, nil)
}

func readString(u *unstructured.Unstructured, fields ...string) string {
    v, found, _ := unstructured.NestedString(u.Object, fields...)
    if !found {
        return ""
    }
    return v
}

