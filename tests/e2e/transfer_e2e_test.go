//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestE2E_HappyPath covers TST-E2E-1: create 2 wallets, seed the source via
// the Ledger API, transfer, poll balances, and confirm the ledger row and
// Kafka event both exist -- this also completes TST-INT-1's "ledger rows
// exist" half, deferred here per the M3 overview's decision #5.
func TestE2E_HappyPath(t *testing.T) {
	userID := uuid.New()
	token := signJWT(t, userID, "user")

	source := newWallet(t, token, userID)
	destination := newWallet(t, token, uuid.New())
	seedDeposit(t, source, "500.00")

	key := uuid.New().String()
	resp, body := postTransfer(t, token, key, source, destination, "100.00")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /transfers = %d, want 201; body: %s", resp.StatusCode, body)
	}
	var created struct {
		TransferID string `json:"transferId"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode transfer response: %v", err)
	}
	if created.Status != "COMPLETED" {
		t.Errorf("transfer status = %s, want COMPLETED", created.Status)
	}
	transactionID := uuid.MustParse(created.TransferID)

	srcBal := pollBalance(t, token, source, 400.00)
	if srcBal.Balance != 400.00 {
		t.Errorf("source balance = %.2f, want 400.00 within the 5s SLO poll window (last observed: %+v)", srcBal.Balance, srcBal)
	}
	dstBal := pollBalance(t, token, destination, 100.00)
	if dstBal.Balance != 100.00 {
		t.Errorf("destination balance = %.2f, want 100.00 within the 5s SLO poll window (last observed: %+v)", dstBal.Balance, dstBal)
	}

	assertLedgerPostedObserved(t, transactionID)
	assertLedgerEntriesForTransaction(t, transactionID, 2)
}

// TestE2E_IdempotentReplay covers TST-E2E-2: the same transfer twice with the
// same key returns identical responses, and exactly one transaction's worth
// of entries exist -- no double-post.
func TestE2E_IdempotentReplay(t *testing.T) {
	userID := uuid.New()
	token := signJWT(t, userID, "user")
	source := newWallet(t, token, userID)
	destination := newWallet(t, token, uuid.New())
	seedDeposit(t, source, "500.00")

	key := uuid.New().String()
	firstResp, firstBody := postTransfer(t, token, key, source, destination, "100.00")
	if firstResp.StatusCode != http.StatusCreated {
		t.Fatalf("first POST /transfers = %d, want 201; body: %s", firstResp.StatusCode, firstBody)
	}
	var first struct {
		TransferID string `json:"transferId"`
	}
	if err := json.Unmarshal([]byte(firstBody), &first); err != nil {
		t.Fatalf("decode first transfer response: %v", err)
	}

	secondResp, secondBody := postTransfer(t, token, key, source, destination, "100.00")
	if secondResp.StatusCode != http.StatusCreated {
		t.Fatalf("second POST /transfers = %d, want 201; body: %s", secondResp.StatusCode, secondBody)
	}
	if firstBody != secondBody {
		t.Errorf("replayed response = %s, want byte-identical to first response %s", secondBody, firstBody)
	}

	transactionID := uuid.MustParse(first.TransferID)
	assertLedgerEntriesForTransaction(t, transactionID, 2)
}

// TestE2E_IdempotencyKeyMismatch covers TST-E2E-3: same key, different body.
func TestE2E_IdempotencyKeyMismatch(t *testing.T) {
	userID := uuid.New()
	token := signJWT(t, userID, "user")
	source := newWallet(t, token, userID)
	destination := newWallet(t, token, uuid.New())
	seedDeposit(t, source, "500.00")

	key := uuid.New().String()
	firstResp, firstBody := postTransfer(t, token, key, source, destination, "100.00")
	if firstResp.StatusCode != http.StatusCreated {
		t.Fatalf("first POST /transfers = %d, want 201; body: %s", firstResp.StatusCode, firstBody)
	}

	secondResp, secondBody := postTransfer(t, token, key, source, destination, "200.00")
	if secondResp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("second POST /transfers (mismatched body) = %d, want 422; body: %s", secondResp.StatusCode, secondBody)
	}
	if !strings.Contains(secondBody, "IDEMPOTENCY_KEY_MISMATCH") {
		t.Errorf("second response body = %s, want to contain IDEMPOTENCY_KEY_MISMATCH", secondBody)
	}
}

// TestE2E_InsufficientFunds covers TST-E2E-4: a transfer exceeding the
// source wallet's balance.
func TestE2E_InsufficientFunds(t *testing.T) {
	userID := uuid.New()
	token := signJWT(t, userID, "user")
	source := newWallet(t, token, userID)
	destination := newWallet(t, token, uuid.New())
	seedDeposit(t, source, "50.00") // not enough for the 999.00 attempt below

	key := uuid.New().String()
	resp, body := postTransfer(t, token, key, source, destination, "999.00")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST /transfers (over balance) = %d, want 422; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "INSUFFICIENT_FUNDS") {
		t.Errorf("response body = %s, want to contain INSUFFICIENT_FUNDS", body)
	}

	assertLedgerEntryCount(t, source, 1) // only the seed deposit's CREDIT entry -- the rejected attempt wrote nothing
}

// TestE2E_TraceContextPropagatedToKafka asserts the ledger.posted.v1 record
// produced for a transfer carries a well-formed W3C traceparent Kafka
// header, and runs against the core-only stack.
func TestE2E_TraceContextPropagatedToKafka(t *testing.T) {
	userID := uuid.New()
	token := signJWT(t, userID, "user")
	source := newWallet(t, token, userID)
	destination := newWallet(t, token, uuid.New())
	seedDeposit(t, source, "500.00")

	key := uuid.New().String()
	resp, body := postTransfer(t, token, key, source, destination, "100.00")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /transfers = %d, want 201; body: %s", resp.StatusCode, body)
	}
	var created struct {
		TransferID string `json:"transferId"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode transfer response: %v", err)
	}
	transactionID := uuid.MustParse(created.TransferID)

	record := assertLedgerPostedObserved(t, transactionID)

	var traceparent string
	for _, h := range record.Headers {
		if h.Key == "traceparent" {
			traceparent = string(h.Value)
		}
	}
	if traceparent == "" {
		t.Fatal("ledger.posted.v1 record carries no traceparent header (NFR-OBS-2)")
	}
	if !regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`).MatchString(traceparent) {
		t.Errorf("traceparent = %q, want a well-formed W3C traceparent", traceparent)
	}

	var payload map[string]any
	if err := json.Unmarshal(record.Value, &payload); err != nil {
		t.Fatalf("decode ledger.posted.v1 payload: %v", err)
	}
	if _, ok := payload["traceparent"]; ok {
		t.Error("payload carries a traceparent field; propagation must be header-only")
	}
}
