// services/ledger-service/tests/posting_http_test.go
package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ledger-platform/ledger-service/internal/handler"
	"github.com/ledger-platform/ledger-service/internal/repository"
	"github.com/ledger-platform/ledger-service/internal/service"
)

// TestPostingHTTP exercises POST /ledger/postings and GET
// /admin/ledger/transactions/:id end-to-end: real gin router, real
// PostingService, real PostingRepository, real Postgres. This is the
// SPEC.md §7.1 contract test — Task 04's "done when" criterion.
//
// 503 (Postgres genuinely unavailable) is intentionally not exercised here;
// killing a dockertest container mid-request is too flaky to be worth it.
// That path is covered by internal/handler's unit tests with a fake service.
func TestPostingHTTP(t *testing.T) {
	_, appDSN := setupLedgerDB(t)
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		t.Fatalf("pgxpool.New() = error %v, want nil", err)
	}
	t.Cleanup(pool.Close)

	repo := repository.NewPostingRepository(pool, testDailyCap)
	svc := service.NewPostingService(repo)
	h := handler.NewPostingHandler(svc)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.RegisterRoutes(router, pool, h)

	postJSON := func(t *testing.T, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/ledger/postings", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	getJSON := func(t *testing.T, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	type entryBody struct {
		AccountID uuid.UUID `json:"accountId"`
		EntryType string    `json:"entryType"`
		Amount    string    `json:"amount"`
	}
	type postingBody struct {
		TransactionID uuid.UUID   `json:"transactionId"`
		Type          string      `json:"type"`
		Entries       []entryBody `json:"entries"`
	}
	type entryResp struct {
		EntryID   string `json:"entryId"`
		AccountID string `json:"accountId"`
		EntryType string `json:"entryType"`
		Amount    string `json:"amount"`
	}
	type postingResp struct {
		TransactionID string      `json:"transactionId"`
		PostedAt      string      `json:"postedAt"`
		Entries       []entryResp `json:"entries"`
	}
	type errorResp struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}

	t.Run("HappyPath/Returns201WithPersistedEntries", func(t *testing.T) {
		debit, credit := uuid.New(), uuid.New()
		seedAccountBalance(t, pool, debit, "200.00")
		txID := uuid.New()

		w := postJSON(t, postingBody{
			TransactionID: txID,
			Type:          "TRANSFER",
			Entries: []entryBody{
				{AccountID: debit, EntryType: "DEBIT", Amount: "100.00"},
				{AccountID: credit, EntryType: "CREDIT", Amount: "100.00"},
			},
		})

		if w.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201, body = %s", w.Code, w.Body.String())
		}
		var got postingResp
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if got.TransactionID != txID.String() {
			t.Errorf("transactionId = %s, want %s", got.TransactionID, txID)
		}
		if got.PostedAt == "" {
			t.Error("postedAt is empty, want a timestamp")
		}
		if len(got.Entries) != 2 {
			t.Fatalf("entries = %d, want 2", len(got.Entries))
		}
	})

	t.Run("DuplicateTransactionID/Returns409WithOriginalPosting", func(t *testing.T) {
		debit, credit := uuid.New(), uuid.New()
		seedAccountBalance(t, pool, debit, "200.00")
		txID := uuid.New()
		body := postingBody{
			TransactionID: txID,
			Type:          "TRANSFER",
			Entries: []entryBody{
				{AccountID: debit, EntryType: "DEBIT", Amount: "50.00"},
				{AccountID: credit, EntryType: "CREDIT", Amount: "50.00"},
			},
		}

		first := postJSON(t, body)
		if first.Code != http.StatusCreated {
			t.Fatalf("first post status = %d, want 201, body = %s", first.Code, first.Body.String())
		}
		var firstResp postingResp
		if err := json.Unmarshal(first.Body.Bytes(), &firstResp); err != nil {
			t.Fatalf("unmarshal first response: %v", err)
		}

		second := postJSON(t, body)
		if second.Code != http.StatusConflict {
			t.Fatalf("second post status = %d, want 409, body = %s", second.Code, second.Body.String())
		}
		var secondResp postingResp
		if err := json.Unmarshal(second.Body.Bytes(), &secondResp); err != nil {
			t.Fatalf("unmarshal second response: %v", err)
		}
		if secondResp.PostedAt != firstResp.PostedAt {
			t.Errorf("409 postedAt = %s, want the original %s", secondResp.PostedAt, firstResp.PostedAt)
		}
		if len(secondResp.Entries) != 2 {
			t.Errorf("409 entries = %d, want 2 (the original posting)", len(secondResp.Entries))
		}
	})

	t.Run("UnbalancedEntries/Returns422AndPersistsNothing", func(t *testing.T) {
		debit, credit := uuid.New(), uuid.New()
		seedAccountBalance(t, pool, debit, "200.00")
		txID := uuid.New()

		w := postJSON(t, postingBody{
			TransactionID: txID,
			Type:          "TRANSFER",
			Entries: []entryBody{
				{AccountID: debit, EntryType: "DEBIT", Amount: "100.00"},
				{AccountID: credit, EntryType: "CREDIT", Amount: "50.00"},
			},
		})

		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422, body = %s", w.Code, w.Body.String())
		}
		var got errorResp
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if got.Code != "UNBALANCED" {
			t.Errorf("code = %s, want UNBALANCED", got.Code)
		}

		var txCount int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM ledger_db.ledger_transactions WHERE id = $1", txID).Scan(&txCount); err != nil {
			t.Fatalf("count ledger_transactions: %v", err)
		}
		if txCount != 0 {
			t.Errorf("ledger_transactions count = %d, want 0 (app-side pre-check must short-circuit before the repo is called)", txCount)
		}
	})

	t.Run("InsufficientFunds/Returns422", func(t *testing.T) {
		debit, credit := uuid.New(), uuid.New()
		seedAccountBalance(t, pool, debit, "10.00")

		w := postJSON(t, postingBody{
			TransactionID: uuid.New(),
			Type:          "TRANSFER",
			Entries: []entryBody{
				{AccountID: debit, EntryType: "DEBIT", Amount: "100.00"},
				{AccountID: credit, EntryType: "CREDIT", Amount: "100.00"},
			},
		})

		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422, body = %s", w.Code, w.Body.String())
		}
		var got errorResp
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if got.Code != "INSUFFICIENT_FUNDS" {
			t.Errorf("code = %s, want INSUFFICIENT_FUNDS", got.Code)
		}
	})

	t.Run("InvalidAmount/NonTwoDecimalPlaces_Returns422", func(t *testing.T) {
		debit, credit := uuid.New(), uuid.New()
		seedAccountBalance(t, pool, debit, "200.00")

		raw := []byte(`{
			"transactionId": "` + uuid.New().String() + `",
			"type": "TRANSFER",
			"entries": [
				{"accountId": "` + debit.String() + `", "entryType": "DEBIT", "amount": "100"},
				{"accountId": "` + credit.String() + `", "entryType": "CREDIT", "amount": "100"}
			]
		}`)
		req := httptest.NewRequest(http.MethodPost, "/ledger/postings", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422, body = %s", w.Code, w.Body.String())
		}
		var got errorResp
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if got.Code != "INVALID_AMOUNT" {
			t.Errorf("code = %s, want INVALID_AMOUNT", got.Code)
		}
	})

	t.Run("FewerThanTwoEntries/Returns422ValidationError", func(t *testing.T) {
		debit := uuid.New()
		w := postJSON(t, postingBody{
			TransactionID: uuid.New(),
			Type:          "TRANSFER",
			Entries: []entryBody{
				{AccountID: debit, EntryType: "DEBIT", Amount: "100.00"},
			},
		})

		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422, body = %s", w.Code, w.Body.String())
		}
		var got errorResp
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if got.Code != "VALIDATION_ERROR" {
			t.Errorf("code = %s, want VALIDATION_ERROR", got.Code)
		}
	})

	t.Run("GetTransaction/CommittedID_Returns200", func(t *testing.T) {
		debit, credit := uuid.New(), uuid.New()
		seedAccountBalance(t, pool, debit, "200.00")
		txID := uuid.New()

		posted := postJSON(t, postingBody{
			TransactionID: txID,
			Type:          "TRANSFER",
			Entries: []entryBody{
				{AccountID: debit, EntryType: "DEBIT", Amount: "20.00"},
				{AccountID: credit, EntryType: "CREDIT", Amount: "20.00"},
			},
		})
		if posted.Code != http.StatusCreated {
			t.Fatalf("setup post status = %d, want 201, body = %s", posted.Code, posted.Body.String())
		}

		w := getJSON(t, "/admin/ledger/transactions/"+txID.String())
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body = %s", w.Code, w.Body.String())
		}
		var got postingResp
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if got.TransactionID != txID.String() {
			t.Errorf("transactionId = %s, want %s", got.TransactionID, txID)
		}
		if len(got.Entries) != 2 {
			t.Errorf("entries = %d, want 2", len(got.Entries))
		}
	})

	t.Run("GetTransaction/UnknownID_Returns404", func(t *testing.T) {
		w := getJSON(t, "/admin/ledger/transactions/"+uuid.New().String())
		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404, body = %s", w.Code, w.Body.String())
		}
	})
}
