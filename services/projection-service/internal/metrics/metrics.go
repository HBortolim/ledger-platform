// Package metrics defines the projection-service's SPEC.md §7.3 collectors.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// EventsProcessedTotal counts LEDGER_POSTED events processed by the
// consumer, per SPEC.md §7.3. result is one of: applied, skipped, parked, error.
// parked means no entry of the event applied and at least one was parked behind a gap
// (Task 08, SEQ-6).
// One increment per Kafka event, not per ledger entry within it — see
// apply.go's applyEvent for why that's the correct unit.
var EventsProcessedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "projection_events_processed_total",
	Help: "Total LEDGER_POSTED events processed by the projection consumer, labeled by outcome.",
}, []string{"result"})

// LagSeconds observes projection lag (now - event.occurredAt) at apply time,
// across all wallets. Deliberately not labeled per-wallet — SPEC.md §7.3's
// cardinality note calls out a per-wallet gauge as an explosion risk.
var LagSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
	Name:    "projection_lag_seconds",
	Help:    "Observed lag between a ledger entry's occurrence and its projection apply, across all wallets.",
	Buckets: []float64{.05, .1, .25, .5, 1, 2, 5, 10, 30},
})

// MaxLagSeconds is the SPEC.md §7.3 companion gauge to LagSeconds.
var MaxLagSeconds = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "max_projection_lag_seconds",
	Help: "Most recently observed projection lag, in seconds.",
})

// ConsumerLag reports the Kafka consumer group's lag per topic/partition,
// per SPEC.md §7.3.
var ConsumerLag = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "projection_consumer_lag",
	Help: "Kafka consumer lag (high watermark minus last committed offset) per topic/partition.",
}, []string{"topic", "partition"})

// PendingEntries is the number of entries parked behind a gap (Task 08, SEQ-6). It should hover
// near zero; it rises briefly under normal cross-partition reordering and stays up when an
// entry was lost. Refreshed periodically by the consumer, so it can lag by a few seconds.
var PendingEntries = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "projection_pending_entries",
	Help: "Ledger entries parked in pending_entries waiting for an earlier entry of the same wallet.",
})

// SeqGapsTotal counts entries that were parked because an earlier entry of their wallet had not
// arrived (Task 08, SEQ-6). One increment per newly parked entry; redeliveries are not counted.
var SeqGapsTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "projection_seq_gaps_total",
	Help: "Entries parked because an earlier entry of the same wallet had not arrived.",
})

// RebuildsTotal counts admin-triggered projection rebuilds (SPEC.md §9.7),
// labeled by outcome: ok or error. A rebuild is an incident-time action that
// should almost never happen, so this counter is how it stays visible in
// Prometheus after the fact. One increment per rebuild attempted -- a request
// rejected for a malformed wallet id never reaches the service and is not counted.
var RebuildsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "projection_rebuilds_total",
	Help: "Total projection rebuilds attempted from the ledger, labeled by outcome (ok, error).",
}, []string{"result"})

func init() {
	// Materialize both series at 0 so increase()/absent() alerts work before the first
	// rebuild ever happens; otherwise a series only appears after the first incident.
	RebuildsTotal.WithLabelValues("ok")
	RebuildsTotal.WithLabelValues("error")
}
