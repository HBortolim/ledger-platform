// services/ledger-service/internal/metrics/metrics_test.go
package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPostingsTotal_IncrementsPerLabelCombination(t *testing.T) {
	PostingsTotal.WithLabelValues("TRANSFER", "posted").Inc()
	got := testutil.ToFloat64(PostingsTotal.WithLabelValues("TRANSFER", "posted"))
	if got < 1 {
		t.Errorf("PostingsTotal{type=TRANSFER,status=posted} = %v, want >= 1", got)
	}
}

func TestPostingDuration_ObservesPerType(t *testing.T) {
	PostingDuration.WithLabelValues("DEPOSIT").Observe(0.01)
	count := testutil.CollectAndCount(PostingDuration)
	if count == 0 {
		t.Error("PostingDuration has no observations after Observe()")
	}
}
