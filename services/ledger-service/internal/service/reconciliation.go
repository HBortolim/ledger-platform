package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ledger-platform/ledger-service/internal/domain"
	"github.com/ledger-platform/ledger-service/internal/metrics"
)

// ReconciliationRepository is what the service needs from storage.
type ReconciliationRepository interface {
	UnbalancedTransactions(ctx context.Context, window time.Duration, limit int) (domain.InvariantFindings, error)
	ProjectionDrift(ctx context.Context, grace time.Duration, limit int) (domain.DriftFindings, error)
	MoneySupply(ctx context.Context) (domain.MoneySupply, error)

	// AcquirePassLock is the cross-process overlap guard: a nil release with acquired == false
	// means another pass holds it.
	AcquirePassLock(ctx context.Context) (release func(), acquired bool, err error)
}

// ReconciliationOptions are the policy values the repository is deliberately ignorant of.
type ReconciliationOptions struct {
	InvariantWindow time.Duration // check 1 lookback; main.go passes 2 x the ticker interval
	DriftGrace      time.Duration // check 2: entries younger than this are mid-flight, not drift
	PassTimeout     time.Duration // hard bound on one whole pass
}

type ReconciliationService struct {
	repo ReconciliationRepository
	opts ReconciliationOptions
}

func NewReconciliationService(repo ReconciliationRepository, opts ReconciliationOptions) *ReconciliationService {
	return &ReconciliationService{repo: repo, opts: opts}
}

// Run executes all three checks once. It never mutates anything. If the returned error is
// non-nil the report is partial and must be ignored.
func (s *ReconciliationService) Run(ctx context.Context) (domain.ReconciliationReport, error) {
	ctx, cancel := context.WithTimeout(ctx, s.opts.PassTimeout) // bound the whole pass, lock wait included
	defer cancel()

	release, acquired, err := s.repo.AcquirePassLock(ctx) // refuse an overlap, across replicas
	if err != nil {
		metrics.ReconciliationRunsTotal.WithLabelValues("error").Inc()
		slog.ErrorContext(ctx, "reconciliation failed", slog.Any("error", err))
		return domain.ReconciliationReport{}, fmt.Errorf("acquire reconciliation lock: %w", err)
	}
	if !acquired {
		return domain.ReconciliationReport{}, domain.ErrReconciliationInProgress{}
	}
	defer release()

	start := time.Now()
	report, err := s.runChecks(ctx)
	report.Duration = time.Since(start)
	metrics.ReconciliationDuration.Observe(report.Duration.Seconds())

	switch {
	case err != nil:
		metrics.ReconciliationRunsTotal.WithLabelValues("error").Inc()
		slog.ErrorContext(ctx, "reconciliation failed", slog.Any("error", err))
		return report, err
	case !report.Clean():
		metrics.ReconciliationRunsTotal.WithLabelValues("drift").Inc()
		s.countDrift(report)
		s.logDrift(ctx, report)
	default:
		metrics.ReconciliationRunsTotal.WithLabelValues("ok").Inc()
		metrics.ReconciliationLastSuccess.SetToCurrentTime()
		slog.InfoContext(ctx, "reconciliation clean", slog.Int64("duration_ms", report.Duration.Milliseconds()))
	}
	return report, nil
}

func (s *ReconciliationService) runChecks(ctx context.Context) (domain.ReconciliationReport, error) {
	report := domain.ReconciliationReport{RanAt: time.Now()}

	inv, err := s.repo.UnbalancedTransactions(ctx, s.opts.InvariantWindow, domain.MaxReportedFindings)
	if err != nil {
		return report, fmt.Errorf("invariant check: %w", err)
	}
	report.Invariant = inv

	drift, err := s.repo.ProjectionDrift(ctx, s.opts.DriftGrace, domain.MaxReportedFindings)
	if err != nil {
		return report, fmt.Errorf("projection check: %w", err)
	}
	report.Projection = drift

	supply, err := s.repo.MoneySupply(ctx)
	if err != nil {
		return report, fmt.Errorf("money supply check: %w", err)
	}
	report.MoneySupply = supply

	return report, nil
}

// countDrift adds each check's true total, not the sample length.
func (s *ReconciliationService) countDrift(r domain.ReconciliationReport) {
	metrics.ReconciliationDriftTotal.WithLabelValues("invariant").Add(float64(r.Invariant.Total))
	metrics.ReconciliationDriftTotal.WithLabelValues("projection").Add(float64(r.Projection.Total))
	metrics.ReconciliationDriftTotal.WithLabelValues("money_supply").Add(float64(r.MoneySupply.UncleanCount()))
}

func (s *ReconciliationService) logDrift(ctx context.Context, r domain.ReconciliationReport) {
	for _, f := range r.Invariant.Sample {
		slog.ErrorContext(ctx, "reconciliation drift: invariant",
			slog.String("check", "invariant"),
			slog.String("transaction_id", f.TransactionID.String()),
			slog.String("net", f.Net.String()),
			slog.Int("entry_count", f.EntryCount))
	}
	for _, f := range r.Projection.Sample {
		slog.ErrorContext(ctx, "reconciliation drift: projection",
			slog.String("check", "projection"),
			slog.String("wallet_id", f.WalletID.String()),
			slog.String("kind", string(f.Kind)),
			slog.String("projected", f.Projected.String()),
			slog.String("actual", f.Actual.String()))
	}
	if !r.MoneySupply.Clean() {
		slog.ErrorContext(ctx, "reconciliation drift: money_supply",
			slog.String("check", "money_supply"),
			slog.String("ledger_total", r.MoneySupply.LedgerTotal.String()),
			slog.String("cached_total", r.MoneySupply.CachedTotal.String()))
	}
	slog.ErrorContext(ctx, "reconciliation drift detected",
		slog.Int("invariant_findings", r.Invariant.Total),
		slog.Bool("invariant_truncated", r.Invariant.Truncated()),
		slog.Int("projection_findings", r.Projection.Total),
		slog.Bool("projection_truncated", r.Projection.Truncated()),
		slog.Int("money_supply_unclean_totals", r.MoneySupply.UncleanCount()))
}
