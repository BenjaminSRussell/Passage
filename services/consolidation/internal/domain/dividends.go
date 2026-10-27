package domain

import (
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// RuleE6 is the intercompany dividend elimination (FR-40.9, E6).
const RuleE6 = "E6"

// Accounts used by E6 and by the per-rule rounding line (BR-40.9). The parent's
// dividend income account is AcctDividendIncome.
const (
	AcctDividendsDeclared = "DIVIDENDS_DECLARED"
	AcctRounding          = "ROUNDING"
)

// Dividend is one dividend a subsidiary declared to its shareholders in the
// period. Amount is in the subsidiary's currency and Rate converts it to the
// group currency Currency on the dividend date.
type Dividend struct {
	Currency     string
	OwnershipPct decimal.Decimal
	Rate         decimal.Decimal
	Amount       decimal.Decimal
}

// EliminateDividends builds E6 (FR-40.9): the whole dividend is removed from
// dividends declared in the subsidiary's equity (credit), the parent's share
// from its dividend income (debit) and the non-controlling share from NCI
// equity (debit), because the NCI's part was paid out of the group. Each line
// is rounded to the group currency minor unit and the per-rule rounding line
// keeps E6 balanced (BR-40.9).
func EliminateDividends(d Dividend) ([]Entry, error) {
	switch {
	case len(d.Currency) != 3:
		return nil, invalid("group currency must be an ISO 4217 code")
	case !d.OwnershipPct.IsPositive() || d.OwnershipPct.GreaterThan(decimal.NewFromInt(100)):
		return nil, invalid("ownership must be above 0 and at most 100 percent")
	case !d.Rate.IsPositive():
		return nil, invalid("rate must be positive")
	case !d.Amount.IsPositive():
		return nil, invalid("dividend must be positive")
	}
	gross := d.Amount.Mul(d.Rate)
	parent := gross.Mul(d.OwnershipPct).Div(decimal.NewFromInt(100))

	var out []Entry
	out = leg(out, RuleE6, AcctDividendIncome, round(parent, d.Currency))
	out = leg(out, RuleE6, AcctNCIEquity, round(NCIShare(gross, d.OwnershipPct), d.Currency))
	out = leg(out, RuleE6, AcctDividendsDeclared, round(gross, d.Currency).Neg())
	out, err := BalanceRule(out, RuleE6, AcctRounding, d.Currency)
	if err != nil {
		return nil, err
	}
	return out, CheckBalanced(out)
}

// BalanceRule appends the per-rule rounding line of BR-40.9: one line on
// account that takes the net residual of rule after each line was rounded to
// the currency minor unit. A balanced rule is returned unchanged. Rounding can
// leave at most half a minor unit per line; a larger residual is an elimination
// defect, not rounding, and fails with CNS-0903.
func BalanceRule(entries []Entry, rule, account, currency string) ([]Entry, error) {
	net := decimal.Zero
	lines := 0
	for _, e := range entries {
		if e.RuleCode == rule {
			net = net.Add(e.Debit).Sub(e.Credit)
			lines++
		}
	}
	if net.IsZero() {
		return entries, nil
	}
	halfUnit := decimal.New(5, -money.MinorUnits(currency)-1)
	if net.Abs().GreaterThan(halfUnit.Mul(decimal.NewFromInt(int64(lines)))) {
		return nil, &Error{Code: CodeUnbalancedRule, Detail: fmt.Sprintf("rule %s unbalanced by %s", rule, net.Abs().String())}
	}
	return leg(entries, rule, account, net.Neg()), nil
}
