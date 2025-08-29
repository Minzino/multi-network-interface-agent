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
    "log"
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
    log.Printf("reconcile: ns=%s name=%s", namespace, name)
    u, err := c.Dyn.Resource(nodeCRGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
    if err != nil {
        log.Printf("reconcile get CR error: %v", err)
        return err
    }

    nodeName := nestedString(u, "spec", "nodeName")
    if nodeName == "" {
        nodeName = u.GetName()
    }
    // instance-id verification via spec.instanceId or label
    instanceID := nestedString(u, "spec", "instanceId")
    if instanceID == "" {
        instanceID = u.GetLabels()["multinic.io/instance-id"]
    }

    node, err := c.Client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
    if err != nil {
        return fmt.Errorf("failed to get node %s: %w", nodeName, err)
    }

    // Verify mapping if label provided
    if instanceID != "" {
        sysUUID := normalizeUUID(node.Status.NodeInfo.SystemUUID)
        if normalizeUUID(instanceID) != sysUUID {
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
        log.Printf("job already exists: %s/%s", namespace, job.Name)
        return nil
    }
    if _, err := c.Client.BatchV1().Jobs(namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil {
        log.Printf("create job error: %v", err)
        return err
    }
    log.Printf("job created: %s/%s for node=%s osImage=%s", namespace, job.Name, nodeName, osImage)
    return nil
}

func nestedString(u *unstructured.Unstructured, fields ...string) string {
    v, found, _ := unstructured.NestedString(u.Object, fields...)
    if !found {
        return ""
    }
    return v
}

func normalizeUUID(s string) string {
    // lower-case trim spaces; keep hyphens for consistent comparison
    return strings.ToLower(strings.TrimSpace(s))
}

// ProcessAll lists all MultiNicNodeConfig in namespace and reconciles them
func (c *Controller) ProcessAll(ctx context.Context, namespace string) error {
    list, err := c.Dyn.Resource(nodeCRGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
    if err != nil { return err }
    for i := range list.Items {
        name := list.Items[i].GetName()
        log.Printf("processAll reconcile: %s/%s", namespace, name)
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
        currentState, _, _ := unstructured.NestedString(u.Object, "status", "state")
        if job.Status.Succeeded > 0 {
            if currentState != "Configured" {
                log.Printf("job succeeded: %s/%s", namespace, job.Name)
                _ = c.updateCRStatus(ctx, u, map[string]any{
                    "state": "Configured",
                    "conditions": []any{ map[string]any{"type": "Ready", "status": "True", "reason": "JobSucceeded"} },
                })
                // Immediate cleanup of succeeded job
                _ = c.deleteJob(ctx, namespace, job.Name)
            }
        } else if job.Status.Failed > 0 {
            if currentState != "Failed" {
                log.Printf("job failed: %s/%s", namespace, job.Name)
                _ = c.updateCRStatus(ctx, u, map[string]any{
                    "state": "Failed",
                    "conditions": []any{ map[string]any{"type": "Ready", "status": "False", "reason": "JobFailed"} },
                })
                // Cleanup failed job as well (controller may recreate on next reconcile)
                _ = c.deleteJob(ctx, namespace, job.Name)
            }
        }
    }
    return nil
}

// deleteJob removes a Job by name, ignore errors
func (c *Controller) deleteJob(ctx context.Context, namespace, name string) error {
    policy := metav1.DeletePropagationBackground
    opts := metav1.DeleteOptions{PropagationPolicy: &policy}
    if err := c.Client.BatchV1().Jobs(namespace).Delete(ctx, name, opts); err != nil {
        log.Printf("delete job error: %v", err)
        return err
    }
    log.Printf("job deleted: %s/%s", namespace, name)
    return nil
}

// DeleteJobForNode removes job by node name with naming convention
func (c *Controller) DeleteJobForNode(ctx context.Context, namespace, nodeName string) {
    name := fmt.Sprintf("multinic-agent-%s", nodeName)
    _ = c.deleteJob(ctx, namespace, name)
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
