package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// AcctICDifference is the consolidation account that takes a two-sided IC
// difference within the threshold (BR-40.15).
const AcctICDifference = "IC_DIFFERENCE"

// DefaultICThreshold is consol.icThreshold: 1,000.00 in the group currency.
var DefaultICThreshold = decimal.NewFromInt(1000)

// EliminateICPair eliminates one partner pair's intercompany balances two-sided
// (BR-40.15). The inputs are the summed receivable side and payable side after
// translation, in the group currency. The difference is receivable − payable.
// A difference within the threshold posts to IC_DIFFERENCE, debit when positive
// and credit when negative, and the entries balance. A larger difference fails
// with CNS-0905 and the task ends in Error.
func EliminateICPair(receivable, payable, threshold decimal.Decimal, currency string) ([]Entry, decimal.Decimal, error) {
	if len(currency) != 3 {
		return nil, decimal.Zero, invalid("currency must be an ISO 4217 code")
	}
	if threshold.IsNegative() {
		return nil, decimal.Zero, invalid("IC threshold must not be negative")
	}
	r := round(receivable, currency)
	p := round(payable, currency)
	diff := r.Sub(p)
	if diff.Abs().GreaterThan(threshold) {
		return nil, diff, &Error{Code: CodeICThreshold, Detail: fmt.Sprintf("IC difference %s exceeds threshold %s", diff.Abs().StringFixed(2), threshold.StringFixed(2))}
	}
	var out []Entry
	out = leg(out, RuleE2, AcctICPayable, p)
	out = leg(out, RuleE2, AcctICReceivable, r.Neg())
	out = leg(out, RuleE2, AcctICDifference, diff)
	if err := CheckBalanced(out); err != nil {
		return nil, diff, err
	}
	return out, diff, nil
}
