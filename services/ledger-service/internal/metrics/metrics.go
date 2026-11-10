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

// ReconciliationRunsTotal counts reconciliation passes (SPEC.md §7.6), labeled by
// result: ok, drift or error. A pass refused because another is running is not a
// pass and is not counted.
var ReconciliationRunsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "reconciliation_runs_total",
	Help: "Total reconciliation passes, labeled by result (ok, drift, error).",
}, []string{"result"})

// ReconciliationDriftTotal is SPEC.md §7.6's RECONCILIATION_DRIFT in Prometheus naming.
// Each drifting pass adds the true finding count (not the sample length) for the check;
// for money_supply it adds the number of non-zero totals (0-2). Persistent drift keeps
// adding on every pass, so alert on increase(reconciliation_drift_total[2h]) > 0, not
// on the absolute value.
var ReconciliationDriftTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "reconciliation_drift_total",
	Help: "Findings reported by drifting reconciliation passes, labeled by check (invariant, projection, money_supply).",
}, []string{"check"})

// ReconciliationLastSuccess is the Unix time of the last clean pass. Drift and error
// passes do not advance it, so "time since last success" grows both when the job has
// died and when drift persists.
var ReconciliationLastSuccess = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "reconciliation_last_success_timestamp",
	Help: "Unix timestamp of the last clean reconciliation pass.",
})

// ReconciliationDuration observes the wall-clock time of the checks in a pass,
// excluding the wait for the pass lock.
var ReconciliationDuration = promauto.NewHistogram(prometheus.HistogramOpts{
	Name: "reconciliation_duration_seconds",
	Help: "Wall-clock duration of a reconciliation pass.",
})

// Pre-create every label value so increase() and absent() alerts behave before the
// first incident.
func init() {
	for _, r := range []string{"ok", "drift", "error"} {
		ReconciliationRunsTotal.WithLabelValues(r)
	}
	for _, c := range []string{"invariant", "projection", "money_supply"} {
		ReconciliationDriftTotal.WithLabelValues(c)
	}
}
