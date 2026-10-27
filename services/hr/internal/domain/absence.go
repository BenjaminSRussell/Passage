package domain

import (
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// Absence request states (8.42.7 state machine). DRAFT is not stored: a request
// is validated and submitted in one call. Only a SUBMITTED request can be
// CANCELED in this slice; cancelling an APPROVED one is a later slice.
const (
	RequestSubmitted = "SUBMITTED"
	RequestApproved  = "APPROVED"
	RequestRejected  = "REJECTED"
	RequestCanceled  = "CANCELED"
)

// Decisions a manager may apply to a SUBMITTED request.
const (
	DecisionApprove = "APPROVE"
	DecisionReject  = "REJECT"
)

// NegativeFloorMultiplier is BR-42.4: a plan that allows negatives may fall to
// −(accrual rate × 2) and no further.
const NegativeFloorMultiplier = 2

// Rule codes raised by this file. HCM-ABS-001..003 are the 8.42.18 catalog
// (HCM-ABS-002 is raised by Ledger.Consume); the named codes are this
// service's own and are listed in the README.
const (
	CodeOverlapApproved = "HCM-ABS-001"
	CodeOverlapPending  = "HCM-ABS-003"
	CodeRequestState    = "HCM-ABS-STATE"
	CodeSelfDecision    = "HCM-ABS-SELF-APPROVAL"
	CodePeriod          = "HCM-ABS-PERIOD"
	CodeYearSpan        = "HCM-ABS-YEAR-SPAN"
)

// RuleWarning is a non-blocking rule result returned with a successful write.
type RuleWarning struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// RequestSpan is an existing absence request as an inclusive day range.
type RequestSpan struct {
	ID, Status string
	From, To   time.Time
}

// CheckOverlap is BR-42.5. A request overlapping an APPROVED one is refused
// (HCM-ABS-001); overlapping SUBMITTED ones only warn (HCM-ABS-003). Ranges are
// inclusive on both ends because absence days are whole days. selfID is
// skipped, so an approval can recheck its own request against the others.
func CheckOverlap(selfID string, from, to time.Time, existing []RequestSpan) ([]RuleWarning, error) {
	var warnings []RuleWarning
	for _, s := range existing {
		if s.ID == selfID || s.From.After(to) || from.After(s.To) {
			continue
		}
		switch s.Status {
		case RequestApproved:
			return nil, &Error{Code: CodeOverlapApproved, Detail: fmt.Sprintf("Request overlaps an approved absence %s", s.ID)}
		case RequestSubmitted:
			warnings = append(warnings, RuleWarning{Code: CodeOverlapPending, Detail: fmt.Sprintf("Request overlaps submitted request %s", s.ID)})
		}
	}
	return warnings, nil
}

// BalanceFloor is the lowest total a request may take a balance to (BR-42.4).
func BalanceFloor(accrualRate decimal.Decimal, negativeAllowed bool) decimal.Decimal {
	if !negativeAllowed {
		return decimal.Zero
	}
	return accrualRate.Mul(decimal.NewFromInt(-NegativeFloorMultiplier))
}

// NextRequestStatus applies a decision to a request's status. changed is false
// when the request already has that outcome, so a replayed decision is a no-op.
func NextRequestStatus(status, decision string) (next string, changed bool, err error) {
	switch {
	case status == RequestSubmitted && decision == DecisionApprove:
		return RequestApproved, true, nil
	case status == RequestSubmitted && decision == DecisionReject:
		return RequestRejected, true, nil
	case status == RequestApproved && decision == DecisionApprove:
		return status, false, nil
	case status == RequestRejected && decision == DecisionReject:
		return status, false, nil
	}
	return "", false, &Error{Code: CodeRequestState, Detail: fmt.Sprintf("request is %s and cannot be %s", status, decision)}
}

// CanCancel reports whether a request in status may be withdrawn. changed is
// false for a request already CANCELED, so a replayed cancel is a no-op.
func CanCancel(status string) (changed bool, err error) {
	switch status {
	case RequestSubmitted:
		return true, nil
	case RequestCanceled:
		return false, nil
	}
	return false, &Error{Code: CodeRequestState, Detail: fmt.Sprintf("request is %s; only a submitted request can be cancelled", status)}
}

// CheckNotSelf refuses a decision by the requester themselves (8.42.17: approve
// own request blocked). A missing actor or requester identity cannot match.
func CheckNotSelf(actorUserID string, requesterUserID *string) error {
	if actorUserID != "" && requesterUserID != nil && *requesterUserID == actorUserID {
		return &Error{Code: CodeSelfDecision, Detail: "an approver cannot decide their own absence request"}
	}
	return nil
}

// InclusiveDays counts the calendar days from..to, both included. Dates are UTC
// midnights; a reversed range has no days.
func InclusiveDays(from, to time.Time) int {
	if to.Before(from) {
		return 0
	}
	return int(to.Sub(from)/(24*time.Hour)) + 1
}

// DaySpan is a stretch of employment in whole days, From and To inclusive.
// A nil To means the employment is still open.
type DaySpan struct {
	From time.Time
	To   *time.Time
}

// CoveredDays counts the days of [from, to] inside at least one span. Overlapping
// spans (concurrent PRIMARY employments in two entities) count each day once.
func CoveredDays(from, to time.Time, spans []DaySpan) int {
	type interval struct{ a, b time.Time }
	var ivs []interval
	for _, s := range spans {
		a, b := from, to
		if s.From.After(a) {
			a = s.From
		}
		if s.To != nil && s.To.Before(b) {
			b = *s.To
		}
		if !a.After(b) {
			ivs = append(ivs, interval{a, b})
		}
	}
	sort.Slice(ivs, func(i, j int) bool { return ivs[i].a.Before(ivs[j].a) })

	total := 0
	var curA, curB time.Time
	open := false
	for _, v := range ivs {
		if !open {
			curA, curB, open = v.a, v.b, true
			continue
		}
		// Adjacent days merge too, so [1,3] and [4,6] count as six days once.
		if !v.a.After(curB.AddDate(0, 0, 1)) {
			if v.b.After(curB) {
				curB = v.b
			}
			continue
		}
		total += InclusiveDays(curA, curB)
		curA, curB = v.a, v.b
	}
	if open {
		total += InclusiveDays(curA, curB)
	}
	return total
}

// PeriodInput is one worker's position for one accrual period.
type PeriodInput struct {
	Rate           decimal.Decimal
	Prorate        bool
	From, To       time.Time
	Employed       []DaySpan
	HireDate       time.Time
	MinServiceDays int
}

// PeriodAccrual is BR-42.3 for one worker and period. A worker with fewer
// service days than the plan's minimum at period end accrues nothing, and a
// worker not employed in the period accrues nothing.
func PeriodAccrual(in PeriodInput) decimal.Decimal {
	if in.MinServiceDays > 0 && int(in.To.Sub(in.HireDate)/(24*time.Hour)) < in.MinServiceDays {
		return decimal.Zero
	}
	employed := CoveredDays(in.From, in.To, in.Employed)
	if employed == 0 {
		return decimal.Zero
	}
	return AccrualAmount(in.Rate, employed, InclusiveDays(in.From, in.To), in.Prorate)
}

// ParsePlanYearStart reads an MM-DD plan-year start. The day must exist in
// every year, so 02-29 and 04-31 are refused.
func ParsePlanYearStart(mmdd string) (time.Month, int, bool) {
	t, err := time.Parse("2006-01-02", "2025-"+mmdd)
	if err != nil || len(mmdd) != 5 {
		return 0, 0, false
	}
	return t.Month(), t.Day(), true
}

// PlanYearStart is the first day of the plan year that contains d (UTC days).
func PlanYearStart(mmdd string, d time.Time) time.Time {
	m, day, ok := ParsePlanYearStart(mmdd)
	if !ok {
		m, day = time.January, 1
	}
	start := time.Date(d.Year(), m, day, 0, 0, 0, 0, time.UTC)
	if start.After(d) {
		start = start.AddDate(-1, 0, 0)
	}
	return start
}

// Accrual frequencies (FR-42.6).
const (
	FrequencyMonthly       = "MONTHLY"
	FrequencyBiweekly      = "BIWEEKLY"
	FrequencyAnnual        = "ANNUAL"
	FrequencyPerHourWorked = "PER_HOUR_WORKED"
)

// CheckAccrualPeriod refuses a period that is not one whole accrual period of
// the plan: a calendar month for MONTHLY, 14 days for BIWEEKLY, the plan year
// for ANNUAL. PER_HOUR_WORKED needs worked hours from time (wave 25) and is
// refused. Every refusal is HCM-ABS-PERIOD.
func CheckAccrualPeriod(frequency, planYearStart string, from, to time.Time) error {
	ok := false
	want := ""
	switch frequency {
	case FrequencyMonthly:
		ok = from.Day() == 1 && to.Equal(from.AddDate(0, 1, -1))
		want = "a calendar month, first to last day"
	case FrequencyBiweekly:
		ok = to.Equal(from.AddDate(0, 0, 13))
		want = "14 days"
	case FrequencyAnnual:
		start := PlanYearStart(planYearStart, from)
		ok = from.Equal(start) && to.Equal(start.AddDate(1, 0, -1))
		want = "the whole plan year"
	case FrequencyPerHourWorked:
		return &Error{Code: CodePeriod, Detail: "PER_HOUR_WORKED plans accrue from time registrations (wave 25), which are not available yet"}
	default:
		want = "a known accrual frequency"
	}
	if !ok {
		return &Error{Code: CodePeriod, Detail: fmt.Sprintf("a %s plan accrues %s; %s to %s is not one period", frequency, want, from.Format("2006-01-02"), to.Format("2006-01-02"))}
	}
	return nil
}

// CloseYear is the BR-42.3 year end for one ledger: the total carries into the
// next plan year up to carryMax and the rest expires. The closed ledger keeps
// its history and records the expiry, taken from CARRY_OVER first, so its
// balance afterwards equals what was carried. A negative total carries as is.
func CloseYear(l Ledger, carryMax decimal.Decimal) (closed Ledger, carried, expired decimal.Decimal) {
	next, expired := l.YearEnd(carryMax)
	carried = next.Carry.Accrued
	closed = l
	fromCarry := decimal.Min(expired, decimal.Max(l.Carry.Balance(), decimal.Zero))
	closed.Carry.Expired = closed.Carry.Expired.Add(fromCarry)
	closed.Current.Expired = closed.Current.Expired.Add(expired.Sub(fromCarry))
	return closed, carried, expired
}

// ExpireCarryOver ends the carry-over bucket at its expiry date (BR-42.3):
// whatever is still unused expires. A negative carry-over is debt, not leave,
// and does not expire.
func ExpireCarryOver(l Ledger) (Ledger, decimal.Decimal) {
	left := l.Carry.Balance()
	if !left.IsPositive() {
		return l, decimal.Zero
	}
	l.Carry.Expired = l.Carry.Expired.Add(left)
	return l, left
}

// MaxRequestQuantity bounds a request by its calendar span: one day per day
// for DAYS plans, 24 hours per day for HOURS plans.
func MaxRequestQuantity(unit string, from, to time.Time) decimal.Decimal {
	days := decimal.NewFromInt(int64(InclusiveDays(from, to)))
	if unit == "HOURS" {
		return days.Mul(decimal.NewFromInt(24))
	}
	return days
}
