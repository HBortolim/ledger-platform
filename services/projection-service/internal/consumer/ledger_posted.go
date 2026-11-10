package consumer

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/ledger-platform/projection-service/internal/config"
	"github.com/ledger-platform/projection-service/internal/metrics"
)

const ledgerPostedTopic = "ledger.posted.v1"

// applyRetryBackoff spaces out retries of a partition stopped on a failed apply, so a database
// outage is not a hot loop of failing applies and error logs.
const applyRetryBackoff = time.Second

var tracer = otel.Tracer("github.com/ledger-platform/projection-service/internal/consumer")

// recordCarrier adapts a consumed record's headers to OTel's TextMapCarrier.
// Extract-only; Set is a no-op.
type recordCarrier []kgo.RecordHeader

func (c recordCarrier) Get(key string) string {
	for _, h := range c {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

func (c recordCarrier) Set(string, string) {}

func (c recordCarrier) Keys() []string {
	keys := make([]string, len(c))
	for i, h := range c {
		keys[i] = h.Key
	}
	return keys
}

// LedgerPostedConsumer drains ledger.posted.v1 into projection_db.wallet_balances.
// Kafka offsets are committed only after the corresponding DB transaction
// commits (NFR-AVAIL-3): a crash between the two simply redelivers the
// record, and the per-wallet sequence makes the re-apply a no-op.
//
// A failed apply stops its partition at that record (SPEC.md §9.9): a Kafka commit is a
// per-partition high-water mark, so committing any later record would commit past the failed
// one and it would never be redelivered.
type LedgerPostedConsumer struct {
	pool         *pgxpool.Pool
	kafkaBrokers []string
	groupID      string
	alertAfter   time.Duration // a parked entry older than this is a gap that did not fill (SEQ-9)

	client *kgo.Client
	admin  *kadm.Client
}

func NewLedgerPostedConsumer(pool *pgxpool.Pool, cfg *config.Config) *LedgerPostedConsumer {
	return &LedgerPostedConsumer{
		pool:         pool,
		kafkaBrokers: cfg.KafkaBrokers,
		groupID:      cfg.KafkaGroupID,
		alertAfter:   cfg.PendingEntryAlertAfter,
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
		// Rebalances wait for AllowRebalance, so a partition cannot be revoked between the poll
		// and the SetOffsets rewind in Tick (franz-go's guidance for SetOffsets in a group).
		kgo.BlockRebalanceOnPoll(),
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
		slog.ErrorContext(ctx, "projection consumer: cannot create kafka client", slog.Any("error", err))
		os.Exit(1)
	}
	defer c.Close()

	go c.reportPendingLoop(ctx)

	slog.InfoContext(ctx, "projection consumer started")
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := c.Tick(ctx); err != nil {
			slog.ErrorContext(ctx, "projection consumer: tick failed", slog.Any("error", err))
		}
	}
}

// Tick runs one PollFetches cycle: fetch whatever is currently available,
// apply and commit each record in order. Exported so integration tests can
// drive the consumer deterministically instead of racing an async Run loop
// (mirrors outbox.Worker.Poll in the ledger-service).
//
// Records are processed partition by partition. When an apply fails, that partition stops at
// the failed record: its remaining records in this batch are not processed, and the client is
// rewound to the failed record so the next poll delivers it again. Other partitions carry on.
func (c *LedgerPostedConsumer) Tick(ctx context.Context) error {
	fetches := c.client.PollFetches(ctx)
	if fetches.IsClientClosed() {
		return nil
	}
	if ctx.Err() != nil {
		c.client.AllowRebalance()
		return nil
	}

	fetches.EachError(func(topic string, partition int32, err error) {
		slog.ErrorContext(ctx, "projection consumer: fetch error",
			slog.String("topic", topic), slog.Int("partition", int(partition)), slog.Any("error", err))
	})

	rewind := map[string]map[int32]kgo.EpochOffset{}
	fetches.EachPartition(func(p kgo.FetchTopicPartition) {
		if _, stopped := rewind[p.Topic][p.Partition]; stopped {
			return // a partition can appear in more than one fetch of a poll
		}
		for _, r := range p.Records {
			if c.applyRecord(ctx, r) {
				continue
			}
			if rewind[p.Topic] == nil {
				rewind[p.Topic] = map[int32]kgo.EpochOffset{}
			}
			rewind[p.Topic][p.Partition] = kgo.EpochOffset{Epoch: r.LeaderEpoch, Offset: r.Offset}
			return
		}
	})

	if len(rewind) == 0 {
		c.client.AllowRebalance()
		return nil
	}

	// The poll already advanced the client past these records; move the stopped partitions
	// back to their failed record. Buffered data is discarded without moving any other
	// partition's position.
	c.client.SetOffsets(rewind)
	c.client.AllowRebalance()
	for topic, partitions := range rewind {
		for partition, at := range partitions {
			slog.WarnContext(ctx, "projection consumer: partition stopped on a failed apply, will retry",
				slog.String("topic", topic), slog.Int("partition", int(partition)), slog.Int64("offset", at.Offset))
		}
	}
	select {
	case <-ctx.Done():
	case <-time.After(applyRetryBackoff):
	}
	return nil
}

// applyRecord decodes and applies one Kafka record and reports whether the partition may move
// on past it. A decode failure is treated as a poison message: logged, counted, and committed
// past — it can never succeed, and a stuck partition is worse than a counted skip. Any other
// failure (e.g. the database being down) returns false: the offset stays uncommitted and Tick
// stops the partition at this record so it is retried (§9.9).
func (c *LedgerPostedConsumer) applyRecord(ctx context.Context, r *kgo.Record) bool {
	ctx = otel.GetTextMapPropagator().Extract(ctx, recordCarrier(r.Headers))
	ctx, span := tracer.Start(ctx, "projection apply",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.source.name", r.Topic),
			attribute.Int64("messaging.kafka.message.offset", r.Offset),
		))
	defer span.End()

	event, err := decodeLedgerPostedEvent(r.Value)
	if err != nil {
		slog.ErrorContext(ctx, "projection consumer: poison message",
			slog.String("topic", r.Topic),
			slog.Int("partition", int(r.Partition)),
			slog.Int64("offset", r.Offset),
			slog.Any("error", err))
		span.RecordError(err)
		span.SetStatus(codes.Error, "poison message")
		metrics.EventsProcessedTotal.WithLabelValues("error").Inc()
		if err := c.client.CommitRecords(ctx, r); err != nil {
			slog.ErrorContext(ctx, "projection consumer: commit past poison message failed", slog.Any("error", err))
		}
		return true
	}

	res, err := applyEvent(ctx, c.pool, event, c.groupID, r.Topic, r.Partition, r.Offset)
	if err != nil {
		slog.ErrorContext(ctx, "projection consumer: apply failed, will retry",
			slog.String("transaction_id", event.TransactionID.String()),
			slog.Any("error", err))
		span.RecordError(err)
		span.SetStatus(codes.Error, "apply failed")
		return false
	}
	slog.InfoContext(ctx, "projection consumer: apply succeeded",
		slog.String("transaction_id", event.TransactionID.String()),
		slog.String("result", res.Outcome),
		slog.Int64("offset", r.Offset))
	metrics.EventsProcessedTotal.WithLabelValues(res.Outcome).Inc()
	metrics.SeqGapsTotal.Add(float64(res.NewlyParked))
	if res.Touched {
		c.refreshPendingGauge(ctx)
	}

	lag := max(time.Since(event.OccurredAt).Seconds(), 0)
	metrics.LagSeconds.Observe(lag)
	metrics.MaxLagSeconds.Set(lag)

	// TODO(M6+): publish BALANCE_UPDATED here once a consumer needs it
	// (notification-service is optional and unbuilt per SPEC.md §5.2).

	// The apply is committed in Postgres, so the partition may move on even if this offset commit
	// fails: a later record's commit covers it, and a redelivery is skipped by its seq.
	if err := c.client.CommitRecords(ctx, r); err != nil {
		slog.ErrorContext(ctx, "projection consumer: commit offset failed",
			slog.String("transaction_id", event.TransactionID.String()),
			slog.Any("error", err))
		return true
	}

	c.reportConsumerLag(ctx, r.Topic, r.Partition)
	return true
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
