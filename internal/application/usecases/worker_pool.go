package usecases

import "context"

// WorkerPool is a queue-based worker pool skeleton (Phase 2 preparation).
// Not wired yet; intended to replace the semaphore pattern in Execute.
type WorkerPool[T any] struct {
    jobs    chan T
    workers int
}

func NewWorkerPool[T any](workers, queueSize int) *WorkerPool[T] {
    if workers <= 0 { workers = 1 }
    if queueSize <= 0 { queueSize = workers }
    return &WorkerPool[T]{jobs: make(chan T, queueSize), workers: workers}
}

func (p *WorkerPool[T]) Start(ctx context.Context, handler func(context.Context, T)) func() {
    for i := 0; i < p.workers; i++ {
        go func() {
            for {
                select {
                case <-ctx.Done():
                    return
                case job, ok := <-p.jobs:
                    if !ok { return }
                    handler(ctx, job)
                }
            }
        }()
    }
    // returns a stop func
    return func() { close(p.jobs) }
}

func (p *WorkerPool[T]) Submit(job T) { p.jobs <- job }

