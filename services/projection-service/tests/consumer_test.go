package tests

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/ledger-platform/projection-service/internal/config"
	"github.com/ledger-platform/projection-service/internal/consumer"
	"github.com/ledger-platform/projection-service/internal/metrics"
)

// TestConsumer_AppliesLedgerPosted_ToWalletBalances produces a real
// LEDGER_POSTED transfer event and asserts the consumer applies both
// entries to projection_db.wallet_balances and records its offset.
func TestConsumer_AppliesLedgerPosted_ToWalletBalances(t *testing.T) {
	_, appDSN := setupProjectionDB(t)
	bootstrap := setupKafka(t)
	pool := connectPool(t, appDSN)

	debitWallet, creditWallet := uuid.New(), uuid.New()
	txID := uuid.New()
	produceLedgerPosted(t, bootstrap, txID, time.Now().UTC(), []entryFixture{
		{EntryID: uuid.New(), AccountID: debitWallet, EntryType: "DEBIT", Amount: "100.00"},
		{EntryID: uuid.New(), AccountID: creditWallet, EntryType: "CREDIT", Amount: "100.00"},
	})

	c := consumer.NewLedgerPostedConsumer(pool, &config.Config{
		KafkaBrokers: []string{bootstrap},
		KafkaGroupID: "test-apply-group",
	})
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect() = error %v, want nil", err)
	}
	t.Cleanup(c.Close)

	tickUntil(t, c, func() bool {
		return walletBalance(t, pool, debitWallet) == "-100.00" && walletBalance(t, pool, creditWallet) == "100.00"
	})

	var group, topic string
	var partition int32
	var offset int64
	err := pool.QueryRow(context.Background(),
		`SELECT consumer_group, topic, partition, offset_committed FROM projection_db.projection_offsets WHERE consumer_group = $1`,
		"test-apply-group",
	).Scan(&group, &topic, &partition, &offset)
	if err != nil {
		t.Fatalf("query projection_offsets: %v", err)
	}
	if topic != "ledger.posted.v1" {
		t.Errorf("offsets row topic = %q, want ledger.posted.v1", topic)
	}
	if offset != 0 {
		t.Errorf("offsets row offset_committed = %d, want 0 (first and only message)", offset)
	}
}

// TestConsumer_DuplicateDelivery_IsNoOp redelivers the same LEDGER_POSTED
// message to a second consumer instance in a fresh group (which — per
// SPEC.md §9.6/§9.8 — must independently re-fetch the same record from the
// start of the topic) and asserts the balance-and-metrics effect of
// applying it is exactly a no-op the second time.
func TestConsumer_DuplicateDelivery_IsNoOp(t *testing.T) {
	_, appDSN := setupProjectionDB(t)
	bootstrap := setupKafka(t)
	pool := connectPool(t, appDSN)

	debitWallet, creditWallet := uuid.New(), uuid.New()
	txID := uuid.New()
	produceLedgerPosted(t, bootstrap, txID, time.Now().UTC(), []entryFixture{
		{EntryID: uuid.New(), AccountID: debitWallet, EntryType: "DEBIT", Amount: "50.00"},
		{EntryID: uuid.New(), AccountID: creditWallet, EntryType: "CREDIT", Amount: "50.00"},
	})

	first := consumer.NewLedgerPostedConsumer(pool, &config.Config{
		KafkaBrokers: []string{bootstrap}, KafkaGroupID: "dup-test-first",
	})
	if err := first.Connect(); err != nil {
		t.Fatalf("Connect() (first) = error %v, want nil", err)
	}
	t.Cleanup(first.Close)
	tickUntil(t, first, func() bool {
		return walletBalance(t, pool, debitWallet) == "-50.00" && walletBalance(t, pool, creditWallet) == "50.00"
	})

	skippedBefore := testutil.ToFloat64(metrics.EventsProcessedTotal.WithLabelValues("skipped"))

	// A second, independent consumer group has never committed an offset for
	// this topic, so per Connect()'s AtStart() reset it redelivers the same
	// message from the beginning — a faithful reproduction of a Kafka
	// at-least-once redelivery, not a hand-rolled duplicate call.
	second := consumer.NewLedgerPostedConsumer(pool, &config.Config{
		KafkaBrokers: []string{bootstrap}, KafkaGroupID: "dup-test-second",
	})
	if err := second.Connect(); err != nil {
		t.Fatalf("Connect() (second) = error %v, want nil", err)
	}
	t.Cleanup(second.Close)
	tickUntil(t, second, func() bool {
		return testutil.ToFloat64(metrics.EventsProcessedTotal.WithLabelValues("skipped")) >= skippedBefore+1
	})

	if got := testutil.ToFloat64(metrics.EventsProcessedTotal.WithLabelValues("skipped")); got != skippedBefore+1 {
		t.Errorf("EventsProcessedTotal{result=skipped} after one redelivered 2-entry event = %v, want exactly %v (one increment per event, not per entry)", got, skippedBefore+1)
	}

	if got := walletBalance(t, pool, debitWallet); got != "-50.00" {
		t.Errorf("debit wallet balance after redelivery = %s, want -50.00 (must not double-apply)", got)
	}
	if got := walletBalance(t, pool, creditWallet); got != "50.00" {
		t.Errorf("credit wallet balance after redelivery = %s, want 50.00 (must not double-apply)", got)
	}
}

// TestConsumer_PoisonMessage_CountsAndContinues publishes an undecodable
// record followed by a valid one, and asserts the consumer counts the
// poison message as an error, commits past it, and still applies the valid
// message that follows — a stuck partition would fail this test's deadline.
func TestConsumer_PoisonMessage_CountsAndContinues(t *testing.T) {
	_, appDSN := setupProjectionDB(t)
	bootstrap := setupKafka(t)
	pool := connectPool(t, appDSN)

	producer, err := kgo.NewClient(kgo.SeedBrokers(bootstrap), kgo.ClientID("poison-test-producer"))
	if err != nil {
		t.Fatalf("kgo.NewClient() = error %v, want nil", err)
	}
	defer producer.Close()
	produceCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	res := producer.ProduceSync(produceCtx, &kgo.Record{
		Topic: "ledger.posted.v1",
		Key:   []byte("poison"),
		Value: []byte("{not valid json"),
	})
	cancel()
	if err := res.FirstErr(); err != nil {
		t.Fatalf("produce poison message: %v", err)
	}

	wallet := uuid.New()
	produceLedgerPosted(t, bootstrap, uuid.New(), time.Now().UTC(), []entryFixture{
		{EntryID: uuid.New(), AccountID: wallet, EntryType: "CREDIT", Amount: "10.00"},
	})

	errorsBefore := testutil.ToFloat64(metrics.EventsProcessedTotal.WithLabelValues("error"))

	c := consumer.NewLedgerPostedConsumer(pool, &config.Config{
		KafkaBrokers: []string{bootstrap}, KafkaGroupID: "poison-test-group",
	})
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect() = error %v, want nil", err)
	}
	t.Cleanup(c.Close)

	tickUntil(t, c, func() bool {
		return walletBalance(t, pool, wallet) == "10.00"
	})

	if got := testutil.ToFloat64(metrics.EventsProcessedTotal.WithLabelValues("error")); got < errorsBefore+1 {
		t.Errorf("EventsProcessedTotal{result=error} = %v, want >= %v (poison message counted)", got, errorsBefore+1)
	}
}

// tickUntil calls c.Tick repeatedly (each call itself blocks briefly inside
// PollFetches while the consumer group assignment settles and fetches
// arrive) until condition reports true or the deadline elapses.
func tickUntil(t *testing.T, c *consumer.LedgerPostedConsumer, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		tickCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = c.Tick(tickCtx)
		cancel()
		if condition() {
			return
		}
	}
	t.Fatal("condition not met before deadline")
}

func walletBalance(t *testing.T, pool *pgxpool.Pool, walletID uuid.UUID) string {
	t.Helper()
	var balance string
	err := pool.QueryRow(context.Background(),
		"SELECT balance::text FROM projection_db.wallet_balances WHERE wallet_id = $1", walletID,
	).Scan(&balance)
	if err != nil {
		return "<no row>"
	}
	return balance
}
