//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestE2E_FrozenWalletRejectsTransfer: a frozen wallet is refused as a transfer source AND as a
// destination (AC-3.4's second half is the easy one to forget); unfreezing restores both.
func TestE2E_FrozenWalletRejectsTransfer(t *testing.T) {
	srcOwner, dstOwner := uuid.New(), uuid.New()
	srcToken, dstToken := signJWT(t, srcOwner, "user"), signJWT(t, dstOwner, "user")
	source := newWallet(t, srcToken, srcOwner)
	destination := newWallet(t, dstToken, dstOwner)
	seedDeposit(t, source, "100.00")

	cases := []struct {
		name       string
		frozen     uuid.UUID
		ownerToken string
	}{
		{"frozen source", source, srcToken},
		{"frozen destination", destination, dstToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if resp, body := walletAction(t, tc.ownerToken, tc.frozen, "freeze"); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"FROZEN"`) {
				t.Fatalf("freeze = %d %s, want 200 FROZEN", resp.StatusCode, body)
			}
			resp, body := postTransfer(t, srcToken, uuid.New().String(), source, destination, "10.00")
			if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "WALLET_NOT_ACTIVE") {
				t.Errorf("transfer with a %s = %d %s, want 422 WALLET_NOT_ACTIVE", tc.name, resp.StatusCode, body)
			}

			if resp, body := walletAction(t, tc.ownerToken, tc.frozen, "unfreeze"); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"ACTIVE"`) {
				t.Fatalf("unfreeze = %d %s, want 200 ACTIVE", resp.StatusCode, body)
			}
			if resp, body := postTransfer(t, srcToken, uuid.New().String(), source, destination, "10.00"); resp.StatusCode != http.StatusCreated {
				t.Errorf("transfer after unfreeze = %d %s, want 201", resp.StatusCode, body)
			}
		})
	}
}

// TestE2E_CloseWalletRules: a funded wallet cannot be closed; once drained it can; a closed wallet
// cannot be frozen or used, and its ledger history stays queryable (AC-4.1..4.3, AC-3.3). Every
// transition writes an audit row (AC-3.5).
func TestE2E_CloseWalletRules(t *testing.T) {
	userID := uuid.New()
	token := signJWT(t, userID, "user")
	_, admin := newAdmin(t)
	wallet := newWallet(t, token, userID)
	other := newWallet(t, token, uuid.New())
	seedDeposit(t, wallet, "60.00")

	resp, body := walletAction(t, token, wallet, "close")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "NONZERO_BALANCE") {
		t.Fatalf("closing a funded wallet = %d %s, want 409 NONZERO_BALANCE", resp.StatusCode, body)
	}

	if resp, body := postWithdrawal(t, admin, uuid.New().String(), wallet, "60.00"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("draining withdrawal = %d %s, want 201", resp.StatusCode, body)
	}
	if resp, body := walletAction(t, token, wallet, "close"); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"CLOSED"`) {
		t.Fatalf("closing the drained wallet = %d %s, want 200 CLOSED", resp.StatusCode, body)
	}

	if resp, body := walletAction(t, token, wallet, "freeze"); resp.StatusCode != http.StatusConflict || !strings.Contains(body, "WALLET_CLOSED") {
		t.Errorf("freezing a closed wallet = %d %s, want 409 WALLET_CLOSED", resp.StatusCode, body)
	}
	if resp, body := walletAction(t, token, wallet, "unfreeze"); resp.StatusCode != http.StatusConflict {
		t.Errorf("reopening a closed wallet via unfreeze = %d %s, want 409", resp.StatusCode, body)
	}
	if resp, body := postTransfer(t, token, uuid.New().String(), wallet, other, "1.00"); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "WALLET_NOT_ACTIVE") {
		t.Errorf("transfer from a closed wallet = %d %s, want 422 WALLET_NOT_ACTIVE", resp.StatusCode, body)
	}

	assertLedgerEntryCount(t, wallet, 2) // AC-4.3: the deposit and the withdrawal are still there

	var closes int
	if err := ownerQueryRow(t,
		`SELECT count(*) FROM wallet_db.wallet_audit_log WHERE wallet_id = $1 AND action = 'CLOSE' AND actor_id = $2
		   AND before_state->>'status' = 'ACTIVE' AND after_state->>'status' = 'CLOSED'`,
		wallet, userID).Scan(&closes); err != nil || closes != 1 {
		t.Errorf("CLOSE audit rows = %d, err=%v, want exactly 1 (ACTIVE -> CLOSED by the owner)", closes, err)
	}
}
