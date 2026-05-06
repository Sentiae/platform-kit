// Cron scheduler for retention workers. Drives one or more
// `Worker` instances on a fixed cadence (default daily at 03:00
// service-local). Each service that emits `signals`, `logs`,
// `artifacts`, or `audit` rows wires its own Cron + Workers using
// the policy returned by `DefaultsFor(plan_slug)` per org.
//
// Phase-1 ships the scheduler skeleton; per-service wiring in
// ops/work/git/identity drops Cron into their existing background
// goroutine pool.
package retention

import (
	"context"
	"log"
	"sync"
	"time"
)

// Cron runs one tick per Interval, fanning every registered Worker
// in parallel. Failures log + continue; one bad worker doesn't
// starve the others.
type Cron struct {
	workers  []*Worker
	interval time.Duration
	logf     func(format string, args ...any)
	mu       sync.Mutex
	stopped  bool
}

// NewCron builds the scheduler. interval <= 0 falls back to 24h.
func NewCron(interval time.Duration, workers ...*Worker) *Cron {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	return &Cron{
		workers:  workers,
		interval: interval,
		logf:     log.Printf,
	}
}

// AddWorker registers another worker. Safe to call before Run.
func (c *Cron) AddWorker(w *Worker) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.workers = append(c.workers, w)
}

// Run blocks until ctx is cancelled. Fires one tick immediately on
// startup so a freshly-deployed service catches up on overdue
// retention without waiting a full interval.
func (c *Cron) Run(ctx context.Context) {
	c.tick(ctx)
	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.tick(ctx)
		}
	}
}

// tick fans every worker in parallel. Each gets its own goroutine
// + a per-worker timeout (fraction of the interval) so a slow
// archiver doesn't block the cohort.
func (c *Cron) tick(ctx context.Context) {
	c.mu.Lock()
	workers := append([]*Worker(nil), c.workers...)
	c.mu.Unlock()
	if len(workers) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, w := range workers {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			runCtx, cancel := context.WithTimeout(ctx, c.interval/2)
			defer cancel()
			stats, err := w.Run(runCtx)
			if err != nil {
				c.logf("retention worker error: %v", err)
				return
			}
			c.logf("retention worker stats: archived=%d deleted=%d", stats.Archived, stats.Deleted)
		}()
	}
	wg.Wait()
}
