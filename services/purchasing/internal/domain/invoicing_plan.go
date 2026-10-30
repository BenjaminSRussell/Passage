package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// Invoicing plan kinds and frequencies (FR-17.21).
const (
	PlanPeriodic = "PERIODIC"
	PlanPartial  = "PARTIAL"

	FreqMonthly   = "MONTHLY"
	FreqQuarterly = "QUARTERLY"
)

// Plan row statuses. Wave 18 (FR-18.17) moves a row to INVOICED when its
// settlement run invoices it: receiving reports the settlement and this service
// flips the row (MarkPlanRowInvoiced); it only ever generates OPEN rows.
const (
	PlanRowOpen     = "OPEN"
	PlanRowInvoiced = "INVOICED"
)

// Invoicing plan problem codes (listed in the service README).
const (
	// CodeInvoicingPlan is PRC-IP-001 (BR-17.17): the plan does not add up to
	// the PO line amount, or a changed line amount cannot be shared over it.
	CodeInvoicingPlan = "PRC-IP-001"
	// CodePlanInvoiced: a plan with invoiced rows cannot be replaced or removed.
	CodePlanInvoiced = "PRC-IP-002"
	// CodePlanTarget: the PO or line cannot carry a plan (cancelled or finalized).
	CodePlanTarget = "PRC-IP-003"
	// CodePlanRowSettlement is PRC-IP-004 (409): the row is already settled under
	// another reference, or the settled amount differs from the row's.
	CodePlanRowSettlement = "PRC-IP-004"
)

// MaxPlanRows bounds the rows of one plan (ten years of monthly periods).
const MaxPlanRows = 120

// maxPercentScale is the decimal places a milestone percent may carry (numeric(9,6)).
const maxPercentScale = 6

// PlanRow is one scheduled invoice of a PO line plan. Percent is set on the
// rows of a PARTIAL plan given in percent; Amount is always set.
type PlanRow struct {
	Seq     int              `json:"seq"`
	DueDate Day              `json:"dueDate"`
	Amount  decimal.Decimal  `json:"amount"`
	Percent *decimal.Decimal `json:"percent,omitempty"`
	Status  string           `json:"status"`
}

// InvoicingPlan is the schedule attached to one PO line (FR-17.21). Wave 18
// FR-18.17 invoices OPEN rows on their due date; SettleWithoutReceipt tells it
// not to wait for a product receipt. LineAmount and Currency are the line's
// when the rows were last written, so Σ rows = LineAmount (BR-17.17).
type InvoicingPlan struct {
	PurchaseOrderID      string          `json:"purchaseOrderId"`
	LineNo               int             `json:"lineNo"`
	Kind                 string          `json:"kind"`
	Frequency            string          `json:"frequency,omitempty"`
	BillingDay           int             `json:"billingDay,omitempty"`
	SettleWithoutReceipt bool            `json:"settleWithoutReceipt"`
	LineAmount           decimal.Decimal `json:"lineAmount"`
	Currency             string          `json:"currency"`
	Rows                 []PlanRow       `json:"rows"`
	UpdatedAt            time.Time       `json:"updatedAt"`
}

// PeriodicSpec describes a PERIODIC plan: AmountPerPeriod is invoiced Periods
// times, the first in Start's month and then every 1 (MONTHLY) or 3
// (QUARTERLY) months, on BillingDay (Start's day when zero).
type PeriodicSpec struct {
	Start           Day
	Frequency       string
	Periods         int
	BillingDay      int
	AmountPerPeriod decimal.Decimal
}

// Milestone is one PARTIAL row: either Percent of the line amount or a fixed Amount.
type Milestone struct {
	Date    Day
	Percent *decimal.Decimal
	Amount  *decimal.Decimal
}

// CheckPlanTarget refuses a plan on a cancelled or finalized PO or on a
// cancelled line.
func CheckPlanTarget(poStatus, lineStatus string) error {
	if poStatus == POStatusCancelled || poStatus == POStatusFinalized {
		return fail(CodePlanTarget, "a purchase order in %s cannot take an invoicing plan", poStatus)
	}
	if lineStatus == POLineCancelled {
		return fail(CodePlanTarget, "a cancelled line cannot take an invoicing plan")
	}
	return nil
}

// GeneratePeriodic builds the rows of a PERIODIC plan and checks that periods ×
// amount per period equals the line amount (BR-17.17). A billing day past the
// end of a month falls on that month's last day.
func GeneratePeriodic(spec PeriodicSpec, lineAmount decimal.Decimal, minor int32) ([]PlanRow, error) {
	step, err := periodStep(spec.Frequency)
	if err != nil {
		return nil, err
	}
	if spec.Start.IsZero() {
		return nil, fail(CodeValidation, "start is required")
	}
	if spec.Periods < 1 || spec.Periods > MaxPlanRows {
		return nil, fail(CodeValidation, "periods must be from 1 to %d", MaxPlanRows)
	}
	if spec.BillingDay == 0 {
		spec.BillingDay = spec.Start.Day()
	}
	if spec.BillingDay < 1 || spec.BillingDay > 31 {
		return nil, fail(CodeValidation, "billing day must be from 1 to 31")
	}
	if err := checkAmount("amount per period", spec.AmountPerPeriod, minor); err != nil {
		return nil, err
	}
	total := spec.AmountPerPeriod.Mul(decimal.NewFromInt(int64(spec.Periods)))
	if !total.Equal(lineAmount) {
		return nil, fail(CodeInvoicingPlan, "invoicing plan total %s (%d × %s) ≠ line amount %s",
			total.StringFixed(minor), spec.Periods, spec.AmountPerPeriod.StringFixed(minor), lineAmount.StringFixed(minor))
	}
	first := spec.Start.Time
	rows := make([]PlanRow, 0, spec.Periods)
	for i := 0; i < spec.Periods; i++ {
		month := time.Date(first.Year(), first.Month()+time.Month(i*step), 1, 0, 0, 0, 0, time.UTC)
		last := month.AddDate(0, 1, -1).Day()
		due := time.Date(month.Year(), month.Month(), min(spec.BillingDay, last), 0, 0, 0, 0, time.UTC)
		rows = append(rows, PlanRow{Seq: i + 1, DueDate: Day{due}, Amount: spec.AmountPerPeriod, Status: PlanRowOpen})
	}
	return rows, nil
}

// GeneratePartial builds the rows of a PARTIAL plan. Milestones are all
// percentages (Σ = 100; amounts are shared to minor units so they sum to the
// line amount exactly) or all fixed amounts (Σ = line amount). Dates must not
// decrease, and every row must come to at least one minor unit.
func GeneratePartial(ms []Milestone, lineAmount decimal.Decimal, minor int32) ([]PlanRow, error) {
	if len(ms) == 0 || len(ms) > MaxPlanRows {
		return nil, fail(CodeValidation, "a partial plan needs from 1 to %d milestones", MaxPlanRows)
	}
	byPercent := ms[0].Percent != nil
	for i, m := range ms {
		if (m.Percent != nil) != byPercent || (m.Amount != nil) == (m.Percent != nil) {
			return nil, fail(CodeValidation, "milestone %d must carry either a percent or an amount, and all milestones the same one", i+1)
		}
		if m.Date.IsZero() {
			return nil, fail(CodeValidation, "milestone %d needs a date", i+1)
		}
		if i > 0 && m.Date.Before(ms[i-1].Date.Time) {
			return nil, fail(CodeValidation, "milestone dates must not decrease (milestone %d)", i+1)
		}
	}
	rows := make([]PlanRow, len(ms))
	if !byPercent {
		sum := decimal.Zero
		for i, m := range ms {
			if err := checkAmount("milestone amount", *m.Amount, minor); err != nil {
				return nil, err
			}
			sum = sum.Add(*m.Amount)
			rows[i] = PlanRow{Seq: i + 1, DueDate: m.Date, Amount: *m.Amount, Status: PlanRowOpen}
		}
		if !sum.Equal(lineAmount) {
			return nil, fail(CodeInvoicingPlan, "invoicing plan total %s ≠ line amount %s", sum.StringFixed(minor), lineAmount.StringFixed(minor))
		}
		return rows, nil
	}
	sum := decimal.Zero
	weights := make([]decimal.Decimal, len(ms))
	for i, m := range ms {
		if !m.Percent.IsPositive() || m.Percent.GreaterThan(hundred) || !m.Percent.Equal(m.Percent.Round(maxPercentScale)) {
			return nil, fail(CodeValidation, "milestone %d percent must be above 0 and at most 100, with up to %d decimals", i+1, maxPercentScale)
		}
		sum = sum.Add(*m.Percent)
		weights[i] = *m.Percent
	}
	if !sum.Equal(hundred) {
		return nil, fail(CodeInvoicingPlan, "milestone percents sum to %s, not 100", sum.String())
	}
	amounts := Allocate(lineAmount, weights, minor)
	for i, m := range ms {
		if !amounts[i].IsPositive() {
			return nil, fail(CodeInvoicingPlan, "milestone %d (%s %%) of %s rounds to nothing", i+1, m.Percent.String(), lineAmount.StringFixed(minor))
		}
		pct := *m.Percent
		rows[i] = PlanRow{Seq: i + 1, DueDate: m.Date, Amount: amounts[i], Percent: &pct, Status: PlanRowOpen}
	}
	return rows, nil
}

// Reproportion applies a changed line amount to a plan (BR-17.17): INVOICED
// rows keep their amount and the rest is shared over the OPEN rows in
// proportion to their current amounts, to minor units, so the rows still sum to
// the line amount exactly. OPEN rows whose share comes to zero are dropped
// (the line is then fully covered by the rows before them). It refuses a line
// amount below what is already invoiced, and an increase when no row is open.
// The input is not modified.
func Reproportion(rows []PlanRow, lineAmount decimal.Decimal, minor int32) ([]PlanRow, error) {
	invoiced := decimal.Zero
	var open []int
	weights := []decimal.Decimal{}
	for i, r := range rows {
		if r.Status == PlanRowInvoiced {
			invoiced = invoiced.Add(r.Amount)
			continue
		}
		open = append(open, i)
		weights = append(weights, r.Amount)
	}
	remaining := lineAmount.Sub(invoiced)
	if remaining.IsNegative() {
		return nil, fail(CodeInvoicingPlan, "line amount %s is below the %s already invoiced by its plan",
			lineAmount.StringFixed(minor), invoiced.StringFixed(minor))
	}
	if len(open) == 0 && !remaining.IsZero() {
		return nil, fail(CodeInvoicingPlan, "every plan row is invoiced; the line amount %s cannot change from %s",
			lineAmount.StringFixed(minor), invoiced.StringFixed(minor))
	}
	shares := Allocate(remaining, weights, minor)
	out := make([]PlanRow, 0, len(rows))
	k := 0
	for i, r := range rows {
		if k < len(open) && open[k] == i {
			r.Amount = shares[k]
			k++
			if r.Amount.IsZero() {
				continue
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// PlanTotal is the sum of the plan's row amounts.
func PlanTotal(rows []PlanRow) decimal.Decimal {
	sum := decimal.Zero
	for _, r := range rows {
		sum = sum.Add(r.Amount)
	}
	return sum
}

// Allocate splits total over weights to minor units with the largest-remainder
// method: each share is floored, then the minor units left over go one at a
// time to the largest remainders (earliest first on a tie). The shares are
// never negative for a non-negative total and always sum to total exactly.
// With all weights zero the shares are equal.
func Allocate(total decimal.Decimal, weights []decimal.Decimal, minor int32) []decimal.Decimal {
	n := len(weights)
	out := make([]decimal.Decimal, n)
	if n == 0 {
		return out
	}
	sumW := decimal.Zero
	for _, w := range weights {
		sumW = sumW.Add(w)
	}
	if !sumW.IsPositive() {
		weights = make([]decimal.Decimal, n)
		for i := range weights {
			weights[i] = decimal.NewFromInt(1)
		}
		sumW = decimal.NewFromInt(int64(n))
	}
	unit := decimal.New(1, -minor)
	given := decimal.Zero
	rem := make([]decimal.Decimal, n)
	for i, w := range weights {
		exact := total.Mul(w).DivRound(sumW, minor+12)
		out[i] = exact.RoundFloor(minor)
		rem[i] = exact.Sub(out[i])
		given = given.Add(out[i])
	}
	left := total.Sub(given)
	for left.GreaterThanOrEqual(unit) {
		best := 0
		for i := 1; i < n; i++ {
			if rem[i].GreaterThan(rem[best]) {
				best = i
			}
		}
		out[best] = out[best].Add(unit)
		rem[best] = rem[best].Sub(unit)
		left = left.Sub(unit)
	}
	if !left.IsZero() {
		// total carries more decimals than the currency: the residue stays on
		// the largest share so the sum is still exact.
		best := 0
		for i := 1; i < n; i++ {
			if out[i].GreaterThan(out[best]) {
				best = i
			}
		}
		out[best] = out[best].Add(left)
	}
	return out
}

// checkAmount requires a positive amount in the currency's minor units.
func checkAmount(what string, v decimal.Decimal, minor int32) error {
	if !v.IsPositive() {
		return fail(CodeValidation, "%s must be positive", what)
	}
	if !v.Equal(v.Round(minor)) {
		return fail(CodeValidation, "%s %s has more decimals than the currency's %d", what, v.String(), minor)
	}
	return nil
}

func periodStep(freq string) (int, error) {
	switch freq {
	case FreqMonthly:
		return 1, nil
	case FreqQuarterly:
		return 3, nil
	}
	return 0, fail(CodeValidation, "frequency must be %s or %s", FreqMonthly, FreqQuarterly)
}
