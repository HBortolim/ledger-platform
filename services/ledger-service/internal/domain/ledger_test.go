package domain

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func mustMoney(t *testing.T, s string) Money {
	t.Helper()
	m, err := NewMoney(s)
	if err != nil {
		t.Fatalf("NewMoney(%q) = error %v, want nil", s, err)
	}
	return m
}

func TestValidateBalance_TwoEntryBalanced(t *testing.T) {
	txID := uuid.New()
	tx := LedgerTransaction{
		ID: txID,
		Entries: []LedgerEntry{
			{TransactionID: txID, EntryType: Debit, Amount: mustMoney(t, "100.00")},
			{TransactionID: txID, EntryType: Credit, Amount: mustMoney(t, "100.00")},
		},
	}
	if err := tx.ValidateBalance(); err != nil {
		t.Errorf("ValidateBalance() = %v, want nil", err)
	}
}

func TestValidateBalance_MultiEntryBalanced(t *testing.T) {
	txID := uuid.New()
	tx := LedgerTransaction{
		ID: txID,
		Entries: []LedgerEntry{
			{TransactionID: txID, EntryType: Debit, Amount: mustMoney(t, "60.00")},
			{TransactionID: txID, EntryType: Debit, Amount: mustMoney(t, "40.00")},
			{TransactionID: txID, EntryType: Credit, Amount: mustMoney(t, "100.00")},
		},
	}
	if err := tx.ValidateBalance(); err != nil {
		t.Errorf("ValidateBalance() = %v, want nil", err)
	}
}

func TestValidateBalance_Unbalanced(t *testing.T) {
	txID := uuid.New()
	tx := LedgerTransaction{
		ID: txID,
		Entries: []LedgerEntry{
			{TransactionID: txID, EntryType: Debit, Amount: mustMoney(t, "100.00")},
			{TransactionID: txID, EntryType: Credit, Amount: mustMoney(t, "50.00")},
		},
	}
	err := tx.ValidateBalance()
	if err == nil {
		t.Fatal("ValidateBalance() = nil, want an unbalanced error")
	}
	if !strings.Contains(err.Error(), txID.String()) {
		t.Errorf("error %q does not name the transaction ID %s", err.Error(), txID)
	}
}

func TestValidateBalance_SignConvention(t *testing.T) {
	// A single CREDIT with no offsetting DEBIT nets positive, not zero.
	txID := uuid.New()
	tx := LedgerTransaction{
		ID: txID,
		Entries: []LedgerEntry{
			{TransactionID: txID, EntryType: Credit, Amount: mustMoney(t, "10.00")},
		},
	}
	if err := tx.ValidateBalance(); err == nil {
		t.Error("ValidateBalance() = nil for a lone CREDIT entry, want an unbalanced error")
	}
}

func TestValidateBalance_EmptyEntries(t *testing.T) {
	tx := LedgerTransaction{ID: uuid.New()}
	if err := tx.ValidateBalance(); err != nil {
		t.Errorf("ValidateBalance() with no entries = %v, want nil (net is zero)", err)
	}
}
