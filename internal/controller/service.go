package controller

import (
    "context"
    "time"
)

// Service provides a simple polling runner around the Controller
type Service struct {
    Controller *Controller
    Namespace  string
    Interval   time.Duration
}

// RunOnce runs a single reconcile + jobs processing cycle
func (s *Service) RunOnce(ctx context.Context) error {
    if err := s.Controller.ProcessAll(ctx, s.Namespace); err != nil { return err }
    if err := s.Controller.ProcessJobs(ctx, s.Namespace); err != nil { return err }
    return nil
}

// Start runs the polling loop until context is cancelled
func (s *Service) Start(ctx context.Context) error {
    ticker := time.NewTicker(s.Interval)
    defer ticker.Stop()
    // initial run
    if err := s.RunOnce(ctx); err != nil { return err }
    for {
        select {
        case <-ctx.Done():
            return nil
        case <-ticker.C:
            if err := s.RunOnce(ctx); err != nil { return err }
        }
    }
}

