package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Both label values must exist from process start, so increase()/absent() alerting works
// before the first rebuild ever happens. This lives in the metrics package's OWN test binary
// on purpose: in any package that also calls the service, an earlier test would already have
// created the series and this assertion could never fail.
func TestRebuildsTotal_BothSeriesExistBeforeAnyRebuild(t *testing.T) {
	if n := testutil.CollectAndCount(RebuildsTotal); n != 2 {
		t.Errorf("projection_rebuilds_total has %d series at startup, want exactly 2 (ok, error)", n)
	}
}
