package constants

// RunMode는 에이전트 실행 모드를 나타냅니다
type RunMode string

const (
	RunModeService RunMode = "service"
	RunModeJob     RunMode = "job"
)

// String은 RunMode의 문자열 표현을 반환합니다
func (r RunMode) String() string {
	return string(r)
}

// IsValid는 RunMode가 유효한지 확인합니다
func (r RunMode) IsValid() bool {
	switch r {
	case RunModeService, RunModeJob:
		return true
	default:
		return false
	}
}

// AgentAction은 에이전트의 특별한 액션을 나타냅니다
type AgentAction string

const (
	AgentActionCleanup   AgentAction = "cleanup"
	AgentActionConfigure AgentAction = "configure"
)

// String은 AgentAction의 문자열 표현을 반환합니다
func (a AgentAction) String() string {
	return string(a)
}

// IsValid는 AgentAction이 유효한지 확인합니다
func (a AgentAction) IsValid() bool {
	switch a {
	case AgentActionCleanup, AgentActionConfigure:
		return true
	default:
		return false
	}
}

// OSType은 운영체제 타입을 나타냅니다
type OSType string

const (
	OSTypeUbuntu OSType = "ubuntu"
	OSTypeRHEL   OSType = "rhel"
	OSTypeCentOS OSType = "centos"
	OSTypeDebian OSType = "debian"
)

// String은 OSType의 문자열 표현을 반환합니다
func (o OSType) String() string {
	return string(o)
}

// IsValid는 OSType이 유효한지 확인합니다
func (o OSType) IsValid() bool {
	switch o {
	case OSTypeUbuntu, OSTypeRHEL, OSTypeCentOS, OSTypeDebian:
		return true
	default:
		return false
	}
}

// UsesNetplan은 해당 OS가 Netplan을 사용하는지 반환합니다
func (o OSType) UsesNetplan() bool {
	switch o {
	case OSTypeUbuntu, OSTypeDebian:
		return true
	default:
		return false
	}
}

// KubernetesLabel은 쿠버네티스 라벨을 나타냅니다
type KubernetesLabel string

const (
	KubernetesLabelName        KubernetesLabel = "app.kubernetes.io/name"
	KubernetesLabelComponent   KubernetesLabel = "app.kubernetes.io/component"
	KubernetesLabelInstance    KubernetesLabel = "app.kubernetes.io/instance"
	KubernetesLabelVersion     KubernetesLabel = "app.kubernetes.io/version"
	KubernetesLabelManagedBy   KubernetesLabel = "app.kubernetes.io/managed-by"
	MultiNicLabelNodeName      KubernetesLabel = "multinic.io/node-name"
	MultiNicLabelInstanceID    KubernetesLabel = "multinic.io/instance-id"
)

// String은 KubernetesLabel의 문자열 표현을 반환합니다
func (k KubernetesLabel) String() string {
	return string(k)
}