package usecases

import (
    "context"
    "runtime"
    "sync/atomic"
    "time"

    infraMetrics "multinic-agent/internal/infrastructure/metrics"
)

// RetryPolicy decides whether to retry and for how long to back off.
type RetryPolicy[T any] func(job T, err error, attempt int) (retry bool, backoff time.Duration)

type jobEnvelope[T any] struct {
    payload  T
    enqueued time.Time
    attempt  int
}

// WorkerPool is a queue-based generic worker pool with metrics and safety.
type WorkerPool[T any] struct {
    jobs         chan jobEnvelope[T]
    workers      int
    name         string
    retryPolicy  RetryPolicy[T]
    panicHandler func(job T, recovered any)
    active       atomic.Int64
    after        func(job T, status string, duration time.Duration, attempt int, err error)
}

// WorkerPoolOption configures WorkerPool behavior.
type WorkerPoolOption[T any] func(*WorkerPool[T])

func WithPoolName[T any](name string) WorkerPoolOption[T] { return func(p *WorkerPool[T]) { p.name = name } }
func WithRetryPolicy[T any](policy RetryPolicy[T]) WorkerPoolOption[T] { return func(p *WorkerPool[T]) { p.retryPolicy = policy } }
func WithPanicHandler[T any](h func(T, any)) WorkerPoolOption[T] { return func(p *WorkerPool[T]) { p.panicHandler = h } }
func WithAfterHook[T any](h func(job T, status string, dur time.Duration, attempt int, err error)) WorkerPoolOption[T] {
    return func(p *WorkerPool[T]) { p.after = h }
}

func NewWorkerPool[T any](workers, queueSize int, opts ...WorkerPoolOption[T]) *WorkerPool[T] {
    if workers <= 0 { workers = 1 }
    if queueSize <= 0 { queueSize = workers }
    p := &WorkerPool[T]{
        jobs:    make(chan jobEnvelope[T], queueSize),
        workers: workers,
        name:    "default",
    }
    for _, opt := range opts { opt(p) }
    // initialize metrics
    infraMetrics.SetWorkerQueueDepth(p.name, 0)
    infraMetrics.SetWorkerActive(p.name, 0, p.workers)
    return p
}

// Start starts workers with a handler that returns error for retry decision.
func (p *WorkerPool[T]) StartE(ctx context.Context, handler func(context.Context, T) error) func() {
    for i := 0; i < p.workers; i++ {
        go func() {
            for {
                select {
                case <-ctx.Done():
                    return
                case env, ok := <-p.jobs:
                    if !ok { return }
                    // metrics: queue depth after dequeue
                    infraMetrics.SetWorkerQueueDepth(p.name, len(p.jobs))

                    // worker active + panic recovery
                    p.active.Add(1)
                    infraMetrics.SetWorkerActive(p.name, int(p.active.Load()), p.workers)

                    start := time.Now()
                    status := "success"
                    var lastErr error
                    func() {
                        defer func() {
                            if r := recover(); r != nil {
                                status = "panic"
                                infraMetrics.IncWorkerPanic(p.name)
                                if p.panicHandler != nil {
                                    p.panicHandler(env.payload, r)
                                }
                                // let worker continue
                            }
                        }()

                        if err := handler(ctx, env.payload); err != nil {
                            lastErr = err
                            // retry decision
                            if p.retryPolicy != nil {
                                if retry, backoff := p.retryPolicy(env.payload, err, env.attempt); retry {
                                    status = "retried"
                                    infraMetrics.IncWorkerRetry(p.name)
                                    // small backoff with context awareness
                                    t := time.NewTimer(backoff)
                                    select {
                                    case <-ctx.Done():
                                        t.Stop()
                                    case <-t.C:
                                    }
                                    // re-enqueue if context not cancelled
                                    if ctx.Err() == nil {
                                        env.attempt++
                                        env.enqueued = time.Now()
                                        p.jobs <- env
                                        infraMetrics.SetWorkerQueueDepth(p.name, len(p.jobs))
                                    }
                                    return
                                }
                            }
                            status = "failed"
                        }
                    }()

                    dur := time.Since(start).Seconds()
                    infraMetrics.ObserveWorkerTask(p.name, status, dur)

                    // Call after-hook only for terminal states (success/failed/panic)
                    if p.after != nil && (status == "success" || status == "failed" || status == "panic") {
                        // pass duration as time.Duration for convenience
                        p.after(env.payload, status, time.Duration(dur*float64(time.Second)), env.attempt, lastErr)
                    }

                    // done: update active
                    p.active.Add(-1)
                    infraMetrics.SetWorkerActive(p.name, int(p.active.Load()), p.workers)
                }
            }
        }()
    }
    // returns a stop func
    return func() { close(p.jobs) }
}

// Start is a compatibility wrapper for non-error handlers.
func (p *WorkerPool[T]) Start(ctx context.Context, handler func(context.Context, T)) func() {
    return p.StartE(ctx, func(ctx context.Context, t T) error { handler(ctx, t); return nil })
}

func (p *WorkerPool[T]) Submit(job T) {
    p.jobs <- jobEnvelope[T]{payload: job, enqueued: time.Now(), attempt: 0}
    infraMetrics.SetWorkerQueueDepth(p.name, len(p.jobs))
}

// QueueLen exposes current queue depth (primarily for tests/observability without Prometheus scraping).
func (p *WorkerPool[T]) QueueLen() int { return len(p.jobs) }

// ActiveWorkers exposes current active worker count (approximation).
func (p *WorkerPool[T]) ActiveWorkers() int { return int(p.active.Load()) }

// NumWorkers returns configured number of workers.
func (p *WorkerPool[T]) NumWorkers() int { return p.workers }

// NumCPUHint returns GOMAXPROCS or runtime.NumCPU as a hint for default worker sizing.
func NumCPUHint() int { return runtime.GOMAXPROCS(0) }
