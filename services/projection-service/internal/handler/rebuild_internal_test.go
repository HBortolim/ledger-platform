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

	"github.com/ledger-platform/projection-service/internal/utils"
)

func init() { gin.SetMode(gin.TestMode) }

type ctxKey struct{}

type fakeRebuildService struct {
	calls     int
	gotCtx    context.Context
	gotWallet uuid.UUID
	result    utils.Result
	err       error
}

func (f *fakeRebuildService) Rebuild(ctx context.Context, walletID uuid.UUID) (utils.Result, error) {
	f.calls++
	f.gotCtx = ctx
	f.gotWallet = walletID
	return f.result, f.err
}

// rebuildResponse is the documented 200 body (Task 04, Step 4).
type rebuildResponse struct {
	WalletID       string    `json:"walletId"`
	Balance        string    `json:"balance"`
	EntriesApplied int       `json:"entriesApplied"`
	RebuiltAt      time.Time `json:"rebuiltAt"`
}

// postRebuild drives the route through the real router, so the path, the method and the
// :walletId binding are all exercised -- not just the handler function in isolation.
func postRebuild(t *testing.T, svc rebuildService, walletIDPath string) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	RegisterRoutes(router, nil, NewRebuildHandler(svc)) // nil pool: only /health/ready would touch it

	req := httptest.NewRequest(http.MethodPost, "/admin/projections/"+walletIDPath+"/rebuild", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestRebuild_ValidWallet_Returns200WithTheDocumentedBody(t *testing.T) {
	wallet := uuid.New()
	rebuiltAt := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := &fakeRebuildService{result: utils.Result{
		WalletID:       wallet,
		Balance:        decimal.RequireFromString("300.00"),
		EntriesApplied: 2,
		RebuiltAt:      rebuiltAt,
	}}

	rec := postRebuild(t, svc, wallet.String())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	var got rebuildResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not the documented JSON: %v; body: %s", err, rec.Body)
	}
	if got.WalletID != wallet.String() {
		t.Errorf("walletId = %q, want %q", got.WalletID, wallet)
	}
	if got.Balance != "300.00" {
		t.Errorf("balance = %q, want %q", got.Balance, "300.00")
	}
	if got.EntriesApplied != 2 {
		t.Errorf("entriesApplied = %d, want 2", got.EntriesApplied)
	}
	if !got.RebuiltAt.Equal(rebuiltAt) {
		t.Errorf("rebuiltAt = %s, want %s", got.RebuiltAt, rebuiltAt)
	}
}

func TestRebuild_ValidWallet_CallsTheServiceOnceWithTheParsedID(t *testing.T) {
	wallet := uuid.New()
	svc := &fakeRebuildService{}

	postRebuild(t, svc, wallet.String())

	if svc.calls != 1 {
		t.Errorf("service called %d times, want exactly 1", svc.calls)
	}
	if svc.gotWallet != wallet {
		t.Errorf("service got wallet %s, want %s", svc.gotWallet, wallet)
	}
}

// Money is rendered with exactly two decimals (§3.4), whatever scale the decimal carries.
func TestRebuild_Balance_IsAlwaysRenderedWithTwoDecimals(t *testing.T) {
	tests := []struct {
		name    string
		balance decimal.Decimal
		want    string
	}{
		{"whole number", decimal.NewFromInt(5), "5.00"},
		{"zero", decimal.Zero, "0.00"},
		{"negative", decimal.RequireFromString("-10"), "-10.00"},
		{"already two places", decimal.RequireFromString("0.01"), "0.01"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeRebuildService{result: utils.Result{Balance: tc.balance}}

			rec := postRebuild(t, svc, uuid.NewString())

			var got rebuildResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("unmarshal: %v; body: %s", err, rec.Body)
			}
			if got.Balance != tc.want {
				t.Errorf("balance = %q, want %q", got.Balance, tc.want)
			}
		})
	}
}

func TestRebuild_InvalidWalletID_Returns400AndNeverCallsTheService(t *testing.T) {
	for _, bad := range []string{"not-a-uuid", "123", "00000000-0000-0000-0000-00000000000Z"} {
		t.Run(bad, func(t *testing.T) {
			svc := &fakeRebuildService{}

			rec := postRebuild(t, svc, bad)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body: %s", rec.Code, rec.Body)
			}
			if svc.calls != 0 {
				t.Errorf("service called %d times, want 0: a malformed id must not reach the database", svc.calls)
			}
		})
	}
}

// NFR-AVAIL-style degradation: a downstream failure is a 503, never a 500 or a stack trace.
func TestRebuild_ServiceError_Returns503(t *testing.T) {
	svc := &fakeRebuildService{err: errors.New("connection refused")}

	rec := postRebuild(t, svc, uuid.NewString())

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body: %s", rec.Code, rec.Body)
	}
}

// The body is read by the Wallet Service and possibly surfaced to an operator; internals
// (hostnames, role names, SQL) must not leak through it.
func TestRebuild_ServiceError_DoesNotLeakInternalDetails(t *testing.T) {
	svc := &fakeRebuildService{err: errors.New(`pq: password authentication failed for user "projection_app"`)}

	rec := postRebuild(t, svc, uuid.NewString())

	for _, secret := range []string{"password", "projection_app", "pq:"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("error body %q leaks %q", rec.Body, secret)
		}
	}
}

// A cancelled request must be able to abort the rebuild transaction, so the handler must hand
// the service the REQUEST's context, not context.Background().
func TestRebuild_PassesTheRequestContextToTheService(t *testing.T) {
	svc := &fakeRebuildService{}
	router := gin.New()
	RegisterRoutes(router, nil, NewRebuildHandler(svc))

	ctx := context.WithValue(context.Background(), ctxKey{}, "from-the-request")
	req := httptest.NewRequest(http.MethodPost, "/admin/projections/"+uuid.NewString()+"/rebuild", nil).WithContext(ctx)
	router.ServeHTTP(httptest.NewRecorder(), req)

	if svc.gotCtx == nil || svc.gotCtx.Value(ctxKey{}) != "from-the-request" {
		t.Error("service did not receive the request's context")
	}
}

// Rebuild rewrites state, so it must not be reachable with a read-shaped verb.
func TestRebuild_OnlyPOSTIsRouted(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			svc := &fakeRebuildService{}
			router := gin.New()
			RegisterRoutes(router, nil, NewRebuildHandler(svc))

			req := httptest.NewRequest(method, "/admin/projections/"+uuid.NewString()+"/rebuild", nil)
			router.ServeHTTP(httptest.NewRecorder(), req)

			if svc.calls != 0 {
				t.Errorf("%s reached the rebuild handler, want it unrouted", method)
			}
		})
	}
}
