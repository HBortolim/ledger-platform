package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

type Money struct {
	amount decimal.Decimal
}

func NewMoney(s string) (Money, error) {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return Money{}, fmt.Errorf("invalid money value %q: %w", s, err)
	}
	if d.Exponent() < -2 {
		return Money{}, fmt.Errorf("money value %q has more than 2 decimal places", s)
	}
	if d.IsNegative() || d.IsZero() {
		return Money{}, fmt.Errorf("money value must be positive, got %q", s)
	}
	return Money{amount: d}, nil
}

func (m Money) IsPositive() bool       { return m.amount.IsPositive() }
func (m Money) GreaterThan(o Money) bool { return m.amount.GreaterThan(o.amount) }
func (m Money) String() string         { return m.amount.StringFixed(2) }
func (m Money) Decimal() decimal.Decimal { return m.amount }
