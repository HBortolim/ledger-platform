package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// MaxReportedFindings bounds how many individual findings one check carries in a
// report and how many the service logs. The Total counts are never capped.
const MaxReportedFindings = 50

// Check 1
type UnbalancedTransaction struct {
	TransactionID uuid.UUID
	Net           decimal.Decimal // signed: credits minus debits
	EntryCount    int             // 0 means a transaction header with no entries at all
}

type InvariantFindings struct {
	Total  int
	Sample []UnbalancedTransaction
}

func (f InvariantFindings) Clean() bool     { return f.Total == 0 }
func (f InvariantFindings) Truncated() bool { return f.Total > len(f.Sample) }

// Check 2

// DriftKind says which way a wallet's projection disagrees with the ledger.
type DriftKind string

const (
	DriftMismatch          DriftKind = "MISMATCH"           // both exist, balances differ
	DriftProjectionMissing DriftKind = "PROJECTION_MISSING" // ledger entries, no projection row
	DriftProjectionOrphan  DriftKind = "PROJECTION_ORPHAN"  // projection row, no ledger entries
)

type ProjectionDrift struct {
	WalletID  uuid.UUID
	Kind      DriftKind
	Projected decimal.Decimal // zero when Kind == DriftProjectionMissing
	Actual    decimal.Decimal // zero when Kind == DriftProjectionOrphan
}

// DriftFindings is check 2's result.
type DriftFindings struct {
	Total  int
	Sample []ProjectionDrift
}

func (f DriftFindings) Clean() bool     { return f.Total == 0 }
func (f DriftFindings) Truncated() bool { return f.Total > len(f.Sample) }

// Check 3

// MoneySupply is check 3's result (NFR-CONS-5): two independent totals that must
// each be exactly zero. They diagnose different bugs, so they stay separate.
type MoneySupply struct {
	LedgerTotal decimal.Decimal // signed sum of every ledger entry
	CachedTotal decimal.Decimal // sum of every account_balances_locked.balance
}

func (m MoneySupply) LedgerClean() bool { return m.LedgerTotal.IsZero() }
func (m MoneySupply) CachedClean() bool { return m.CachedTotal.IsZero() }
func (m MoneySupply) Clean() bool       { return m.LedgerClean() && m.CachedClean() }

// UncleanCount is how many of the two totals are non-zero (0..2).
func (m MoneySupply) UncleanCount() int {
	count := 0
	if !m.LedgerClean() {
		count++
	}
	if !m.CachedClean() {
		count++
	}
	return count
}

// Full Check Report
type ReconciliationReport struct {
	RanAt       time.Time
	Duration    time.Duration
	Invariant   InvariantFindings
	Projection  DriftFindings
	MoneySupply MoneySupply
}

func (r ReconciliationReport) Clean() bool {
	return r.Invariant.Clean() && r.Projection.Clean() && r.MoneySupply.Clean()
}
