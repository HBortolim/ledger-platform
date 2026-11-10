//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// movementTransactionID reads a deposit/withdrawal response. Movements answer
// {"transactionId", "status", "postedAt"} (Task 03), unlike transfers' "transferId".
func movementTransactionID(t *testing.T, body string) uuid.UUID {
	t.Helper()
	var r struct {
		TransactionID string `json:"transactionId"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("decode movement response: %v; body: %s", err, body)
	}
	return uuid.MustParse(r.TransactionID)
}

// TestE2E_Deposit: an admin deposit posts one DEBIT on the system account and one CREDIT on the
// wallet (AC-6.1), and the projection converges.
func TestE2E_Deposit(t *testing.T) {
	userID := uuid.New()
	token := signJWT(t, userID, "user")
	_, admin := newAdmin(t)
	wallet := newWallet(t, token, userID)

	resp, body := postDeposit(t, admin, uuid.New().String(), wallet, "250.00")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /deposits = %d, want 201; body: %s", resp.StatusCode, body)
	}
	txID := movementTransactionID(t, body)
	assertLedgerEntriesForTransaction(t, txID, 2)

	var debit, credit string
	if err := ownerQueryRow(t, `SELECT
			(SELECT account_id::text FROM ledger_db.ledger_entries WHERE transaction_id = $1 AND entry_type = 'DEBIT'),
			(SELECT account_id::text FROM ledger_db.ledger_entries WHERE transaction_id = $1 AND entry_type = 'CREDIT')`,
		txID).Scan(&debit, &credit); err != nil {
		t.Fatalf("read deposit entries: %v", err)
	}
	if debit != systemAccountID || credit != wallet.String() {
		t.Errorf("deposit entries: DEBIT %s / CREDIT %s, want DEBIT system account / CREDIT %s", debit, credit, wallet)
	}
	if got := pollBalance(t, token, wallet, 250.00); got.Balance != 250.00 {
		t.Errorf("balance = %.2f, want 250.00 (last observed: %+v)", got.Balance, got)
	}
}

// TestE2E_DepositRequiresAdmin: both external movements refuse a user token (AC-6.2, FR-7).
func TestE2E_DepositRequiresAdmin(t *testing.T) {
	userID := uuid.New()
	token := signJWT(t, userID, "user")
	wallet := newWallet(t, token, userID)

	if resp, body := postDeposit(t, token, uuid.New().String(), wallet, "1.00"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /deposits with a user token = %d, want 403; body: %s", resp.StatusCode, body)
	}
	if resp, body := postWithdrawal(t, token, uuid.New().String(), wallet, "1.00"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /withdrawals with a user token = %d, want 403; body: %s", resp.StatusCode, body)
	}
	assertLedgerEntryCount(t, wallet, 0)
}

// TestE2E_Withdrawal: deposit then withdraw everything; the balance returns to 0.00, and a
// withdrawal beyond the balance is refused.
func TestE2E_Withdrawal(t *testing.T) {
	userID := uuid.New()
	token := signJWT(t, userID, "user")
	_, admin := newAdmin(t)
	wallet := newWallet(t, token, userID)
	seedDeposit(t, wallet, "80.00")

	resp, body := postWithdrawal(t, admin, uuid.New().String(), wallet, "80.00")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /withdrawals = %d, want 201; body: %s", resp.StatusCode, body)
	}
	if got := pollBalance(t, token, wallet, 0.00); got.Balance != 0.00 {
		t.Errorf("balance after withdrawing everything = %.2f, want 0.00", got.Balance)
	}

	resp, body = postWithdrawal(t, admin, uuid.New().String(), wallet, "1.00")
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "INSUFFICIENT_FUNDS") {
		t.Errorf("overdrawing withdrawal = %d %s, want 422 INSUFFICIENT_FUNDS", resp.StatusCode, body)
	}
	assertLedgerEntryCount(t, wallet, 2) // the deposit's credit and the withdrawal's debit only
}

// TestE2E_DepositIdempotency: the same Idempotency-Key twice returns identical bodies and posts
// exactly one ledger transaction (AC-6.3).
func TestE2E_DepositIdempotency(t *testing.T) {
	userID := uuid.New()
	token := signJWT(t, userID, "user")
	_, admin := newAdmin(t)
	wallet := newWallet(t, token, userID)

	key := uuid.New().String()
	first, firstBody := postDeposit(t, admin, key, wallet, "40.00")
	second, secondBody := postDeposit(t, admin, key, wallet, "40.00")
	if first.StatusCode != http.StatusCreated || second.StatusCode != http.StatusCreated {
		t.Fatalf("deposits = %d / %d, want 201 / 201", first.StatusCode, second.StatusCode)
	}
	if firstBody != secondBody {
		t.Errorf("replayed body differs:\n first: %s\nsecond: %s", firstBody, secondBody)
	}
	assertLedgerEntryCount(t, wallet, 1)
	if got := pollBalance(t, token, wallet, 40.00); got.Balance != 40.00 {
		t.Errorf("balance = %.2f, want 40.00 (not 80.00)", got.Balance)
	}
}

// TestE2E_MoneySupplyHoldsAfterExternalMovements: after deposits, a transfer and a withdrawal,
// the system as a whole still holds zero net (NFR-CONS-5), observed through reconciliation.
// Needs a stack started fresh with `make down && make up` (Task 06's zeroed system seed).
func TestE2E_MoneySupplyHoldsAfterExternalMovements(t *testing.T) {
	userID := uuid.New()
	token := signJWT(t, userID, "user")
	_, admin := newAdmin(t)
	a, b := newWallet(t, token, userID), newWallet(t, token, uuid.New())

	seedDeposit(t, a, "300.00")
	if resp, body := postTransfer(t, token, uuid.New().String(), a, b, "120.00"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("transfer = %d, want 201; body: %s", resp.StatusCode, body)
	}
	if resp, body := postWithdrawal(t, admin, uuid.New().String(), a, "30.00"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("withdrawal = %d, want 201; body: %s", resp.StatusCode, body)
	}

	status, report := runReconciliation(t)
	for status == http.StatusConflict {
		status, report = runReconciliation(t)
	}
	if status != http.StatusOK {
		t.Fatalf("reconciliation = %d, want 200", status)
	}
	ms := report.MoneySupply
	if !ms.Clean || ms.LedgerTotal != "0.00" || ms.CachedTotal != "0.00" {
		t.Errorf("money supply = %+v, want clean with both totals 0.00", ms)
	}
	if !report.Invariant.Clean {
		t.Errorf("invariant check found %d unbalanced transactions", report.Invariant.FindingCount)
	}
}
