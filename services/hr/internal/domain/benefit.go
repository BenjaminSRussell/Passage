package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// Benefit vocabularies (FR-42.8, 8.42.4).
var (
	BenefitPlanTypes     = []string{"MEDICAL", "DENTAL", "VISION", "LIFE", "RETIREMENT_401K", "HSA", "FSA", "OTHER"}
	BenefitRateBases     = []string{"PER_PAY_PERIOD", "MONTHLY"}
	LifeEventKinds       = []string{"MARRIAGE", "BIRTH", "ADOPTION", "DIVORCE", "LOSS_OF_COVERAGE", "OTHER"}
	EnrollmentEndReasons = []string{EndTermination, EndLifeEvent, EndPlanEnd}
)

// Enrollment end reasons (8.42.4 BenefitEnrollment.end_reason).
const (
	EndTermination = "TERMINATION"
	EndLifeEvent   = "LIFE_EVENT"
	EndPlanEnd     = "PLAN_END"
)

// DefaultLifeEventWindowDays is the life-event enrollment window (8.42.5,
// 8.42.13 default 30). The per-legal-entity parameter is a later slice.
const DefaultLifeEventWindowDays = 30

// MaxRuleLength bounds an eligibility expression; the parser recurses per
// nesting level, so the length also bounds its depth.
const MaxRuleLength = 2000

// Problem codes raised by the benefit rules. HCM-BEN-001 and 002 are the
// 8.42.18 catalog; 005 is this service's (listed in the README).
const (
	CodeBenefitWindow     = "HCM-BEN-001"
	CodeBenefitIneligible = "HCM-BEN-002"
	CodeBenefitDateOrder  = "HCM-BEN-005"
)

// CheckEnrollmentWindow answers whether an enrollment effective on eff may be
// recorded (8.42.5): either the day falls inside the plan's open enrollment
// window (both ends inclusive) or, with a life event, it falls on the event
// day or within DefaultLifeEventWindowDays after it. Otherwise HCM-BEN-001.
func CheckEnrollmentWindow(eff, start, end time.Time, lifeEvent *time.Time) error {
	eff = AsOfDay(eff)
	if !eff.Before(AsOfDay(start)) && !eff.After(AsOfDay(end)) {
		return nil
	}
	if lifeEvent != nil {
		le := AsOfDay(*lifeEvent)
		if !eff.Before(le) && !eff.After(le.AddDate(0, 0, DefaultLifeEventWindowDays)) {
			return nil
		}
	}
	return &Error{Code: CodeBenefitWindow, Detail: "Enrollment outside the open window or life-event period"}
}

// ServiceDays is the whole number of days from hire to on; negative before hire.
func ServiceDays(hire, on time.Time) int64 {
	return int64(AsOfDay(on).Sub(AsOfDay(hire)) / (24 * time.Hour))
}

// EnrollmentValidTo is the exclusive end of an enrollment whose last covered
// day is lastDay. A TERMINATION keeps coverage to the end of the termination
// month (BR-42.6), so lastDay is the termination date and the range ends on
// the first day of the next month; any other reason ends the day after lastDay.
func EnrollmentValidTo(lastDay time.Time, reason string) time.Time {
	d := AsOfDay(lastDay)
	if reason == EndTermination {
		return time.Date(d.Year(), d.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	}
	return d.AddDate(0, 0, 1)
}

// EligibilityFacts are the values a benefit rule can read as of the enrollment
// effective date (BR-42.6). FTE is nil when the worker has no primary position
// assignment on that date; age and job type are nil until their sources exist
// (the date of birth is not collected yet and the Job entity is a later slice).
// A rule that reads a nil fact fails with ErrUnknownIdentifier rather than
// guessing a value.
type EligibilityFacts struct {
	EmploymentType string
	FTE            *decimal.Decimal
	ServiceDays    int64
	LegalEntityID  string
	JobType        *string
	Age            *int64
}

// Env renders the facts for Rule.Eval.
func (f EligibilityFacts) Env() Env {
	env := Env{
		"employment_type": f.EmploymentType,
		"service_days":    decimal.NewFromInt(f.ServiceDays),
		"legal_entity":    f.LegalEntityID,
	}
	if f.FTE != nil {
		env["fte"] = *f.FTE
	}
	if f.JobType != nil {
		env["job.type"] = *f.JobType
	}
	if f.Age != nil {
		env["age"] = decimal.NewFromInt(*f.Age)
	}
	return env
}

// Eligibility is the answer of BR-42.6 for one employment: Eligible, and a
// Reason a person can read (the waiting period, the rule result or why the
// rule could not be evaluated).
type Eligibility struct {
	Eligible bool
	Reason   string
}

// Evaluate applies BR-42.6 to one employment: the waiting period counted from
// the hire date must have passed (service_days >= waitingDays), then the rule
// must be true. A rule that cannot be evaluated (unknown fact, type mismatch)
// is not eligible and says why. Enrollment and the simulation both call this,
// so the simulation predicts exactly what enrollment will decide.
func Evaluate(rule Rule, waitingDays int, facts EligibilityFacts) Eligibility {
	if facts.ServiceDays < int64(waitingDays) {
		return Eligibility{Reason: fmt.Sprintf("waiting period of %d days from hire not complete (%d days of service)", waitingDays, facts.ServiceDays)}
	}
	ok, err := rule.Eval(facts.Env())
	switch {
	case err != nil:
		return Eligibility{Reason: "rule could not be evaluated: " + err.Error()}
	case ok:
		return Eligibility{Eligible: true, Reason: "rule true"}
	default:
		return Eligibility{Reason: "rule false"}
	}
}
