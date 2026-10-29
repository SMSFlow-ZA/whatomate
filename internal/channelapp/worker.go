package channelapp

import (
	"context"
	"sync"
	"time"
)

// Run keeps receiving independent of outgoing network latency. Each lane is
// bounded to one worker per process; database claims/cursor CAS fence replicas.
// Cancellation waits for all lanes, preventing work after application shutdown.
func (s *Service) Run(ctx context.Context, interval time.Duration, onError func(error)) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	var workers sync.WaitGroup
	lanes := []func(context.Context, time.Time) error{s.Dispatch, s.RefreshBalances, func(ctx context.Context, _ time.Time) error { return s.ReconcileUnknown(ctx) }, s.PollDue}
	for _, lane := range lanes {
		workers.Add(1)
		go func(work func(context.Context, time.Time) error) {
			defer workers.Done()
			timer := time.NewTicker(interval)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case now := <-timer.C:
					if err := work(ctx, now); err != nil && ctx.Err() == nil && onError != nil {
						onError(err)
					}
				}
			}
		}(lane)
	}
	workers.Wait()
}
