package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ledger-platform/ledger-service/internal/handler"
	"github.com/ledger-platform/ledger-service/internal/repository"
	"github.com/ledger-platform/ledger-service/internal/service"
	"time"
)

type reconBody struct {
	Clean      bool `json:"clean"`
	Projection struct {
		Findings []struct {
			WalletID  string `json:"walletId"`
			Kind      string `json:"kind"`
			Projected string `json:"projected"`
			Actual    string `json:"actual"`
		} `json:"findings"`
	} `json:"projection"`
}

// TestReconciliationHTTP drives POST /admin/reconciliation/run through the real router,
// service and repository against Postgres.
func TestReconciliationHTTP(t *testing.T) {
	f, _, _ := newReconFixture(t)

	svc := service.NewReconciliationService(f.repo, service.ReconciliationOptions{
		InvariantWindow: 2 * time.Hour,
		DriftGrace:      10 * time.Second,
		PassTimeout:     time.Minute,
	})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	posting := handler.NewPostingHandler(service.NewPostingService(repository.NewPostingRepository(f.app, testDailyCap, systemFundingAccountID)))
	handler.RegisterRoutes(router, f.app, posting, handler.NewReconciliationHandler(svc))

	run := func(t *testing.T) (int, reconBody) {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/reconciliation/run", nil))
		var body reconBody
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %q: %v", w.Body.String(), err)
		}
		return w.Code, body
	}

	wallet, counter := uuid.New(), systemFundingAccountID
	f.balancedTransfer(counter, wallet, "50.00", "1 hour")
	f.project(wallet, "50.00")
	f.project(counter, "-50.00")

	if code, body := run(t); code != http.StatusOK || !body.Clean {
		t.Fatalf("clean system: status=%d clean=%v, want 200/true", code, body.Clean)
	}

	f.project(wallet, "999.00") // corrupt the projection; its entry is an hour old
	code, body := run(t)
	if code != http.StatusOK || body.Clean || len(body.Projection.Findings) != 1 {
		t.Fatalf("drift: status=%d clean=%v findings=%d, want 200/false/1", code, body.Clean, len(body.Projection.Findings))
	}
	if g := body.Projection.Findings[0]; g.WalletID != wallet.String() || g.Projected != "999.00" || g.Actual != "50.00" || g.Kind != "MISMATCH" {
		t.Errorf("finding = %+v, want the corrupted wallet with both numbers", g)
	}

	f.project(wallet, "50.00") // repair
	if code, body := run(t); code != http.StatusOK || !body.Clean {
		t.Errorf("after repair: status=%d clean=%v, want 200/true", code, body.Clean)
	}
}
