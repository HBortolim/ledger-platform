package consumer

import (
	"context"
	"log"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/ledger-platform/projection-service/internal/config"
	"github.com/ledger-platform/projection-service/internal/metrics"
)

const ledgerPostedTopic = "ledger.posted.v1"

// LedgerPostedConsumer drains ledger.posted.v1 into projection_db.wallet_balances.
// Kafka offsets are committed only after the corresponding DB transaction
// commits (NFR-AVAIL-3): a crash between the two simply redelivers the
// record, and applyEntry's idempotency check makes the re-apply a no-op.
type LedgerPostedConsumer struct {
	pool         *pgxpool.Pool
	kafkaBrokers []string
	groupID      string

	client *kgo.Client
	admin  *kadm.Client
}

func NewLedgerPostedConsumer(pool *pgxpool.Pool, cfg *config.Config) *LedgerPostedConsumer {
	return &LedgerPostedConsumer{
		pool:         pool,
		kafkaBrokers: cfg.KafkaBrokers,
		groupID:      cfg.KafkaGroupID,
	}
}

// Connect creates the Kafka client and admin handle. Separated from Run so
// integration tests can drive Tick deterministically instead of racing an
// async Run loop.
func (c *LedgerPostedConsumer) Connect() error {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(c.kafkaBrokers...),
		kgo.ConsumerGroup(c.groupID),
		kgo.ConsumeTopics(ledgerPostedTopic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
	)
	if err != nil {
		return err
	}
	c.client = cl
	c.admin = kadm.NewClient(cl)
	return nil
}

// Close releases the Kafka client. Safe to call after Connect; a no-op
// before it.
func (c *LedgerPostedConsumer) Close() {
	if c.client != nil {
		c.client.Close()
	}
}

// Run connects to Kafka and processes ledger.posted.v1 until ctx is
// cancelled.
func (c *LedgerPostedConsumer) Run(ctx context.Context) {
	if err := c.Connect(); err != nil {
		log.Fatalf("projection consumer: cannot create kafka client: %v", err)
	}
	defer c.Close()

	log.Println("projection consumer started")
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := c.Tick(ctx); err != nil {
			log.Printf("projection consumer: tick error: %v", err)
		}
	}
}

// Tick runs one PollFetches cycle: fetch whatever is currently available,
// apply and commit each record in order. Exported so integration tests can
// drive the consumer deterministically instead of racing an async Run loop
// (mirrors outbox.Worker.Poll in the ledger-service).
func (c *LedgerPostedConsumer) Tick(ctx context.Context) error {
	fetches := c.client.PollFetches(ctx)
	if fetches.IsClientClosed() || ctx.Err() != nil {
		return nil
	}

	fetches.EachError(func(topic string, partition int32, err error) {
		log.Printf("projection consumer: fetch error topic=%s partition=%d: %v", topic, partition, err)
	})

	fetches.EachRecord(func(r *kgo.Record) {
		c.applyRecord(ctx, r)
	})
	return nil
}

// applyRecord decodes and applies one Kafka record. A decode failure is
// treated as a poison message: logged, counted, and committed past — a
// stuck partition is worse than a counted skip. Any other failure (e.g. the
// database being down) leaves the offset uncommitted so the record is
// redelivered on the next tick (§9.9).
func (c *LedgerPostedConsumer) applyRecord(ctx context.Context, r *kgo.Record) {
	event, err := decodeLedgerPostedEvent(r.Value)
	if err != nil {
		log.Printf("projection consumer: poison message at %s[%d]@%d: %v", r.Topic, r.Partition, r.Offset, err)
		metrics.EventsProcessedTotal.WithLabelValues("error").Inc()
		if err := c.client.CommitRecords(ctx, r); err != nil {
			log.Printf("projection consumer: commit past poison message: %v", err)
		}
		return
	}

	applied, skipped, err := applyEvent(ctx, c.pool, event, c.groupID, r.Topic, r.Partition, r.Offset)
	if err != nil {
		log.Printf("projection consumer: apply transaction %s failed, will retry: %v", event.TransactionID, err)
		return
	}
	for range applied {
		metrics.EventsProcessedTotal.WithLabelValues("applied").Inc()
	}
	for range skipped {
		metrics.EventsProcessedTotal.WithLabelValues("skipped").Inc()
	}

	lag := max(time.Since(event.OccurredAt).Seconds(), 0)
	metrics.LagSeconds.Observe(lag)
	metrics.MaxLagSeconds.Set(lag)

	// TODO(M6+): publish BALANCE_UPDATED here once a consumer needs it
	// (notification-service is optional and unbuilt per SPEC.md §5.2).

	if err := c.client.CommitRecords(ctx, r); err != nil {
		log.Printf("projection consumer: commit offset for %s: %v", event.TransactionID, err)
		return
	}

	c.reportConsumerLag(ctx, r.Topic, r.Partition)
}

// reportConsumerLag is best-effort observability: a failed lookup must never
// affect the apply/commit path above.
func (c *LedgerPostedConsumer) reportConsumerLag(ctx context.Context, topic string, partition int32) {
	lagCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	endOffsets, err := c.admin.ListEndOffsets(lagCtx, topic)
	if err != nil {
		return
	}
	end, ok := endOffsets.Lookup(topic, partition)
	if !ok || end.Offset < 0 {
		return
	}

	group, err := c.admin.FetchOffsets(lagCtx, c.groupID)
	if err != nil {
		return
	}
	committed, ok := group.Lookup(topic, partition)
	if !ok {
		return
	}

	lag := max(end.Offset-committed.At, 0)
	metrics.ConsumerLag.WithLabelValues(topic, strconv.FormatInt(int64(partition), 10)).Set(float64(lag))
}
