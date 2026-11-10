package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

const (
	PlanDraft     = "Draft"
	PlanSubmitted = "Submitted"
	PlanReviewed  = "Reviewed"
	PlanApproved  = "Approved"

	StageReview  = "Review"
	StageApprove = "Approve"

	MaxScenarios = 10
	amountScale  = 4
)

var scenarioKinds = map[string]bool{"Baseline": true, "Optimistic": true, "Pessimistic": true, "Custom": true}

func ValidScenarioKind(kind string) bool { return scenarioKinds[kind] }

func ValidStage(stage string) bool { return stage == StageReview || stage == StageApprove }

// StageOpenStatus is the process status in which a stage collects its sign-offs.
func StageOpenStatus(stage string) string {
	if stage == StageReview {
		return PlanSubmitted
	}
	return PlanReviewed
}

// StageForStatus is the stage that is open in a process status, or "" when none is.
func StageForStatus(status string) string {
	switch status {
	case PlanSubmitted:
		return StageReview
	case PlanReviewed:
		return StageApprove
	}
	return ""
}

// Periods lists the 12 periods of a fiscal year as YYYY-MM.
func Periods(year int) []string {
	out := make([]string, 12)
	for m := range out {
		out[m] = fmt.Sprintf("%04d-%02d", year, m+1)
	}
	return out
}

// PeriodInYear reports whether p is a YYYY-MM period of the fiscal year.
func PeriodInYear(p string, year int) bool {
	t, err := time.Parse("2006-01", p)
	return err == nil && len(p) == 7 && t.Year() == year
}

// DimensionKey is the register combination key of a plan cell's account and cost center.
func DimensionKey(account, costCenter string) string { return account + "|" + costCenter }

// SpreadWeighted splits a non-negative total in proportion to weights, to four decimal places. The rounding
// remainder goes to the heaviest weight (the first on a tie), so the shares always add up to the total. Weights
// that are all zero spread evenly.
func SpreadWeighted(total decimal.Decimal, weights []decimal.Decimal) []decimal.Decimal {
	if len(weights) == 0 {
		return nil
	}
	w := make([]decimal.Decimal, len(weights))
	sum := decimal.Zero
	for i, v := range weights {
		if v.Sign() > 0 {
			w[i] = v
			sum = sum.Add(v)
		}
	}
	if sum.IsZero() {
		for i := range w {
			w[i] = decimal.NewFromInt(1)
		}
		sum = decimal.NewFromInt(int64(len(w)))
	}
	shares := make([]decimal.Decimal, len(w))
	allocated, heaviest := decimal.Zero, 0
	for i, v := range w {
		shares[i] = total.Mul(v).Div(sum).Truncate(amountScale)
		allocated = allocated.Add(shares[i])
		if v.GreaterThan(w[heaviest]) {
			heaviest = i
		}
	}
	shares[heaviest] = shares[heaviest].Add(total.Sub(allocated))
	return shares
}

// SpreadEven splits a total evenly over n parts.
func SpreadEven(total decimal.Decimal, n int) []decimal.Decimal {
	ones := make([]decimal.Decimal, n)
	for i := range ones {
		ones[i] = decimal.NewFromInt(1)
	}
	return SpreadWeighted(total, ones)
}

// PlanCell is one amount of the plan grid.
type PlanCell struct {
	Account    string          `json:"account"`
	CostCenter string          `json:"costCenter"`
	Period     string          `json:"period"`
	Amount     decimal.Decimal `json:"amount"`
}

// RegisterLines turns plan cells into register lines, one per combination and period; zero cells carry no budget.
func RegisterLines(cells []PlanCell) []RegisterLine {
	lines := make([]RegisterLine, 0, len(cells))
	for _, c := range cells {
		if c.Amount.Sign() > 0 {
			lines = append(lines, RegisterLine{DimensionKey: DimensionKey(c.Account, c.CostCenter), SpanKey: c.Period, Amount: c.Amount})
		}
	}
	return lines
}
