package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// RecoveryPolicy is the share of an input tax a business may reclaim, per tax code and, optionally, per item tax group (entertainment
// 0 percent, company cars 50 percent), for the dates it is valid. An empty ItemGroup applies to every group of the code; a policy for
// the line's own group beats it, and of two the later ValidFrom wins.
type RecoveryPolicy struct {
	Code           string `json:"code"`
	ItemGroup      string `json:"item_group"`
	RecoverablePct Dec    `json:"recoverable_pct"`
	ValidFrom      Date   `json:"valid_from"`
	ValidTo        Date   `json:"valid_to"`
	Note           string `json:"note"`
}

// ValidateRecoveryPolicy checks a policy; knownCodes are the stored tax codes it may name and knownItemGroups the item tax groups.
func ValidateRecoveryPolicy(p RecoveryPolicy, knownCodes, knownItemGroups map[string]bool) error {
	switch {
	case !knownCodes[p.Code]:
		return invalid("code", "tax code %q does not exist", p.Code)
	case p.ItemGroup != "" && !knownItemGroups[p.ItemGroup]:
		return invalid("item_group", "item tax group %q does not exist", p.ItemGroup)
	case p.RecoverablePct.IsNegative() || p.RecoverablePct.GreaterThan(hundred):
		return invalid("recoverable_pct", "recoverable_pct must be between 0 and 100")
	case p.RecoverablePct.Exponent() < -4:
		return invalid("recoverable_pct", "recoverable_pct has at most 4 decimal places")
	case p.ValidFrom.IsZero():
		return invalid("valid_from", "valid_from is required")
	case !p.ValidTo.IsZero() && p.ValidTo.Before(p.ValidFrom.Time):
		return invalid("valid_to", "valid_to must not be before valid_from")
	case len(p.Note) > 200:
		return invalid("note", "note is at most 200 characters")
	}
	return nil
}

// RecoveryFor picks the policy that applies to a code and item group on a date, or nil when none does.
func RecoveryFor(policies []RecoveryPolicy, code, itemGroup string, on time.Time) *Dec {
	var best *RecoveryPolicy
	for i := range policies {
		p := &policies[i]
		if p.Code != code || (p.ItemGroup != "" && p.ItemGroup != itemGroup) || !within(p.ValidFrom, p.ValidTo, on) {
			continue
		}
		switch {
		case best == nil:
			best = p
		case p.ItemGroup != "" && best.ItemGroup == "":
			best = p
		case p.ItemGroup == best.ItemGroup && p.ValidFrom.After(best.ValidFrom.Time):
			best = p
		}
	}
	if best == nil {
		return nil
	}
	pct := best.RecoverablePct
	return &pct
}

// ReclaimTxn is one recorded input or use tax transaction with what is known of its recovery.
type ReclaimTxn struct {
	Code        string
	Direction   string
	TaxDate     time.Time
	Amount      decimal.Decimal
	Recoverable *decimal.Decimal // what the caller computed with the recovery policy, when it did
}

// ReclaimRow is the input tax of one code over a period and how much of it may be reclaimed.
type ReclaimRow struct {
	Code           string          `json:"code"`
	InputTax       decimal.Decimal `json:"input_tax"`
	Recoverable    decimal.Decimal `json:"recoverable"`
	NonRecoverable decimal.Decimal `json:"non_recoverable"`
	Transactions   int             `json:"transactions"`
	// Estimated counts the transactions recorded without a recoverable amount, which took the code's own recoverable_pct.
	Estimated int `json:"estimated"`
}

// Reclaim is the input tax to reclaim for a period: what the return claims, and what stays in the cost of the purchases.
type Reclaim struct {
	PeriodFrom          Date            `json:"period_from"`
	PeriodTo            Date            `json:"period_to"`
	Rows                []ReclaimRow    `json:"rows"`
	TotalInputTax       decimal.Decimal `json:"total_input_tax"`
	TotalRecoverable    decimal.Decimal `json:"total_recoverable"`
	TotalNonRecoverable decimal.Decimal `json:"total_non_recoverable"`
}

// ComputeReclaim totals the input tax of the period per code. A transaction's recoverable amount is the one recorded with it, else the
// code's recoverable_pct (codePct, absent means all of it) of its amount, rounded to the cent; it never exceeds the tax itself.
func ComputeReclaim(period Period, codePct map[string]*Dec, txns []ReclaimTxn) Reclaim {
	out := Reclaim{PeriodFrom: period.From, PeriodTo: period.To, Rows: []ReclaimRow{}}
	byCode := map[string]*ReclaimRow{}
	for _, t := range txns {
		if t.Direction != DirInput || t.TaxDate.Before(period.From.Time) || t.TaxDate.After(period.To.Time) {
			continue
		}
		row := byCode[t.Code]
		if row == nil {
			row = &ReclaimRow{Code: t.Code}
			byCode[t.Code] = row
		}
		rec := t.Amount
		switch {
		case t.Recoverable != nil:
			rec = *t.Recoverable
		case codePct[t.Code] != nil:
			rec = t.Amount.Mul(codePct[t.Code].Decimal).Div(hundred).Round(2)
			row.Estimated++
		default:
			row.Estimated++
		}
		if rec.Abs().GreaterThan(t.Amount.Abs()) {
			rec = t.Amount
		}
		row.InputTax = row.InputTax.Add(t.Amount)
		row.Recoverable = row.Recoverable.Add(rec)
		row.NonRecoverable = row.NonRecoverable.Add(t.Amount.Sub(rec))
		row.Transactions++
	}
	codes := make([]string, 0, len(byCode))
	for c := range byCode {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	for _, c := range codes {
		r := byCode[c]
		out.Rows = append(out.Rows, *r)
		out.TotalInputTax = out.TotalInputTax.Add(r.InputTax)
		out.TotalRecoverable = out.TotalRecoverable.Add(r.Recoverable)
		out.TotalNonRecoverable = out.TotalNonRecoverable.Add(r.NonRecoverable)
	}
	return out
}
