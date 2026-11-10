package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/shopspring/decimal"

	"github.com/ledger-platform/projection-service/internal/config"
	"github.com/ledger-platform/projection-service/internal/consumer"
	"github.com/ledger-platform/projection-service/internal/metrics"
	"github.com/ledger-platform/projection-service/internal/repository"
	"github.com/ledger-platform/projection-service/internal/sequence"
)

// seqEnv is one Postgres, one Kafka topic (one partition, so offsets count events) and ONE
// consumer, shared by the subtests so its position carries across them. Every subtest uses
// fresh wallets.
type seqEnv struct {
	t        *testing.T
	owner    *pgxpool.Pool
	app      *pgxpool.Pool
	rebuild  *repository.RebuildRepository
	consumer *consumer.LedgerPostedConsumer
	boot     string
	produced int64 // events produced so far; the next offset to be committed is produced-1
}

func newSeqEnv(t *testing.T, alertAfter time.Duration) *seqEnv {
	t.Helper()
	ownerDSN, appDSN := setupProjectionDB(t)
	boot := setupKafka(t)
	e := &seqEnv{t: t, owner: connectPool(t, ownerDSN), app: connectPool(t, appDSN), boot: boot}
	e.rebuild = repository.NewRebuildRepository(e.app)
	e.consumer = consumer.NewLedgerPostedConsumer(e.app, &config.Config{
		KafkaBrokers: []string{boot}, KafkaGroupID: "seq-test-group", PendingEntryAlertAfter: alertAfter,
	})
	if err := e.consumer.Connect(); err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	t.Cleanup(e.consumer.Close)
	return e
}

// deliver produces one event carrying the entries and waits until the consumer has processed it.
func (e *seqEnv) deliver(entries ...entryFixture) {
	e.t.Helper()
	produceLedgerPosted(e.t, e.boot, uuid.New(), time.Now().UTC(), entries, "")
	e.produced++
	e.waitProcessed()
}

func (e *seqEnv) waitProcessed() {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		tickCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_ = e.consumer.Tick(tickCtx)
		cancel()
		var off int64
		err := e.owner.QueryRow(context.Background(),
			`SELECT offset_committed FROM projection_db.projection_offsets WHERE consumer_group = 'seq-test-group'`).Scan(&off)
		if err == nil && off >= e.produced-1 {
			return
		}
	}
	e.t.Fatalf("consumer did not reach offset %d", e.produced-1)
}

func (e *seqEnv) pending(wallet uuid.UUID) int {
	e.t.Helper()
	var n int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM projection_db.pending_entries WHERE wallet_id = $1`, wallet).Scan(&n); err != nil {
		e.t.Fatalf("count pending: %v", err)
	}
	return n
}

func processed(result string) float64 {
	return testutil.ToFloat64(metrics.EventsProcessedTotal.WithLabelValues(result))
}

func TestEntrySequence(t *testing.T) {
	env := newSeqEnv(t, 500*time.Millisecond)
	ctx := context.Background()
	credit := func(wallet uuid.UUID, seq int64, amount string) entryFixture {
		return entryFixture{EntryID: uuid.New(), AccountID: wallet, EntryType: "CREDIT", Amount: amount, Seq: seq}
	}

	// The scenario the task exists for (Goal table): a rebuild while the consumer lags must not
	// let the lagging events double-apply.
	t.Run("a rebuild during consumer lag is not double-counted", func(t *testing.T) {
		wallet := uuid.New()
		base := time.Now().UTC().Add(-time.Hour)
		e1 := insertLedgerEntrySeq(t, env.owner, wallet, 1, "CREDIT", "100.00", base)
		e2 := insertLedgerEntrySeq(t, env.owner, wallet, 2, "CREDIT", "50.00", base.Add(time.Second))
		e3 := insertLedgerEntrySeq(t, env.owner, wallet, 3, "CREDIT", "25.00", base.Add(2*time.Second))

		env.deliver(entryFixture{EntryID: e1, AccountID: wallet, EntryType: "CREDIT", Amount: "100.00", Seq: 1})
		if got := walletBalance(t, env.app, wallet); got != "100.00" {
			t.Fatalf("after E1, balance = %s, want 100.00", got)
		}

		// E2 and E3 are still "in Kafka" while an operator rebuilds from the ledger.
		res, err := env.rebuild.Rebuild(ctx, wallet)
		if err != nil {
			t.Fatalf("Rebuild() = %v", err)
		}
		if res.Balance.StringFixed(2) != "175.00" {
			t.Fatalf("rebuilt balance = %s, want 175.00", res.Balance.StringFixed(2))
		}

		skippedBefore := processed("skipped")
		env.deliver(entryFixture{EntryID: e2, AccountID: wallet, EntryType: "CREDIT", Amount: "50.00", Seq: 2})
		env.deliver(entryFixture{EntryID: e3, AccountID: wallet, EntryType: "CREDIT", Amount: "25.00", Seq: 3})

		if got := walletBalance(t, env.app, wallet); got != "175.00" {
			t.Errorf("after the lagging events arrive, balance = %s, want 175.00 (the old last_entry_id guard gives 250.00)", got)
		}
		if got := processed("skipped") - skippedBefore; got != 2 {
			t.Errorf("skipped events = %v, want 2", got)
		}
		if got := lastSeqOf(t, env.app, wallet); got != 3 {
			t.Errorf("last_seq = %d, want 3", got)
		}
	})

	t.Run("out-of-order delivery parks, then converges", func(t *testing.T) {
		wallet := uuid.New()
		parkedBefore, gapsBefore, appliedBefore := processed("parked"), testutil.ToFloat64(metrics.SeqGapsTotal), processed("applied")

		env.deliver(credit(wallet, 3, "30.00"))
		env.deliver(credit(wallet, 2, "20.00"))
		if got := env.pending(wallet); got != 2 {
			t.Fatalf("after seq 3 and 2, pending = %d, want 2", got)
		}
		if got := lastSeqOf(t, env.app, wallet); got != 0 {
			t.Errorf("last_seq = %d, want 0 (nothing applied while seq 1 is missing)", got)
		}
		if got := walletBalance(t, env.app, wallet); got != "0.00" {
			t.Errorf("balance = %s, want 0.00 while everything is parked", got)
		}

		env.deliver(credit(wallet, 1, "10.00"))
		if got := walletBalance(t, env.app, wallet); got != "60.00" {
			t.Errorf("balance = %s, want 60.00 once seq 1 arrives", got)
		}
		if got := lastSeqOf(t, env.app, wallet); got != 3 {
			t.Errorf("last_seq = %d, want 3", got)
		}
		if got := env.pending(wallet); got != 0 {
			t.Errorf("pending = %d, want 0 after the drain", got)
		}
		if got := processed("parked") - parkedBefore; got != 2 {
			t.Errorf("parked events = %v, want 2", got)
		}
		if got := testutil.ToFloat64(metrics.SeqGapsTotal) - gapsBefore; got != 2 {
			t.Errorf("seq gaps = %v, want 2", got)
		}
		if got := processed("applied") - appliedBefore; got != 1 {
			t.Errorf("applied events = %v, want 1 (the arrival of seq 1, which drained the rest)", got)
		}
	})

	t.Run("a duplicate of a parked entry stays one row", func(t *testing.T) {
		wallet := uuid.New()
		gapsBefore := testutil.ToFloat64(metrics.SeqGapsTotal)
		dup := credit(wallet, 5, "5.00")
		env.deliver(dup)
		env.deliver(dup)
		if got := env.pending(wallet); got != 1 {
			t.Errorf("pending = %d, want 1", got)
		}
		if got := testutil.ToFloat64(metrics.SeqGapsTotal) - gapsBefore; got != 1 {
			t.Errorf("seq gaps = %v, want 1 (a redelivery of a parked entry is not a new gap)", got)
		}
	})

	t.Run("a rebuild discards covered parked entries and drains contiguous ones", func(t *testing.T) {
		wallet := uuid.New()
		base := time.Now().UTC().Add(-time.Hour)
		for seq := int64(1); seq <= 3; seq++ {
			insertLedgerEntrySeq(t, env.owner, wallet, seq, "CREDIT", "10.00", base.Add(time.Duration(seq)*time.Second))
		}
		// Parked as the consumer would have left them: seq 2 is covered by the rebuild, seq 4 is
		// contiguous with it, seq 6 is still behind a gap (seq 5 is missing).
		for _, p := range []struct {
			seq   int64
			delta string
		}{{2, "10.00"}, {4, "7.00"}, {6, "100.00"}} {
			if _, err := env.owner.Exec(ctx, `INSERT INTO projection_db.pending_entries (wallet_id, seq, entry_id, delta) VALUES ($1, $2, $3, $4::numeric)`,
				wallet, p.seq, uuid.New(), p.delta); err != nil {
				t.Fatalf("park seq %d: %v", p.seq, err)
			}
		}

		res, err := env.rebuild.Rebuild(ctx, wallet)
		if err != nil {
			t.Fatalf("Rebuild() = %v", err)
		}
		if res.Balance.StringFixed(2) != "37.00" || res.EntriesApplied != 4 {
			t.Errorf("Result = balance %s applied %d, want 37.00 / 4 (3 replayed + seq 4 drained)", res.Balance.StringFixed(2), res.EntriesApplied)
		}
		if got := walletBalance(t, env.app, wallet); got != "37.00" {
			t.Errorf("balance = %s, want 37.00", got)
		}
		if got := lastSeqOf(t, env.app, wallet); got != 4 {
			t.Errorf("last_seq = %d, want 4", got)
		}
		if got := env.pending(wallet); got != 1 {
			t.Errorf("pending = %d, want 1 (only seq 6 remains)", got)
		}
	})

	t.Run("a v1 event is deduplicated via the ledger lookup", func(t *testing.T) {
		wallet := uuid.New()
		base := time.Now().UTC().Add(-time.Hour)
		e1 := insertLedgerEntrySeq(t, env.owner, wallet, 1, "CREDIT", "40.00", base)
		e2 := insertLedgerEntrySeq(t, env.owner, wallet, 2, "CREDIT", "2.00", base.Add(time.Second))

		v1 := func(id uuid.UUID, amount string) entryFixture {
			return entryFixture{EntryID: id, AccountID: wallet, EntryType: "CREDIT", Amount: amount, V1: true}
		}
		env.deliver(v1(e1, "40.00"))
		env.deliver(v1(e1, "40.00")) // redelivery
		env.deliver(v1(e2, "2.00"))
		if got := walletBalance(t, env.app, wallet); got != "42.00" {
			t.Errorf("balance = %s, want 42.00 (the redelivered v1 entry must not apply twice)", got)
		}
		if got := lastSeqOf(t, env.app, wallet); got != 2 {
			t.Errorf("last_seq = %d, want 2", got)
		}
	})

	t.Run("a gap that never fills is reported, and a rebuild clears it", func(t *testing.T) {
		wallet := uuid.New()
		var logs bytes.Buffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
		t.Cleanup(func() { slog.SetDefault(prev) })

		env.deliver(credit(wallet, 2, "20.00")) // seq 1 never arrives
		env.consumer.ReportPending(ctx)
		if bytes.Contains(logs.Bytes(), []byte("gap did not fill")) {
			t.Fatal("reported a gap before the alert threshold elapsed")
		}
		if got := testutil.ToFloat64(metrics.PendingEntries); got < 1 {
			t.Errorf("projection_pending_entries = %v, want >= 1", got)
		}

		time.Sleep(700 * time.Millisecond) // alertAfter is 500ms in this test
		env.consumer.ReportPending(ctx)

		var found bool
		for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
			var rec map[string]any
			if json.Unmarshal(line, &rec) != nil {
				continue
			}
			if rec["wallet_id"] == wallet.String() && rec["level"] == "ERROR" {
				found = true
				if rec["missing_seq"] != float64(1) {
					t.Errorf("missing_seq = %v, want 1", rec["missing_seq"])
				}
			}
		}
		if !found {
			t.Fatalf("no error log naming wallet %s; logs:\n%s", wallet, logs.String())
		}

		// The ledger does have both entries (the event for seq 1 was lost): a rebuild repairs it.
		base := time.Now().UTC().Add(-time.Hour)
		insertLedgerEntrySeq(t, env.owner, wallet, 1, "CREDIT", "10.00", base)
		insertLedgerEntrySeq(t, env.owner, wallet, 2, "CREDIT", "20.00", base.Add(time.Second))
		if _, err := env.rebuild.Rebuild(ctx, wallet); err != nil {
			t.Fatalf("Rebuild() = %v", err)
		}
		if got := env.pending(wallet); got != 0 {
			t.Errorf("pending after rebuild = %d, want 0", got)
		}
		if got := walletBalance(t, env.app, wallet); got != "30.00" {
			t.Errorf("balance after rebuild = %s, want 30.00", got)
		}
	})
}

// TestEntrySequence_RebuildsRaceTheConsumer grows one wallet's ledger the way production does --
// commit the entry, THEN publish its event -- slightly out of order, while rebuilds of the wallet
// run continuously. Rebuilds therefore keep landing mid-stream: some events are already covered by
// one (must be skipped), some are not (must apply or park), and a consumer that decides on a
// stale last_seq would double-count. Every entry must count exactly once.
func TestEntrySequence_RebuildsRaceTheConsumer(t *testing.T) {
	env := newSeqEnv(t, 30*time.Second)
	ctx := context.Background()
	const n = 40

	wallet := uuid.New()
	base := time.Now().UTC().Add(-time.Hour)
	commit := func(seq int) uuid.UUID {
		return insertLedgerEntrySeq(t, env.owner, wallet, int64(seq), "CREDIT", "3.00", base.Add(time.Duration(seq)*time.Second))
	}
	publish := func(seq int, id uuid.UUID) {
		produceLedgerPosted(t, env.boot, uuid.New(), time.Now().UTC(), []entryFixture{
			{EntryID: id, AccountID: wallet, EntryType: "CREDIT", Amount: "3.00", Seq: int64(seq)},
		}, "")
		env.produced++
	}

	// Invariant checker: every entry is a 3.00 credit applied strictly in seq order, so in EVERY
	// committed state balance == 3 x last_seq. A consumer that decided on a stale last_seq (no
	// row lock) and applied on top of a rebuild breaks it for a moment even if a later rebuild
	// repairs the end state, so the end state alone cannot prove exclusion.
	stop := make(chan struct{})
	rebuilds, violations := make(chan int, 1), make(chan []string, 1)
	go func() {
		count := 0
		for {
			select {
			case <-stop:
				rebuilds <- count
				return
			default:
			}
			if _, err := env.rebuild.Rebuild(ctx, wallet); err != nil {
				t.Errorf("Rebuild() = %v", err)
			}
			count++
		}
	}()
	go func() {
		var bad []string
		for {
			select {
			case <-stop:
				violations <- bad
				return
			default:
			}
			var bal string
			var seq int64
			if err := env.owner.QueryRow(ctx,
				`SELECT balance::text, last_seq FROM projection_db.wallet_balances WHERE wallet_id = $1`, wallet).Scan(&bal, &seq); err != nil {
				continue
			}
			if bal != decimalString(3*seq) && len(bad) < 5 {
				bad = append(bad, "balance "+bal+" with last_seq "+decimalString(seq))
			}
		}
	}()
	// Commit and publish in pairs, publishing each pair reversed (2,1,4,3,...): events arrive out
	// of order, and every event is published only after its entry is committed.
	for seq := 1; seq <= n; seq += 2 {
		first, second := commit(seq), commit(seq+1)
		publish(seq+1, second)
		publish(seq, first)
	}
	env.waitProcessed()
	close(stop)
	t.Logf("%d rebuilds ran while %d events were consumed", <-rebuilds, n)
	if bad := <-violations; len(bad) > 0 {
		t.Errorf("balance != 3 x last_seq in a committed state: %v", bad)
	}

	if got := walletBalance(t, env.app, wallet); got != decimalString(3*n) {
		t.Errorf("balance = %s, want %s: an entry was double-counted or lost", got, decimalString(3*n))
	}
	if got := lastSeqOf(t, env.app, wallet); got != n {
		t.Errorf("last_seq = %d, want %d", got, n)
	}
	if got := env.pending(wallet); got != 0 {
		t.Errorf("pending = %d, want 0", got)
	}
}

// decimalString renders a whole number as a 2dp amount, e.g. 9 -> "9.00".
func decimalString(n int64) string { return fmt.Sprintf("%d.00", n) }

// TestEntrySequence_WalletRowLockExcludesRebuildAndOtherAppliers is the deterministic proof of
// "lock first": while one transaction holds the wallet's row lock (as the consumer does between
// reading last_seq and writing), both a rebuild and a second applier must wait. Without the lock
// both could read the same last_seq and apply the same seq.
func TestEntrySequence_WalletRowLockExcludesRebuildAndOtherAppliers(t *testing.T) {
	ownerDSN, appDSN := setupProjectionDB(t)
	owner, app := connectPool(t, ownerDSN), connectPool(t, appDSN)
	ctx := context.Background()
	rebuild := repository.NewRebuildRepository(app)

	wallet := uuid.New()
	base := time.Now().UTC().Add(-time.Hour)
	insertLedgerEntrySeq(t, owner, wallet, 1, "CREDIT", "10.00", base)
	// The wallet's row must already be COMMITTED: if the holder's own uncommitted INSERT created it,
	// the second party would block on that insert instead of on the FOR UPDATE row lock under test.
	seedWalletBalance(t, owner, wallet, "0.00")

	waitLockWaiter := func() {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			var n int
			if err := owner.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock'`).Scan(&n); err == nil && n > 0 {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("nobody is waiting on the wallet row lock: the second party is not excluded")
	}
	assertBlocked := func(done <-chan error, what string) {
		t.Helper()
		select {
		case err := <-done:
			t.Fatalf("%s finished (err=%v) while another transaction held the wallet's row lock", what, err)
		case <-time.After(300 * time.Millisecond):
		}
	}

	t.Run("rebuild waits for the lock holder", func(t *testing.T) {
		holder, err := app.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer holder.Rollback(ctx) //nolint:errcheck
		if _, err := sequence.LockWallet(ctx, holder, wallet); err != nil {
			t.Fatalf("LockWallet() = %v", err)
		}

		done := make(chan error, 1)
		go func() { _, err := rebuild.Rebuild(ctx, wallet); done <- err }()
		waitLockWaiter()
		assertBlocked(done, "Rebuild")

		if err := holder.Commit(ctx); err != nil {
			t.Fatalf("commit holder: %v", err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Rebuild() = %v after the lock was released", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Rebuild never finished after the lock was released")
		}
	})

	t.Run("a second applier waits for the lock holder", func(t *testing.T) {
		holder, err := app.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer holder.Rollback(ctx) //nolint:errcheck
		if _, err := sequence.LockWallet(ctx, holder, wallet); err != nil {
			t.Fatalf("LockWallet() = %v", err)
		}

		done := make(chan error, 1)
		go func() {
			tx, err := app.Begin(ctx)
			if err != nil {
				done <- err
				return
			}
			defer tx.Rollback(ctx) //nolint:errcheck
			_, err = sequence.ApplyEntry(ctx, tx, wallet, uuid.New(), 2, decimal.RequireFromString("1.00"))
			if err == nil {
				err = tx.Commit(ctx)
			}
			done <- err
		}()
		waitLockWaiter()
		assertBlocked(done, "ApplyEntry")

		if err := holder.Commit(ctx); err != nil {
			t.Fatalf("commit holder: %v", err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("ApplyEntry() = %v after the lock was released", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("ApplyEntry never finished after the lock was released")
		}
	})
}
