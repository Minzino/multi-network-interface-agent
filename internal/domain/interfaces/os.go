package interfaces

import (
	"context"
	"os"
	"time"
)

// CommandExecutor는 시스템 명령을 실행하는 인터페이스입니다
type CommandExecutor interface {
	// Execute는 명령을 실행하고 결과를 반환합니다
	Execute(ctx context.Context, command string, args ...string) ([]byte, error)

	// ExecuteWithTimeout은 타임아웃을 적용하여 명령을 실행합니다
	ExecuteWithTimeout(ctx context.Context, timeout time.Duration, command string, args ...string) ([]byte, error)
}

// FileSystem은 파일 시스템 작업을 추상화하는 인터페이스입니다
type FileSystem interface {
	// ReadFile은 파일을 읽습니다
	ReadFile(path string) ([]byte, error)

	// WriteFile은 파일에 데이터를 씁니다
	WriteFile(path string, data []byte, perm os.FileMode) error

	// Exists는 파일이나 디렉토리가 존재하는지 확인합니다
	Exists(path string) bool

	// MkdirAll은 디렉토리를 재귀적으로 생성합니다
	MkdirAll(path string, perm os.FileMode) error

	// Remove는 파일이나 디렉토리를 삭제합니다
	Remove(path string) error

	// ListFiles는 디렉토리의 파일 목록을 반환합니다
	ListFiles(path string) ([]string, error)
}

// Clock은 시간 관련 작업을 추상화하는 인터페이스입니다
type Clock interface {
	// Now는 현재 시간을 반환합니다
	Now() time.Time
}

// OSDetector는 운영체제를 감지하는 인터페이스입니다
type OSDetector interface {
	// DetectOS는 현재 운영체제 타입을 반환합니다
	DetectOS() (OSType, error)
}

// OSType은 운영체제 타입을 나타냅니다
type OSType string

const (
	OSTypeUbuntu OSType = "ubuntu"
	OSTypeRHEL   OSType = "rhel"
)
