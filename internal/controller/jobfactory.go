package controller

import (
    batchv1 "k8s.io/api/batch/v1"
    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/util/intstr"
    "k8s.io/utils/pointer"
    "strings"
)

type OSFamily string

const (
    OSUbuntu OSFamily = "ubuntu"
    OSRHEL   OSFamily = "rhel"
)

// OSFamilyFromOSImage returns a normalized OS family based on Node.status.nodeInfo.osImage
func OSFamilyFromOSImage(osImage string) OSFamily {
    s := strings.ToLower(osImage)
    if strings.Contains(s, "ubuntu") {
        return OSUbuntu
    }
    // Treat any Red Hat family as RHEL
    if strings.Contains(s, "red hat") || strings.Contains(s, "rhel") || strings.Contains(s, "centos") || strings.Contains(s, "rocky") || strings.Contains(s, "alma") || strings.Contains(s, "oracle") {
        return OSRHEL
    }
    // default to RHEL path if unknown (safer for enterprise images using NM)
    return OSRHEL
}

type JobParams struct {
    Namespace           string
    Name                string
    Image               string
    PullPolicy          corev1.PullPolicy
    ServiceAccountName  string
    NodeName            string
    NodeCRNamespace     string
    TTLSecondsAfterDone *int32
    Action              string // "" | "cleanup"
}

// BuildAgentJob builds a Job manifest targeting a specific node with OS-aware mounts.
func BuildAgentJob(osImage string, p JobParams) *batchv1.Job {
    family := OSFamilyFromOSImage(osImage)

    // Volumes and mounts based on OS family
    var volumes []corev1.Volume
    var mounts []corev1.VolumeMount

    if family == OSUbuntu {
        volumes = append(volumes, corev1.Volume{
            Name: "netplan",
            VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
                Path: "/etc/netplan",
                Type: hostPathType(corev1.HostPathDirectoryOrCreate),
            }},
        })
        mounts = append(mounts, corev1.VolumeMount{Name: "netplan", MountPath: "/etc/netplan"})
    } else { // RHEL family
        volumes = append(volumes, corev1.Volume{
            Name: "nm-connections",
            VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
                Path: "/etc/NetworkManager/system-connections",
                Type: hostPathType(corev1.HostPathDirectoryOrCreate),
            }},
        })
        mounts = append(mounts, corev1.VolumeMount{Name: "nm-connections", MountPath: "/etc/NetworkManager/system-connections"})
    }

    backoffLimit := int32(1)

    // derive action label
    action := p.Action
    if strings.TrimSpace(action) == "" {
        action = "apply"
    }

    job := &batchv1.Job{
        ObjectMeta: metav1.ObjectMeta{
            Name:      p.Name,
            Namespace: p.Namespace,
            Labels: map[string]string{
                "app.kubernetes.io/name":       "multinic-agent",
                "app.kubernetes.io/managed-by": "multinic-controller",
                "multinic.io/node-name":        p.NodeName,
                "multinic.io/action":           action,
            },
        },
        Spec: batchv1.JobSpec{
            TTLSecondsAfterFinished: p.TTLSecondsAfterDone,
            BackoffLimit:            &backoffLimit,
            Template: corev1.PodTemplateSpec{
                ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
                    "app.kubernetes.io/name": "multinic-agent",
                }},
                Spec: corev1.PodSpec{
                    ServiceAccountName: p.ServiceAccountName,
                    RestartPolicy:      corev1.RestartPolicyOnFailure,
                    HostNetwork:        true,
                    HostPID:            true,
                    NodeSelector: map[string]string{
                        "kubernetes.io/hostname": p.NodeName,
                    },
                    Containers: []corev1.Container{
                        {
                            Name:            "multinic-agent",
                            Image:           p.Image,
                            ImagePullPolicy: p.PullPolicy,
                            // 종료 전 대기를 강제하기 위해 셸 래퍼로 실행
                            Command:         []string{"./multinic-agent"},
                            Env: []corev1.EnvVar{
                                {Name: "RUN_MODE", Value: "job"},
                                {Name: "DATA_SOURCE", Value: "nodecr"},
                                {Name: "NODE_CR_NAMESPACE", Value: p.NodeCRNamespace},
                                {Name: "NODE_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}}},
                                {Name: "LOG_LEVEL", Value: "info"},
                                {Name: "POLL_INTERVAL", Value: "30s"},
                                // optional action: cleanup
                                {Name: "AGENT_ACTION", Value: p.Action},
                            },
                            Ports: []corev1.ContainerPort{{Name: "health", ContainerPort: 8080}},
                            LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstrFrom(8080)}}, InitialDelaySeconds: 30, PeriodSeconds: 30},
                            ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstrFrom(8080)}}, InitialDelaySeconds: 5, PeriodSeconds: 10},
                            SecurityContext: &corev1.SecurityContext{Privileged: pointer.Bool(true), Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN", "SYS_ADMIN"}}},
                            VolumeMounts:    mounts,
                        },
                    },
                    Volumes: volumes,
                    Tolerations: []corev1.Toleration{
                        {
                            Key:      "node-role.kubernetes.io/control-plane",
                            Operator: corev1.TolerationOpExists,
                            Effect:   corev1.TaintEffectNoSchedule,
                        },
                        {
                            Key:      "node-role.kubernetes.io/master",
                            Operator: corev1.TolerationOpExists,
                            Effect:   corev1.TaintEffectNoSchedule,
                        },
                    },
                },
            },
        },
    }
    return job
}

func hostPathType(t corev1.HostPathType) *corev1.HostPathType { return &t }

// helpers
func intstrFrom(port int32) intstr.IntOrString { return intstr.FromInt(int(port)) }
