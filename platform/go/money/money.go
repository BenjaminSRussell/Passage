// Package money is the only way Passage represents amounts: a decimal value
// with a currency, rounded to the ISO 4217 minor unit of that currency.
package money

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

// Amount is a currency-tagged decimal. The zero value is invalid (no currency).
type Amount struct {
	Value    decimal.Decimal
	Currency string
}

// ErrCurrencyMismatch is returned when two amounts in different currencies are combined.
var ErrCurrencyMismatch = errors.New("money: currency mismatch")

// minorUnits lists ISO 4217 exponents that differ from the default of 2.
var minorUnits = map[string]int32{
	"BHD": 3, "IQD": 3, "JOD": 3, "KWD": 3, "LYD": 3, "OMR": 3, "TND": 3,
	"BIF": 0, "CLP": 0, "DJF": 0, "GNF": 0, "ISK": 0, "JPY": 0, "KMF": 0, "KRW": 0, "PYG": 0, "RWF": 0, "UGX": 0, "UYI": 0, "VND": 0, "VUV": 0, "XAF": 0, "XOF": 0, "XPF": 0,
	"CLF": 4, "UYW": 4,
}

// MinorUnits returns the number of decimal places for a currency (2 if unknown).
func MinorUnits(currency string) int32 {
	if e, ok := minorUnits[currency]; ok {
		return e
	}
	return 2
}

// New builds an amount from a decimal string, rounding half-away-from-zero to the minor unit.
func New(value, currency string) (Amount, error) {
	if len(currency) != 3 {
		return Amount{}, fmt.Errorf("money: invalid currency %q", currency)
	}
	d, err := decimal.NewFromString(value)
	if err != nil {
		return Amount{}, err
	}
	return Amount{Value: d, Currency: currency}.Round(), nil
}

// Must is New for constants in tests and fixtures.
func Must(value, currency string) Amount {
	a, err := New(value, currency)
	if err != nil {
		panic(err)
	}
	return a
}

// Round rounds to the currency's minor unit, half away from zero (the
// convention AX and SAP both use for document totals).
func (a Amount) Round() Amount {
	return Amount{Value: a.Value.Round(MinorUnits(a.Currency)), Currency: a.Currency}
}

// Add returns a+b or ErrCurrencyMismatch.
func (a Amount) Add(b Amount) (Amount, error) {
	if a.Currency != b.Currency {
		return Amount{}, ErrCurrencyMismatch
	}
	return Amount{Value: a.Value.Add(b.Value), Currency: a.Currency}, nil
}

// Neg returns -a.
func (a Amount) Neg() Amount { return Amount{Value: a.Value.Neg(), Currency: a.Currency} }

// Mul multiplies by an unrounded factor (price × quantity, FX rate) and rounds once.
func (a Amount) Mul(f decimal.Decimal) Amount {
	return Amount{Value: a.Value.Mul(f), Currency: a.Currency}.Round()
}

// IsZero reports whether the value is zero.
func (a Amount) IsZero() bool { return a.Value.IsZero() }

// String renders "123.45 USD".
func (a Amount) String() string { return a.Value.StringFixed(MinorUnits(a.Currency)) + " " + a.Currency }

// Allocate splits a into n parts whose sum is exactly a, distributing the
// remainder one minor unit at a time from the first part (largest-remainder).
func (a Amount) Allocate(n int) []Amount {
	if n <= 0 {
		return nil
	}
	exp := MinorUnits(a.Currency)
	unit := decimal.New(1, -exp)
	base := a.Value.Div(decimal.NewFromInt(int64(n))).RoundDown(exp)
	parts := make([]Amount, n)
	remainder := a.Value.Sub(base.Mul(decimal.NewFromInt(int64(n))))
	for i := range parts {
		v := base
		if remainder.Abs().GreaterThanOrEqual(unit) {
			if remainder.Sign() >= 0 {
				v = v.Add(unit)
				remainder = remainder.Sub(unit)
			} else {
				v = v.Sub(unit)
				remainder = remainder.Add(unit)
			}
		}
		parts[i] = Amount{Value: v, Currency: a.Currency}
	}
	return parts
}

// Sum adds amounts, failing on a currency mismatch. Sum of nothing is a zero in cur.
func Sum(cur string, amounts ...Amount) (Amount, error) {
	total := Amount{Value: decimal.Zero, Currency: cur}
	for _, x := range amounts {
		var err error
		if total, err = total.Add(x); err != nil {
			return Amount{}, err
		}
	}
	return total, nil
}
