package domain

import (
	"strings"

	"github.com/shopspring/decimal"
)

// Variance is the FR-24.12 / BR-24.9 flow-period variance:
// material + conversion − output, rounded to 4 places.
func Variance(material, conversion, output decimal.Decimal) decimal.Decimal {
	return material.Add(conversion).Sub(output).Round(4)
}

// CheckSettleable implements BR-24.9: a flow period settles only when every card
// of the period is Completed or Emptied. The caller passes the cards that have
// jobs in the period. Otherwise the result is LEAN-0003 listing the open cards.
func CheckSettleable(kanbans []Kanban) error {
	var open []string
	for _, k := range kanbans {
		if k.State != Completed && k.State != Emptied {
			open = append(open, k.ID)
		}
	}
	if len(open) > 0 {
		return errorf(CodeOpenKanbans, "period cannot be settled: %d kanbans open (%s)", len(open), strings.Join(open, ", "))
	}
	return nil
}
