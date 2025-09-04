package controller

import (
    "context"
    "testing"

    batchv1 "k8s.io/api/batch/v1"
    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/apimachinery/pkg/runtime"
    "k8s.io/apimachinery/pkg/runtime/schema"
    dynamicfake "k8s.io/client-go/dynamic/fake"
    k8sfake "k8s.io/client-go/kubernetes/fake"
)

func makeNodeCR(ns, name, nodeName, instanceID string) *unstructured.Unstructured {
    u := &unstructured.Unstructured{
        Object: map[string]interface{}{
            "apiVersion": "multinic.io/v1alpha1",
            "kind":       "MultiNicNodeConfig",
            "metadata": map[string]interface{}{
                "name":      name,
                "namespace": ns,
                "labels": map[string]interface{}{
                    "multinic.io/instance-id": instanceID,
                },
            },
            "spec": map[string]interface{}{
                "nodeName": nodeName,
                "interfaces": []interface{}{
                    map[string]interface{}{"id": int64(1), "macAddress": "02:00:00:00:01:01"},
                },
            },
        },
    }
    u.SetGroupVersionKind(schema.GroupVersionKind{Group: "multinic.io", Version: "v1alpha1", Kind: "MultiNicNodeConfig"})
    return u
}

func TestReconcile_CreatesJobWithOSAwareMounts_RHEL(t *testing.T) {
    scheme := runtime.NewScheme()
    dyn := dynamicfake.NewSimpleDynamicClient(scheme, makeNodeCR("multinic-system", "worker-node-01", "worker-node-01", "6d4a3c2a-f1c4-414b-bedd-4938b4924f53"))
    kclient := k8sfake.NewSimpleClientset(
        &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-node-01"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{OSImage: "Red Hat Enterprise Linux 9.4 (Plow)", SystemUUID: "6d4a3c2a-f1c4-414b-bedd-4938b4924f53"}}},
    )

    c := &Controller{Dyn: dyn, Client: kclient, AgentImage: "multinic-agent:dev", ImagePullPolicy: corev1.PullIfNotPresent, ServiceAccount: "sa", NodeCRNamespace: "multinic-system"}

    err := c.Reconcile(context.Background(), "multinic-system", "worker-node-01")
    if err != nil { t.Fatalf("reconcile error: %v", err) }

    job, err := kclient.BatchV1().Jobs("multinic-system").Get(context.Background(), "multinic-agent-worker-node-01-g0", metav1.GetOptions{})
    if err != nil { t.Fatalf("job not found: %v", err) }

    assertRHELJob(t, job)
}

func TestProcessJobs_UpdatesCRStatus_OnSuccess(t *testing.T) {
    scheme := runtime.NewScheme()
    cr := makeNodeCR("multinic-system", "worker-node-01", "worker-node-01", "6d4a3c2a-f1c4-414b-bedd-4938b4924f53")
    dyn := dynamicfake.NewSimpleDynamicClient(scheme, cr)
    kclient := k8sfake.NewSimpleClientset(
        &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-node-01"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{OSImage: "Red Hat Enterprise Linux 9.4 (Plow)", SystemUUID: "6d4a3c2a-f1c4-414b-bedd-4938b4924f53"}}},
        &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "multinic-agent-worker-node-01", Namespace: "multinic-system", Labels: map[string]string{"app.kubernetes.io/name": "multinic-agent", "multinic.io/node-name": "worker-node-01"}}, Status: batchv1.JobStatus{Succeeded: 1}},
    )

    c := &Controller{Dyn: dyn, Client: kclient, AgentImage: "multinic-agent:dev", ImagePullPolicy: corev1.PullIfNotPresent, ServiceAccount: "sa", NodeCRNamespace: "multinic-system"}

    if err := c.ProcessJobs(context.Background(), "multinic-system"); err != nil { t.Fatalf("process jobs error: %v", err) }

    got, err := dyn.Resource(nodeCRGVR).Namespace("multinic-system").Get(context.Background(), "worker-node-01", metav1.GetOptions{})
    if err != nil { t.Fatalf("get cr error: %v", err) }
    state, _, _ := unstructured.NestedString(got.Object, "status", "state")
    if state != "Configured" { t.Fatalf("expected status.state=Configured, got %q", state) }
}

func assertRHELJob(t *testing.T, job *batchv1.Job) {
    t.Helper()
    mounts := job.Spec.Template.Spec.Containers[0].VolumeMounts
    found := false
    for _, m := range mounts {
        if m.MountPath == "/etc/NetworkManager/system-connections" { found = true; break }
    }
    if !found { t.Fatalf("expected /etc/NetworkManager/system-connections mount, got %#v", mounts) }
}
