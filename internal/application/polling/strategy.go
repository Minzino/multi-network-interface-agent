package polling

import (
	"context"
	"math"
	"multinic-agent/internal/infrastructure/metrics"
	"time"

	"github.com/sirupsen/logrus"
)

// Strategy는 폴링 전략 인터페이스입니다
type Strategy interface {
	// NextInterval은 다음 폴링까지의 대기 시간을 반환합니다
	NextInterval(success bool) time.Duration
	// Reset은 폴링 전략을 초기 상태로 리셋합니다
	Reset()
}

// ExponentialBackoffStrategy는 지수 백오프를 구현하는 폴링 전략입니다
type ExponentialBackoffStrategy struct {
	baseInterval   time.Duration
	maxInterval    time.Duration
	multiplier     float64
	currentBackoff int
	logger         *logrus.Logger
}

// NewExponentialBackoffStrategy는 새로운 지수 백오프 전략을 생성합니다
func NewExponentialBackoffStrategy(
	baseInterval time.Duration,
	maxInterval time.Duration,
	multiplier float64,
	logger *logrus.Logger,
) *ExponentialBackoffStrategy {
	if multiplier <= 1 {
		multiplier = 2.0
	}
	
	return &ExponentialBackoffStrategy{
		baseInterval:   baseInterval,
		maxInterval:    maxInterval,
		multiplier:     multiplier,
		currentBackoff: 0,
		logger:         logger,
	}
}

// NextInterval은 다음 폴링까지의 대기 시간을 계산합니다
func (s *ExponentialBackoffStrategy) NextInterval(success bool) time.Duration {
	if success {
		// 성공하면 백오프 리셋
		if s.currentBackoff > 0 {
			s.logger.Debug("Resetting backoff after success")
			s.currentBackoff = 0
			metrics.SetBackoffLevel(0)
		}
		return s.baseInterval
	}

	// 실패 시 백오프 증가
	s.currentBackoff++
	metrics.SetBackoffLevel(float64(s.currentBackoff))
	
	// 지수 백오프 계산
	backoffDuration := float64(s.baseInterval) * math.Pow(s.multiplier, float64(s.currentBackoff-1))
	nextInterval := time.Duration(backoffDuration)
	
	// 최대 간격 제한
	if nextInterval > s.maxInterval {
		nextInterval = s.maxInterval
	}
	
	s.logger.WithFields(logrus.Fields{
		"backoff_count": s.currentBackoff,
		"next_interval": nextInterval,
		"max_interval":  s.maxInterval,
	}).Debug("Exponential backoff calculated")
	
	return nextInterval
}

// Reset은 백오프 카운터를 리셋합니다
func (s *ExponentialBackoffStrategy) Reset() {
	s.currentBackoff = 0
	metrics.SetBackoffLevel(0)
}

// AdaptiveStrategy는 작업량에 따라 동적으로 폴링 간격을 조정하는 전략입니다
type AdaptiveStrategy struct {
	minInterval        time.Duration
	maxInterval        time.Duration
	idleInterval       time.Duration
	workDetectedCount  int
	noWorkCount        int
	thresholdForSlow   int
	thresholdForFast   int
	currentInterval    time.Duration
	logger             *logrus.Logger
}

// NewAdaptiveStrategy는 새로운 적응형 폴링 전략을 생성합니다
func NewAdaptiveStrategy(
	minInterval time.Duration,
	maxInterval time.Duration,
	idleInterval time.Duration,
	logger *logrus.Logger,
) *AdaptiveStrategy {
	return &AdaptiveStrategy{
		minInterval:      minInterval,
		maxInterval:      maxInterval,
		idleInterval:     idleInterval,
		thresholdForSlow: 5,  // 5번 연속 작업 없으면 속도 감소
		thresholdForFast: 2,  // 2번 연속 작업 있으면 속도 증가
		currentInterval:  minInterval,
		logger:           logger,
	}
}

// NextInterval은 작업량에 따라 다음 폴링 간격을 결정합니다
func (s *AdaptiveStrategy) NextInterval(hasWork bool) time.Duration {
	if hasWork {
		s.workDetectedCount++
		s.noWorkCount = 0
		
		// 작업이 많으면 폴링 속도 증가
		if s.workDetectedCount >= s.thresholdForFast {
			s.currentInterval = s.minInterval
			s.logger.WithField("interval", s.currentInterval).Debug("Increased polling frequency due to work")
		}
	} else {
		s.noWorkCount++
		s.workDetectedCount = 0
		
		// 작업이 없으면 폴링 속도 감소
		if s.noWorkCount >= s.thresholdForSlow {
			if s.currentInterval < s.maxInterval {
				// 점진적으로 증가
				s.currentInterval = time.Duration(float64(s.currentInterval) * 1.5)
				if s.currentInterval > s.maxInterval {
					s.currentInterval = s.maxInterval
				}
			}
			
			// 장시간 작업이 없으면 idle 모드로
			if s.noWorkCount >= s.thresholdForSlow*3 {
				s.currentInterval = s.idleInterval
			}
			
			s.logger.WithFields(logrus.Fields{
				"interval":    s.currentInterval,
				"no_work_count": s.noWorkCount,
			}).Debug("Decreased polling frequency due to no work")
		}
	}
	
	return s.currentInterval
}

// Reset은 전략을 초기 상태로 리셋합니다
func (s *AdaptiveStrategy) Reset() {
	s.workDetectedCount = 0
	s.noWorkCount = 0
	s.currentInterval = s.minInterval
}

// PollingController는 폴링을 관리하는 컨트롤러입니다
type PollingController struct {
	strategy Strategy
	ticker   *time.Ticker
	logger   *logrus.Logger
}

// NewPollingController는 새로운 폴링 컨트롤러를 생성합니다
func NewPollingController(strategy Strategy, logger *logrus.Logger) *PollingController {
	return &PollingController{
		strategy: strategy,
		logger:   logger,
	}
}

// Start는 폴링을 시작합니다
func (c *PollingController) Start(ctx context.Context, task func(context.Context) error) error {
	// 초기 간격으로 ticker 생성
	initialInterval := c.strategy.NextInterval(true)
	c.ticker = time.NewTicker(initialInterval)
	defer c.ticker.Stop()
	
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
			
		case <-c.ticker.C:
			// 작업 실행
			err := task(ctx)
			success := err == nil
			
			// 다음 간격 계산
			nextInterval := c.strategy.NextInterval(success)
			
			// ticker 재설정
			c.ticker.Reset(nextInterval)
			
			if err != nil {
				c.logger.WithError(err).Error("Polling task failed")
			}
		}
	}
}