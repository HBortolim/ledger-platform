package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ledger-platform/ledger-service/internal/domain"
	"github.com/ledger-platform/ledger-service/internal/repository"
	"github.com/ledger-platform/ledger-service/internal/service"
)

func init() {
	gin.SetMode(gin.TestMode)
}

type fakePostingService struct {
	postFunc func(ctx context.Context, in service.PostInput) (domain.LedgerTransaction, error)
	getFunc  func(ctx context.Context, id uuid.UUID) (domain.LedgerTransaction, error)
}

func (f *fakePostingService) Post(ctx context.Context, in service.PostInput) (domain.LedgerTransaction, error) {
	return f.postFunc(ctx, in)
}

func (f *fakePostingService) GetTransaction(ctx context.Context, id uuid.UUID) (domain.LedgerTransaction, error) {
	return f.getFunc(ctx, id)
}

func newTestRouter(svc postingService) *gin.Engine {
	h := NewPostingHandler(svc)
	r := gin.New()
	r.POST("/ledger/postings", h.PostPosting)
	r.GET("/admin/ledger/transactions/:id", h.GetTransaction)
	return r
}

func validPostBody(txID, debit, credit uuid.UUID) []byte {
	body, _ := json.Marshal(postingRequest{
		TransactionID: txID,
		Type:          "TRANSFER",
		Entries: []postingEntryRequest{
			{AccountID: debit, EntryType: "DEBIT", Amount: "100.00"},
			{AccountID: credit, EntryType: "CREDIT", Amount: "100.00"},
		},
	})
	return body
}

func mustMoney(t *testing.T, s string) domain.Money {
	t.Helper()
	m, err := domain.NewMoney(s)
	if err != nil {
		t.Fatalf("domain.NewMoney(%q) = error %v, want nil", s, err)
	}
	return m
}

func assertErrorCode(t *testing.T, w *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if w.Code != wantStatus {
		t.Fatalf("status = %d, want %d, body = %s", w.Code, wantStatus, w.Body.String())
	}
	var got errorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.Code != wantCode {
		t.Errorf("code = %q, want %q", got.Code, wantCode)
	}
}

func TestPostPosting_Success_Returns201WithEntries(t *testing.T) {
	txID, debit, credit := uuid.New(), uuid.New(), uuid.New()
	postedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	persisted := domain.LedgerTransaction{
		ID:        txID,
		Type:      domain.TransactionTransfer,
		CreatedAt: postedAt,
		Entries: []domain.LedgerEntry{
			{ID: uuid.New(), AccountID: debit, EntryType: domain.Debit, Amount: mustMoney(t, "100.00")},
			{ID: uuid.New(), AccountID: credit, EntryType: domain.Credit, Amount: mustMoney(t, "100.00")},
		},
	}
	svc := &fakePostingService{
		postFunc: func(ctx context.Context, in service.PostInput) (domain.LedgerTransaction, error) {
			return persisted, nil
		},
	}
	r := newTestRouter(svc)

	req := httptest.NewRequest(http.MethodPost, "/ledger/postings", bytes.NewReader(validPostBody(txID, debit, credit)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body = %s", w.Code, http.StatusCreated, w.Body.String())
	}
	var got postingResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(got.Entries) != 2 {
		t.Errorf("entries = %d, want 2", len(got.Entries))
	}
	if !got.PostedAt.Equal(postedAt) {
		t.Errorf("postedAt = %v, want %v", got.PostedAt, postedAt)
	}
}

func TestPostPosting_Duplicate_Returns409WithOriginalPosting(t *testing.T) {
	txID, debit, credit := uuid.New(), uuid.New(), uuid.New()
	original := domain.LedgerTransaction{
		ID:   txID,
		Type: domain.TransactionTransfer,
		Entries: []domain.LedgerEntry{
			{ID: uuid.New(), AccountID: debit, EntryType: domain.Debit, Amount: mustMoney(t, "100.00")},
			{ID: uuid.New(), AccountID: credit, EntryType: domain.Credit, Amount: mustMoney(t, "100.00")},
		},
	}
	svc := &fakePostingService{
		postFunc: func(ctx context.Context, in service.PostInput) (domain.LedgerTransaction, error) {
			return domain.LedgerTransaction{}, repository.ErrDuplicateTransaction{ID: txID}
		},
		getFunc: func(ctx context.Context, id uuid.UUID) (domain.LedgerTransaction, error) {
			return original, nil
		},
	}
	r := newTestRouter(svc)

	req := httptest.NewRequest(http.MethodPost, "/ledger/postings", bytes.NewReader(validPostBody(txID, debit, credit)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body = %s", w.Code, http.StatusConflict, w.Body.String())
	}
	var got postingResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.TransactionID != txID.String() {
		t.Errorf("transactionId = %s, want %s", got.TransactionID, txID)
	}
	if len(got.Entries) != 2 {
		t.Errorf("entries = %d, want 2 (the original posting's entries)", len(got.Entries))
	}
}

func TestPostPosting_DuplicateButFetchFails_Returns503(t *testing.T) {
	txID, debit, credit := uuid.New(), uuid.New(), uuid.New()
	svc := &fakePostingService{
		postFunc: func(ctx context.Context, in service.PostInput) (domain.LedgerTransaction, error) {
			return domain.LedgerTransaction{}, repository.ErrDuplicateTransaction{ID: txID}
		},
		getFunc: func(ctx context.Context, id uuid.UUID) (domain.LedgerTransaction, error) {
			return domain.LedgerTransaction{}, errors.New("connection refused")
		},
	}
	r := newTestRouter(svc)

	req := httptest.NewRequest(http.MethodPost, "/ledger/postings", bytes.NewReader(validPostBody(txID, debit, credit)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d, body = %s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header missing")
	}
}

func TestPostPosting_Unbalanced_Returns422UnbalancedCode(t *testing.T) {
	txID, debit, credit := uuid.New(), uuid.New(), uuid.New()
	svc := &fakePostingService{
		postFunc: func(ctx context.Context, in service.PostInput) (domain.LedgerTransaction, error) {
			return domain.LedgerTransaction{}, service.ErrUnbalanced{Message: "transaction is unbalanced (net = 50.00)"}
		},
	}
	r := newTestRouter(svc)

	req := httptest.NewRequest(http.MethodPost, "/ledger/postings", bytes.NewReader(validPostBody(txID, debit, credit)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assertErrorCode(t, w, http.StatusUnprocessableEntity, "UNBALANCED")
}

func TestPostPosting_InvalidAmount_Returns422InvalidAmountCode(t *testing.T) {
	txID, debit, credit := uuid.New(), uuid.New(), uuid.New()
	svc := &fakePostingService{
		postFunc: func(ctx context.Context, in service.PostInput) (domain.LedgerTransaction, error) {
			return domain.LedgerTransaction{}, service.ErrInvalidAmount{Reason: `money value "100" must have exactly 2 decimal places`}
		},
	}
	r := newTestRouter(svc)

	req := httptest.NewRequest(http.MethodPost, "/ledger/postings", bytes.NewReader(validPostBody(txID, debit, credit)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assertErrorCode(t, w, http.StatusUnprocessableEntity, "INVALID_AMOUNT")
}

func TestPostPosting_InsufficientFunds_Returns422InsufficientFundsCode(t *testing.T) {
	txID, debit, credit := uuid.New(), uuid.New(), uuid.New()
	svc := &fakePostingService{
		postFunc: func(ctx context.Context, in service.PostInput) (domain.LedgerTransaction, error) {
			return domain.LedgerTransaction{}, repository.ErrInsufficientFunds{AccountID: debit}
		},
	}
	r := newTestRouter(svc)

	req := httptest.NewRequest(http.MethodPost, "/ledger/postings", bytes.NewReader(validPostBody(txID, debit, credit)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assertErrorCode(t, w, http.StatusUnprocessableEntity, "INSUFFICIENT_FUNDS")
}

func TestPostPosting_OpaqueError_Returns503WithRetryAfter(t *testing.T) {
	txID, debit, credit := uuid.New(), uuid.New(), uuid.New()
	svc := &fakePostingService{
		postFunc: func(ctx context.Context, in service.PostInput) (domain.LedgerTransaction, error) {
			return domain.LedgerTransaction{}, errors.New("connection refused")
		},
	}
	r := newTestRouter(svc)

	req := httptest.NewRequest(http.MethodPost, "/ledger/postings", bytes.NewReader(validPostBody(txID, debit, credit)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assertErrorCode(t, w, http.StatusServiceUnavailable, "DB_UNAVAILABLE")
	if w.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header missing")
	}
}

func TestPostPosting_FewerThanTwoEntries_Returns422ValidationError(t *testing.T) {
	svc := &fakePostingService{}
	r := newTestRouter(svc)

	body, _ := json.Marshal(map[string]any{
		"transactionId": uuid.New(),
		"type":          "TRANSFER",
		"entries": []map[string]any{
			{"accountId": uuid.New(), "entryType": "DEBIT", "amount": "100.00"},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/ledger/postings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assertErrorCode(t, w, http.StatusUnprocessableEntity, "VALIDATION_ERROR")
}

func TestGetTransaction_Found_Returns200(t *testing.T) {
	id := uuid.New()
	want := domain.LedgerTransaction{
		ID:   id,
		Type: domain.TransactionDeposit,
		Entries: []domain.LedgerEntry{
			{ID: uuid.New(), AccountID: uuid.New(), EntryType: domain.Debit, Amount: mustMoney(t, "10.00")},
		},
	}
	svc := &fakePostingService{
		getFunc: func(ctx context.Context, gotID uuid.UUID) (domain.LedgerTransaction, error) {
			return want, nil
		},
	}
	r := newTestRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/admin/ledger/transactions/"+id.String(), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestGetTransaction_NotFound_Returns404(t *testing.T) {
	svc := &fakePostingService{
		getFunc: func(ctx context.Context, id uuid.UUID) (domain.LedgerTransaction, error) {
			return domain.LedgerTransaction{}, repository.ErrNotFound
		},
	}
	r := newTestRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/admin/ledger/transactions/"+uuid.New().String(), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assertErrorCode(t, w, http.StatusNotFound, "NOT_FOUND")
}

func TestGetTransaction_InvalidUUID_Returns400(t *testing.T) {
	svc := &fakePostingService{}
	r := newTestRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/admin/ledger/transactions/not-a-uuid", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assertErrorCode(t, w, http.StatusBadRequest, "INVALID_ID")
}

func TestGetTransaction_OpaqueError_Returns503(t *testing.T) {
	svc := &fakePostingService{
		getFunc: func(ctx context.Context, id uuid.UUID) (domain.LedgerTransaction, error) {
			return domain.LedgerTransaction{}, errors.New("connection refused")
		},
	}
	r := newTestRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/admin/ledger/transactions/"+uuid.New().String(), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assertErrorCode(t, w, http.StatusServiceUnavailable, "DB_UNAVAILABLE")
}
