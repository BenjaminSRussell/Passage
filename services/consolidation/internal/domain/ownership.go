package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// OwnershipInterval is a holding effective from From (inclusive) to To (exclusive).
// A zero To means open-ended.
type OwnershipInterval struct {
	From    time.Time
	To      time.Time
	Percent decimal.Decimal
}

// ValidateOwnership checks that every interval is a valid percentage and that
// the percentages in force never exceed 100.00 % on any date (CNS-0904). The
// total only rises at an interval start, so checking those dates is sufficient.
func ValidateOwnership(intervals []OwnershipInterval) error {
	for _, iv := range intervals {
		if iv.Percent.IsNegative() || iv.Percent.GreaterThan(decimal.NewFromInt(100)) {
			return invalid("ownership percent must be between 0 and 100")
		}
		if !iv.To.IsZero() && !iv.To.After(iv.From) {
			return invalid("ownership interval must end after it starts")
		}
	}
	for _, start := range intervals {
		total := decimal.Zero
		for _, iv := range intervals {
			if inForce(iv, start.From) {
				total = total.Add(iv.Percent)
			}
		}
		if total.GreaterThan(decimal.NewFromInt(100)) {
			return &Error{Code: CodeOwnership, Detail: fmt.Sprintf("ownership totals %s%% on %s", total.StringFixed(2), start.From.Format("2006-01-02"))}
		}
	}
	return nil
}

func inForce(iv OwnershipInterval, on time.Time) bool {
	return !on.Before(iv.From) && (iv.To.IsZero() || on.Before(iv.To))
}
