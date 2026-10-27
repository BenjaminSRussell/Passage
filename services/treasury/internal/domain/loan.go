package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// DayCount is the accrual convention of a loan or deposit (FR-36.19, BR-36.15).
type DayCount string

// Supported day-count conventions.
const (
	DayCountACT360 DayCount = "ACT/360"
	DayCountACT365 DayCount = "ACT/365"
	DayCount30360  DayCount = "30/360"
)

// ScheduleType is the repayment profile of a loan.
type ScheduleType string

// Supported repayment profiles.
const (
	ScheduleAnnuity ScheduleType = "Annuity"
	ScheduleLinear  ScheduleType = "Linear"
	ScheduleBullet  ScheduleType = "Bullet"
)

// CodeLoanTerms is raised when loan terms cannot produce a schedule (see README).
const CodeLoanTerms = "TRS-0446"

// ValidDayCount reports whether dc is a supported convention.
func ValidDayCount(dc DayCount) bool {
	return dc == DayCountACT360 || dc == DayCountACT365 || dc == DayCount30360
}

// ValidScheduleType reports whether st is a supported repayment profile.
func ValidScheduleType(st ScheduleType) bool {
	return st == ScheduleAnnuity || st == ScheduleLinear || st == ScheduleBullet
}

// ValidPeriodMonths accepts monthly, quarterly, semi-annual and annual periods.
func ValidPeriodMonths(m int) bool {
	return m == 1 || m == 3 || m == 6 || m == 12
}

func (dc DayCount) basis() int64 {
	if dc == DayCountACT365 {
		return 365
	}
	return 360
}

// DaysBetween counts accrual days from start (inclusive) to end (exclusive).
// ACT conventions count calendar days; 30/360 uses the European 30E/360 rule.
// Both dates must be UTC midnight, as produced by time.Parse("2006-01-02").
func DaysBetween(start, end time.Time, dc DayCount) (int, error) {
	if !ValidDayCount(dc) {
		return 0, &Error{Code: CodeLoanTerms, Detail: "unknown day-count convention"}
	}
	if !end.After(start) {
		return 0, &Error{Code: CodeLoanTerms, Detail: "period end must be after period start"}
	}
	if dc == DayCount30360 {
		y1, m1, d1 := start.Date()
		y2, m2, d2 := end.Date()
		d1 = min(d1, 30)
		d2 = min(d2, 30)
		return (y2-y1)*360 + (int(m2)-int(m1))*30 + (d2 - d1), nil
	}
	return int(end.Sub(start) / (24 * time.Hour)), nil
}

// Interest returns principal × rate × days / basis, rounded half-up to the
// currency minor unit. rate is annual, e.g. 0.03 for 3.00%.
func Interest(principal, rate decimal.Decimal, days int, dc DayCount, currency string) decimal.Decimal {
	raw := principal.Mul(rate).Mul(decimal.NewFromInt(int64(days))).Div(decimal.NewFromInt(dc.basis()))
	return raw.Round(money.MinorUnits(currency))
}

// LoanTerms is the input to BuildSchedule.
type LoanTerms struct {
	Principal    decimal.Decimal
	Currency     string
	Start        time.Time
	Maturity     time.Time
	ScheduleType ScheduleType
	Rate         decimal.Decimal // annual fixed rate
	DayCount     DayCount
	PeriodMonths int
}

// ScheduleLine is one accrual period of a loan schedule.
type ScheduleLine struct {
	PeriodNo           int             `json:"periodNo"`
	PeriodStart        string          `json:"periodStart"`
	PeriodEnd          string          `json:"periodEnd"`
	Days               int             `json:"days"`
	OpeningPrincipal   decimal.Decimal `json:"openingPrincipal"`
	Interest           decimal.Decimal `json:"interest"`
	PrincipalRepayment decimal.Decimal `json:"principalRepayment"`
	ClosingPrincipal   decimal.Decimal `json:"closingPrincipal"`
}

// BuildSchedule produces the repayment and interest schedule (FR-36.19).
//
// Interest per period uses the day-count convention and rounds half-up to the
// currency minor unit. Annuity instalments are sized on the nominal period
// rate (rate × months / 12) and rounded to the minor unit; the final period
// repays whatever principal remains, so Σ principal repayment equals the
// principal exactly (BR-36.15). Linear repays principal in equal rounded
// instalments with the same final-period absorption; Bullet repays at maturity.
func BuildSchedule(t LoanTerms) ([]ScheduleLine, error) {
	if !t.Principal.IsPositive() {
		return nil, terms("principal must be positive")
	}
	if t.Rate.IsNegative() {
		return nil, terms("rate must not be negative")
	}
	if !ValidScheduleType(t.ScheduleType) || !ValidDayCount(t.DayCount) || !ValidPeriodMonths(t.PeriodMonths) {
		return nil, terms("unknown schedule type, day count or period length")
	}
	if !t.Maturity.After(t.Start) {
		return nil, terms("maturity must be after the start date")
	}
	months := (t.Maturity.Year()-t.Start.Year())*12 + int(t.Maturity.Month()-t.Start.Month())
	if months <= 0 || months%t.PeriodMonths != 0 || !AddMonths(t.Start, months).Equal(t.Maturity) {
		return nil, terms("maturity must fall on a whole number of periods after the start date")
	}
	n := months / t.PeriodMonths
	minor := money.MinorUnits(t.Currency)
	periodRate := t.Rate.Mul(decimal.NewFromInt(int64(t.PeriodMonths))).Div(decimal.NewFromInt(12))
	instalment := annuityInstalment(t.Principal, periodRate, n).Round(minor)
	linear := t.Principal.Div(decimal.NewFromInt(int64(n))).Round(minor)

	lines := make([]ScheduleLine, 0, n)
	opening := t.Principal
	periodStart := t.Start
	for k := 1; k <= n; k++ {
		periodEnd := AddMonths(t.Start, k*t.PeriodMonths)
		days, err := DaysBetween(periodStart, periodEnd, t.DayCount)
		if err != nil {
			return nil, err
		}
		interest := Interest(opening, t.Rate, days, t.DayCount, t.Currency)
		var principal decimal.Decimal
		switch {
		case k == n:
			principal = opening
		case t.ScheduleType == ScheduleAnnuity:
			principal = instalment.Sub(interest)
		case t.ScheduleType == ScheduleLinear:
			principal = linear
		default:
			principal = decimal.Zero
		}
		if principal.IsNegative() {
			return nil, terms("instalment does not cover the interest; the schedule would not amortize")
		}
		// Rounded instalments can exceed a small remaining balance; never repay more than is owed.
		if principal.GreaterThan(opening) {
			principal = opening
		}
		closing := opening.Sub(principal)
		lines = append(lines, ScheduleLine{
			PeriodNo:           k,
			PeriodStart:        periodStart.Format("2006-01-02"),
			PeriodEnd:          periodEnd.Format("2006-01-02"),
			Days:               days,
			OpeningPrincipal:   opening,
			Interest:           interest,
			PrincipalRepayment: principal,
			ClosingPrincipal:   closing,
		})
		opening = closing
		periodStart = periodEnd
	}
	return lines, nil
}

// AddMonths moves d by n calendar months and clamps the day to the end of the
// target month, so a loan starting on 31 January has periods ending 28/29
// February, 31 March, 30 April and so on (time.AddDate would roll into March).
func AddMonths(d time.Time, n int) time.Time {
	y, m, day := d.Date()
	first := time.Date(y, m+time.Month(n), 1, 0, 0, 0, 0, d.Location())
	last := first.AddDate(0, 1, -1).Day()
	return first.AddDate(0, 0, min(day, last)-1)
}

// annuityInstalment is P × r / (1 − (1 + r)^−n); zero rate spreads principal evenly.
func annuityInstalment(principal, r decimal.Decimal, n int) decimal.Decimal {
	if r.IsZero() {
		return principal.Div(decimal.NewFromInt(int64(n)))
	}
	growth := decimal.NewFromInt(1)
	onePlusR := decimal.NewFromInt(1).Add(r)
	for i := 0; i < n; i++ {
		growth = growth.Mul(onePlusR)
	}
	discount := decimal.NewFromInt(1).Div(growth)
	return principal.Mul(r).Div(decimal.NewFromInt(1).Sub(discount))
}

func terms(detail string) error {
	return &Error{Code: CodeLoanTerms, Detail: fmt.Sprintf("loan terms: %s", detail)}
}
