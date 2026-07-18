// services/ledger-service/internal/metrics/metrics.go
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// PostingsTotal counts ledger posting attempts, per SPEC.md §7.3.
// status is one of: posted, duplicate, rejected, error.
var PostingsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "ledger_postings_total",
	Help: "Total number of ledger posting attempts, labeled by transaction type and outcome.",
}, []string{"type", "status"})

// PostingDuration observes PostingRepository.Post call latency, per SPEC.md §7.3.
var PostingDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name: "ledger_posting_duration_seconds",
	Help: "Latency of PostingRepository.Post calls, labeled by transaction type.",
}, []string{"type"})

// OutboxDepth reports the count of unpublished outbox rows, sampled once per
// outbox worker poll tick, per SPEC.md §7.3.
var OutboxDepth = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "ledger_outbox_depth",
	Help: "Current count of unpublished ledger_db.outbox rows.",
})

// OutboxPublishFailures counts outbox-to-Kafka publish failures, labeled by a
// low-cardinality reason (never raw error text), per SPEC.md §7.3.
var OutboxPublishFailures = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "ledger_outbox_publish_failures_total",
	Help: "Total outbox publish failures, labeled by reason.",
}, []string{"reason"})
