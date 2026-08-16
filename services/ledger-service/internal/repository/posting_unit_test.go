package repository

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/ledger-platform/ledger-service/internal/domain"
)

func TestCheckAvailableBalance_SystemAccountExemptFromOverdraftCheck(t *testing.T) {
	systemAccountID := uuid.New()
	r := &PostingRepository{systemAccountID: systemAccountID}

	amount, err := domain.NewMoney("500.00")
	if err != nil {
		t.Fatalf("domain.NewMoney() = error %v, want nil", err)
	}
	entries := []domain.LedgerEntry{
		{AccountID: systemAccountID, EntryType: domain.Debit, Amount: amount},
	}
	balances := map[uuid.UUID]decimal.Decimal{systemAccountID: decimal.Zero}

	if err := r.checkAvailableBalance(entries, balances); err != nil {
		t.Errorf("checkAvailableBalance() = error %v, want nil (system account must be exempt)", err)
	}
}

func TestCheckAvailableBalance_NormalAccountStillRejectsOverdraft(t *testing.T) {
	systemAccountID := uuid.New()
	normalAccountID := uuid.New()
	r := &PostingRepository{systemAccountID: systemAccountID}

	amount, err := domain.NewMoney("500.00")
	if err != nil {
		t.Fatalf("domain.NewMoney() = error %v, want nil", err)
	}
	entries := []domain.LedgerEntry{
		{AccountID: normalAccountID, EntryType: domain.Debit, Amount: amount},
	}
	balances := map[uuid.UUID]decimal.Decimal{normalAccountID: decimal.Zero}

	err = r.checkAvailableBalance(entries, balances)
	var insufficient domain.ErrInsufficientFunds
	if !errors.As(err, &insufficient) {
		t.Fatalf("checkAvailableBalance() = %v, want ErrInsufficientFunds", err)
	}
	if insufficient.AccountID != normalAccountID {
		t.Errorf("ErrInsufficientFunds.AccountID = %s, want %s", insufficient.AccountID, normalAccountID)
	}
}
