package repository

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ledger-platform/ledger-service/internal/domain"
	"github.com/shopspring/decimal"
)

func mustMoney(t *testing.T, s string) domain.Money {
	t.Helper()
	m, err := domain.NewMoney(s)
	if err != nil {
		t.Fatalf("domain.NewMoney(%q) = error %v, want nil", s, err)
	}
	return m
}

func TestAllAccountIDs_DedupesPreservingOrder(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	entries := []domain.LedgerEntry{
		{AccountID: a, EntryType: domain.Debit},
		{AccountID: b, EntryType: domain.Credit},
		{AccountID: a, EntryType: domain.Credit}, // duplicate of a
	}

	got := allAccountIDs(entries)
	want := []uuid.UUID{a, b}
	if len(got) != len(want) {
		t.Fatalf("allAccountIDs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("allAccountIDs()[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

// sortedAccountIDs must include BOTH debit and credit accounts: Post() locks
// this full set up front (ADR-002) specifically because applyBalanceDeltas
// later mutates credit accounts too — excluding them here is what caused a
// real deadlock between opposite-direction transfers (caught by
// tests/posting_concurrency_test.go's ConcurrentOppositeOrderPostings case).
func TestSortedAccountIDs_DedupesAllAccountsAndSorts(t *testing.T) {
	// Deliberately construct accounts whose UUIDs sort in the opposite order
	// from how they're listed, plus a duplicate entry, to verify both
	// dedup and sort — across debit AND credit roles.
	low := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	high := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")

	entries := []domain.LedgerEntry{
		{AccountID: high, EntryType: domain.Debit},
		{AccountID: low, EntryType: domain.Credit},
		{AccountID: high, EntryType: domain.Debit}, // duplicate
	}

	got := sortedAccountIDs(entries)
	want := []uuid.UUID{low, high}
	if len(got) != len(want) {
		t.Fatalf("sortedAccountIDs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sortedAccountIDs()[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestNilIfEmpty(t *testing.T) {
	if got := nilIfEmpty(""); got != nil {
		t.Errorf("nilIfEmpty(\"\") = %v, want nil", got)
	}
	got := nilIfEmpty("a description")
	if got == nil || *got != "a description" {
		t.Errorf("nilIfEmpty(\"a description\") = %v, want pointer to \"a description\"", got)
	}
}

func TestCheckAvailableBalance_Sufficient(t *testing.T) {
	r := &PostingRepository{}
	account := uuid.New()
	entries := []domain.LedgerEntry{
		{AccountID: account, EntryType: domain.Debit, Amount: mustMoney(t, "50.00")},
	}
	balances := map[uuid.UUID]decimal.Decimal{account: decimal.NewFromInt(100)}

	if err := r.checkAvailableBalance(entries, balances); err != nil {
		t.Errorf("checkAvailableBalance() = %v, want nil", err)
	}
}

func TestCheckAvailableBalance_Insufficient(t *testing.T) {
	r := &PostingRepository{}
	account := uuid.New()
	entries := []domain.LedgerEntry{
		{AccountID: account, EntryType: domain.Debit, Amount: mustMoney(t, "150.00")},
	}
	balances := map[uuid.UUID]decimal.Decimal{account: decimal.NewFromInt(100)}

	err := r.checkAvailableBalance(entries, balances)
	var insufficient domain.ErrInsufficientFunds
	if !errors.As(err, &insufficient) {
		t.Fatalf("checkAvailableBalance() = %v, want ErrInsufficientFunds", err)
	}
	if insufficient.AccountID != account {
		t.Errorf("ErrInsufficientFunds.AccountID = %s, want %s", insufficient.AccountID, account)
	}
}

// Two individually-affordable debits against the same account must be summed
// before comparing against the cached balance — this is the exact
// accumulation bug checkAvailableBalance exists to prevent.
func TestCheckAvailableBalance_SumsMultipleDebitsAgainstSameAccount(t *testing.T) {
	r := &PostingRepository{}
	account := uuid.New()
	entries := []domain.LedgerEntry{
		{AccountID: account, EntryType: domain.Debit, Amount: mustMoney(t, "60.00")},
		{AccountID: account, EntryType: domain.Debit, Amount: mustMoney(t, "60.00")},
	}
	balances := map[uuid.UUID]decimal.Decimal{account: decimal.NewFromInt(100)}

	err := r.checkAvailableBalance(entries, balances)
	var insufficient domain.ErrInsufficientFunds
	if !errors.As(err, &insufficient) {
		t.Fatalf("checkAvailableBalance() = %v, want ErrInsufficientFunds (60+60 > 100)", err)
	}
}

func TestCheckAvailableBalance_CreditOnlyNeverChecked(t *testing.T) {
	r := &PostingRepository{}
	account := uuid.New()
	entries := []domain.LedgerEntry{
		{AccountID: account, EntryType: domain.Credit, Amount: mustMoney(t, "1000000.00")},
	}
	// No balance entry at all for this account — a credit-only entry list
	// must never consult the balances map.
	if err := r.checkAvailableBalance(entries, map[uuid.UUID]decimal.Decimal{}); err != nil {
		t.Errorf("checkAvailableBalance() = %v, want nil for credit-only entries", err)
	}
}
