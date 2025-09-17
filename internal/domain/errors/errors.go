package errors

import (
	"errors"
	"fmt"
	"runtime"
	"time"
)

// ErrorType은 에러의 종류를 나타냅니다
type ErrorType string

const (
	// ErrorTypeValidation은 유효성 검증 실패를 나타냅니다
	ErrorTypeValidation ErrorType = "VALIDATION"

	// ErrorTypeNotFound는 리소스를 찾을 수 없음을 나타냅니다
	ErrorTypeNotFound ErrorType = "NOT_FOUND"

	// ErrorTypeConflict는 충돌이 발생했음을 나타냅니다
	ErrorTypeConflict ErrorType = "CONFLICT"

	// ErrorTypeSystem은 시스템 레벨 에러를 나타냅니다
	ErrorTypeSystem ErrorType = "SYSTEM"

	// ErrorTypeNetwork는 네트워크 관련 에러를 나타냅니다
	ErrorTypeNetwork ErrorType = "NETWORK"

	// ErrorTypeTimeout은 타임아웃 에러를 나타냅니다
	ErrorTypeTimeout ErrorType = "TIMEOUT"

	// ErrorTypeConfiguration은 설정 관련 에러를 나타냅니다
	ErrorTypeConfiguration ErrorType = "CONFIGURATION"

	// ErrorTypePermission은 권한 관련 에러를 나타냅니다
	ErrorTypePermission ErrorType = "PERMISSION"

	// ErrorTypeResource은 리소스 부족 에러를 나타냅니다
	ErrorTypeResource ErrorType = "RESOURCE"
)

// ErrorContext는 에러에 대한 추가 컨텍스트 정보를 제공합니다
type ErrorContext struct {
	// 기본 정보
	Timestamp time.Time              `json:"timestamp"`
	Operation string                 `json:"operation,omitempty"`
	Component string                 `json:"component,omitempty"`
	
	// 기술적 정보
	StackTrace string                `json:"stack_trace,omitempty"`
	File       string                `json:"file,omitempty"`
	Line       int                   `json:"line,omitempty"`
	
	// 추가 메타데이터
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// DomainError는 도메인 레벨의 에러를 나타냅니다
type DomainError struct {
	Type     ErrorType     `json:"type"`
	Code     string        `json:"code,omitempty"`     // 에러 코드 (예: "NET001", "VAL002")
	Message  string        `json:"message"`
	Cause    error         `json:"-"`                  // JSON에서 제외
	Context  *ErrorContext `json:"context,omitempty"`
	
	// 추가 필드들
	Retryable bool                   `json:"retryable"`
	Details   map[string]interface{} `json:"details,omitempty"`
}

// Error는 error 인터페이스를 구현합니다
func (e *DomainError) Error() string {
	var errorMsg string
	if e.Code != "" {
		errorMsg = fmt.Sprintf("[%s:%s] %s", e.Type, e.Code, e.Message)
	} else {
		errorMsg = fmt.Sprintf("[%s] %s", e.Type, e.Message)
	}
	
	if e.Cause != nil {
		errorMsg += fmt.Sprintf(": %v", e.Cause)
	}
	
	return errorMsg
}

// WithContext는 에러에 컨텍스트 정보를 추가합니다
func (e *DomainError) WithContext(operation, component string) *DomainError {
	if e.Context == nil {
		e.Context = &ErrorContext{}
	}
	e.Context.Operation = operation
	e.Context.Component = component
	e.Context.Timestamp = time.Now()
	
	// 스택 트레이스 정보 추가
	if _, file, line, ok := runtime.Caller(1); ok {
		e.Context.File = file
		e.Context.Line = line
	}
	
	return e
}

// WithDetails는 에러에 세부 정보를 추가합니다
func (e *DomainError) WithDetails(details map[string]interface{}) *DomainError {
	if e.Details == nil {
		e.Details = make(map[string]interface{})
	}
	for k, v := range details {
		e.Details[k] = v
	}
	return e
}

// WithRetryable은 에러의 재시도 가능 여부를 설정합니다
func (e *DomainError) WithRetryable(retryable bool) *DomainError {
	e.Retryable = retryable
	return e
}

// Unwrap은 내부 에러를 반환합니다
func (e *DomainError) Unwrap() error {
	return e.Cause
}

// Is는 에러 비교를 위한 메서드입니다
func (e *DomainError) Is(target error) bool {
	t, ok := target.(*DomainError)
	if !ok {
		return false
	}
	return e.Type == t.Type
}

// 생성자 함수들

// NewError는 기본 도메인 에러를 생성합니다
func NewError(errType ErrorType, code, message string, cause error) *DomainError {
	return &DomainError{
		Type:    errType,
		Code:    code,
		Message: message,
		Cause:   cause,
		Context: &ErrorContext{
			Timestamp: time.Now(),
		},
		Retryable: false,
	}
}

// NewValidationError는 유효성 검증 에러를 생성합니다
func NewValidationError(message string, cause error) *DomainError {
	return NewError(ErrorTypeValidation, "VAL001", message, cause)
}

// NewValidationErrorWithCode는 코드를 포함한 유효성 검증 에러를 생성합니다
func NewValidationErrorWithCode(code, message string, cause error) *DomainError {
	return NewError(ErrorTypeValidation, code, message, cause)
}

// NewNotFoundError는 리소스를 찾을 수 없는 에러를 생성합니다
func NewNotFoundError(message string) *DomainError {
	return NewError(ErrorTypeNotFound, "NOT001", message, nil)
}

// NewConflictError는 충돌 에러를 생성합니다
func NewConflictError(message string) *DomainError {
	return NewError(ErrorTypeConflict, "CON001", message, nil)
}

// NewSystemError는 시스템 에러를 생성합니다
func NewSystemError(message string, cause error) *DomainError {
	return NewError(ErrorTypeSystem, "SYS001", message, cause).WithRetryable(true)
}

// NewNetworkError는 네트워크 관련 에러를 생성합니다
func NewNetworkError(message string, cause error) *DomainError {
	return NewError(ErrorTypeNetwork, "NET001", message, cause).WithRetryable(true)
}

// NewTimeoutError는 타임아웃 에러를 생성합니다
func NewTimeoutError(message string) *DomainError {
	return NewError(ErrorTypeTimeout, "TIM001", message, nil).WithRetryable(true)
}

// NewConfigurationError는 설정 관련 에러를 생성합니다
func NewConfigurationError(message string, cause error) *DomainError {
	return NewError(ErrorTypeConfiguration, "CFG001", message, cause)
}

// NewPermissionError는 권한 관련 에러를 생성합니다
func NewPermissionError(message string, cause error) *DomainError {
	return NewError(ErrorTypePermission, "PER001", message, cause)
}

// NewResourceError는 리소스 부족 에러를 생성합니다
func NewResourceError(message string, cause error) *DomainError {
	return NewError(ErrorTypeResource, "RES001", message, cause).WithRetryable(true)
}

// 에러 타입 확인 헬퍼 함수들

// IsValidationError는 유효성 검증 에러인지 확인합니다
func IsValidationError(err error) bool {
	var domainErr *DomainError
	if errors.As(err, &domainErr) {
		return domainErr.Type == ErrorTypeValidation
	}
	return false
}

// IsNotFoundError는 리소스를 찾을 수 없는 에러인지 확인합니다
func IsNotFoundError(err error) bool {
	var domainErr *DomainError
	if errors.As(err, &domainErr) {
		return domainErr.Type == ErrorTypeNotFound
	}
	return false
}

// IsSystemError는 시스템 에러인지 확인합니다
func IsSystemError(err error) bool {
	var domainErr *DomainError
	if errors.As(err, &domainErr) {
		return domainErr.Type == ErrorTypeSystem
	}
	return false
}

// IsNetworkError는 네트워크 에러인지 확인합니다
func IsNetworkError(err error) bool {
	var domainErr *DomainError
	if errors.As(err, &domainErr) {
		return domainErr.Type == ErrorTypeNetwork
	}
	return false
}

// IsTimeoutError는 타임아웃 에러인지 확인합니다
func IsTimeoutError(err error) bool {
	var domainErr *DomainError
	if errors.As(err, &domainErr) {
		return domainErr.Type == ErrorTypeTimeout
	}
	return false
}
