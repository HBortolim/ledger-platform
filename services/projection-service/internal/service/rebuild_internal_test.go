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
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/shopspring/decimal"

	"github.com/ledger-platform/projection-service/internal/metrics"
	"github.com/ledger-platform/projection-service/internal/utils"
)

// ctxKey is unexported so no other package can collide with the test's context value.
type ctxKey struct{}

type fakeRebuildRepo struct {
	calls     int
	gotCtx    context.Context
	gotWallet uuid.UUID
	result    utils.Result
	err       error
}

func (f *fakeRebuildRepo) Rebuild(ctx context.Context, walletID uuid.UUID) (utils.Result, error) {
	f.calls++
	f.gotCtx = ctx
	f.gotWallet = walletID
	return f.result, f.err
}

func TestRebuild_Success_ReturnsRepoResultUnchanged(t *testing.T) {
	wallet := uuid.New()
	want := utils.Result{
		WalletID:       wallet,
		Balance:        decimal.RequireFromString("300.00"),
		EntriesApplied: 2,
		RebuiltAt:      time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC),
	}
	repo := &fakeRebuildRepo{result: want}

	got, err := NewRebuildService(repo).Rebuild(context.Background(), wallet)

	if err != nil {
		t.Fatalf("Rebuild() = error %v, want nil", err)
	}
	if got.WalletID != want.WalletID || !got.Balance.Equal(want.Balance) ||
		got.EntriesApplied != want.EntriesApplied || !got.RebuiltAt.Equal(want.RebuiltAt) {
		t.Errorf("Rebuild() = %+v, want %+v", got, want)
	}
}

func TestRebuild_Success_CallsRepoOnceWithTheGivenWallet(t *testing.T) {
	wallet := uuid.New()
	repo := &fakeRebuildRepo{}

	if _, err := NewRebuildService(repo).Rebuild(context.Background(), wallet); err != nil {
		t.Fatalf("Rebuild() = error %v, want nil", err)
	}

	if repo.calls != 1 {
		t.Errorf("repo called %d times, want exactly 1", repo.calls)
	}
	if repo.gotWallet != wallet {
		t.Errorf("repo got wallet %s, want %s", repo.gotWallet, wallet)
	}
}

func TestRebuild_RepoError_PropagatesUnchanged(t *testing.T) {
	sentinel := errors.New("ledger read failed")
	repo := &fakeRebuildRepo{err: sentinel}

	_, err := NewRebuildService(repo).Rebuild(context.Background(), uuid.New())

	// errors.Is, not ==: the service may legitimately wrap later; what it must never do is
	// swallow or replace the cause, because the handler decides the status code from it.
	if !errors.Is(err, sentinel) {
		t.Errorf("Rebuild() error = %v, want it to wrap %v", err, sentinel)
	}
	if repo.calls != 1 {
		t.Errorf("repo called %d times, want exactly 1 (no retries, this is an incident-time action)", repo.calls)
	}
}

func TestRebuild_PassesTheCallersContextToTheRepo(t *testing.T) {
	// A cancelled HTTP request must be able to abort the rebuild's transaction, so the
	// service has to forward the caller's context, not context.Background().
	ctx := context.WithValue(context.Background(), ctxKey{}, "from-the-request")
	repo := &fakeRebuildRepo{}

	if _, err := NewRebuildService(repo).Rebuild(ctx, uuid.New()); err != nil {
		t.Fatalf("Rebuild() = error %v, want nil", err)
	}

	if got := repo.gotCtx.Value(ctxKey{}); got != "from-the-request" {
		t.Errorf("repo context value = %v, want the caller's context to be forwarded", got)
	}
}

// captureLogs routes slog's default logger into a buffer for the test and restores it after.
// It swaps process-global state, so tests using it must not call t.Parallel().
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// logRecords decodes every JSON line the capture buffer holds.
func logRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("log line is not JSON: %v: %s", err, line)
		}
		records = append(records, rec)
	}
	return records
}

// rebuilds reads the counter's current value. The collector is process-global, so tests
// assert on the delta across the call rather than an absolute number.
func rebuilds(result string) float64 {
	return testutil.ToFloat64(metrics.RebuildsTotal.WithLabelValues(result))
}

func TestRebuild_Success_CountsAnOkRebuildAndNothingElse(t *testing.T) {
	okBefore, errBefore := rebuilds("ok"), rebuilds("error")

	if _, err := NewRebuildService(&fakeRebuildRepo{}).Rebuild(context.Background(), uuid.New()); err != nil {
		t.Fatalf("Rebuild() = error %v, want nil", err)
	}

	if got := rebuilds("ok") - okBefore; got != 1 {
		t.Errorf("projection_rebuilds_total{result=ok} moved by %v, want 1", got)
	}
	if got := rebuilds("error") - errBefore; got != 0 {
		t.Errorf("projection_rebuilds_total{result=error} moved by %v, want 0", got)
	}
}

func TestRebuild_RepoError_CountsAnErrorRebuildAndNothingElse(t *testing.T) {
	okBefore, errBefore := rebuilds("ok"), rebuilds("error")

	_, err := NewRebuildService(&fakeRebuildRepo{err: errors.New("boom")}).Rebuild(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("Rebuild() = nil error, want the repo's error")
	}

	if got := rebuilds("error") - errBefore; got != 1 {
		t.Errorf("projection_rebuilds_total{result=error} moved by %v, want 1", got)
	}
	if got := rebuilds("ok") - okBefore; got != 0 {
		t.Errorf("projection_rebuilds_total{result=ok} moved by %v, want 0 (a failed rebuild must never count as ok)", got)
	}
}

func TestRebuild_Success_LogsOneInfoLineWithWalletCountAndBalance(t *testing.T) {
	logs := captureLogs(t)
	wallet := uuid.New()
	repo := &fakeRebuildRepo{result: utils.Result{
		WalletID:       wallet,
		Balance:        decimal.RequireFromString("300.5"), // scale 1: the log must still say 300.50
		EntriesApplied: 2,
	}}

	if _, err := NewRebuildService(repo).Rebuild(context.Background(), wallet); err != nil {
		t.Fatalf("Rebuild() = error %v, want nil", err)
	}

	records := logRecords(t, logs)
	if len(records) != 1 {
		t.Fatalf("got %d log lines, want exactly 1: %s", len(records), logs)
	}
	rec := records[0]
	if rec["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", rec["level"])
	}
	if rec["wallet_id"] != wallet.String() {
		t.Errorf("wallet_id = %v, want %s", rec["wallet_id"], wallet)
	}
	if rec["entries_applied"] != float64(2) { // encoding/json decodes numbers as float64
		t.Errorf("entries_applied = %v, want 2", rec["entries_applied"])
	}
	if rec["balance"] != "300.50" {
		t.Errorf("balance = %v, want %q", rec["balance"], "300.50")
	}
}

// The handler returns a generic 503 with no internals, so this log line is the ONLY record of
// why a rebuild failed. Without it an operator sees a metric tick and has no cause.
func TestRebuild_RepoError_LogsOneErrorLineWithWalletAndCause(t *testing.T) {
	logs := captureLogs(t)
	wallet := uuid.New()
	repo := &fakeRebuildRepo{err: errors.New("read ledger entries: permission denied")}

	_, _ = NewRebuildService(repo).Rebuild(context.Background(), wallet)

	records := logRecords(t, logs)
	if len(records) != 1 {
		t.Fatalf("got %d log lines, want exactly 1: %s", len(records), logs)
	}
	rec := records[0]
	if rec["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", rec["level"])
	}
	if rec["wallet_id"] != wallet.String() {
		t.Errorf("wallet_id = %v, want %s", rec["wallet_id"], wallet)
	}
	if rec["error"] != "read ledger entries: permission denied" {
		t.Errorf("error = %v, want the repo's error text", rec["error"])
	}
	// A failed rebuild must not also emit the success line.
	if _, has := rec["entries_applied"]; has {
		t.Errorf("error line carries success fields: %v", rec)
	}
}
