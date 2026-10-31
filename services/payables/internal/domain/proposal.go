package domain

import (
	"cmp"
	"slices"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	CodeProposalConflict = "AP-PRP-001"
	CodeProposalState    = "AP-PRP-002"
	CodeSegregation      = "AP-PRP-003"
	CodeRunDatePassed    = "AP-PRP-004"
	CodeNothingDue       = "AP-PRP-005"
	CodeWithholding      = "AP-PRP-006"

	ProposalDraft     = "DRAFT"
	ProposalApproved  = "APPROVED"
	ProposalCancelled = "CANCELLED"
)

func ValidPaymentMethod(method string) bool {
	_, ok := methodFormats[method]
	return ok
}

type ProposalCandidate struct {
	ID               string
	VendorID         string
	Currency         string
	Version          int
	OpenAmount       decimal.Decimal
	DiscountAmount   decimal.Decimal
	DiscountDate     *time.Time
	PartiallySettled bool
}

type ProposalInput struct {
	RunDate             time.Time
	LegalEntityCurrency string
	DefaultMethod       string
	VendorMethods       map[string]string
	Rates               map[string]decimal.Decimal
	Candidates          []ProposalCandidate
}

type ProposalLine struct {
	OpenItemID      string          `json:"openItemId"`
	OpenItemVersion int             `json:"-"`
	OpenAmount      decimal.Decimal `json:"openAmount"`
	Discount        decimal.Decimal `json:"discount"`
	Pay             decimal.Decimal `json:"pay"`
	Withheld        decimal.Decimal `json:"withheld"`
	WithholdingCode string          `json:"withholdingCode,omitempty"`
	Rate            decimal.Decimal `json:"rate"`
	Valued          decimal.Decimal `json:"valued"`
}

type ProposalGroup struct {
	ID       string          `json:"id"`
	VendorID string          `json:"vendorId"`
	Currency string          `json:"currency"`
	Method   string          `json:"paymentMethod"`
	Gross    decimal.Decimal `json:"gross"`
	Discount decimal.Decimal `json:"discount"`
	Withheld decimal.Decimal `json:"withheld"`
	Net      decimal.Decimal `json:"net"`
	Valued   decimal.Decimal `json:"valued"`
	Lines    []ProposalLine  `json:"lines"`

	BankAccountID   string `json:"bankAccountId,omitempty"`
	BankLast4       string `json:"bankLast4,omitempty"`
	BankFingerprint string `json:"-"`
}

func discountFor(c ProposalCandidate, runDate time.Time) decimal.Decimal {
	if c.DiscountDate == nil || c.PartiallySettled || !c.DiscountAmount.IsPositive() || !c.DiscountAmount.LessThan(c.OpenAmount) || runDate.After(*c.DiscountDate) {
		return decimal.Zero
	}
	return c.DiscountAmount
}

func BuildProposal(in ProposalInput) ([]ProposalGroup, error) {
	type key struct{ vendor, currency, method string }
	groups := map[key]*ProposalGroup{}
	minor := money.MinorUnits(in.LegalEntityCurrency)
	for _, c := range in.Candidates {
		rate := decimal.NewFromInt(1)
		if c.Currency != in.LegalEntityCurrency {
			var ok bool
			if rate, ok = in.Rates[c.Currency]; !ok || !rate.IsPositive() {
				return nil, &Error{Code: CodeCurrency, Detail: "no exchange rate from " + c.Currency + " to " + in.LegalEntityCurrency}
			}
		}
		method := in.DefaultMethod
		if m, ok := in.VendorMethods[c.VendorID]; ok {
			method = m
		}
		k := key{c.VendorID, c.Currency, method}
		g := groups[k]
		if g == nil {
			g = &ProposalGroup{ID: ids.New(), VendorID: c.VendorID, Currency: c.Currency, Method: method}
			groups[k] = g
		}
		discount := discountFor(c, in.RunDate)
		pay := c.OpenAmount.Sub(discount)
		line := ProposalLine{OpenItemID: c.ID, OpenItemVersion: c.Version, OpenAmount: c.OpenAmount, Discount: discount, Pay: pay, Rate: rate, Valued: pay.Mul(rate).Round(minor)}
		g.Lines = append(g.Lines, line)
		g.Gross = g.Gross.Add(c.OpenAmount)
		g.Discount = g.Discount.Add(discount)
		g.Net = g.Net.Add(pay)
		g.Valued = g.Valued.Add(line.Valued)
	}
	out := make([]ProposalGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	slices.SortFunc(out, func(a, b ProposalGroup) int {
		return cmp.Or(cmp.Compare(a.VendorID, b.VendorID), cmp.Compare(a.Currency, b.Currency), cmp.Compare(a.Method, b.Method))
	})
	return out, nil
}

func (g *ProposalGroup) Withhold(line int, code string, amount decimal.Decimal, minor int32) {
	l := &g.Lines[line]
	valued := l.Pay.Sub(amount).Mul(l.Rate).Round(minor)
	g.Valued = g.Valued.Sub(l.Valued).Add(valued)
	g.Net = g.Net.Sub(amount)
	g.Withheld = g.Withheld.Add(amount)
	l.Valued, l.Withheld, l.WithholdingCode = valued, amount, code
}

func HasWithholding(groups []ProposalGroup) bool {
	for _, g := range groups {
		if g.Withheld.IsPositive() {
			return true
		}
	}
	return false
}
