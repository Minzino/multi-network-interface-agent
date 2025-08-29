package controller

import (
    "context"
    "fmt"
    "strings"

    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/apimachinery/pkg/runtime/schema"
    "k8s.io/client-go/dynamic"
    "k8s.io/client-go/kubernetes"
)

// Controller reconciles MultiNicNodeConfig into Jobs per node
type Controller struct {
    Dyn              dynamic.Interface
    Client           kubernetes.Interface
    AgentImage       string
    ImagePullPolicy  corev1.PullPolicy
    ServiceAccount   string
    NodeCRNamespace  string
    JobTTLSeconds    *int32
}

var nodeCRGVR = schema.GroupVersionResource{Group: "multinic.io", Version: "v1alpha1", Resource: "multinicnodeconfigs"}

// Reconcile ensures a Job exists targeting the node specified by the MultiNicNodeConfig
func (c *Controller) Reconcile(ctx context.Context, namespace, name string) error {
    u, err := c.Dyn.Resource(nodeCRGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
    if err != nil {
        return err
    }

    nodeName := nestedString(u, "spec", "nodeName")
    if nodeName == "" {
        nodeName = u.GetName()
    }
    // optional instance-id verification via label
    instanceID := u.GetLabels()["multinic.io/instance-id"]

    node, err := c.Client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
    if err != nil {
        return fmt.Errorf("failed to get node %s: %w", nodeName, err)
    }

    // Verify mapping if label provided
    if instanceID != "" {
        sysUUID := strings.ToLower(node.Status.NodeInfo.SystemUUID)
        if strings.ToLower(instanceID) != sysUUID {
            return fmt.Errorf("instance-id mismatch: cr=%s node=%s", instanceID, node.Status.NodeInfo.SystemUUID)
        }
    }

    osImage := node.Status.NodeInfo.OSImage

    job := BuildAgentJob(osImage, JobParams{
        Namespace:          namespace,
        Name:               fmt.Sprintf("multinic-agent-%s", nodeName),
        Image:              c.AgentImage,
        PullPolicy:         c.ImagePullPolicy,
        ServiceAccountName: c.ServiceAccount,
        NodeName:           nodeName,
        NodeCRNamespace:    c.NodeCRNamespace,
        TTLSecondsAfterDone: c.JobTTLSeconds,
    })

    // Mark CR as InProgress
    _ = c.updateCRStatus(ctx, u, map[string]any{
        "state": "InProgress",
        "conditions": []any{
            map[string]any{"type": "InProgress", "status": "True", "reason": "JobScheduled"},
        },
    })

    // Upsert: if exists, return nil; else create
    if _, err := c.Client.BatchV1().Jobs(namespace).Get(ctx, job.Name, metav1.GetOptions{}); err == nil {
        return nil
    }
    if _, err := c.Client.BatchV1().Jobs(namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil {
        return err
    }
    return nil
}

func nestedString(u *unstructured.Unstructured, fields ...string) string {
    v, found, _ := unstructured.NestedString(u.Object, fields...)
    if !found {
        return ""
    }
    return v
}

// ProcessAll lists all MultiNicNodeConfig in namespace and reconciles them
func (c *Controller) ProcessAll(ctx context.Context, namespace string) error {
    list, err := c.Dyn.Resource(nodeCRGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
    if err != nil { return err }
    for i := range list.Items {
        name := list.Items[i].GetName()
        if err := c.Reconcile(ctx, namespace, name); err != nil {
            return err
        }
    }
    return nil
}

// ProcessJobs scans Jobs and updates corresponding CR status based on Job completion
func (c *Controller) ProcessJobs(ctx context.Context, namespace string) error {
    jobs, err := c.Client.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=multinic-agent"})
    if err != nil { return err }
    for i := range jobs.Items {
        job := &jobs.Items[i]
        nodeName := job.Labels["multinic.io/node-name"]
        if nodeName == "" { continue }
        u, err := c.Dyn.Resource(nodeCRGVR).Namespace(namespace).Get(ctx, nodeName, metav1.GetOptions{})
        if err != nil { continue }

        // Determine completion state
        if job.Status.Succeeded > 0 {
            _ = c.updateCRStatus(ctx, u, map[string]any{
                "state": "Configured",
                "conditions": []any{ map[string]any{"type": "Ready", "status": "True", "reason": "JobSucceeded"} },
            })
        } else if job.Status.Failed > 0 {
            _ = c.updateCRStatus(ctx, u, map[string]any{
                "state": "Failed",
                "conditions": []any{ map[string]any{"type": "Ready", "status": "False", "reason": "JobFailed"} },
            })
        }
    }
    return nil
}

func (c *Controller) updateCRStatus(ctx context.Context, u *unstructured.Unstructured, status map[string]any) error {
    obj := u.DeepCopy()
    // merge into status
    current, _, _ := unstructured.NestedMap(obj.Object, "status")
    if current == nil { current = map[string]any{} }
    for k, v := range status { current[k] = v }
    _ = unstructured.SetNestedMap(obj.Object, current, "status")
    // Try UpdateStatus, fallback to Update for fake client compatibility
    if _, err := c.Dyn.Resource(nodeCRGVR).Namespace(obj.GetNamespace()).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
        // ignore error in this simple flow
        return err
    }
    return nil
}
