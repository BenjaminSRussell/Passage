package api

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
)

var taxCodeText = regexp.MustCompile(`^[A-Z0-9_-]{1,20}$`)

// ErrTaxUnavailable is returned by a TaxEngine that cannot quote.
var ErrTaxUnavailable = errors.New("tax engine unavailable")

// UnknownTaxCodeError is returned by a TaxEngine that does not know a line's tax code.
type UnknownTaxCodeError struct{ Code string }

func (e *UnknownTaxCodeError) Error() string {
	return "tax code " + e.Code + " is not known to the tax engine"
}

// TaxEngine quotes one tax amount per line (FR-29.6). Implementations return
// ErrTaxUnavailable rather than zero when they cannot answer.
type TaxEngine interface {
	Quote(ctx context.Context, currency string, lines []domain.Line) ([]decimal.Decimal, error)
}

// UnavailableTax is the engine used when no rates are configured and the
// wave-13 tax client is not wired: every quote fails, so no invoice is proposed
// or posted with silent zero tax.
type UnavailableTax struct{}

// Quote always fails with ErrTaxUnavailable.
func (UnavailableTax) Quote(context.Context, string, []domain.Line) ([]decimal.Decimal, error) {
	return nil, ErrTaxUnavailable
}

// RateTableTax is the interim engine for environments without the tax service:
// an explicit percentage per tax code (BILLING_TAX_RATES), applied to the
// rounded line net. A code that is not in the table is refused, never taxed at zero.
type RateTableTax struct{ rates map[string]decimal.Decimal }

// Quote returns line_net × rate / 100 per line; rounding to the minor unit is
// left to domain.Compute (LINE rounding, BR-29.3).
func (t RateTableTax) Quote(_ context.Context, currency string, lines []domain.Line) ([]decimal.Decimal, error) {
	out := make([]decimal.Decimal, len(lines))
	for i, l := range lines {
		rate, ok := t.rates[l.TaxCode]
		if !ok {
			return nil, &UnknownTaxCodeError{Code: l.TaxCode}
		}
		out[i] = l.Net(currency).Mul(rate).Div(decimal.NewFromInt(100))
	}
	return out, nil
}

// ParseTaxRates reads BILLING_TAX_RATES ("DE19=19,DE7=7,EXEMPT=0"). An empty
// setting returns UnavailableTax, so posting fails closed with BIL-0001.
func ParseTaxRates(raw string) (TaxEngine, error) {
	if strings.TrimSpace(raw) == "" {
		return UnavailableTax{}, nil
	}
	rates := map[string]decimal.Decimal{}
	for _, pair := range strings.Split(raw, ",") {
		code, pct, ok := strings.Cut(strings.TrimSpace(pair), "=")
		rate, err := decimal.NewFromString(pct)
		if !ok || !taxCodeText.MatchString(code) || err != nil || rate.IsNegative() || rate.GreaterThan(decimal.NewFromInt(100)) {
			return nil, fmt.Errorf("BILLING_TAX_RATES entry %q must be CODE=percent with a percent from 0 to 100", pair)
		}
		if _, dup := rates[code]; dup {
			return nil, fmt.Errorf("BILLING_TAX_RATES lists %s twice", code)
		}
		rates[code] = rate
	}
	return RateTableTax{rates: rates}, nil
}
