package tests

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ledger-platform/projection-service/internal/config"
	"github.com/ledger-platform/projection-service/internal/consumer"
)

// TestConsumer_TransientFailure_IsRedeliveredAfterRestart is Task 08's Finding 2: a failed apply
// leaves its offset uncommitted but the loop moves on to the next record, and a Kafka commit is a
// per-partition high-water mark. If the next record on the same partition then succeeds and is
// committed, the failed one is behind the committed offset and is never redelivered.
//
// The topic has one partition, so both events share it. The first event's wallet row is blocked
// behind an uncommitted owner INSERT with a short lock_timeout on the consumer's role, so that
// apply fails transiently; the second event is unaffected.
//
// Reproduced (Task 08, Step 0): the blocked wallet has no row after the restart. The test asserts
// the CORRECT behaviour and is skipped until the consumer loop stops committing past a failed
// record. Until then Task 08's gap detection makes the loss visible and a rebuild repairs it.
func TestConsumer_TransientFailure_IsRedeliveredAfterRestart(t *testing.T) {
	t.Skip("known defect: see Task 08 Finding 2 -- a failed apply is committed past by the next record on the partition")

	ownerDSN, appDSN := setupProjectionDB(t)
	bootstrap := setupKafka(t)
	ctx := context.Background()

	// Set on the role (new sessions pick it up) so the blocked apply fails fast with a lock timeout
	// instead of consuming the whole tick's context deadline.
	setup, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("connect as owner: %v", err)
	}
	if _, err := setup.Exec(ctx, `ALTER ROLE projection_app SET lock_timeout = '300ms'`); err != nil {
		t.Fatalf("set lock_timeout: %v", err)
	}
	_ = setup.Close(ctx)
	pool := connectPool(t, appDSN)

	blocked, healthy := uuid.New(), uuid.New()
	produceLedgerPosted(t, bootstrap, uuid.New(), time.Now().UTC(), []entryFixture{
		{EntryID: uuid.New(), AccountID: blocked, EntryType: "CREDIT", Amount: "10.00"},
	}, "")
	produceLedgerPosted(t, bootstrap, uuid.New(), time.Now().UTC(), []entryFixture{
		{EntryID: uuid.New(), AccountID: healthy, EntryType: "CREDIT", Amount: "20.00"},
	}, "")

	// An uncommitted owner INSERT of the blocked wallet's row: the consumer's upsert of the
	// same key waits on it until lock_timeout.
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("connect as owner: %v", err)
	}
	defer func() { _ = owner.Close(ctx) }()
	holder, err := owner.Begin(ctx)
	if err != nil {
		t.Fatalf("begin holder: %v", err)
	}
	if _, err := holder.Exec(ctx, `INSERT INTO projection_db.wallet_balances (wallet_id, balance) VALUES ($1, 0)`, blocked); err != nil {
		t.Fatalf("hold blocked wallet row: %v", err)
	}

	cfg := &config.Config{KafkaBrokers: []string{bootstrap}, KafkaGroupID: "offset-test-group"}
	first := consumer.NewLedgerPostedConsumer(pool, cfg)
	if err := first.Connect(); err != nil {
		t.Fatalf("Connect() = %v", err)
	}
	// A fresh consumer spends the start of its first Tick joining the group; the generous
	// context keeps that from eating into the apply, so the lock timeout is the only fault.
	tick := func(c *consumer.LedgerPostedConsumer) {
		tickCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		_ = c.Tick(tickCtx)
	}
	for i := 0; i < 5 && walletBalance(t, pool, healthy) != "20.00"; i++ {
		tick(first)
	}
	if got := walletBalance(t, pool, healthy); got != "20.00" {
		t.Fatalf("healthy wallet balance = %s, want 20.00 (the second event must apply)", got)
	}
	first.Close()

	// The transient fault clears.
	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("release holder: %v", err)
	}

	// A restarted consumer in the same group resumes from the committed offset.
	second := consumer.NewLedgerPostedConsumer(pool, cfg)
	if err := second.Connect(); err != nil {
		t.Fatalf("Connect() (restart) = %v", err)
	}
	t.Cleanup(second.Close)
	for i := 0; i < 3 && walletBalance(t, pool, blocked) != "10.00"; i++ {
		tick(second)
	}

	if got := walletBalance(t, pool, blocked); got != "10.00" {
		t.Errorf("blocked wallet balance after restart = %s, want 10.00: the failed event was committed past and never redelivered", got)
	}
}
