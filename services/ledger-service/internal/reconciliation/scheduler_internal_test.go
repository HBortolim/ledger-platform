package reconciliation

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ledger-platform/ledger-service/internal/domain"
)

type fakeRunner struct {
	calls atomic.Int32
	err   error
}

func (f *fakeRunner) Run(ctx context.Context) (domain.ReconciliationReport, error) {
	f.calls.Add(1)
	return domain.ReconciliationReport{}, f.err
}

func startScheduler(t *testing.T, r *fakeRunner, interval time.Duration) (cancel func(), done <-chan error) {
	t.Helper()
	ctx, cancelCtx := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	go func() { ch <- NewScheduler(r, interval).Run(ctx) }()
	t.Cleanup(cancelCtx)
	return cancelCtx, ch
}

func TestScheduler_NoPassAtStartup(t *testing.T) {
	r := &fakeRunner{}
	startScheduler(t, r, 200*time.Millisecond)

	time.Sleep(60 * time.Millisecond)
	if got := r.calls.Load(); got != 0 {
		t.Fatalf("runner called %d times before the first interval, want 0", got)
	}
	time.Sleep(300 * time.Millisecond)
	if r.calls.Load() == 0 {
		t.Error("runner never called after the interval elapsed")
	}
}

func TestScheduler_ReturnsNilOnCancel(t *testing.T) {
	cancel, done := startScheduler(t, &fakeRunner{}, time.Hour)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run() = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

// A returned error would cancel main's errgroup and take the HTTP server down.
func TestScheduler_FailingAndRefusedPassesDoNotStopTheLoop(t *testing.T) {
	for name, err := range map[string]error{
		"error":       errors.New("db down"),
		"in progress": domain.ErrReconciliationInProgress{},
	} {
		t.Run(name, func(t *testing.T) {
			r := &fakeRunner{err: err}
			_, done := startScheduler(t, r, 15*time.Millisecond)

			time.Sleep(200 * time.Millisecond)
			select {
			case e := <-done:
				t.Fatalf("Run returned %v; it must keep ticking", e)
			default:
			}
			if got := r.calls.Load(); got < 3 {
				t.Errorf("runner called %d times, want several", got)
			}
		})
	}
}
