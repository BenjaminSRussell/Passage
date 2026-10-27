package domain

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Depreciation run codes (FR-37.5, BR-37.3, BR-37.9; PRD 7.37.18).
const (
	CodeAlreadyPosted    = "AST-0502"
	CodePreviousUnposted = "AST-0503"
	CodeRunLocked        = "AST-0504"
	CodeBackdated        = "AST-0508"
)

// RunOutcome is the decision a depreciation run takes for one book.
type RunOutcome string

const (
	// RunNone means nothing is scheduled for the run period on this book.
	RunNone RunOutcome = "None"
	// RunDue means the run period is the book's next unposted line.
	RunDue RunOutcome = "Due"
	// RunException keeps the book out of the run; other books still post.
	RunException RunOutcome = "Exception"
)

// RunDecision is the outcome for one book with the scheduled line when due.
type RunDecision struct {
	Outcome RunOutcome
	Line    Line
	Code    string
	Detail  string
}

// DecideRun applies the sequential rule (BR-37.3, no catch-up) to one book's
// schedule. lastPosted is the book's last depreciated period ("" when none) and
// period the run period. A monthly run ("2026-03") only touches monthly
// schedules and a yearly run ("2026") only yearly ones; within one granularity
// period keys sort as text.
func DecideRun(lines []Line, lastPosted, period string) RunDecision {
	if len(lines) == 0 || len(lines[0].Period) != len(period) {
		return RunDecision{Outcome: RunNone}
	}
	if lastPosted != "" && period <= lastPosted {
		return RunDecision{Outcome: RunException, Code: CodeAlreadyPosted, Detail: "period " + period + " is already depreciated"}
	}
	var next *Line
	for i := range lines {
		if lastPosted == "" || lines[i].Period > lastPosted {
			next = &lines[i]
			break
		}
	}
	switch {
	case next == nil:
		return RunDecision{Outcome: RunNone}
	case next.Period == period:
		return RunDecision{Outcome: RunDue, Line: *next}
	case next.Period < period:
		return RunDecision{Outcome: RunException, Code: CodePreviousUnposted, Detail: "previous period " + next.Period + " is not depreciated"}
	default:
		return RunDecision{Outcome: RunNone}
	}
}

// RunAmount is the charge for a due line given the book's current balances.
// Without adjustments it equals the scheduled amount. After an acquisition
// adjustment, revaluation, impairment or partial disposal the remaining
// depreciable basis (cost − salvage − accumulated, FR-37.3) is spread over the
// remaining periods in the proportions of the original schedule, so changes
// apply prospectively (BR-37.4) and the final period clears the basis exactly
// (BR-37.1). The result is rounded half-up to the currency minor unit and is
// never negative.
func RunAmount(lines []Line, due Line, cost, salvage, accumulated decimal.Decimal, currency string) decimal.Decimal {
	basis := cost.Sub(salvage).Sub(accumulated)
	if !basis.IsPositive() {
		return decimal.Zero
	}
	remaining, count := decimal.Zero, 0
	for _, l := range lines {
		if l.Index >= due.Index {
			remaining = remaining.Add(l.Amount)
			count++
		}
	}
	var amount decimal.Decimal
	switch {
	case count <= 1:
		amount = basis
	case remaining.IsPositive():
		amount = basis.Mul(due.Amount).Div(remaining)
	default:
		amount = basis.Div(decimal.NewFromInt(int64(count)))
	}
	amount = amount.Round(money.MinorUnits(currency))
	if amount.GreaterThan(basis) {
		return basis
	}
	return amount
}

// ScheduledThrough is the scheduled accumulated depreciation at the end of
// period ("" means nothing is depreciated yet).
func ScheduledThrough(lines []Line, period string) decimal.Decimal {
	acc := decimal.Zero
	if period == "" {
		return acc
	}
	for _, l := range lines {
		if len(l.Period) == len(period) && l.Period <= period {
			acc = l.Accumulated
		}
	}
	return acc
}

// PeriodStart returns the first day of a period key ("2026-03" or "2026") and
// rejects anything else. Runs use it as the transaction date.
func PeriodStart(period string) (time.Time, error) {
	layout := "2006"
	switch len(period) {
	case 7:
		layout = "2006-01"
	case 4:
	default:
		return time.Time{}, fail(CodeInvalidDepreciation, "period must be YYYY or YYYY-MM")
	}
	t, err := time.Parse(layout, period)
	if err != nil {
		return time.Time{}, fail(CodeInvalidDepreciation, "period must be YYYY or YYYY-MM")
	}
	return t.UTC(), nil
}

// PeriodEnd returns the last day of a period key.
func PeriodEnd(period string) (time.Time, error) {
	start, err := PeriodStart(period)
	if err != nil {
		return time.Time{}, err
	}
	if len(period) == 4 {
		return start.AddDate(1, 0, -1), nil
	}
	return start.AddDate(0, 1, -1), nil
}

// CheckDisposalDate rejects a disposal dated before the end of the book's last
// depreciated period: the later depreciation would have to be reversed first
// (BR-37.6, AST-0508). The reversal set itself is not offered yet.
func CheckDisposalDate(date time.Time, lastDepreciated string) error {
	if lastDepreciated == "" {
		return nil
	}
	end, err := PeriodEnd(lastDepreciated)
	if err != nil {
		return err
	}
	if date.Before(end) {
		return fail(CodeBackdated, "disposal date precedes the end of the last depreciated period "+lastDepreciated+"; reverse the later depreciation first")
	}
	return nil
}
