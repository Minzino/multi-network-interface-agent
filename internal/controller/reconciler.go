package controller

import (
    "context"
    "fmt"
    "strings"
    "time"

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
    JobDeleteDelaySeconds int // optional grace period before deleting jobs (seconds)
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
    
    // Check if CR is already in a final state (Configured/Failed)
    currentState, _, _ := unstructured.NestedString(u.Object, "status", "state")
    if currentState == "Configured" || currentState == "Failed" {
        log.Printf("reconcile: CR %s/%s is already in final state '%s', skipping job creation", namespace, name, currentState)
        return nil
    }
    
    // Log interface details
    c.logInterfaceDetails(u, nodeName)
    
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
        Action:             "", // default apply
    })

    // Mark CR as InProgress with interface details
    interfaceStatuses := c.buildInterfaceStatuses(u, "InProgress", "JobScheduled")
    _ = c.updateCRStatus(ctx, u, map[string]any{
        "state": "InProgress",
        "conditions": []any{
            map[string]any{"type": "InProgress", "status": "True", "reason": "JobScheduled"},
        },
        "interfaceStatuses": interfaceStatuses,
        "lastUpdated": time.Now().Format(time.RFC3339),
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
        
        // Also update interface states to keep CR status current
        if err := c.updateInterfaceStates(ctx, namespace, name); err != nil {
            log.Printf("processAll: failed to update interface states for %s/%s: %v", namespace, name, err)
            // Don't return error for interface state updates, just log
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
        action := job.Labels["multinic.io/action"]
        if err != nil {
            // CR missing (e.g., during cleanup). If cleanup job finished, delete it.
            if action == "cleanup" {
                if job.Status.Succeeded > 0 || job.Status.Failed > 0 {
                    c.scheduleJobDeletion(ctx, namespace, job.Name)
                }
            }
            continue
        }

        // Determine completion state
        currentState, _, _ := unstructured.NestedString(u.Object, "status", "state")
        if job.Status.Succeeded > 0 {
            if currentState != "Configured" {
                log.Printf("job succeeded: %s/%s", namespace, job.Name)
                interfaceStatuses := c.buildInterfaceStatuses(u, "Configured", "JobSucceeded")
                _ = c.updateCRStatus(ctx, u, map[string]any{
                    "state": "Configured",
                    "conditions": []any{ map[string]any{"type": "Ready", "status": "True", "reason": "JobSucceeded"} },
                    "interfaceStatuses": interfaceStatuses,
                    "lastUpdated": time.Now().Format(time.RFC3339),
                })
                // cleanup of succeeded job (optionally delay for log scraping)
                c.scheduleJobDeletion(ctx, namespace, job.Name)
            }
        } else if job.Status.Failed > 0 {
            if currentState != "Failed" {
                log.Printf("job failed: %s/%s", namespace, job.Name)
                interfaceStatuses := c.buildInterfaceStatuses(u, "Failed", "JobFailed")
                _ = c.updateCRStatus(ctx, u, map[string]any{
                    "state": "Failed",
                    "conditions": []any{ map[string]any{"type": "Ready", "status": "False", "reason": "JobFailed"} },
                    "interfaceStatuses": interfaceStatuses,
                    "lastUpdated": time.Now().Format(time.RFC3339),
                })
                // Cleanup failed job as well (optionally delay)
                c.scheduleJobDeletion(ctx, namespace, job.Name)
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

// LaunchCleanupJob creates a cleanup-mode job for the given node
func (c *Controller) LaunchCleanupJob(ctx context.Context, namespace, nodeName string) error {
    log.Printf("LaunchCleanupJob: starting cleanup job launch for node=%s namespace=%s", nodeName, namespace)
    
    node, err := c.Client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
    if err != nil { 
        log.Printf("LaunchCleanupJob: failed to get node %s: %v", nodeName, err)
        return err 
    }
    osImage := node.Status.NodeInfo.OSImage
    log.Printf("LaunchCleanupJob: node=%s osImage=%s", nodeName, osImage)
    
    // use a distinct name to avoid colliding with the apply job
    cleanupName := fmt.Sprintf("multinic-agent-cleanup-%s", nodeName)
    log.Printf("LaunchCleanupJob: building job with name=%s", cleanupName)
    
    job := BuildAgentJob(osImage, JobParams{
        Namespace:           namespace,
        Name:                cleanupName,
        Image:               c.AgentImage,
        PullPolicy:          c.ImagePullPolicy,
        ServiceAccountName:  c.ServiceAccount,
        NodeName:            nodeName,
        NodeCRNamespace:     c.NodeCRNamespace,
        TTLSecondsAfterDone: c.JobTTLSeconds,
        Action:              "cleanup",
    })
    
    log.Printf("LaunchCleanupJob: checking if cleanup job already exists: %s/%s", namespace, cleanupName)
    if _, err := c.Client.BatchV1().Jobs(namespace).Get(ctx, job.Name, metav1.GetOptions{}); err == nil {
        log.Printf("cleanup job already exists: %s/%s", namespace, job.Name)
        return nil
    }
    
    log.Printf("LaunchCleanupJob: attempting to create cleanup job: %s/%s", namespace, cleanupName)
    if _, err := c.Client.BatchV1().Jobs(namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil {
        log.Printf("create cleanup job error: %v", err)
        return err
    }
    log.Printf("cleanup job created: %s/%s for node=%s osImage=%s", namespace, job.Name, nodeName, osImage)
    return nil
}

// scheduleJobDeletion deletes a job now or after optional delay
func (c *Controller) scheduleJobDeletion(ctx context.Context, namespace, name string) {
    if c.JobDeleteDelaySeconds <= 0 {
        _ = c.deleteJob(ctx, namespace, name)
        return
    }
    delay := time.Duration(c.JobDeleteDelaySeconds) * time.Second
    go func() {
        <-time.After(delay)
        _ = c.deleteJob(context.Background(), namespace, name)
    }()
}

func (c *Controller) updateCRStatus(ctx context.Context, u *unstructured.Unstructured, status map[string]any) error {
    obj := u.DeepCopy()
    // merge into status
    current, _, _ := unstructured.NestedMap(obj.Object, "status")
    if current == nil { current = map[string]any{} }
    for k, v := range status { current[k] = v }
    _ = unstructured.SetNestedMap(obj.Object, current, "status")
    
    // Try UpdateStatus first (proper way for status subresource)
    client := c.Dyn.Resource(nodeCRGVR).Namespace(obj.GetNamespace())
    if _, err := client.UpdateStatus(ctx, obj, metav1.UpdateOptions{}); err != nil {
        // Fallback to regular Update if UpdateStatus fails
        log.Printf("UpdateStatus failed, trying regular Update: %v", err)
        if _, err2 := client.Update(ctx, obj, metav1.UpdateOptions{}); err2 != nil {
            log.Printf("Status update failed: %v", err2)
            return err2
        }
    }
    return nil
}

// logInterfaceDetails logs detailed information about network interfaces from the CR
func (c *Controller) logInterfaceDetails(u *unstructured.Unstructured, nodeName string) {
    // Extract interfaces array from spec
    interfaces, found, err := unstructured.NestedSlice(u.Object, "spec", "interfaces")
    if !found || err != nil {
        log.Printf("No interfaces found in CR %s/%s", u.GetNamespace(), u.GetName())
        return
    }

    log.Printf("=== Interface Details for Node: %s (CR: %s/%s) ===", nodeName, u.GetNamespace(), u.GetName())
    
    for i, iface := range interfaces {
        ifaceMap, ok := iface.(map[string]interface{})
        if !ok {
            continue
        }
        
        // Extract interface details
        id := getIntFromMap(ifaceMap, "id")
        macAddress := getStringFromMap(ifaceMap, "macAddress")
        ipAddress := getStringFromMap(ifaceMap, "address")
        cidr := getStringFromMap(ifaceMap, "cidr")
        mtu := getIntFromMap(ifaceMap, "mtu")
        
        log.Printf("  Interface[%d]: ID=%d, MAC=%s, IP=%s, CIDR=%s, MTU=%d", 
            i, id, macAddress, ipAddress, cidr, mtu)
    }
    log.Printf("=== End Interface Details ===")
}

// Helper functions for extracting values from interface maps
func getStringFromMap(m map[string]interface{}, key string) string {
    if val, ok := m[key]; ok {
        if str, ok := val.(string); ok {
            return str
        }
    }
    return ""
}

func getIntFromMap(m map[string]interface{}, key string) int {
    if val, ok := m[key]; ok {
        if intVal, ok := val.(int); ok {
            return intVal
        }
        if int64Val, ok := val.(int64); ok {
            return int(int64Val)
        }
        if floatVal, ok := val.(float64); ok {
            return int(floatVal)
        }
        // Debug: log unexpected type
        log.Printf("DEBUG: getIntFromMap key=%s, unexpected type: %T, value: %v", key, val, val)
    }
    return 0
}

// buildInterfaceStatuses creates detailed status information for each interface in the CR
func (c *Controller) buildInterfaceStatuses(u *unstructured.Unstructured, status, reason string) []any {
    interfaces, found, err := unstructured.NestedSlice(u.Object, "spec", "interfaces")
    if !found || err != nil {
        log.Printf("No interfaces found when building status for CR %s/%s", u.GetNamespace(), u.GetName())
        return []any{}
    }

    var interfaceStatuses []any
    
    for i, iface := range interfaces {
        ifaceMap, ok := iface.(map[string]interface{})
        if !ok {
            continue
        }
        
        // Extract interface details
        id := getIntFromMap(ifaceMap, "id")
        macAddress := getStringFromMap(ifaceMap, "macAddress")
        ipAddress := getStringFromMap(ifaceMap, "address")
        cidr := getStringFromMap(ifaceMap, "cidr")
        mtu := getIntFromMap(ifaceMap, "mtu")
        
        // Build interface status (convert int types to int64 for unstructured compatibility)
        interfaceStatus := map[string]any{
            "interfaceIndex": int64(i),
            "id":            int64(id),
            "macAddress":    macAddress,
            "address":       ipAddress,
            "cidr":         cidr,
            "mtu":          int64(mtu),
            "status":       status,
            "reason":       reason,
            "lastUpdated":  time.Now().Format(time.RFC3339),
        }
        
        // Generate interface name based on index (multinic0, multinic1, etc.)
        interfaceName := fmt.Sprintf("multinic%d", i)
        interfaceStatus["interfaceName"] = interfaceName
        
        interfaceStatuses = append(interfaceStatuses, interfaceStatus)
        
        log.Printf("Interface[%d] status: ID=%d, MAC=%s, IP=%s, Status=%s, Reason=%s", 
            i, id, macAddress, ipAddress, status, reason)
    }
    
    return interfaceStatuses
}

// getInterfaceNameForMAC attempts to determine the interface name (multinicX) for a given MAC address
func (c *Controller) getInterfaceNameForMAC(macAddress string) string {
    if macAddress == "" {
        return ""
    }
    
    // This is a simple mapping - in a real scenario, you might want to maintain 
    // a more sophisticated mapping or query the actual system
    // For now, we'll use a simple index-based naming
    
    // In practice, the Agent will create interfaces as multinic0, multinic1, etc.
    // We could enhance this by storing the mapping in the CR status or elsewhere
    // For now, return empty string - the actual interface name will be determined by the Agent
    return ""
}

// updateInterfaceStates periodically updates the interface states in the CR status 
// by checking the actual node interface states via API or node status
func (c *Controller) updateInterfaceStates(ctx context.Context, namespace, nodeName string) error {
    log.Printf("updateInterfaceStates: checking interface states for node %s", nodeName)
    
    // Get the CR for this node
    u, err := c.Dyn.Resource(nodeCRGVR).Namespace(namespace).Get(ctx, nodeName, metav1.GetOptions{})
    if err != nil {
        log.Printf("updateInterfaceStates: failed to get CR for node %s: %v", nodeName, err)
        return err
    }
    
    // Get node information to check actual interface states
    node, err := c.Client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
    if err != nil {
        log.Printf("updateInterfaceStates: failed to get node %s: %v", nodeName, err)
        return err
    }
    
    // Build enhanced interface statuses with actual system state
    interfaceStatuses := c.buildEnhancedInterfaceStatuses(u, node)
    
    // Update CR status with current interface states
    currentState, _, _ := unstructured.NestedString(u.Object, "status", "state")
    if currentState == "" {
        currentState = "Unknown"
    }
    
    _ = c.updateCRStatus(ctx, u, map[string]any{
        "interfaceStatuses": interfaceStatuses,
        "lastInterfaceCheck": time.Now().Format(time.RFC3339),
        "nodeReady": c.isNodeReady(node),
    })
    
    log.Printf("updateInterfaceStates: updated interface states for node %s with %d interfaces", 
        nodeName, len(interfaceStatuses))
    
    return nil
}

// buildEnhancedInterfaceStatuses creates detailed status with actual system state check
func (c *Controller) buildEnhancedInterfaceStatuses(u *unstructured.Unstructured, node *corev1.Node) []any {
    interfaces, found, err := unstructured.NestedSlice(u.Object, "spec", "interfaces")
    if !found || err != nil {
        return []any{}
    }

    var interfaceStatuses []any
    
    for i, iface := range interfaces {
        ifaceMap, ok := iface.(map[string]interface{})
        if !ok {
            continue
        }
        
        // Extract interface details
        id := getIntFromMap(ifaceMap, "id")
        macAddress := getStringFromMap(ifaceMap, "macAddress")
        ipAddress := getStringFromMap(ifaceMap, "address")
        cidr := getStringFromMap(ifaceMap, "cidr")
        mtu := getIntFromMap(ifaceMap, "mtu")
        
        // Determine actual interface state
        interfaceName := fmt.Sprintf("multinic%d", i)
        actualState := c.getActualInterfaceState(node, macAddress, interfaceName)
        
        // Build comprehensive interface status (convert int types to int64 for unstructured compatibility)
        interfaceStatus := map[string]any{
            "interfaceIndex": int64(i),
            "id":            id,
            "macAddress":    macAddress,
            "address":       ipAddress,
            "cidr":         cidr,
            "mtu":          int64(mtu),
            "interfaceName": interfaceName,
            "actualState":   actualState,
            "lastChecked":  time.Now().Format(time.RFC3339),
        }
        
        interfaceStatuses = append(interfaceStatuses, interfaceStatus)
    }
    
    return interfaceStatuses
}

// getActualInterfaceState checks the actual state of an interface on the node
func (c *Controller) getActualInterfaceState(node *corev1.Node, macAddress, interfaceName string) string {
    // Check node conditions and capacity for network interface information
    
    // Check if node is ready
    if !c.isNodeReady(node) {
        return "NodeNotReady"
    }
    
    // In a real implementation, you would:
    // 1. Query node metrics or use a custom agent to check interface state
    // 2. Check if the interface exists and is up
    // 3. Verify IP configuration matches expected state
    // 4. Check connectivity or other health metrics
    
    // For now, we'll return a placeholder based on node readiness
    // This should be enhanced to actually check interface state
    
    // Check node addresses to see if we can infer interface state
    for _, addr := range node.Status.Addresses {
        if addr.Type == corev1.NodeInternalIP {
            // If node has internal IP, assume basic networking is working
            return "Configured"
        }
    }
    
    return "Unknown"
}

// isNodeReady checks if the node is in Ready condition
func (c *Controller) isNodeReady(node *corev1.Node) bool {
    for _, condition := range node.Status.Conditions {
        if condition.Type == corev1.NodeReady {
            return condition.Status == corev1.ConditionTrue
        }
    }
    return false
}
