package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// RetainedAmount is the retention held from one invoice line (BR-41.10):
// round_half_even(line amount × retention percent ÷ 100) to the minor unit.
// retentionPct is a percent value, so 10 means 10 %.
func RetainedAmount(lineAmount, retentionPct decimal.Decimal, currency string) (decimal.Decimal, error) {
	if len(currency) != 3 {
		return decimal.Zero, fmt.Errorf("%w: currency", ErrInvalidInput)
	}
	if lineAmount.IsNegative() || retentionPct.IsNegative() || retentionPct.GreaterThan(decimal.NewFromInt(100)) {
		return decimal.Zero, fmt.Errorf("%w: amount and percent out of range", ErrInvalidInput)
	}
	return lineAmount.Mul(retentionPct).Div(decimal.NewFromInt(100)).RoundBank(money.MinorUnits(currency)), nil
}

// ReleaseTrigger says when held retention may be released (BR-41.10).
type ReleaseTrigger string

const (
	TriggerMilestone       ReleaseTrigger = "MILESTONE"
	TriggerProjectFinished ReleaseTrigger = "PROJECT_FINISHED"
	TriggerDateOffset      ReleaseTrigger = "DATE_OFFSET"
)

// ReleaseState is what a trigger is evaluated against.
type ReleaseState struct {
	MilestoneComplete bool
	Stage             Stage
	InvoiceDate       time.Time
	OffsetDays        int
	AsOf              time.Time
}

// ReleaseDue reports whether the trigger has fired.
func ReleaseDue(trigger ReleaseTrigger, st ReleaseState) bool {
	switch trigger {
	case TriggerMilestone:
		return st.MilestoneComplete
	case TriggerProjectFinished:
		return st.Stage == StageFinished
	case TriggerDateOffset:
		return !st.AsOf.Before(st.InvoiceDate.AddDate(0, 0, st.OffsetDays))
	}
	return false
}

// CheckRelease returns PROJ-RET-001 for a release before its trigger unless the
// caller holds ProjBillingPost and gives a reason (override).
func CheckRelease(trigger ReleaseTrigger, st ReleaseState, override bool) error {
	if ReleaseDue(trigger, st) || override {
		return nil
	}
	return rule(CodeRetentionEarly, "Retention cannot be released before its trigger (%s)", trigger)
}

// DayCount is the interest convention (FR-41.16).
type DayCount string

const (
	ACT365 DayCount = "ACT_365"
	ACT360 DayCount = "ACT_360"
)

// InterestCharge is one month of project interest (BR-41.12):
// average balance × annual rate × days ÷ basis, rounded half-even to the minor unit.
func InterestCharge(avgBalance, annualRate decimal.Decimal, days int, dc DayCount, currency string) (decimal.Decimal, error) {
	var basis int64
	switch dc {
	case ACT365:
		basis = 365
	case ACT360:
		basis = 360
	default:
		return decimal.Zero, fmt.Errorf("%w: day count %q", ErrInvalidInput, dc)
	}
	if len(currency) != 3 || days < 0 || avgBalance.IsNegative() || annualRate.IsNegative() {
		return decimal.Zero, fmt.Errorf("%w: interest inputs", ErrInvalidInput)
	}
	charge := avgBalance.Mul(annualRate).Mul(decimal.NewFromInt(int64(days))).Div(decimal.NewFromInt(basis))
	return charge.RoundBank(money.MinorUnits(currency)), nil
}
