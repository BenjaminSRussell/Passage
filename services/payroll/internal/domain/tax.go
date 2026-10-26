package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Rounding: every tax is computed on the unrounded basis and rounded to 0.01
// once per line (BR-43.2). Round is half away from zero, which is half-up for
// the non-negative amounts a payroll line carries.

// W4 is the worker's Form W-4 (8.43.4 PayStatement.w4). Amounts in Steps 3 and
// 4(a)/4(b) are annual; ExtraWithholding (Step 4(c)) is per pay period. A form
// from 2019 or earlier is Legacy and uses Allowances instead of Steps 2 to 4(b).
type W4 struct {
	FilingStatus     string
	Legacy           bool
	Allowances       int
	MultipleJobs     bool            // Step 2 checkbox
	DependentsCredit decimal.Decimal // Step 3
	OtherIncome      decimal.Decimal // Step 4(a)
	Deductions       decimal.Decimal // Step 4(b)
	ExtraWithholding decimal.Decimal // Step 4(c)
}

// BracketRef records which schedule row produced a withholding amount, so a
// statement line can be explained without re-running the engine (8.43.1).
type BracketRef struct {
	Schedule     string          `json:"schedule"`
	FilingStatus string          `json:"filingStatus"`
	Over         decimal.Decimal `json:"over"`
	Rate         decimal.Decimal `json:"rate"`
	BaseTax      decimal.Decimal `json:"baseTax"`
	AdjustedWage decimal.Decimal `json:"adjustedAnnualWage"`
}

// tableStatus maps a W-4 filing status to the schedule column Pub 15-T uses.
func tableStatus(w W4) (string, bool) {
	switch w.FilingStatus {
	case FilingSingle, FilingMarriedFilingSeparately:
		return FilingSingle, true
	case FilingMarriedFilingJointly:
		return FilingMarriedFilingJointly, true
	case FilingHeadOfHousehold:
		return FilingHeadOfHousehold, !w.Legacy // pre-2020 forms had no head of household
	}
	return "", false
}

// FederalWithholding is Pub 15-T Worksheet 1A (percentage method tables for
// automated payroll systems). Step 1 annualizes the period wage, adds Step 4(a)
// and subtracts Step 4(b) plus the line 1g adjustment (2020+ W-4 without the
// Step 2 box), or the allowances of a pre-2020 W-4. Step 2 looks the adjusted
// annual wage up in the standard or Step 2 checkbox schedule and divides by the
// periods. Step 3 subtracts the per-period dependents credit (not below zero),
// and Step 4 adds the extra withholding. The result is rounded once. A filing
// status or schedule the pack does not cover is an error, never a guess.
func FederalWithholding(rs RuleSet, w W4, wage decimal.Decimal, periods int) (decimal.Decimal, *BracketRef, error) {
	if periods < 1 {
		return decimal.Zero, nil, &Error{Code: CodeNoRule, Detail: "periods per year must be positive"}
	}
	status, ok := tableStatus(w)
	if !ok {
		return decimal.Zero, nil, &Error{Code: CodeNoRule, Detail: fmt.Sprintf("filing status %q is not covered by %s", w.FilingStatus, rs.Code)}
	}
	if !wage.IsPositive() {
		return decimal.Zero, nil, nil
	}
	n := decimal.NewFromInt(int64(periods))
	annual := wage.Mul(n)
	sched := ScheduleStandard
	if w.Legacy {
		annual = annual.Sub(rs.AllowanceValue.Mul(decimal.NewFromInt(int64(w.Allowances))))
	} else {
		annual = annual.Add(w.OtherIncome).Sub(w.Deductions)
		if w.MultipleJobs {
			sched = ScheduleCheckbox
		} else {
			annual = annual.Sub(rs.Step2Adjustment[status])
		}
	}
	annual = decimal.Max(decimal.Zero, annual)

	brackets := rs.Schedules[sched][status]
	if len(brackets) == 0 {
		return decimal.Zero, nil, &Error{Code: CodeNoRule, Detail: fmt.Sprintf("no %s %s schedule in %s", sched, status, rs.Code)}
	}
	b := brackets[0]
	for _, candidate := range brackets {
		if annual.GreaterThanOrEqual(candidate.Over) {
			b = candidate
		}
	}
	tentative := b.BaseTax.Add(annual.Sub(b.Over).Mul(b.Rate)).Div(n)
	if !w.Legacy {
		tentative = decimal.Max(decimal.Zero, tentative.Sub(w.DependentsCredit.Div(n)))
	}
	ref := &BracketRef{Schedule: sched, FilingStatus: status, Over: b.Over, Rate: b.Rate, BaseTax: b.BaseTax, AdjustedWage: annual}
	return tentative.Add(w.ExtraWithholding).Round(2), ref, nil
}

// SocialSecurity returns the employee tax and the taxable base after the wage
// base cap (BR-43.3): tax stops when YTD FICA wages reach the base, and the
// crossing period is taxed only on the part below the base.
func SocialSecurity(rs RuleSet, gross, ytdWages decimal.Decimal) (tax, base decimal.Decimal) {
	room := decimal.Max(decimal.Zero, rs.SSWageBase.Sub(ytdWages))
	base = decimal.Max(decimal.Zero, decimal.Min(gross, room))
	return base.Mul(rs.SSRate).Round(2), base
}

// Medicare returns the employee Medicare tax: 1.45% on all FICA wages plus
// 0.9% on the part of cumulative wages above 200,000, whatever the filing
// status (BR-43.3). The two parts are summed unrounded and rounded once.
func Medicare(rs RuleSet, gross, ytdWages decimal.Decimal) decimal.Decimal {
	regular := gross.Mul(rs.MedicareRate)
	cumulative := ytdWages.Add(gross)
	over := decimal.Max(decimal.Zero, cumulative.Sub(decimal.Max(rs.AdditionalMedicareThreshold, ytdWages)))
	return regular.Add(over.Mul(rs.AdditionalMedicareRate)).Round(2)
}

// MedicareEmployer is the employer share: 1.45% on all FICA wages. The 0.9%
// additional Medicare tax is withheld from the employee only.
func MedicareEmployer(rs RuleSet, gross decimal.Decimal) decimal.Decimal {
	return gross.Mul(rs.MedicareRate).Round(2)
}

// FUTA returns the employer unemployment tax on wages below the FUTA base.
func FUTA(rs RuleSet, gross, ytdWages decimal.Decimal) decimal.Decimal {
	room := decimal.Max(decimal.Zero, rs.FUTAWageBase.Sub(ytdWages))
	return decimal.Max(decimal.Zero, decimal.Min(gross, room)).Mul(rs.FUTARate).Round(2)
}

// SupplementalFederal withholds a supplemental wage at the flat rate (BR-43.11):
// the portion of cumulative supplemental wages above the threshold is taxed at
// the high rate.
func SupplementalFederal(rs RuleSet, wage, ytdSupplemental decimal.Decimal) decimal.Decimal {
	if wage.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero
	}
	low := decimal.Min(wage, decimal.Max(decimal.Zero, rs.SupplementalThreshold.Sub(ytdSupplemental)))
	high := wage.Sub(low)
	return low.Mul(rs.SupplementalRate).Add(high.Mul(rs.SupplementalHighRate)).Round(2)
}
