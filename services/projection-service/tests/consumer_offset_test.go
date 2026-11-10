package tests

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/ledger-platform/projection-service/internal/config"
	"github.com/ledger-platform/projection-service/internal/consumer"
)

// Task 08 Finding 2: a failed apply used to leave its offset uncommitted while the loop moved on,
// and a Kafka commit is a per-partition high-water mark -- so the next successful record on the
// partition committed past the failed one, which was never redelivered. These tests fail on
// that behaviour: the blocked wallet ended with no row, even after a consumer restart.

// blockWallet makes applies to wallet fail transiently: an uncommitted owner INSERT of its row
// makes the consumer's upsert wait, and a short lock_timeout on projection_app turns the wait
// into an error. Rolling back the returned transaction clears the fault.
func blockWallet(t *testing.T, ctx context.Context, ownerDSN string, wallet uuid.UUID) pgx.Tx {
	t.Helper()
	conn, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("connect as owner: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	if _, err := conn.Exec(ctx, `ALTER ROLE projection_app SET lock_timeout = '300ms'`); err != nil {
		t.Fatalf("set lock_timeout: %v", err)
	}
	holder, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin holder: %v", err)
	}
	if _, err := holder.Exec(ctx, `INSERT INTO projection_db.wallet_balances (wallet_id, balance) VALUES ($1, 0)`, wallet); err != nil {
		t.Fatalf("hold wallet row: %v", err)
	}
	return holder
}

// tickN ticks with a generous context: a fresh consumer spends the start of its first Tick
// joining the group, which must not eat into the apply under test.
func tickN(c *consumer.LedgerPostedConsumer, n int) {
	for i := 0; i < n; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_ = c.Tick(ctx)
		cancel()
	}
}

func committedOffset(t *testing.T, ctx context.Context, bootstrap, group string, partition int32) int64 {
	t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(bootstrap))
	if err != nil {
		t.Fatalf("admin client: %v", err)
	}
	defer cl.Close()
	offsets, err := kadm.NewClient(cl).FetchOffsets(ctx, group)
	if err != nil {
		t.Fatalf("fetch offsets: %v", err)
	}
	o, ok := offsets.Lookup("ledger.posted.v1", partition)
	if !ok {
		return -1
	}
	return o.At
}

// TestConsumer_FailedApply_StopsThePartitionAndRetries: two events on one partition, the first
// failing transiently. The partition must stop at the first (the second, healthy one waits behind
// it, nothing is committed past it), resume on its own once the fault clears -- no restart -- and a
// later restart must not apply anything twice.
func TestConsumer_FailedApply_StopsThePartitionAndRetries(t *testing.T) {
	ownerDSN, appDSN := setupProjectionDB(t)
	bootstrap := setupKafka(t) // one partition, so both events share it
	ctx := context.Background()

	blocked, healthy := uuid.New(), uuid.New()
	produceLedgerPosted(t, bootstrap, uuid.New(), time.Now().UTC(), []entryFixture{
		{EntryID: uuid.New(), AccountID: blocked, EntryType: "CREDIT", Amount: "10.00"},
	}, "")
	produceLedgerPosted(t, bootstrap, uuid.New(), time.Now().UTC(), []entryFixture{
		{EntryID: uuid.New(), AccountID: healthy, EntryType: "CREDIT", Amount: "20.00"},
	}, "")
	holder := blockWallet(t, ctx, ownerDSN, blocked)
	pool := connectPool(t, appDSN)

	cfg := &config.Config{KafkaBrokers: []string{bootstrap}, KafkaGroupID: "offset-test-group", PendingEntryAlertAfter: time.Minute}
	c := consumer.NewLedgerPostedConsumer(pool, cfg)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	tickN(c, 3) // each tick retries the first event and fails on the lock

	if got := walletBalance(t, pool, healthy); got != "<no row>" {
		t.Errorf("healthy wallet = %s while the event before it is failing, want <no row>: the partition moved past a failed record", got)
	}
	if got := committedOffset(t, ctx, bootstrap, "offset-test-group", 0); got > 0 {
		t.Errorf("committed offset = %d, want nothing committed past the failed record at offset 0", got)
	}

	// The fault clears: the same consumer, without a restart, delivers the failed event again.
	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("release holder: %v", err)
	}
	for i := 0; i < 5 && walletBalance(t, pool, healthy) != "20.00"; i++ {
		tickN(c, 1)
	}
	if got := walletBalance(t, pool, blocked); got != "10.00" {
		t.Errorf("blocked wallet = %s after the fault cleared, want 10.00", got)
	}
	if got := walletBalance(t, pool, healthy); got != "20.00" {
		t.Errorf("healthy wallet = %s after the fault cleared, want 20.00", got)
	}
	c.Close()

	// A restart resumes from the committed offset: nothing is redelivered and nothing doubles.
	restarted := consumer.NewLedgerPostedConsumer(pool, cfg)
	if err := restarted.Connect(); err != nil {
		t.Fatalf("Connect() (restart) = %v", err)
	}
	t.Cleanup(restarted.Close)
	tickN(restarted, 2)
	if b, h := walletBalance(t, pool, blocked), walletBalance(t, pool, healthy); b != "10.00" || h != "20.00" {
		t.Errorf("after restart: blocked=%s healthy=%s, want 10.00 / 20.00", b, h)
	}
}

// TestConsumer_FailedApply_OtherPartitionsKeepFlowing: stopping a partition must not stop the
// consumer. Partition 0 holds a failing event and a healthy one behind it; partition 1 holds a
// healthy event. Partition 1 applies while partition 0 waits; everything applies once the fault
// clears.
func TestConsumer_FailedApply_OtherPartitionsKeepFlowing(t *testing.T) {
	ownerDSN, appDSN := setupProjectionDB(t)
	bootstrap := setupKafka(t)
	ctx := context.Background()

	adminCl, err := kgo.NewClient(kgo.SeedBrokers(bootstrap))
	if err != nil {
		t.Fatalf("admin client: %v", err)
	}
	if _, err := kadm.NewClient(adminCl).UpdatePartitions(ctx, 2, "ledger.posted.v1"); err != nil {
		t.Fatalf("add a partition: %v", err)
	}
	adminCl.Close()

	blocked, behind, other := uuid.New(), uuid.New(), uuid.New()
	credit := func(w uuid.UUID, amount string) []entryFixture {
		return []entryFixture{{EntryID: uuid.New(), AccountID: w, EntryType: "CREDIT", Amount: amount}}
	}
	produceLedgerPostedToPartition(t, bootstrap, 0, credit(blocked, "10.00"))
	produceLedgerPostedToPartition(t, bootstrap, 0, credit(behind, "20.00"))
	produceLedgerPostedToPartition(t, bootstrap, 1, credit(other, "30.00"))
	holder := blockWallet(t, ctx, ownerDSN, blocked)
	pool := connectPool(t, appDSN)

	c := consumer.NewLedgerPostedConsumer(pool, &config.Config{
		KafkaBrokers: []string{bootstrap}, KafkaGroupID: "two-partition-group", PendingEntryAlertAfter: time.Minute,
	})
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	t.Cleanup(c.Close)

	for i := 0; i < 5 && walletBalance(t, pool, other) != "30.00"; i++ {
		tickN(c, 1)
	}
	if got := walletBalance(t, pool, other); got != "30.00" {
		t.Fatalf("partition 1's wallet = %s, want 30.00: a failure on partition 0 must not stop partition 1", got)
	}
	tickN(c, 1)
	if got := walletBalance(t, pool, behind); got != "<no row>" {
		t.Errorf("wallet behind the failure on partition 0 = %s, want <no row>", got)
	}

	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("release holder: %v", err)
	}
	for i := 0; i < 5 && walletBalance(t, pool, behind) != "20.00"; i++ {
		tickN(c, 1)
	}
	if b, h := walletBalance(t, pool, blocked), walletBalance(t, pool, behind); b != "10.00" || h != "20.00" {
		t.Errorf("after the fault cleared: blocked=%s behind=%s, want 10.00 / 20.00", b, h)
	}
}
