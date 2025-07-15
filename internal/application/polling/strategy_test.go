package polling

import (
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func TestExponentialBackoffStrategy(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)

	t.Run("성공 시 기본 간격 반환", func(t *testing.T) {
		strategy := NewExponentialBackoffStrategy(
			30*time.Second,
			300*time.Second,
			2.0,
			logger,
		)

		// 첫 번째 성공
		interval := strategy.NextInterval(true)
		assert.Equal(t, 30*time.Second, interval)

		// 계속 성공
		interval = strategy.NextInterval(true)
		assert.Equal(t, 30*time.Second, interval)
	})

	t.Run("실패 시 지수 백오프", func(t *testing.T) {
		strategy := NewExponentialBackoffStrategy(
			30*time.Second,
			300*time.Second,
			2.0,
			logger,
		)

		// 첫 번째 실패: 30s
		interval := strategy.NextInterval(false)
		assert.Equal(t, 30*time.Second, interval)

		// 두 번째 실패: 60s
		interval = strategy.NextInterval(false)
		assert.Equal(t, 60*time.Second, interval)

		// 세 번째 실패: 120s
		interval = strategy.NextInterval(false)
		assert.Equal(t, 120*time.Second, interval)

		// 네 번째 실패: 240s
		interval = strategy.NextInterval(false)
		assert.Equal(t, 240*time.Second, interval)

		// 다섯 번째 실패: 300s (최대값)
		interval = strategy.NextInterval(false)
		assert.Equal(t, 300*time.Second, interval)

		// 여섯 번째 실패: 여전히 300s
		interval = strategy.NextInterval(false)
		assert.Equal(t, 300*time.Second, interval)
	})

	t.Run("실패 후 성공 시 리셋", func(t *testing.T) {
		strategy := NewExponentialBackoffStrategy(
			30*time.Second,
			300*time.Second,
			2.0,
			logger,
		)

		// 실패 몇 번
		strategy.NextInterval(false)
		strategy.NextInterval(false)
		strategy.NextInterval(false)

		// 성공하면 리셋
		interval := strategy.NextInterval(true)
		assert.Equal(t, 30*time.Second, interval)

		// 다시 실패하면 처음부터
		interval = strategy.NextInterval(false)
		assert.Equal(t, 30*time.Second, interval)
	})

	t.Run("다른 지수 계수", func(t *testing.T) {
		strategy := NewExponentialBackoffStrategy(
			10*time.Second,
			100*time.Second,
			1.5,
			logger,
		)

		// 첫 번째 실패: 10s
		interval := strategy.NextInterval(false)
		assert.Equal(t, 10*time.Second, interval)

		// 두 번째 실패: 15s
		interval = strategy.NextInterval(false)
		assert.Equal(t, 15*time.Second, interval)

		// 세 번째 실패: 22.5s
		interval = strategy.NextInterval(false)
		assert.Equal(t, time.Duration(22.5*float64(time.Second)), interval)
	})

	t.Run("Reset 메서드", func(t *testing.T) {
		strategy := NewExponentialBackoffStrategy(
			30*time.Second,
			300*time.Second,
			2.0,
			logger,
		)

		// 실패 몇 번
		strategy.NextInterval(false)
		strategy.NextInterval(false)

		// Reset
		strategy.Reset()

		// 다시 실패하면 처음부터
		interval := strategy.NextInterval(false)
		assert.Equal(t, 30*time.Second, interval)
	})
}

func TestAdaptiveStrategy(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)

	t.Run("작업이 있을 때 빠른 폴링", func(t *testing.T) {
		strategy := NewAdaptiveStrategy(
			10*time.Second,
			60*time.Second,
			120*time.Second,
			logger,
		)

		// 작업 감지
		strategy.NextInterval(true)
		interval := strategy.NextInterval(true)
		assert.Equal(t, 10*time.Second, interval) // 최소 간격
	})

	t.Run("작업이 없을 때 느린 폴링", func(t *testing.T) {
		strategy := NewAdaptiveStrategy(
			10*time.Second,
			60*time.Second,
			120*time.Second,
			logger,
		)

		// 작업 없음을 여러 번 보고
		for i := 0; i < 5; i++ {
			strategy.NextInterval(false)
		}

		// 간격이 증가해야 함
		interval := strategy.NextInterval(false)
		assert.Greater(t, interval, 10*time.Second)
		assert.LessOrEqual(t, interval, 60*time.Second)
	})

	t.Run("장시간 작업 없으면 idle 모드", func(t *testing.T) {
		strategy := NewAdaptiveStrategy(
			10*time.Second,
			60*time.Second,
			120*time.Second,
			logger,
		)

		// 작업 없음을 15번 이상 보고
		for i := 0; i < 16; i++ {
			strategy.NextInterval(false)
		}

		// idle 간격이 되어야 함
		interval := strategy.NextInterval(false)
		assert.Equal(t, 120*time.Second, interval)
	})
}