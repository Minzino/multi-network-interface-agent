package constants

// 시스템 경로 상수들
const (
	// Ubuntu/Netplan 관련 경로
	NetplanConfigDir = "/etc/netplan"

	// RHEL/CentOS 관련 경로
	RHELNetworkScriptsDir = "/etc/sysconfig/network-scripts"
	NetworkManagerDir     = "/etc/NetworkManager/system-connections"

	// systemd-udev 링크 이름 매핑(.link) 경로 (영구 인터페이스 네이밍)
	SystemdNetworkDir = "/etc/systemd/network"

	// OS 감지 관련 경로
	OSReleaseFile = "/host/etc/os-release"

	// 백업 디렉토리
	DefaultBackupDir = "/var/lib/multinic/backups"

	// 시스템 네트워크 경로
	SysClassNet = "/sys/class/net"

	// Kubernetes 관련 경로
	KubernetesTerminationLogPath = "/dev/termination-log"
)

// 네트워크 설정 관련 상수들
const (
	// 인터페이스 이름 패턴
	InterfacePrefix = "multinic"
	MaxInterfaces   = 10

	// 파일 권한
	ConfigFilePermission = 0644

	// 타임아웃 (초)
	DefaultCommandTimeout = 30  // seconds
	NetplanTryTimeout     = 120 // seconds
	DefaultPollInterval   = 30  // seconds  
	DefaultRetryDelay     = 2   // seconds

	// 재시도 설정
	DefaultMaxRetries        = 3
	DefaultMaxConcurrentTasks = 5
	DefaultBackoffMultiplier  = 2.0

	// 네임스페이스
	DefaultNodeCRNamespace = "multinic-system"
)

// 기본값 상수들
const (
	// 데이터베이스 기본값
	DefaultDBHost = "localhost"
	DefaultDBPort = "3306"
	DefaultDBName = "multinic"

	// 에이전트 기본값
	DefaultPollIntervalStr = "30s"
	DefaultLogLevel        = "info"
	DefaultHealthPort      = "8080"
)
