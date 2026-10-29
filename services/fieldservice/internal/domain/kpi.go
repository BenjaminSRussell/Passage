package domain

import "github.com/shopspring/decimal"

// CanFollowUp reports whether a work order may get a follow-up order (BR-45.5).
// Only a finished visit can need another one: COMPLETED, or POSTED when billing
// ran before the customer reported the problem was not fixed.
func CanFollowUp(s WOStatus) bool { return s == WOCompleted || s == WOPosted }

// Percent returns 100*part/whole rounded half-even to two places, or nil when
// whole is not positive (an empty denominator has no rate).
func Percent(part, whole int64) *decimal.Decimal {
	if whole <= 0 {
		return nil
	}
	p := decimal.NewFromInt(part).Mul(decimal.NewFromInt(100)).Div(decimal.NewFromInt(whole)).RoundBank(2)
	return &p
}
