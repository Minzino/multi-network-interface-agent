package services

import (
	"context"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"multinic-agent/internal/infrastructure/metrics"
)

// RoutingCoordinator는 네트워크 라우팅 변경 작업의 전역 직렬화를 담당합니다.
// 여러 인터페이스가 동시에 라우팅 테이블을 수정하려 할 때 발생할 수 있는 충돌을 방지합니다.
type RoutingCoordinator struct {
	mutex  sync.Mutex
	logger *logrus.Logger
}

// NewRoutingCoordinator는 새로운 RoutingCoordinator를 생성합니다
func NewRoutingCoordinator(logger *logrus.Logger) *RoutingCoordinator {
	return &RoutingCoordinator{
		logger: logger,
	}
}

// RoutingOperation은 라우팅 작업을 나타내는 함수 타입입니다
type RoutingOperation func(ctx context.Context) error

// ExecuteWithLock은 라우팅 작업을 전역 뮤텍스 보호 하에서 실행합니다.
// 이는 기본 경로 변경, 라우팅 테이블 수정 등의 작업이 동시에 실행되어
// 네트워크 설정에 충돌이 발생하는 것을 방지합니다.
func (rc *RoutingCoordinator) ExecuteWithLock(ctx context.Context, interfaceName string, operation RoutingOperation) error {
	startTime := time.Now()
	
	rc.logger.WithField("interface", interfaceName).Debug("Acquiring routing lock")
	
	// 전역 라우팅 뮤텍스 획득
	rc.mutex.Lock()
	defer rc.mutex.Unlock()
	
	lockAcquiredTime := time.Now()
	lockWaitDuration := lockAcquiredTime.Sub(startTime)
	
	rc.logger.WithFields(logrus.Fields{
		"interface":         interfaceName,
		"lock_wait_duration": lockWaitDuration.Milliseconds(),
	}).Debug("Routing lock acquired")
	
	// 메트릭 수집: 라우팅 락 대기 시간
	metrics.ObserveRoutingLockWaitDuration(lockWaitDuration.Seconds())
	
	// 라우팅 작업 실행
	err := operation(ctx)
	
	operationDuration := time.Since(lockAcquiredTime)
	
	if err != nil {
		rc.logger.WithFields(logrus.Fields{
			"interface": interfaceName,
			"error":     err.Error(),
			"duration":  operationDuration.Milliseconds(),
		}).Error("Routing operation failed")
		metrics.IncRoutingOperationFailures()
	} else {
		rc.logger.WithFields(logrus.Fields{
			"interface": interfaceName,
			"duration":  operationDuration.Milliseconds(),
		}).Debug("Routing operation completed successfully")
	}
	
	// 메트릭 수집: 라우팅 작업 소요 시간
	metrics.ObserveRoutingOperationDuration(operationDuration.Seconds())
	
	totalDuration := time.Since(startTime)
	rc.logger.WithFields(logrus.Fields{
		"interface":         interfaceName,
		"total_duration":    totalDuration.Milliseconds(),
		"lock_wait":         lockWaitDuration.Milliseconds(),
		"operation":         operationDuration.Milliseconds(),
	}).Debug("Routing lock released")
	
	return err
}

// IsLocked는 현재 라우팅 뮤텍스가 잠겨있는지 확인합니다 (테스트 및 모니터링용)
func (rc *RoutingCoordinator) IsLocked() bool {
	// TryLock을 사용하여 잠금 상태 확인
	if rc.mutex.TryLock() {
		rc.mutex.Unlock()
		return false
	}
	return true
}