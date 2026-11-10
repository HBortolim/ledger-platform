package reconciliation

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ledger-platform/ledger-service/internal/domain"
)

type runner interface {
	Run(ctx context.Context) (domain.ReconciliationReport, error)
}

// Scheduler runs reconciliation on a ticker and does nothing else; the service owns
// the overlap guard, metrics and logging.
type Scheduler struct {
	runner   runner
	interval time.Duration
}

func NewScheduler(r runner, interval time.Duration) *Scheduler {
	return &Scheduler{runner: r, interval: interval}
}

// Run blocks until ctx is cancelled, then returns nil. There is no pass at startup:
// the first one fires one interval in. It never returns an error because a pass failed,
// since main's errgroup would then cancel the HTTP server and outbox worker too.
func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.runOnce(ctx)
		}
	}
}

func (s *Scheduler) runOnce(ctx context.Context) {
	_, err := s.runner.Run(ctx)
	var inProgress domain.ErrReconciliationInProgress
	if errors.As(err, &inProgress) {
		slog.InfoContext(ctx, "scheduled reconciliation skipped: a pass is already running")
	}
	// Any other error was already logged and counted by the service.
}
