package domain

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/shopspring/decimal"
)

// One-time customer orders (FR-27.22, AC-27.14): an order for a walk-in buyer
// is sold to the legal entity's shared one-time account and carries the
// buyer's name, address and tax id on the order header, for the invoice (w29)
// and the open item (w30) to copy.

const (
	maxOneTimeName    = 140
	maxOneTimeLine    = 100
	maxOneTimeTaxID   = 40
	oneTimeLimitScale = 2
)

var (
	countryCode    = regexp.MustCompile(`^[A-Z]{2}$`)
	currencyAlpha3 = regexp.MustCompile(`^[A-Z]{3}$`)
)

// DefaultOneTimeOrderLimits are the one_time_order_limit defaults per order
// currency (PRD 5.27.13), used when a legal entity saved none.
var DefaultOneTimeOrderLimits = map[string]decimal.Decimal{
	"USD": decimal.NewFromInt(5000),
	"EUR": decimal.NewFromInt(4500),
}

// OneTimeParty is the document-level party of a one-time order.
type OneTimeParty struct {
	Name       string `json:"name"`
	Line1      string `json:"line1"`
	Line2      string `json:"line2,omitempty"`
	City       string `json:"city"`
	Region     string `json:"region,omitempty"`
	PostalCode string `json:"postalCode"`
	Country    string `json:"country"`
	TaxID      string `json:"taxId,omitempty"`
}

// Validate returns the field errors of p keyed by JSON name (without prefix).
func (p OneTimeParty) Validate() map[string]string {
	f := map[string]string{}
	required := func(name, v string, max int) {
		if n := len(strings.TrimSpace(v)); n == 0 || len(v) > max {
			f[name] = fmt.Sprintf("must be 1 to %d characters", max)
		}
	}
	optional := func(name, v string, max int) {
		if len(v) > max {
			f[name] = fmt.Sprintf("must be at most %d characters", max)
		}
	}
	required("name", p.Name, maxOneTimeName)
	required("line1", p.Line1, maxOneTimeLine)
	optional("line2", p.Line2, maxOneTimeLine)
	required("city", p.City, maxOneTimeLine)
	optional("region", p.Region, maxOneTimeLine)
	required("postalCode", p.PostalCode, 20)
	if !countryCode.MatchString(p.Country) {
		f["country"] = "must be an ISO 3166 alpha-2 code"
	}
	optional("taxId", p.TaxID, maxOneTimeTaxID)
	return f
}

// OneTimeLimit returns the one_time_order_limit that applies to an order in
// currency: the legal entity's configured limit when it is stated in that
// currency, otherwise the default for USD or EUR. ok is false when no limit
// is known for the currency; the order is then refused, never waved through.
func (p Parameters) OneTimeLimit(currency string) (limit decimal.Decimal, ok bool) {
	if p.OneTimeOrderLimit != nil && p.OneTimeOrderLimitCurrency != nil {
		if *p.OneTimeOrderLimitCurrency == currency {
			return *p.OneTimeOrderLimit, true
		}
		return decimal.Zero, false
	}
	limit, ok = DefaultOneTimeOrderLimits[currency]
	return limit, ok
}

// OneTimeLimitError is the reason a one-time order is over its limit.
type OneTimeLimitError struct{ Detail string }

func (e *OneTimeLimitError) Error() string { return e.Detail }

// CheckOneTimeLimit refuses an order whose net total exceeds the one-time
// limit (SLS-0016). A total equal to the limit passes.
func (p Parameters) CheckOneTimeLimit(net decimal.Decimal, currency string) error {
	limit, ok := p.OneTimeLimit(currency)
	if !ok {
		return &OneTimeLimitError{fmt.Sprintf("no one-time order limit is set in %s; set one in the sales parameters or create a regular customer", currency)}
	}
	if net.GreaterThan(limit) {
		return &OneTimeLimitError{fmt.Sprintf("One-time order exceeds limit %s %s; create a regular customer", limit.StringFixed(oneTimeLimitScale), currency)}
	}
	return nil
}
