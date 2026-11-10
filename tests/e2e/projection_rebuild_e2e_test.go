//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// findDrift polls reconciliation until wallet appears in projection.findings or the deadline
// passes. A wallet that moved in the last 10s is treated as in-flight, not drifting (Task 06's
// grace period), so the first passes are expected to be clean; a 409 (a pass already running)
// is simply retried.
func findDrift(t *testing.T, wallet uuid.UUID, deadline time.Duration) (kind, projected, actual string, ok bool) {
	t.Helper()
	until := time.Now().Add(deadline)
	for time.Now().Before(until) {
		status, report := runReconciliation(t)
		if status == http.StatusOK {
			for _, f := range report.Projection.Findings {
				if f.WalletID == wallet.String() {
					return f.Kind, f.Projected, f.Actual, true
				}
			}
		} else if status != http.StatusConflict {
			t.Fatalf("POST /admin/reconciliation/run = %d, want 200 or 409", status)
		}
		time.Sleep(2 * time.Second)
	}
	return "", "", "", false
}

// TestE2E_ProjectionRebuild is TST-E2E-6 and SPEC §11 Milestone 6's "Demonstrable" line: corrupt
// a projection in Postgres, watch reconciliation report it, rebuild through the Wallet Service,
// and watch the balance and the reconciliation come back clean.
func TestE2E_ProjectionRebuild(t *testing.T) {
	userID := uuid.New()
	token := signJWT(t, userID, "user")
	adminID, admin := newAdmin(t)

	wallet := newWallet(t, token, userID)
	seedDeposit(t, wallet, "500.00")
	if got := pollBalance(t, token, wallet, 500.00); got.Balance != 500.00 {
		t.Fatalf("balance = %.2f, want 500.00 before corrupting it (last observed: %+v)", got.Balance, got)
	}

	corruptProjection(t, wallet, "999999.00")

	kind, projected, actual, ok := findDrift(t, wallet, 25*time.Second)
	if !ok {
		t.Fatalf("reconciliation never reported wallet %s as drifting within 25s", wallet)
	}
	if kind != "MISMATCH" || projected != "999999.00" || actual != "500.00" {
		t.Errorf("finding = %s projected=%s actual=%s, want MISMATCH 999999.00 / 500.00", kind, projected, actual)
	}

	resp, body := rebuildProjection(t, admin, wallet)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /admin/projections/%s/rebuild = %d, want 200; body: %s", wallet, resp.StatusCode, body)
	}
	var rebuilt struct {
		Balance json.Number `json:"balance"`
	}
	if err := json.Unmarshal([]byte(body), &rebuilt); err != nil {
		t.Fatalf("decode rebuild response: %v; body: %s", err, body)
	}
	if b, _ := rebuilt.Balance.Float64(); b != 500.00 {
		t.Errorf("rebuild reported balance %s, want 500.00", rebuilt.Balance)
	}

	got := pollBalance(t, token, wallet, 500.00)
	if got.Balance != 500.00 || got.Stale {
		t.Errorf("GET balance after rebuild = %.2f stale=%v, want 500.00 and not stale", got.Balance, got.Stale)
	}

	status, report := runReconciliation(t)
	if status != http.StatusOK {
		t.Fatalf("reconciliation after rebuild = %d, want 200", status)
	}
	for _, f := range report.Projection.Findings {
		if f.WalletID == wallet.String() {
			t.Errorf("wallet still reported after the rebuild: %+v", f)
		}
	}
	if !report.Clean {
		t.Errorf("reconciliation after rebuild is not clean: %+v", report)
	}

	// §9.7's "clear audit event", end to end.
	var actor uuid.UUID
	err := ownerQueryRow(t,
		`SELECT actor_id FROM wallet_db.wallet_audit_log WHERE wallet_id = $1 AND action = 'PROJECTION_REBUILD' ORDER BY id DESC LIMIT 1`,
		wallet).Scan(&actor)
	if err != nil {
		t.Fatalf("no PROJECTION_REBUILD audit row for wallet %s: %v", wallet, err)
	}
	if actor != adminID {
		t.Errorf("audit actor = %s, want the admin %s", actor, adminID)
	}
}

// TestE2E_RebuildDuringConsumerLagDoesNotDoubleCount is Task 08's scenario against the real stack:
// deposits commit while the projection cannot see them, an operator rebuilds, and then the
// lagging events arrive. The balance must equal the ledger, not the ledger plus the replay.
//
// The lag is made by stopping Kafka, not the Projection Service: the rebuild IS a Projection
// Service endpoint, so stopping that container would turn the rebuild into a 503. With Kafka down
// the postings commit to the ledger and wait in the outbox; the rebuild reads the ledger; when
// Kafka returns, the outbox publishes and every lagging event must be skipped.
func TestE2E_RebuildDuringConsumerLagDoesNotDoubleCount(t *testing.T) {
	userID := uuid.New()
	token := signJWT(t, userID, "user")
	_, admin := newAdmin(t)

	wallet := newWallet(t, token, userID)
	seedDeposit(t, wallet, "100.00")
	if got := pollBalance(t, token, wallet, 100.00); got.Balance != 100.00 {
		t.Fatalf("balance = %.2f, want 100.00 before the lag (last observed: %+v)", got.Balance, got)
	}

	compose(t, "stop", "kafka")
	t.Cleanup(func() { compose(t, "start", "kafka") }) // never leave the stack broken for later tests

	for i := 0; i < 3; i++ {
		seedDeposit(t, wallet, "50.00") // commits to the ledger; the event waits in the outbox
	}
	// Prove the lag is real before rebuilding: the projection has not seen the three deposits.
	// Without this, a Kafka that had not stopped in time would let the test pass vacuously.
	time.Sleep(2 * time.Second)
	if got := pollBalance(t, token, wallet, 100.00); got.Balance != 100.00 {
		t.Fatalf("projection = %.2f before the rebuild, want 100.00: the consumer was not lagging", got.Balance)
	}
	if n := unpublishedOutbox(t); n < 3 {
		t.Fatalf("%d unpublished outbox rows during the lag, want >= 3", n)
	}

	resp, body := rebuildProjection(t, admin, wallet)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rebuild during lag = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if got := pollBalance(t, token, wallet, 250.00); got.Balance != 250.00 {
		t.Fatalf("balance after rebuild = %.2f, want 250.00 (the ledger total)", got.Balance)
	}

	compose(t, "start", "kafka")

	// Lag 0: the outbox has published everything, and the consumer has had its SLO to apply it.
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) && unpublishedOutbox(t) > 0 {
		time.Sleep(time.Second)
	}
	if n := unpublishedOutbox(t); n > 0 {
		t.Fatalf("%d outbox rows still unpublished 90s after Kafka came back", n)
	}
	time.Sleep(5 * time.Second) // NFR-CONS-2: consumption within 5s of publication

	got := pollBalance(t, token, wallet, 250.00)
	if got.Balance != 250.00 {
		t.Errorf("balance after the lagging events arrived = %.2f, want 250.00 (400.00 would mean the three deposits were applied twice)", got.Balance)
	}
}

func ownerQueryRow(t *testing.T, sql string, args ...any) pgx.Row {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), ownerDSN)
	if err != nil {
		t.Fatalf("connect as owner: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn.QueryRow(context.Background(), sql, args...)
}

func unpublishedOutbox(t *testing.T) int {
	t.Helper()
	var n int
	if err := ownerQueryRow(t, `SELECT count(*) FROM ledger_db.outbox WHERE published_at IS NULL`).Scan(&n); err != nil {
		t.Fatalf("count unpublished outbox rows: %v", err)
	}
	return n
}

// compose runs `docker compose <args>` from the repository root.
func compose(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("docker", append([]string{"compose"}, args...)...)
	cmd.Dir = "../.."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker compose %v: %v\n%s", args, err, out)
	}
}
