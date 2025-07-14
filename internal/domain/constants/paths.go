package constants

// 시스템 경로 상수들
const (
	// Ubuntu/Netplan 관련 경로
	NetplanConfigDir = "/etc/netplan"
	
	// RHEL/CentOS 관련 경로
	RHELNetworkScriptsDir = "/etc/sysconfig/network-scripts"
	NetworkManagerDir     = "/etc/NetworkManager/system-connections"
	
	// OS 감지 관련 경로
	OSReleaseFile = "/host/etc/os-release"
	
	// 백업 디렉토리
	DefaultBackupDir = "/var/lib/multinic/backups"
	
	// 시스템 네트워크 경로
	SysClassNet = "/sys/class/net"
)

// 네트워크 설정 관련 상수들
const (
	// 인터페이스 이름 패턴
	InterfacePrefix = "multinic"
	MaxInterfaces   = 10
	
	// 파일 권한
	ConfigFilePermission = 0644
	
	// 타임아웃
	DefaultCommandTimeout = 30 // seconds
	NetplanTryTimeout     = 120 // seconds
)

// 기본값 상수들
const (
	// 데이터베이스 기본값
	DefaultDBHost = "localhost"
	DefaultDBPort = "3306"
	DefaultDBName = "multinic"
	
	// 에이전트 기본값
	DefaultPollInterval = "30s"
	DefaultLogLevel     = "info"
	DefaultHealthPort   = "8080"
)