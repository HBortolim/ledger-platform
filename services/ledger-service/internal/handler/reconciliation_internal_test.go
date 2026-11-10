package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/ledger-platform/ledger-service/internal/domain"
)

type fakeReconSvc struct {
	report domain.ReconciliationReport
	err    error
}

func (f fakeReconSvc) Run(ctx context.Context) (domain.ReconciliationReport, error) {
	return f.report, f.err
}

func reconRouter(svc reconciliationService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/admin/reconciliation/run", NewReconciliationHandler(svc).Run)
	return r
}

func postRecon(r *gin.Engine) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/reconciliation/run", nil))
	return w
}

func TestReconciliationHandler_Clean_FindingsAreEmptyArraysNotNull(t *testing.T) {
	w := postRecon(reconRouter(fakeReconSvc{report: domain.ReconciliationReport{RanAt: time.Now()}}))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "null") {
		t.Errorf("body contains null: %s", body)
	}
	var got reconciliationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Clean || !got.Invariant.Clean || !got.Projection.Clean || !got.MoneySupply.Clean {
		t.Errorf("clean flags = %+v, want all true", got)
	}
	if !strings.Contains(body, `"findings":[]`) {
		t.Errorf("body should carry empty findings arrays: %s", body)
	}
}

func TestReconciliationHandler_Drift_Is200WithDetail(t *testing.T) {
	wallet := uuid.New()
	report := domain.ReconciliationReport{
		RanAt: time.Now(),
		Projection: domain.DriftFindings{Total: 3, Sample: []domain.ProjectionDrift{
			{WalletID: wallet, Kind: domain.DriftMismatch,
				Projected: decimal.RequireFromString("999"), Actual: decimal.RequireFromString("50.5")},
		}},
		MoneySupply: domain.MoneySupply{LedgerTotal: decimal.Zero, CachedTotal: decimal.RequireFromString("1000")},
	}
	w := postRecon(reconRouter(fakeReconSvc{report: report}))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (drift is not a failure)", w.Code)
	}
	var got reconciliationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Clean || got.Projection.Clean {
		t.Error("clean flags should be false")
	}
	p := got.Projection
	if p.FindingCount != 3 || !p.Truncated || len(p.Findings) != 1 {
		t.Errorf("projection = %+v, want count 3, truncated, 1 finding", p)
	}
	f := p.Findings[0]
	if f.WalletID != wallet.String() || f.Kind != "MISMATCH" || f.Projected != "999.00" || f.Actual != "50.50" {
		t.Errorf("finding = %+v", f)
	}
	if got.MoneySupply.CachedClean || !got.MoneySupply.LedgerClean || got.MoneySupply.CachedTotal != "1000.00" {
		t.Errorf("moneySupply = %+v, want ledgerClean and cached unclean independently", got.MoneySupply)
	}
}

func TestReconciliationHandler_InProgress_Is409(t *testing.T) {
	w := postRecon(reconRouter(fakeReconSvc{err: domain.ErrReconciliationInProgress{}}))

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "RECONCILIATION_IN_PROGRESS") {
		t.Errorf("body = %s, want the in-progress code", w.Body.String())
	}
}

func TestReconciliationHandler_OtherError_Is503AndHidesTheCause(t *testing.T) {
	w := postRecon(reconRouter(fakeReconSvc{err: errors.New("dial tcp postgres:5432 as ledger_app: refused")}))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("missing Retry-After")
	}
	if strings.Contains(w.Body.String(), "postgres") || strings.Contains(w.Body.String(), "ledger_app") {
		t.Errorf("body leaks the cause: %s", w.Body.String())
	}
}

func TestReconciliationHandler_OnlyPostIsRouted(t *testing.T) {
	w := httptest.NewRecorder()
	reconRouter(fakeReconSvc{}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/reconciliation/run", nil))
	if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 404/405", w.Code)
	}
}
