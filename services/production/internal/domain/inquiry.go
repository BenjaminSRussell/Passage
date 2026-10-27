package domain

import "github.com/shopspring/decimal"

// PctScale is the precision of the "% of planned" column of the variance report.
const PctScale = 2

// VariancePct is a variance as a percentage of the planned cost of its cost group
// (PRD 4.21.14 variance report), rounded half away from zero to PctScale places.
// It is nil when nothing was planned for the group, where a percentage has no meaning.
func VariancePct(amount, planned decimal.Decimal) *decimal.Decimal {
	if planned.IsZero() {
		return nil
	}
	pct := amount.Mul(hundred).DivRound(planned, PctScale+4).Round(PctScale)
	return &pct
}
