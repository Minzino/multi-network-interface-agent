package controller

import (
    "context"
    "testing"
    "time"

    batchv1 "k8s.io/api/batch/v1"
    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/apimachinery/pkg/runtime"
    dynamicfake "k8s.io/client-go/dynamic/fake"
    k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestService_RunOnce_CreatesJobAndUpdatesStatus(t *testing.T) {
    scheme := runtime.NewScheme()
    ns := "multinic-system"
    dyn := dynamicfake.NewSimpleDynamicClient(scheme, makeNodeCR(ns, "worker-node-01", "worker-node-01", "uuid-1"))
    kclient := k8sfake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-node-01"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{OSImage: "Ubuntu 22.04.4 LTS", SystemUUID: "uuid-1"}}})

    c := &Controller{Dyn: dyn, Client: kclient, AgentImage: "multinic-agent:dev", ImagePullPolicy: corev1.PullIfNotPresent, ServiceAccount: "sa", NodeCRNamespace: ns}
    s := &Service{Controller: c, Namespace: ns, Interval: 10 * time.Second}

    // First run creates job and marks InProgress
    if err := s.RunOnce(context.Background()); err != nil { t.Fatalf("runonce error: %v", err) }
    if _, err := kclient.BatchV1().Jobs(ns).Get(context.Background(), "multinic-agent-worker-node-01-g0", metav1.GetOptions{}); err != nil {
        t.Fatalf("expected job created: %v", err)
    }
    got, err := dyn.Resource(nodeCRGVR).Namespace(ns).Get(context.Background(), "worker-node-01", metav1.GetOptions{})
    if err != nil { t.Fatalf("get cr error: %v", err) }
    state, _, _ := unstructured.NestedString(got.Object, "status", "state")
    if state != "InProgress" { t.Fatalf("expected InProgress, got %q", state) }

    // Simulate job success and second run updates status to Configured
    _, _ = kclient.BatchV1().Jobs(ns).UpdateStatus(context.Background(), &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "multinic-agent-worker-node-01", Namespace: ns, Labels: map[string]string{"app.kubernetes.io/name": "multinic-agent", "multinic.io/node-name": "worker-node-01"}}, Status: batchv1.JobStatus{Succeeded: 1}}, metav1.UpdateOptions{})
    if err := s.RunOnce(context.Background()); err != nil { t.Fatalf("runonce error 2: %v", err) }
    got, _ = dyn.Resource(nodeCRGVR).Namespace(ns).Get(context.Background(), "worker-node-01", metav1.GetOptions{})
    state, _, _ = unstructured.NestedString(got.Object, "status", "state")
    if state != "Configured" { t.Fatalf("expected Configured, got %q", state) }
}
