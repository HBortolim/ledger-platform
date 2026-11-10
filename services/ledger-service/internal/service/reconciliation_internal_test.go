package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/shopspring/decimal"

	"github.com/ledger-platform/ledger-service/internal/domain"
	"github.com/ledger-platform/ledger-service/internal/metrics"
)

type fakeReconRepo struct {
	invariant domain.InvariantFindings
	drift     domain.DriftFindings
	supply    domain.MoneySupply

	invariantErr, driftErr, supplyErr, lockErr error
	notAcquired                                bool
	blockInvariantUntilDone                    bool

	calls                        []string
	releases                     int
	gotWindow, gotGrace          time.Duration
	gotInvariantLim, gotDriftLim int
}

func (f *fakeReconRepo) UnbalancedTransactions(ctx context.Context, w time.Duration, limit int) (domain.InvariantFindings, error) {
	f.calls = append(f.calls, "invariant")
	f.gotWindow, f.gotInvariantLim = w, limit
	if f.blockInvariantUntilDone {
		<-ctx.Done()
		return domain.InvariantFindings{}, ctx.Err()
	}
	return f.invariant, f.invariantErr
}

func (f *fakeReconRepo) ProjectionDrift(ctx context.Context, g time.Duration, limit int) (domain.DriftFindings, error) {
	f.calls = append(f.calls, "projection")
	f.gotGrace, f.gotDriftLim = g, limit
	return f.drift, f.driftErr
}

func (f *fakeReconRepo) MoneySupply(ctx context.Context) (domain.MoneySupply, error) {
	f.calls = append(f.calls, "money_supply")
	return f.supply, f.supplyErr
}

func (f *fakeReconRepo) AcquirePassLock(ctx context.Context) (func(), bool, error) {
	if f.lockErr != nil {
		return nil, false, f.lockErr
	}
	if f.notAcquired {
		return nil, false, nil
	}
	return func() { f.releases++ }, true, nil
}

var testReconOpts = ReconciliationOptions{
	InvariantWindow: 2 * time.Hour,
	DriftGrace:      10 * time.Second,
	PassTimeout:     time.Second,
}

func runs(result string) float64 {
	return testutil.ToFloat64(metrics.ReconciliationRunsTotal.WithLabelValues(result))
}

func drifts(check string) float64 {
	return testutil.ToFloat64(metrics.ReconciliationDriftTotal.WithLabelValues(check))
}

func lastSuccess() float64 { return testutil.ToFloat64(metrics.ReconciliationLastSuccess) }

func durationCount(t *testing.T) uint64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 1)
	metrics.ReconciliationDuration.Collect(ch)
	var m dto.Metric
	if err := (<-ch).Write(&m); err != nil {
		t.Fatalf("write histogram: %v", err)
	}
	return m.GetHistogram().GetSampleCount()
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func logRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func TestReconciliationRun_Clean(t *testing.T) {
	repo := &fakeReconRepo{}
	svc := NewReconciliationService(repo, testReconOpts)
	okBefore, driftBefore, lastBefore := runs("ok"), drifts("projection"), lastSuccess()
	durBefore := durationCount(t)
	logs := captureLogs(t)

	report, err := svc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() = error %v, want nil", err)
	}
	if !report.Clean() {
		t.Error("report should be clean")
	}
	if got := runs("ok") - okBefore; got != 1 {
		t.Errorf("runs{ok} moved by %v, want 1", got)
	}
	if drifts("projection") != driftBefore {
		t.Error("drift counters must not move on a clean pass")
	}
	if lastSuccess() <= lastBefore {
		t.Error("last-success gauge should advance on a clean pass")
	}
	if durationCount(t)-durBefore != 1 {
		t.Error("duration histogram should observe one pass")
	}
	if repo.gotWindow != testReconOpts.InvariantWindow || repo.gotGrace != testReconOpts.DriftGrace {
		t.Errorf("window/grace = %v/%v, want options forwarded", repo.gotWindow, repo.gotGrace)
	}
	if repo.gotInvariantLim != domain.MaxReportedFindings || repo.gotDriftLim != domain.MaxReportedFindings {
		t.Errorf("limits = %d/%d, want MaxReportedFindings", repo.gotInvariantLim, repo.gotDriftLim)
	}
	if repo.releases != 1 {
		t.Errorf("releases = %d, want 1", repo.releases)
	}
	recs := logRecords(t, logs)
	if len(recs) != 1 || recs[0]["msg"] != "reconciliation clean" || recs[0]["level"] != "INFO" {
		t.Errorf("logs = %v, want one INFO 'reconciliation clean'", recs)
	}
}

func driftingRepo() *fakeReconRepo {
	sample := make([]domain.ProjectionDrift, 0, 2)
	for i := 0; i < 2; i++ {
		sample = append(sample, domain.ProjectionDrift{
			WalletID: uuid.New(), Kind: domain.DriftMismatch,
			Projected: decimal.RequireFromString("999"), Actual: decimal.RequireFromString("50"),
		})
	}
	return &fakeReconRepo{
		invariant: domain.InvariantFindings{Total: 1, Sample: []domain.UnbalancedTransaction{
			{TransactionID: uuid.New(), Net: decimal.RequireFromString("40"), EntryCount: 1},
		}},
		drift:  domain.DriftFindings{Total: 120, Sample: sample}, // sample shorter than the true total
		supply: domain.MoneySupply{LedgerTotal: decimal.RequireFromString("5"), CachedTotal: decimal.RequireFromString("1000")},
	}
}

func TestReconciliationRun_Drift_CountsTrueTotalsAndFreezesGauge(t *testing.T) {
	repo := driftingRepo()
	svc := NewReconciliationService(repo, testReconOpts)
	driftRuns, inv, proj, ms, last := runs("drift"), drifts("invariant"), drifts("projection"), drifts("money_supply"), lastSuccess()
	logs := captureLogs(t)

	report, err := svc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() = error %v, want nil (drift is not a failure)", err)
	}
	if report.Clean() {
		t.Error("report should not be clean")
	}
	if got := runs("drift") - driftRuns; got != 1 {
		t.Errorf("runs{drift} moved by %v, want 1", got)
	}
	if got := drifts("invariant") - inv; got != 1 {
		t.Errorf("drift{invariant} moved by %v, want 1", got)
	}
	if got := drifts("projection") - proj; got != 120 {
		t.Errorf("drift{projection} moved by %v, want the true total 120, not the sample length", got)
	}
	if got := drifts("money_supply") - ms; got != 2 {
		t.Errorf("drift{money_supply} moved by %v, want 2 (both totals unclean)", got)
	}
	if lastSuccess() != last {
		t.Error("last-success gauge must not advance on drift")
	}

	// 1 invariant + 2 projection + 1 money supply + 1 summary, all at error level.
	recs := logRecords(t, logs)
	if len(recs) != 5 {
		t.Fatalf("got %d log lines, want 5: %v", len(recs), recs)
	}
	for _, r := range recs {
		if r["level"] != "ERROR" {
			t.Errorf("log %v is not error level", r["msg"])
		}
	}
	summary := recs[len(recs)-1]
	if summary["msg"] != "reconciliation drift detected" ||
		summary["projection_findings"] != float64(120) ||
		summary["projection_truncated"] != true ||
		summary["invariant_truncated"] != false ||
		summary["money_supply_unclean_totals"] != float64(2) {
		t.Errorf("summary line = %v", summary)
	}
}

func TestReconciliationRun_CheckError_FailsFast(t *testing.T) {
	cause := errors.New("permission denied for table wallet_balances")
	repo := &fakeReconRepo{driftErr: cause}
	svc := NewReconciliationService(repo, testReconOpts)
	errRuns, last := runs("error"), lastSuccess()

	_, err := svc.Run(context.Background())
	if !errors.Is(err, cause) {
		t.Fatalf("Run() error = %v, want it to wrap the cause", err)
	}
	if got := err.Error(); len(got) < 16 || got[:16] != "projection check" {
		t.Errorf("error %q should name the failing check", got)
	}
	if got := runs("error") - errRuns; got != 1 {
		t.Errorf("runs{error} moved by %v, want 1", got)
	}
	if lastSuccess() != last {
		t.Error("last-success gauge must not advance on error")
	}
	if len(repo.calls) != 2 || repo.calls[1] != "projection" {
		t.Errorf("calls = %v, want invariant then projection only (fail-fast)", repo.calls)
	}
	if repo.releases != 1 {
		t.Errorf("releases = %d, want 1", repo.releases)
	}
}

func TestReconciliationRun_Overlap_RefusedAndCountsNothing(t *testing.T) {
	repo := &fakeReconRepo{notAcquired: true}
	svc := NewReconciliationService(repo, testReconOpts)
	ok, drift, errRuns, last, dur := runs("ok"), runs("drift"), runs("error"), lastSuccess(), durationCount(t)

	_, err := svc.Run(context.Background())
	var inProgress domain.ErrReconciliationInProgress
	if !errors.As(err, &inProgress) {
		t.Fatalf("Run() error = %v, want ErrReconciliationInProgress", err)
	}
	if len(repo.calls) != 0 {
		t.Errorf("checks ran despite the lock being held: %v", repo.calls)
	}
	if repo.releases != 0 {
		t.Errorf("releases = %d, want 0 when the lock was not acquired", repo.releases)
	}
	if runs("ok") != ok || runs("drift") != drift || runs("error") != errRuns || lastSuccess() != last || durationCount(t) != dur {
		t.Error("a refused overlap must count nothing")
	}
}

func TestReconciliationRun_LockError(t *testing.T) {
	cause := errors.New("connection refused")
	repo := &fakeReconRepo{lockErr: cause}
	svc := NewReconciliationService(repo, testReconOpts)
	errRuns := runs("error")

	_, err := svc.Run(context.Background())
	if !errors.Is(err, cause) {
		t.Fatalf("Run() error = %v, want it to wrap the cause", err)
	}
	if got := err.Error(); got[:len("acquire reconciliation lock")] != "acquire reconciliation lock" {
		t.Errorf("error %q should name the lock step", got)
	}
	if runs("error")-errRuns != 1 {
		t.Error("a lock error counts as result=error")
	}
	if len(repo.calls) != 0 {
		t.Errorf("checks ran without the lock: %v", repo.calls)
	}
}

func TestReconciliationRun_Timeout_StillReleasesTheLock(t *testing.T) {
	repo := &fakeReconRepo{blockInvariantUntilDone: true}
	opts := testReconOpts
	opts.PassTimeout = 20 * time.Millisecond
	svc := NewReconciliationService(repo, opts)
	errRuns := runs("error")

	_, err := svc.Run(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want DeadlineExceeded", err)
	}
	if repo.releases != 1 {
		t.Errorf("releases = %d, want exactly 1 after a timeout", repo.releases)
	}
	if runs("error")-errRuns != 1 {
		t.Error("a timed-out pass counts as result=error")
	}
}
