// Package metrics defines the projection-service's SPEC.md §7.3 collectors.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// EventsProcessedTotal counts LEDGER_POSTED events processed by the
// consumer, per SPEC.md §7.3. result is one of: applied, skipped, error.
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
