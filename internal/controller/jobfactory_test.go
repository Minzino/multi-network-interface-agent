package controller

import (
    batchv1 "k8s.io/api/batch/v1"
    corev1 "k8s.io/api/core/v1"
    "testing"
)

func TestOSFamilyFromOSImage(t *testing.T) {
    if got := OSFamilyFromOSImage("Ubuntu 22.04.4 LTS"); got != OSUbuntu {
        t.Fatalf("expected ubuntu, got %s", got)
    }
    if got := OSFamilyFromOSImage("Red Hat Enterprise Linux 9.4 (Plow)"); got != OSRHEL {
        t.Fatalf("expected rhel, got %s", got)
    }
}

func TestBuildAgentJob_Ubuntu_MountsNetplanOnly(t *testing.T) {
    job := BuildAgentJob("Ubuntu 22.04.4 LTS", JobParams{Namespace: "multinic-system", Name: "test", Image: "multinic-agent:dev", PullPolicy: corev1.PullIfNotPresent, ServiceAccountName: "sa", NodeName: "node-1", NodeCRNamespace: "multinic-system"})
    assertJobBasics(t, job)
    mounts := job.Spec.Template.Spec.Containers[0].VolumeMounts
    vols := job.Spec.Template.Spec.Volumes
    if len(mounts) != 1 || mounts[0].MountPath != "/etc/netplan" {
        t.Fatalf("expected one netplan mount; got %#v", mounts)
    }
    if len(vols) != 1 || vols[0].HostPath == nil || vols[0].HostPath.Path != "/etc/netplan" {
        t.Fatalf("expected one netplan volume; got %#v", vols)
    }
    // tolerations for master/control-plane/infra
    tols := job.Spec.Template.Spec.Tolerations
    if len(tols) < 3 {
        t.Fatalf("expected tolerations for control-plane/master/infra taints")
    }
    // RUN_MODE=job env present
    foundRunMode := false
    for _, e := range job.Spec.Template.Spec.Containers[0].Env {
        if e.Name == "RUN_MODE" && e.Value == "job" { foundRunMode = true }
    }
    if !foundRunMode { t.Fatalf("expected RUN_MODE=job env") }
}

func TestBuildAgentJob_RHEL_MountsNMOnly(t *testing.T) {
    job := BuildAgentJob("Red Hat Enterprise Linux 9.4 (Plow)", JobParams{Namespace: "multinic-system", Name: "test", Image: "multinic-agent:dev", PullPolicy: corev1.PullIfNotPresent, ServiceAccountName: "sa", NodeName: "node-1", NodeCRNamespace: "multinic-system"})
    assertJobBasics(t, job)
    mounts := job.Spec.Template.Spec.Containers[0].VolumeMounts
    vols := job.Spec.Template.Spec.Volumes
    if len(mounts) != 1 || mounts[0].MountPath != "/etc/NetworkManager/system-connections" {
        t.Fatalf("expected one nm-connections mount; got %#v", mounts)
    }
    if len(vols) != 1 || vols[0].HostPath == nil || vols[0].HostPath.Path != "/etc/NetworkManager/system-connections" {
        t.Fatalf("expected one nm-connections volume; got %#v", vols)
    }
}

func assertJobBasics(t *testing.T, job *batchv1.Job) {
    t.Helper()
    if job.Spec.Template.Spec.HostNetwork != true || job.Spec.Template.Spec.HostPID != true {
        t.Fatalf("expected HostNetwork/HostPID true")
    }
    if job.Spec.Template.Spec.Containers[0].Name != "multinic-agent" {
        t.Fatalf("expected container multinic-agent")
    }
    env := job.Spec.Template.Spec.Containers[0].Env
    checkEnv := func(name string) bool {
        for _, e := range env { if e.Name == name { return true } }
        return false
    }
    if !checkEnv("DATA_SOURCE") || !checkEnv("NODE_CR_NAMESPACE") || !checkEnv("NODE_NAME") {
        t.Fatalf("expected required env vars present, got %#v", env)
    }
    if job.Spec.Template.Spec.NodeSelector["kubernetes.io/hostname"] == "" {
        t.Fatalf("expected nodeSelector set")
    }
}
