package domain

import (
	"errors"
	"fmt"
	"sort"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

var (
	// ErrRateMissing means the country has no fee rate for a material that was shipped (FUL-0014).
	ErrRateMissing = errors.New("packaging fee rate missing")
	// ErrRateCurrency means the country rate is in another currency than the slip.
	ErrRateCurrency = errors.New("packaging fee rate currency differs from the slip currency")
	// ErrRTPNegative means a returnable packaging balance would go below zero (FUL-0015).
	ErrRTPNegative = errors.New("returnable packaging balance would become negative")
)

// PackagingUse is the packaging weight that shipped units carry for one material.
// Its weight is weight_per_unit_kg × shipped units, summed per material.
type PackagingUse struct {
	Material string
	WeightKg decimal.Decimal
}

// FeeRate is the country rate per kilogram for one material as of a date.
type FeeRate struct {
	RatePerKg decimal.Decimal
	Currency  string
}

// PackagingFee is one statistical packaging fee line on a packing slip (BR-28.12).
type PackagingFee struct {
	Material  string          `json:"material"`
	WeightKg  decimal.Decimal `json:"weightKg"`
	RatePerKg decimal.Decimal `json:"ratePerKg"`
	Amount    decimal.Decimal `json:"amount"`
	Currency  string          `json:"currency"`
}

// PackagingFees computes the fee per material. Each fee is rounded half-even to
// the minor unit of the currency, once per material. Materials with no weight
// produce no line. Materials are returned in sorted order.
func PackagingFees(uses []PackagingUse, rates map[string]FeeRate, currency string) ([]PackagingFee, error) {
	totals := map[string]decimal.Decimal{}
	for _, u := range uses {
		totals[u.Material] = totals[u.Material].Add(u.WeightKg)
	}
	materials := make([]string, 0, len(totals))
	for m := range totals {
		materials = append(materials, m)
	}
	sort.Strings(materials)

	minor := money.MinorUnits(currency)
	out := []PackagingFee{}
	for _, m := range materials {
		w := totals[m]
		if !w.IsPositive() {
			continue
		}
		rate, ok := rates[m]
		if !ok {
			return nil, fmt.Errorf("%s: %w", m, ErrRateMissing)
		}
		if rate.Currency != currency {
			return nil, fmt.Errorf("%s: %w", m, ErrRateCurrency)
		}
		out = append(out, PackagingFee{
			Material:  m,
			WeightKg:  w,
			RatePerKg: rate.RatePerKg,
			Amount:    w.Mul(rate.RatePerKg).RoundBank(minor),
			Currency:  currency,
		})
	}
	return out, nil
}

// ApplyBalance moves a returnable packaging balance by delta and rejects a
// negative result (BR-28.13).
func ApplyBalance(balance, delta decimal.Decimal) (decimal.Decimal, error) {
	next := balance.Add(delta)
	if next.IsNegative() {
		return balance, ErrRTPNegative
	}
	return next, nil
}
