package domain

import (
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Commission bases (FR-26.18). REVENUE pays on the line net; MARGIN pays on the line net
// less the line's cost snapshot (w20).
const (
	CommissionRevenue = "REVENUE"
	CommissionMargin  = "MARGIN"
	// CommissionDebitAccount and CommissionCreditAccount are the reference commission_accounts
	// (Dr 6200 Commission expense / Cr 2170 Commission payable, PRD §5.26.13).
	CommissionDebitAccount  = "6200"
	CommissionCreditAccount = "2170"
	// MaxReps bounds the reps of one sales group.
	MaxReps = 20
	// MaxCommissionGroup is the longest customer or item commission group key.
	MaxCommissionGroup = 64
	// MaxInvoiceLineID is the longest invoice line id the invoice feed may send.
	MaxInvoiceLineID = 128
)

// Commission problem codes (message catalog PRC-*).
const (
	// CodeCommissionRuleMissing: no rule at any level for the sales group (PRC-0013, BR-26.12).
	CodeCommissionRuleMissing = "PRC-0013"
	// CodeSalesGroupNotFound: the posting or rule names an unknown sales group.
	CodeSalesGroupNotFound = "PRC-0036"
	// CodeCommissionCredit: a credit note cannot be applied to the line it names.
	CodeCommissionCredit = "PRC-0037"
	// CodeCommissionRuleExists: a rule already exists for the same sales group and groups.
	CodeCommissionRuleExists = "PRC-0038"
)

// CommissionRule pays RatePct on a basis for one sales group and one customer/item
// commission group combination. An empty group key means "all" (BR-26.12).
type CommissionRule struct {
	SalesGroupID  string
	CustomerGroup string
	ItemGroup     string
	Basis         string
	RatePct       decimal.Decimal
}

// ValidateCommissionRule checks a rule before it is stored. It returns nil when valid.
func ValidateCommissionRule(r CommissionRule) map[string]string {
	fields := map[string]string{}
	if r.Basis != CommissionRevenue && r.Basis != CommissionMargin {
		fields["basis"] = "must be REVENUE or MARGIN"
	}
	if r.RatePct.IsNegative() || r.RatePct.GreaterThan(decimal.NewFromInt(100)) {
		fields["ratePct"] = "must be from 0 to 100"
	}
	if len(r.CustomerGroup) > MaxCommissionGroup {
		fields["customerGroup"] = "must be at most 64 characters"
	}
	if len(r.ItemGroup) > MaxCommissionGroup {
		fields["itemGroup"] = "must be at most 64 characters"
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// RepIDRule describes a valid rep id for a validation message.
const RepIDRule = "must be 1 to 64 characters, without / or : and not . or .."

// ValidRepID reports whether id can be a rep id. The rep id is a path segment of the commission
// statement (/v1/pricing/commission/reps/{repId}/statement) and the gateway matches that
// segment with a glob whose * stops at / and :, and a client normalises . and .. away, so a rep
// id holding any of them could never be addressed.
func ValidRepID(id string) bool {
	return strings.TrimSpace(id) != "" && len(id) <= 64 && !strings.ContainsAny(id, "/:") && id != "." && id != ".."
}

// ValidateShares checks a sales group's rep shares (FR-26.18, BR-26.12): one to MaxReps reps,
// each rep once with a share above zero, and shares that sum to exactly 100 percent.
// The order of the reps matters: the first rep takes the rounding residual.
func ValidateShares(shares []Share) map[string]string {
	fields := map[string]string{}
	if len(shares) == 0 {
		fields["reps"] = "at least one rep is required"
		return fields
	}
	if len(shares) > MaxReps {
		fields["reps"] = "at most 20 reps"
		return fields
	}
	seen := map[string]bool{}
	total := decimal.Zero
	for i, s := range shares {
		key := "reps[" + strconv.Itoa(i) + "]"
		switch {
		case !ValidRepID(s.RepID):
			fields[key+".repId"] = RepIDRule
		case seen[s.RepID]:
			fields[key+".repId"] = "appears more than once"
		}
		seen[s.RepID] = true
		if !s.SharePct.IsPositive() || s.SharePct.GreaterThan(decimal.NewFromInt(100)) {
			fields[key+".sharePct"] = "must be above 0 and at most 100"
		}
		total = total.Add(s.SharePct)
	}
	if len(fields) == 0 && !total.Equal(decimal.NewFromInt(100)) {
		fields["reps"] = "shares must sum to 100"
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// ResolveCommissionRule returns the rule for a sales group and the line's customer and item
// commission groups (BR-26.12), searching specific to general:
// (sales group, customer group, item group), (sales group, customer group, all),
// (sales group, all, item group), (sales group, all, all). A blank line group only matches
// "all". ok is false when no level matches, which is PRC-0013. The store keeps one rule per
// (sales group, customer group, item group), so at most one rule matches a level.
func ResolveCommissionRule(rules []CommissionRule, salesGroup, customerGroup, itemGroup string) (CommissionRule, bool) {
	levels := [][2]string{
		{customerGroup, itemGroup},
		{customerGroup, ""},
		{"", itemGroup},
		{"", ""},
	}
	for _, lv := range levels {
		for _, r := range rules {
			if r.SalesGroupID == salesGroup && r.CustomerGroup == lv[0] && r.ItemGroup == lv[1] {
				return r, true
			}
		}
	}
	return CommissionRule{}, false
}

// CommissionLine is one posted invoice line (or credit note line) presented for commission.
// A credit note carries a negative LineNet (and, for MARGIN, a negative cost) and names the
// invoice line it reverses.
type CommissionLine struct {
	InvoiceLineID         string
	ReversesInvoiceLineID string
	InvoiceDate           time.Time
	CustomerID            string
	CustomerGroup         string
	ItemGroup             string
	Currency              string
	LineNet               decimal.Decimal
	CostSnapshot          decimal.Decimal
	// HasCost is false when the caller sent no cost snapshot; a MARGIN rule then cannot pay.
	HasCost bool
}

// IsCredit reports whether the line is a credit note line.
func (l CommissionLine) IsCredit() bool { return l.ReversesInvoiceLineID != "" }

// PeriodKey is the commission period (month) the line falls in, used by the inquiry.
func (l CommissionLine) PeriodKey() string { return PeriodKey(PeriodMonth, l.InvoiceDate) }

// ValidateCommissionLine checks a posting before it is applied. It returns nil when valid.
func ValidateCommissionLine(l CommissionLine) map[string]string {
	fields := map[string]string{}
	if l.InvoiceLineID == "" || len(l.InvoiceLineID) > MaxInvoiceLineID {
		fields["invoiceLineId"] = "must be 1 to 128 characters"
	}
	if len(l.ReversesInvoiceLineID) > MaxInvoiceLineID {
		fields["reversesInvoiceLineId"] = "must be at most 128 characters"
	} else if l.IsCredit() && l.ReversesInvoiceLineID == l.InvoiceLineID {
		fields["reversesInvoiceLineId"] = "must name another invoice line"
	}
	if l.InvoiceDate.IsZero() {
		fields["invoiceDate"] = "is required"
	}
	if len(l.Currency) != 3 {
		fields["currency"] = "must be an ISO 4217 code"
	}
	if len(l.CustomerGroup) > MaxCommissionGroup {
		fields["customerGroup"] = "must be at most 64 characters"
	}
	if len(l.ItemGroup) > MaxCommissionGroup {
		fields["itemGroup"] = "must be at most 64 characters"
	}
	switch {
	case l.IsCredit() && !l.LineNet.IsNegative():
		fields["lineNet"] = "a credit note line must carry a negative net"
	case !l.IsCredit() && l.LineNet.IsNegative():
		fields["lineNet"] = "a negative net is a credit note: name the reversed line in reversesInvoiceLineId"
	}
	if l.HasCost && ((l.IsCredit() && l.CostSnapshot.IsPositive()) || (!l.IsCredit() && l.CostSnapshot.IsNegative())) {
		fields["costSnapshot"] = "must carry the same sign as lineNet"
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// CommissionBasis is the amount a rate applies to (FR-26.18, BR-26.12): the line net for
// REVENUE, the line net less the cost snapshot for MARGIN. ok is false for MARGIN without a
// cost snapshot, so commission is never paid on revenue by mistake.
func CommissionBasis(basis string, l CommissionLine) (decimal.Decimal, bool) {
	if basis == CommissionMargin {
		if !l.HasCost {
			return decimal.Zero, false
		}
		return l.LineNet.Sub(l.CostSnapshot), true
	}
	return l.LineNet, true
}

func marginWithoutCost() *Error {
	return &Error{Code: CodeValidation, Detail: "the commission rule pays on MARGIN and needs the line cost",
		Fields: map[string]string{"costSnapshot": "is required for a MARGIN commission rule"}}
}

// CalculateCommission prices one invoice line under a rule and splits it across the sales
// group's reps (BR-26.12): basis x rate rounded half-even once, then each rep's share rounded
// half-even with the residual minor unit on the first rep. It returns the basis and the splits.
func CalculateCommission(rule CommissionRule, shares []Share, l CommissionLine) (decimal.Decimal, []Split, *Error) {
	basis, ok := CommissionBasis(rule.Basis, l)
	if !ok {
		return decimal.Zero, nil, marginWithoutCost()
	}
	amount := CommissionAmount(basis, rule.RatePct, l.Currency)
	return basis, SplitCommission(amount, l.Currency, shares), nil
}

// CommissionOriginal is what a credit note needs from the commission of the invoice line it
// reverses: the basis and rate that applied, the invoiced net, the reps and shares in their
// original order, and the net already credited by earlier credit notes (zero or negative).
type CommissionOriginal struct {
	Basis    string
	RatePct  decimal.Decimal
	LineNet  decimal.Decimal
	Currency string
	Shares   []Share
	Credited decimal.Decimal
}

// CreditCommission prices a credit note line against the commission of the line it reverses
// (BR-26.12): the original basis, rate and rep shares apply, so a full credit posts exactly
// the negative of the original split even if rules or shares changed since. Credits on one
// line may not exceed its invoiced net in total (PRC-0037).
func CreditCommission(orig CommissionOriginal, l CommissionLine) (decimal.Decimal, []Split, *Error) {
	if l.Currency != orig.Currency {
		return decimal.Zero, nil, &Error{Code: CodeCommissionCredit, Detail: "the credit note currency differs from the invoice line it reverses",
			Fields: map[string]string{"currency": "must be " + orig.Currency}}
	}
	if orig.Credited.Add(l.LineNet).Neg().GreaterThan(orig.LineNet) {
		return decimal.Zero, nil, &Error{Code: CodeCommissionCredit,
			Detail: "the credit notes of invoice line " + l.ReversesInvoiceLineID + " would exceed its invoiced net of " + orig.LineNet.String(),
			Fields: map[string]string{"lineNet": "credits may not exceed the invoiced net"}}
	}
	basis, ok := CommissionBasis(orig.Basis, l)
	if !ok {
		return decimal.Zero, nil, marginWithoutCost()
	}
	amount := CommissionAmount(basis, orig.RatePct, l.Currency)
	return basis, SplitCommission(amount, l.Currency, orig.Shares), nil
}
