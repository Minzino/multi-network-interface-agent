package usecases

import (
    "context"
    "testing"
    "time"

    infraMetrics "multinic-agent/internal/infrastructure/metrics"

    "github.com/prometheus/client_golang/prometheus/testutil"
    "github.com/stretchr/testify/require"
)

func TestWorkerPool_RetryPolicyAndMetrics(t *testing.T) {
    poolName := "ut"
    p := NewWorkerPool[int](1, 2, WithPoolName[int](poolName), WithRetryPolicy[int](
        func(job int, err error, attempt int) (bool, time.Duration) {
            if attempt < 1 { // allow 1 retry
                return true, 10 * time.Millisecond
            }
            return false, 0
        },
    ))

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    seen := make(chan int, 2)
    // Fail first time for job==42
    first := true
    stop := p.StartE(ctx, func(_ context.Context, job int) error {
        seen <- job
        if job == 42 && first {
            first = false
            return assertErr
        }
        return nil
    })
    defer stop()

    p.Submit(42)

    // Expect two executions: original + retry
    select {
    case <-seen:
    case <-time.After(500 * time.Millisecond):
        t.Fatal("first execution not observed")
    }
    select {
    case <-seen:
    case <-time.After(2 * time.Second):
        t.Fatal("retry execution not observed")
    }

    // Metrics: retry counter should be >=1
    retries := testutil.ToFloat64(infraMetrics.WorkerRetries.WithLabelValues(poolName))
    require.GreaterOrEqual(t, retries, float64(1))
}

var assertErr = &simpleErr{"ut-error"}

type simpleErr struct{ s string }
func (e *simpleErr) Error() string { return e.s }

func TestWorkerPool_PanicRecovery(t *testing.T) {
    poolName := "ut2"
    p := NewWorkerPool[int](1, 2, WithPoolName[int](poolName))
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    done := make(chan struct{}, 1)
    first := true
    stop := p.StartE(ctx, func(_ context.Context, job int) error {
        if first {
            first = false
            panic("boom")
        }
        done <- struct{}{}
        return nil
    })
    defer stop()

    p.Submit(1) // will panic
    p.Submit(2) // should still be processed

    select {
    case <-done:
    case <-time.After(2 * time.Second):
        t.Fatal("second job not processed after panic recovery")
    }

    panics := testutil.ToFloat64(infraMetrics.WorkerPanics.WithLabelValues(poolName))
    require.GreaterOrEqual(t, panics, float64(1))
}

func TestWorkerPool_QueueDepthGauge(t *testing.T) {
    poolName := "ut3"
    p := NewWorkerPool[int](1, 10, WithPoolName[int](poolName))
    // Not starting workers yet; submit two items -> gauge should reflect depth
    p.Submit(1)
    p.Submit(2)

    depth := testutil.ToFloat64(infraMetrics.WorkerQueueDepth.WithLabelValues(poolName))
    require.Equal(t, float64(2), depth)
}
