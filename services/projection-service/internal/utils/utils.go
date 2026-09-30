package utils

import "github.com/shopspring/decimal"

// SignedDelta converts an entry's unsigned amount into the signed delta
// applied to a wallet's cached balance, per SPEC.md §3.3: CREDIT is
// positive, DEBIT is negative.
func SignedDelta(entryType string, amount decimal.Decimal) decimal.Decimal {
	if entryType == "DEBIT" {
		return amount.Neg()
	}
	return amount
}