package adapters

import (
	"multinic-agent/internal/domain/interfaces"
	"time"
)

// RealClock은 실제 시스템 시간을 사용하는 Clock 구현체입니다
type RealClock struct{}

// NewRealClock은 새로운 RealClock을 생성합니다
func NewRealClock() interfaces.Clock {
	return &RealClock{}
}

// Now는 현재 시간을 반환합니다
func (c *RealClock) Now() time.Time {
	return time.Now()
}
