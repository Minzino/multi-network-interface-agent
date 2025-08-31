package controller

import (
    "context"
    "crypto/sha256"
    "encoding/json"
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
    
    // Detect spec change using metadata.generation vs status.observedGeneration
    currentState, _, _ := unstructured.NestedString(u.Object, "status", "state")
    specGen := u.GetGeneration()
    observedGen, _, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
    specChanged := observedGen == 0 || specGen != observedGen
    // If already in final state and spec hasn't changed, skip scheduling
    if (currentState == "Configured" || currentState == "Failed") && !specChanged {
        log.Printf("reconcile: CR %s/%s is already in final state '%s' with no spec change, skipping job creation", namespace, name, currentState)
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

    // Use generation-aware job name to avoid collisions with stale jobs
    gen := specGen
    job := BuildAgentJob(osImage, JobParams{
        Namespace:          namespace,
        Name:               fmt.Sprintf("multinic-agent-%s-g%d", nodeName, gen),
        Image:              c.AgentImage,
        PullPolicy:         c.ImagePullPolicy,
        ServiceAccountName: c.ServiceAccount,
        NodeName:           nodeName,
        NodeCRNamespace:    c.NodeCRNamespace,
        TTLSecondsAfterDone: c.JobTTLSeconds,
        Action:             "", // default apply
    })

    // Mark CR as InProgress with interface details and record observedGeneration/spec hash
    reason := "JobScheduled"
    if specChanged { reason = "SpecChanged" }
    interfaceStatuses := c.buildInterfaceStatuses(u, "InProgress", reason)
    _ = c.updateCRStatus(ctx, u, map[string]any{
        "state":              "InProgress",
        "observedGeneration": specGen,
        "observedSpecHash":   computeSpecHash(u),
        "lastJobName":        job.Name,
        "conditions": []any{
            map[string]any{"type": "InProgress", "status": "True", "reason": reason},
        },
        "interfaceStatuses": interfaceStatuses,
        "lastUpdated": time.Now().Format(time.RFC3339),
    })

    // If a job with the same generation-aware name exists, skip creating
    if _, err := c.Client.BatchV1().Jobs(namespace).Get(ctx, job.Name, metav1.GetOptions{}); err == nil {
        log.Printf("job already exists: %s/%s", namespace, job.Name)
        return nil
    }
    if _, err := c.Client.BatchV1().Jobs(namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil {
        log.Printf("create job error: %v", err)
        return err
    }
    log.Printf("job created: %s/%s for node=%s osImage=%s", namespace, job.Name, nodeName, osImage)

    // Proactively delete stale jobs for this node (different name)
    jobs, _ := c.Client.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=multinic-agent,multinic.io/node-name=" + nodeName})
    for i := range jobs.Items {
        if jobs.Items[i].Name != job.Name {
            c.scheduleJobDeletion(ctx, namespace, jobs.Items[i].Name)
        }
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
                action := job.Labels["multinic.io/action"]
                if action == "cleanup" {
                    // Cleanup job succeeded: do not overwrite CR state; just delete the job
                    log.Printf("cleanup job succeeded: %s/%s", namespace, job.Name)
                    c.scheduleJobDeletion(ctx, namespace, job.Name)
                    continue
                }
                log.Printf("job succeeded: %s/%s", namespace, job.Name)
                // 종료 메시지(요약) 있으면 참고용 로그 출력
                if msg := c.getJobTerminationMessage(ctx, namespace, job.Name); strings.TrimSpace(msg) != "" {
                    c.logJobSummary(msg)
                }
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
                // 종료 메시지(요약)에서 실패한 인터페이스 상세를 로그로 남김 및 per-interface 상태 반영
                reason := "JobFailed"
                statuses := map[string]any{}
                if msg := c.getJobTerminationMessage(ctx, namespace, job.Name); strings.TrimSpace(msg) != "" {
                    c.logJobSummary(msg)
                    // Try to parse JSON summary and compute per-interface statuses
                    type failure struct {
                        ID        int    `json:"id"`
                        MAC       string `json:"mac"`
                        Name      string `json:"name"`
                        ErrorType string `json:"errorType"`
                        Reason    string `json:"reason"`
                    }
                    var sum struct { Failures []failure `json:"failures"` }
                    if err := json.Unmarshal([]byte(msg), &sum); err == nil && len(sum.Failures) > 0 {
                        // Build lookup maps by ID and MAC (normalized)
                        failByID := map[int]failure{}
                        failByMAC := map[string]failure{}
                        for _, f := range sum.Failures {
                            failByID[f.ID] = f
                            if strings.TrimSpace(f.MAC) != "" {
                                failByMAC[strings.ToLower(strings.TrimSpace(f.MAC))] = f
                            }
                        }
                        // Enumerate spec interfaces and map by id/MAC (more reliable than name)
                        ifaces, found, _ := unstructured.NestedSlice(u.Object, "spec", "interfaces")
                        if found {
                            for i := range ifaces {
                                ifaceMap, _ := ifaces[i].(map[string]any)
                                name := fmt.Sprintf("multinic%d", i)
                                id := getIntFromMap(ifaceMap, "id")
                                mac := strings.ToLower(getStringFromMap(ifaceMap, "macAddress"))
                                if f, ok := failByID[id]; ok && id != 0 {
                                    statuses[name] = map[string]any{
                                        "interfaceIndex": int64(i),
                                        "id":            int64(f.ID),
                                        "macAddress":    mac,
                                        "status":        "Failed",
                                        "reason":        "JobFailed",
                                        "message":       f.Reason,
                                        "lastUpdated":   time.Now().Format(time.RFC3339),
                                    }
                                } else if f, ok := failByMAC[mac]; ok && mac != "" {
                                    statuses[name] = map[string]any{
                                        "interfaceIndex": int64(i),
                                        "id":            int64(f.ID),
                                        "macAddress":    mac,
                                        "status":        "Failed",
                                        "reason":        "JobFailed",
                                        "message":       f.Reason,
                                        "lastUpdated":   time.Now().Format(time.RFC3339),
                                    }
                                } else {
                                    // Treat others as configured in a partial failure scenario
                                    statuses[name] = map[string]any{
                                        "interfaceIndex": int64(i),
                                        "id":            int64(id),
                                        "macAddress":    mac,
                                        "status":        "Configured",
                                        "reason":        "JobPartialSuccess",
                                        "lastUpdated":   time.Now().Format(time.RFC3339),
                                    }
                                }
                            }
                            if len(sum.Failures) < len(ifaces) { reason = "JobFailedPartial" }
                        }
                    }
                }
                // Fallback: if we couldn't compute per-interface, mark all as Failed
                if len(statuses) == 0 {
                    statuses = c.buildInterfaceStatuses(u, "Failed", reason)
                }
                statusPatch := map[string]any{
                    "state": "Failed",
                    "conditions": []any{ map[string]any{"type": "Ready", "status": "False", "reason": reason} },
                    "lastUpdated": time.Now().Format(time.RFC3339),
                    "interfaceStatuses": statuses,
                }
                _ = c.updateCRStatus(ctx, u, statusPatch)
                // Cleanup failed job as well (optionally delay)
                c.scheduleJobDeletion(ctx, namespace, job.Name)
            }
        }
    }
    return nil
}

// getJobTerminationMessage는 Job의 Pod 종료 메시지를 반환합니다 (컨테이너 termination log)
func (c *Controller) getJobTerminationMessage(ctx context.Context, namespace, jobName string) string {
    pods, err := c.Client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + jobName})
    if err != nil || len(pods.Items) == 0 {
        return ""
    }
    // Terminated 컨테이너가 있는 최신 Pod의 종료 메시지를 반환
    var latestMsg string
    var latest time.Time
    for i := range pods.Items {
        p := &pods.Items[i]
        for _, cs := range p.Status.ContainerStatuses {
            if cs.State.Terminated != nil {
                t := cs.State.Terminated.FinishedAt.Time
                if latestMsg == "" || t.After(latest) {
                    latest = t
                    latestMsg = cs.State.Terminated.Message
                }
            }
        }
    }
    return latestMsg
}

// logJobSummary는 종료 메시지(JSON)를 파싱하여 실패 인터페이스를 로그로 출력합니다
func (c *Controller) logJobSummary(msg string) {
    type failure struct {
        ID        int    `json:"id"`
        MAC       string `json:"mac"`
        Name      string `json:"name"`
        ErrorType string `json:"errorType"`
        Reason    string `json:"reason"`
    }
    var sum struct {
        Node      string    `json:"node"`
        Processed int       `json:"processed"`
        Failed    int       `json:"failed"`
        Total     int       `json:"total"`
        Failures  []failure `json:"failures"`
        Timestamp string    `json:"timestamp"`
    }
    if err := json.Unmarshal([]byte(msg), &sum); err != nil {
        // 메시지가 JSON이 아니면 원문만 기록
        log.Printf("job termination message: %s", msg)
        return
    }
    log.Printf("job summary: node=%s processed=%d failed=%d total=%d at=%s", sum.Node, sum.Processed, sum.Failed, sum.Total, sum.Timestamp)
    for _, f := range sum.Failures {
        log.Printf("failed interface: id=%d mac=%s name=%s type=%s reason=%s", f.ID, f.MAC, f.Name, f.ErrorType, f.Reason)
    }
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

// ApplyTerminationSummary parses a termination summary JSON and updates CR per-interface status
func (c *Controller) ApplyTerminationSummary(ctx context.Context, namespace, nodeName, jobName, msg string) error {
    u, err := c.Dyn.Resource(nodeCRGVR).Namespace(namespace).Get(ctx, nodeName, metav1.GetOptions{})
    if err != nil { return err }
    // Parse failures
    type failure struct { ID int `json:"id"`; MAC, Name, ErrorType, Reason string }
    var sum struct { Failures []failure `json:"failures"` }
    if err := json.Unmarshal([]byte(msg), &sum); err != nil { return nil }
    // Build maps by ID/MAC
    failByID := map[int]failure{}
    failByMAC := map[string]failure{}
    for _, f := range sum.Failures { failByID[f.ID] = f; if strings.TrimSpace(f.MAC) != "" { failByMAC[strings.ToLower(strings.TrimSpace(f.MAC))] = f } }
    // Compute per-interface statuses
    statuses := map[string]any{}
    ifaces, found, _ := unstructured.NestedSlice(u.Object, "spec", "interfaces")
    if found {
        for i := range ifaces {
            ifaceMap, _ := ifaces[i].(map[string]any)
            name := fmt.Sprintf("multinic%d", i)
            id := getIntFromMap(ifaceMap, "id")
            mac := strings.ToLower(getStringFromMap(ifaceMap, "macAddress"))
            if f, ok := failByID[id]; ok && id != 0 {
                statuses[name] = map[string]any{"interfaceIndex": int64(i), "id": int64(f.ID), "macAddress": mac, "status": "Failed", "reason": "JobFailed", "message": f.Reason, "lastUpdated": time.Now().Format(time.RFC3339)}
            } else if f, ok := failByMAC[mac]; ok && mac != "" {
                statuses[name] = map[string]any{"interfaceIndex": int64(i), "id": int64(f.ID), "macAddress": mac, "status": "Failed", "reason": "JobFailed", "message": f.Reason, "lastUpdated": time.Now().Format(time.RFC3339)}
            } else {
                statuses[name] = map[string]any{"interfaceIndex": int64(i), "id": int64(id), "macAddress": mac, "status": "Configured", "reason": "JobPartialSuccess", "lastUpdated": time.Now().Format(time.RFC3339)}
            }
        }
    }
    reason := "JobFailed"; if len(sum.Failures) < len(ifaces) { reason = "JobFailedPartial" }
    patch := map[string]any{ "state": "Failed", "conditions": []any{ map[string]any{"type": "Ready", "status": "False", "reason": reason} }, "interfaceStatuses": statuses, "lastUpdated": time.Now().Format(time.RFC3339), "lastJobName": jobName }
    return c.updateCRStatus(ctx, u, patch)
}

// computeSpecHash creates a SHA256 hash of the CR .spec for change tracking
func computeSpecHash(u *unstructured.Unstructured) string {
    spec, found, _ := unstructured.NestedMap(u.Object, "spec")
    if !found || spec == nil {
        return ""
    }
    b, err := json.Marshal(spec)
    if err != nil {
        return ""
    }
    sum := sha256.Sum256(b)
    return fmt.Sprintf("%x", sum)
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
// Returns a map where keys are interface names (multinic0, multinic1, etc.)
func (c *Controller) buildInterfaceStatuses(u *unstructured.Unstructured, status, reason string) map[string]any {
    interfaces, found, err := unstructured.NestedSlice(u.Object, "spec", "interfaces")
    if !found || err != nil {
        log.Printf("No interfaces found when building status for CR %s/%s", u.GetNamespace(), u.GetName())
        return map[string]any{}
    }

    interfaceStatuses := make(map[string]any)
    
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
        
        // Generate interface name based on index (multinic0, multinic1, etc.)
        interfaceName := fmt.Sprintf("multinic%d", i)
        
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
        
        // Use interface name as key in the map
        interfaceStatuses[interfaceName] = interfaceStatus
        
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
// Returns a map where keys are interface names (multinic0, multinic1, etc.)
func (c *Controller) buildEnhancedInterfaceStatuses(u *unstructured.Unstructured, node *corev1.Node) map[string]any {
    interfaces, found, err := unstructured.NestedSlice(u.Object, "spec", "interfaces")
    if !found || err != nil {
        return map[string]any{}
    }

    interfaceStatuses := make(map[string]any)
    
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
        
        // Generate interface name based on index
        interfaceName := fmt.Sprintf("multinic%d", i)
        actualState := c.getActualInterfaceState(node, macAddress, interfaceName)
        
        // Build comprehensive interface status (convert int types to int64 for unstructured compatibility)
        interfaceStatus := map[string]any{
            "interfaceIndex": int64(i),
            "id":            int64(id),
            "macAddress":    macAddress,
            "address":       ipAddress,
            "cidr":         cidr,
            "mtu":          int64(mtu),
            "actualState":   actualState,
            "lastChecked":  time.Now().Format(time.RFC3339),
        }
        
        // Use interface name as key in the map
        interfaceStatuses[interfaceName] = interfaceStatus
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
