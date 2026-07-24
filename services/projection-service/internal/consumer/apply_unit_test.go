package consumer

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestSignedDelta_Credit_ReturnsPositiveAmount(t *testing.T) {
	amount := decimal.RequireFromString("100.00")

	got := signedDelta("CREDIT", amount)

	if !got.Equal(amount) {
		t.Errorf("signedDelta(CREDIT, 100.00) = %s, want 100.00", got)
	}
}

func TestSignedDelta_Debit_ReturnsNegativeAmount(t *testing.T) {
	amount := decimal.RequireFromString("100.00")
	want := decimal.RequireFromString("-100.00")

	got := signedDelta("DEBIT", amount)

	if !got.Equal(want) {
		t.Errorf("signedDelta(DEBIT, 100.00) = %s, want -100.00", got)
	}
}
