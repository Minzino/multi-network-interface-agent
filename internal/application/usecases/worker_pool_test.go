package usecases

import (
    "context"
    "testing"
    "time"

    infraMetrics "multinic-agent/internal/infrastructure/metrics"

    "github.com/prometheus/client_golang/prometheus/testutil"
    "github.com/stretchr/testify/require"
    "sync/atomic"
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

func TestWorkerPool_ConcurrencyCap(t *testing.T) {
    poolName := "conc"
    workers := 3
    p := NewWorkerPool[int](workers, 20, WithPoolName[int](poolName))
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    var current, maxObserved int32
    stop := p.StartE(ctx, func(_ context.Context, job int) error {
        cur := atomic.AddInt32(&current, 1)
        // update maxObserved atomically
        for {
            max := atomic.LoadInt32(&maxObserved)
            if cur > max {
                if atomic.CompareAndSwapInt32(&maxObserved, max, cur) { break }
                continue
            }
            break
        }
        time.Sleep(20 * time.Millisecond)
        atomic.AddInt32(&current, -1)
        return nil
    })
    defer stop()

    for i := 0; i < 10; i++ { p.Submit(i) }
    // wait for queue to drain
    time.Sleep(400 * time.Millisecond)
    require.LessOrEqual(t, int(maxObserved), workers)
}
