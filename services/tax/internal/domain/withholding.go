package domain

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	PartyVendor   = "vendor"
	PartyCustomer = "customer"
)

type WithholdingRate struct {
	ValidFrom Date `json:"valid_from"`
	ValidTo   Date `json:"valid_to"`
	RatePct   Dec  `json:"rate_pct"`
	Threshold *Dec `json:"threshold"`
}

type WithholdingCode struct {
	Code         string            `json:"code"`
	Jurisdiction string            `json:"jurisdiction"`
	IncomeType   string            `json:"income_type"`
	Rates        []WithholdingRate `json:"rates"`
}

type WithholdingRequest struct {
	Currency    string            `json:"currency"`
	PartyType   string            `json:"party_type"`
	PaymentDate Date              `json:"payment_date"`
	Code        string            `json:"code"`
	GrossAmount Dec               `json:"gross_amount"`
	PaidToDate  Dec               `json:"paid_to_date"`
	Codes       []WithholdingCode `json:"codes"`
}

type WithholdingResult struct {
	Code         string   `json:"code"`
	Jurisdiction string   `json:"jurisdiction"`
	IncomeType   string   `json:"income_type"`
	PartyType    string   `json:"party_type"`
	RatePct      Dec      `json:"rate_pct"`
	GrossAmount  Dec      `json:"gross_amount"`
	TaxableBase  Dec      `json:"taxable_base"`
	Withheld     Dec      `json:"withholding_amount"`
	NetPayable   Dec      `json:"net_payable"`
	Trace        []string `json:"determination_trace"`
}

func CalculateWithholding(req WithholdingRequest) (WithholdingResult, error) {
	if err := validateWithholding(req); err != nil {
		return WithholdingResult{}, err
	}
	code, ok := findWithholdingCode(req)
	if !ok {
		return WithholdingResult{}, &Error{Code: CodeGroupUnresolved, Field: "code", Detail: fmt.Sprintf("withholding code %q is not configured", req.Code)}
	}
	rate, err := effectiveWithholdingRate(code, req.PaymentDate)
	if err != nil {
		return WithholdingResult{}, err
	}
	gross, paid := req.GrossAmount.Decimal, req.PaidToDate.Decimal
	res := WithholdingResult{
		Code: code.Code, Jurisdiction: code.Jurisdiction, IncomeType: code.IncomeType, PartyType: req.PartyType,
		RatePct: rate.RatePct, GrossAmount: req.GrossAmount,
		Trace: []string{fmt.Sprintf("step 1: code %s rate %s%% valid on %s", code.Code, rate.RatePct, req.PaymentDate.Format(dateLayout))},
	}
	base := taxableBase(rate, gross, paid)
	res.Trace = append(res.Trace, fmt.Sprintf("step 2: taxable base %s from gross %s after %s paid to date", base, gross, paid))
	withheld := base.Mul(rate.RatePct.Decimal).Div(hundred).Round(money.MinorUnits(req.Currency))
	res.TaxableBase = Dec{base}
	res.Withheld = Dec{withheld}
	res.NetPayable = Dec{gross.Sub(withheld)}
	res.Trace = append(res.Trace, fmt.Sprintf("step 3: withholding %s, net payable %s", withheld, res.NetPayable))
	return res, nil
}

func taxableBase(rate WithholdingRate, gross, paid decimal.Decimal) decimal.Decimal {
	if rate.Threshold == nil {
		return gross
	}
	cumulative := paid.Add(gross)
	if !cumulative.GreaterThan(rate.Threshold.Decimal) {
		return decimal.Zero
	}
	return decimal.Min(gross, cumulative.Sub(rate.Threshold.Decimal))
}

func findWithholdingCode(req WithholdingRequest) (WithholdingCode, bool) {
	for _, c := range req.Codes {
		if c.Code == req.Code {
			return c, true
		}
	}
	return WithholdingCode{}, false
}

func effectiveWithholdingRate(code WithholdingCode, on Date) (WithholdingRate, error) {
	var matches []WithholdingRate
	for _, r := range code.Rates {
		if within(r.ValidFrom, r.ValidTo, on.Time) {
			matches = append(matches, r)
		}
	}
	switch len(matches) {
	case 0:
		return WithholdingRate{}, &Error{Code: CodeRateNotFound, Field: "payment_date", Detail: fmt.Sprintf("no withholding rate for %s valid on %s", code.Code, on.Format(dateLayout))}
	case 1:
		return matches[0], nil
	}
	return WithholdingRate{}, &Error{Code: CodeRateInvalid, Field: "codes", Detail: fmt.Sprintf("overlapping withholding rates for %s on %s", code.Code, on.Format(dateLayout))}
}

func validateWithholding(req WithholdingRequest) error {
	switch {
	case len(req.Currency) != 3 || req.Currency != strings.ToUpper(req.Currency):
		return invalid("currency", "currency must be a 3-letter ISO 4217 code")
	case !oneOf(req.PartyType, PartyVendor, PartyCustomer):
		return invalid("party_type", "party_type must be %q or %q", PartyVendor, PartyCustomer)
	case req.PaymentDate.IsZero():
		return invalid("payment_date", "payment_date is required")
	case req.Code == "":
		return invalid("code", "code is required")
	case !req.GrossAmount.GreaterThan(decimal.Zero):
		return invalid("gross_amount", "gross_amount must be positive")
	case req.GrossAmount.Exponent() < -money.MinorUnits(req.Currency):
		return invalid("gross_amount", "gross_amount has more decimals than %s allows", req.Currency)
	case req.PaidToDate.IsNegative():
		return invalid("paid_to_date", "paid_to_date must not be negative")
	}
	seen := map[string]bool{}
	for i, c := range req.Codes {
		if seen[c.Code] {
			return invalid(fmt.Sprintf("codes[%d].code", i), "duplicate withholding code %q", c.Code)
		}
		seen[c.Code] = true
		for j, r := range c.Rates {
			field := fmt.Sprintf("codes[%d].rates[%d]", i, j)
			switch {
			case r.RatePct.IsNegative() || r.RatePct.GreaterThan(hundred):
				return &Error{Code: CodeRateInvalid, Field: field + ".rate_pct", Detail: "rate_pct must be between 0 and 100"}
			case !r.ValidTo.IsZero() && r.ValidTo.Before(r.ValidFrom.Time):
				return invalid(field+".valid_to", "valid_to must not be before valid_from")
			case r.Threshold != nil && r.Threshold.IsNegative():
				return invalid(field+".threshold", "threshold must not be negative")
			}
		}
	}
	return nil
}
