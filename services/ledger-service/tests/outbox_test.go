package tests

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/ledger-platform/ledger-service/internal/metrics"
	"github.com/ledger-platform/ledger-service/internal/outbox"
	"github.com/ledger-platform/ledger-service/internal/repository"
)

// TestOutboxWorker_PublishesToKafka posts a real transaction (giving a
// realistic outbox row via PostingRepository.Post), runs a single worker
// poll tick, and asserts the row is marked published and the message is
// consumable from Kafka with a matching payload.
func TestOutboxWorker_PublishesToKafka(t *testing.T) {
	_, appDSN := setupLedgerDB(t)
	bootstrap := setupKafka(t)
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		t.Fatalf("pgxpool.New() = error %v, want nil", err)
	}
	t.Cleanup(pool.Close)

	repo := repository.NewPostingRepository(pool)
	debit, credit := uuid.New(), uuid.New()
	seedAccountBalance(t, pool, debit, "200.00")
	tx := newBalancedTransfer(debit, credit, "100.00")
	if err := repo.Post(ctx, tx); err != nil {
		t.Fatalf("Post() = error %v, want nil", err)
	}

	producer, err := kgo.NewClient(kgo.SeedBrokers(bootstrap), kgo.ClientID("outbox-test-producer"))
	if err != nil {
		t.Fatalf("kgo.NewClient() = error %v, want nil", err)
	}
	t.Cleanup(producer.Close)

	worker := outbox.NewWorker(pool, producer)
	if err := worker.Poll(ctx); err != nil {
		t.Fatalf("Poll() = error %v, want nil", err)
	}

	var publishedAt *time.Time
	if err := pool.QueryRow(ctx, "SELECT published_at FROM ledger_db.outbox WHERE key = $1", tx.ID.String()).Scan(&publishedAt); err != nil {
		t.Fatalf("query outbox row: %v", err)
	}
	if publishedAt == nil {
		t.Fatal("published_at is NULL, want it set after Poll()")
	}

	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(bootstrap),
		kgo.ConsumeTopics("ledger.posted.v1"),
		kgo.ConsumeStartOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatalf("kgo.NewClient() (consumer) = error %v, want nil", err)
	}
	t.Cleanup(consumer.Close)

	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var envelope struct {
		SchemaVersion int    `json:"schemaVersion"`
		EventType     string `json:"eventType"`
		TransactionID string `json:"transactionId"`
		Entries       []struct {
			EntryID   string `json:"entryId"`
			AccountID string `json:"accountId"`
			EntryType string `json:"entryType"`
			Amount    string `json:"amount"`
		} `json:"entries"`
	}
	found := false
	for !found {
		fetches := consumer.PollFetches(fetchCtx)
		if fetchCtx.Err() != nil {
			t.Fatalf("timed out waiting for message on ledger.posted.v1: %v", fetchCtx.Err())
		}
		fetches.EachRecord(func(r *kgo.Record) {
			if string(r.Key) != tx.ID.String() {
				return
			}
			if err := json.Unmarshal(r.Value, &envelope); err != nil {
				t.Fatalf("unmarshal kafka message: %v", err)
			}
			found = true
		})
	}

	if envelope.SchemaVersion != 1 || envelope.EventType != "LEDGER_POSTED" || envelope.TransactionID != tx.ID.String() {
		t.Errorf("kafka message envelope mismatch: %+v", envelope)
	}
	if len(envelope.Entries) != 2 {
		t.Errorf("kafka message entries = %d, want 2", len(envelope.Entries))
	}
}

// TestOutboxWorker_KafkaDown_RowsStayPending points the worker at an
// unreachable broker and asserts rows stay unpublished, nothing panics, and
// the publish-failure metric increments.
func TestOutboxWorker_KafkaDown_RowsStayPending(t *testing.T) {
	_, appDSN := setupLedgerDB(t)
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		t.Fatalf("pgxpool.New() = error %v, want nil", err)
	}
	t.Cleanup(pool.Close)

	repo := repository.NewPostingRepository(pool)
	debit, credit := uuid.New(), uuid.New()
	seedAccountBalance(t, pool, debit, "200.00")
	tx := newBalancedTransfer(debit, credit, "50.00")
	if err := repo.Post(ctx, tx); err != nil {
		t.Fatalf("Post() = error %v, want nil", err)
	}

	failureCount := func() float64 {
		return testutil.ToFloat64(metrics.OutboxPublishFailures.WithLabelValues("kafka_unavailable")) +
			testutil.ToFloat64(metrics.OutboxPublishFailures.WithLabelValues("timeout"))
	}
	before := failureCount()

	producer, err := kgo.NewClient(
		kgo.SeedBrokers("127.0.0.1:1"), // deliberately unreachable
		kgo.ClientID("outbox-test-unreachable-producer"),
		kgo.RecordRetries(1),
		kgo.ProduceRequestTimeout(2*time.Second),
	)
	if err != nil {
		t.Fatalf("kgo.NewClient() = error %v, want nil", err)
	}
	t.Cleanup(producer.Close)

	worker := outbox.NewWorker(pool, producer)

	pollCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = worker.Poll(pollCtx) // may or may not return an error depending on how franz-go surfaces the outage; what matters is the row/metric assertions below

	var publishedAt *time.Time
	if err := pool.QueryRow(ctx, "SELECT published_at FROM ledger_db.outbox WHERE key = $1", tx.ID.String()).Scan(&publishedAt); err != nil {
		t.Fatalf("query outbox row: %v", err)
	}
	if publishedAt != nil {
		t.Errorf("published_at = %v, want NULL (Kafka unreachable)", *publishedAt)
	}

	after := failureCount()
	if after <= before {
		t.Errorf("ledger_outbox_publish_failures_total did not increase: before=%v after=%v", before, after)
	}
}

// TestOutboxWorker_RetentionSweep backdates a published row past the 7-day
// retention window and asserts Sweep() removes it.
func TestOutboxWorker_RetentionSweep(t *testing.T) {
	_, appDSN := setupLedgerDB(t)
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		t.Fatalf("pgxpool.New() = error %v, want nil", err)
	}
	t.Cleanup(pool.Close)

	repo := repository.NewPostingRepository(pool)
	debit, credit := uuid.New(), uuid.New()
	seedAccountBalance(t, pool, debit, "200.00")
	tx := newBalancedTransfer(debit, credit, "25.00")
	if err := repo.Post(ctx, tx); err != nil {
		t.Fatalf("Post() = error %v, want nil", err)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE ledger_db.outbox SET published_at = now() - interval '8 days' WHERE key = $1`,
		tx.ID.String(),
	); err != nil {
		t.Fatalf("backdate outbox row: %v", err)
	}

	worker := outbox.NewWorker(pool, nil)
	if err := worker.Sweep(ctx); err != nil {
		t.Fatalf("Sweep() = error %v, want nil", err)
	}

	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM ledger_db.outbox WHERE key = $1", tx.ID.String()).Scan(&count); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if count != 0 {
		t.Errorf("outbox row count after sweep = %d, want 0 (row older than retention window should be deleted)", count)
	}
}
