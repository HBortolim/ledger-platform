package tests

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ledger-platform/ledger-service/internal/domain"
	"github.com/ledger-platform/ledger-service/internal/repository"
	"github.com/ledger-platform/ledger-service/internal/service"
)

// overlapProbe wraps the real repository and records how many passes are inside their
// checks at once. A pass is "inside" from its first check to its last.
type overlapProbe struct {
	*repository.ReconciliationRepository
	active, maxActive *atomic.Int32
	hold              time.Duration // widens each pass so overlaps have a chance to happen
}

func (p overlapProbe) UnbalancedTransactions(ctx context.Context, w time.Duration, limit int) (domain.InvariantFindings, error) {
	n := p.active.Add(1)
	for {
		m := p.maxActive.Load()
		if n <= m || p.maxActive.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(p.hold)
	return p.ReconciliationRepository.UnbalancedTransactions(ctx, w, limit)
}

func (p overlapProbe) MoneySupply(ctx context.Context) (domain.MoneySupply, error) {
	defer p.active.Add(-1)
	return p.ReconciliationRepository.MoneySupply(ctx)
}

func newSizedPool(t *testing.T, dsn string, maxConns int32) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.MaxConns = maxConns
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestReconciliationStress_ConcurrentPassesNeverOverlap fires many concurrent passes from two
// "replicas" (two services, each over its own pool). The advisory lock must let exactly one
// pass run its checks at a time, refuse the rest with ErrReconciliationInProgress, and leave no
// lock behind.
func TestReconciliationStress_ConcurrentPassesNeverOverlap(t *testing.T) {
	f, _, appDSN := newReconFixture(t)
	ctx := context.Background()

	var active, maxActive atomic.Int32
	opts := service.ReconciliationOptions{InvariantWindow: time.Hour, DriftGrace: 10 * time.Second, PassTimeout: time.Minute}
	replicas := []*service.ReconciliationService{
		service.NewReconciliationService(overlapProbe{repository.NewReconciliationRepository(newSizedPool(t, appDSN, 30)), &active, &maxActive, 20 * time.Millisecond}, opts),
		service.NewReconciliationService(overlapProbe{repository.NewReconciliationRepository(newSizedPool(t, appDSN, 30)), &active, &maxActive, 20 * time.Millisecond}, opts),
	}

	const goroutines, rounds = 24, 10
	var ok, refused atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			svc := replicas[g%2]
			<-start
			for r := 0; r < rounds; r++ {
				_, err := svc.Run(ctx)
				var inProgress domain.ErrReconciliationInProgress
				switch {
				case err == nil:
					ok.Add(1)
				case errors.As(err, &inProgress):
					refused.Add(1)
				default:
					t.Errorf("Run() = %v, want nil or ErrReconciliationInProgress", err)
				}
			}
		}(g)
	}
	close(start)
	wg.Wait()

	if got := maxActive.Load(); got != 1 {
		t.Errorf("max concurrent passes = %d, want 1", got)
	}
	if ok.Load() == 0 || refused.Load() == 0 {
		t.Errorf("ok=%d refused=%d, want both > 0 (otherwise the test never contended)", ok.Load(), refused.Load())
	}
	t.Logf("%d passes ran, %d refused", ok.Load(), refused.Load())

	var locks int
	if err := f.owner.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory'`).Scan(&locks); err != nil {
		t.Fatalf("count advisory locks: %v", err)
	}
	if locks != 0 {
		t.Errorf("%d advisory locks left after the storm", locks)
	}
	if _, err := replicas[0].Run(ctx); err != nil {
		t.Errorf("a pass after the storm = %v, want it to run", err)
	}
}

// TestReconciliationStress_NoFalsePositivesUnderConcurrentPostings runs full passes in a loop
// while writers commit deposits and transfers. Every pass must be clean: a posting commits its
// header, entries and cached balances atomically, so no committed state is unbalanced. Check 2
// is not exercised here (there is no consumer; the long grace hides every fresh entry) -- it
// needs a live projection consumer, which is Task 07's e2e.
func TestReconciliationStress_NoFalsePositivesUnderConcurrentPostings(t *testing.T) {
	f, _, appDSN := newReconFixture(t)
	ctx := context.Background()

	writePool := newSizedPool(t, appDSN, 20)
	posting := repository.NewPostingRepository(writePool, testDailyCap, systemFundingAccountID)
	svc := service.NewReconciliationService(f.repo, service.ReconciliationOptions{
		InvariantWindow: time.Hour, DriftGrace: time.Minute, PassTimeout: time.Minute,
	})

	wallets := make([]uuid.UUID, 6)
	for i := range wallets {
		wallets[i] = uuid.New()
	}

	runCtx, stop := context.WithTimeout(ctx, 4*time.Second)
	defer stop()

	var posted atomic.Int32
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; runCtx.Err() == nil; i++ {
				var tx domain.LedgerTransaction
				if i%3 == 0 {
					tx = newBalancedTransfer(systemFundingAccountID, wallets[(w+i)%len(wallets)], "10.00")
				} else {
					tx = newBalancedTransfer(wallets[(w+i)%len(wallets)], wallets[(w+i+1)%len(wallets)], "1.00")
				}
				err := posting.Post(ctx, tx)
				var insufficient domain.ErrInsufficientFunds
				switch {
				case err == nil:
					posted.Add(1)
				case errors.As(err, &insufficient):
				default:
					t.Errorf("Post() = %v", err)
				}
			}
		}(w)
	}

	passes := 0
	for runCtx.Err() == nil {
		report, err := svc.Run(ctx)
		if err != nil {
			t.Fatalf("Run() = %v", err)
		}
		passes++
		if !report.Clean() {
			t.Fatalf("pass %d reported drift under healthy load: invariant=%+v projection=%+v supply=%+v",
				passes, report.Invariant, report.Projection, report.MoneySupply)
		}
	}
	wg.Wait()

	if posted.Load() < 100 || passes < 10 {
		t.Fatalf("posted=%d passes=%d: not enough overlap to mean anything", posted.Load(), passes)
	}
	t.Logf("%d postings committed during %d clean passes", posted.Load(), passes)

	// Settled: the ledger and caches must still net to zero exactly.
	supply, err := f.repo.MoneySupply(ctx)
	if err != nil || !supply.Clean() {
		t.Errorf("after the load: supply=%+v err=%v, want clean", supply, err)
	}
}
