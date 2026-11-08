package domain

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

// Codes of the End readiness check (FR-21.8, FR-21.19). The first two refuse `:end`
// (PROD-0012 and the posting date rule); the last is the PRD's PROD-0020 warning.
// PROD-0014 (subcontract operations with no vendor invoice) is not reported: this service
// receives no PO line, receipt or vendor invoice for a subcontract operation yet (see
// NOT-IMPLEMENTED.md), so it cannot tell an invoiced operation from one that is not.
const (
	CodeUnpostedJournals    = "PRODUCTION_UNPOSTED_JOURNALS"
	CodeEndBeforeLastPosted = "PRODUCTION_END_BEFORE_LAST_JOURNAL"
	// CodeAsBuiltUnresolved is PROD-0020: consumed component serials not assigned to a finished serial.
	CodeAsBuiltUnresolved = "PRODUCTION_AS_BUILT_UNRESOLVED"
)

// ComponentCoverage is one serial-tracked component's consumed quantity against the
// number of its serials that as-built records assign to finished serials or batches.
type ComponentCoverage struct {
	Consumed decimal.Decimal
	Assigned decimal.Decimal
}

// EndFacts is what the End readiness check reads about an order.
type EndFacts struct {
	Status           Status
	QtyRAF           decimal.Decimal
	ZeroOutputDuty   bool
	UnpostedJournals int
	// LastPostedOn is the latest posting date of a posted journal ("" when none) and Today
	// the date End would post on, both YYYY-MM-DD.
	LastPostedOn string
	Today        string
	// AsBuiltRequired and AttachedAsBuilt are the site's "As-built required" parameter and
	// the order's attached as-built records.
	AsBuiltRequired bool
	AttachedAsBuilt int
	// Settlement is End's settlement refusal, if any: no posting profile resolves for the
	// order, or it lacks an account the settlement posts to.
	Settlement *EndFinding
	// Components has one entry per component with at least one attached serial as-built record.
	Components []ComponentCoverage
}

// EndFinding is one blocker or warning of the End check.
type EndFinding struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Count is the number of journals or serials the message speaks of.
	Count int `json:"count,omitempty"`
}

// EndReport says whether `:end` would be accepted now and why not, and what it would
// accept with a warning.
type EndReport struct {
	CanEnd   bool         `json:"canEnd"`
	Blockers []EndFinding `json:"blockers"`
	Warnings []EndFinding `json:"warnings"`
}

// EvaluateEnd applies the End guards to the facts without changing anything, in the order
// `:end` applies them: the status machine, the output rule of BR-21.1, "As-built
// required" (PROD-0021), unposted journals (PROD-0012), the end date against the last
// posting and the settlement's posting profile refuse; unassigned component serials
// (PROD-0020) warn. An order that cannot take `:end` from its status reports that alone.
func EvaluateEnd(f EndFacts) EndReport {
	out := EndReport{Blockers: []EndFinding{}, Warnings: []EndFinding{}}
	if _, err := Transition(f.Status, CmdEnd); err != nil {
		out.Blockers = append(out.Blockers, findingOf(err))
		return out
	}
	if err := CheckEnd(f.Status, f.QtyRAF, f.ZeroOutputDuty); err != nil {
		out.Blockers = append(out.Blockers, findingOf(err))
	}
	if err := CheckAsBuiltRequired(f.AsBuiltRequired, f.QtyRAF, f.AttachedAsBuilt); err != nil {
		out.Blockers = append(out.Blockers, findingOf(err))
	}
	if err := CheckUnpostedJournals(f.UnpostedJournals); err != nil {
		finding := findingOf(err)
		finding.Count = f.UnpostedJournals
		out.Blockers = append(out.Blockers, finding)
	}
	if err := CheckEndDate(f.LastPostedOn, f.Today); err != nil {
		out.Blockers = append(out.Blockers, findingOf(err))
	}
	if f.Settlement != nil {
		out.Blockers = append(out.Blockers, *f.Settlement)
	}
	if n := UnresolvedSerials(f.Components); n > 0 {
		out.Warnings = append(out.Warnings, EndFinding{Code: CodeAsBuiltUnresolved, Count: n,
			Message: fmt.Sprintf("%d component serials are not assigned to a finished serial", n)})
	}
	out.CanEnd = len(out.Blockers) == 0
	return out
}

// CheckUnpostedJournals refuses End while the order has unposted journals (PROD-0012).
func CheckUnpostedJournals(unposted int) error {
	if unposted == 0 {
		return nil
	}
	return &Error{Code: CodeUnpostedJournals, Detail: fmt.Sprintf("cannot end: %d journals are unposted; post or delete them", unposted)}
}

// CheckEndDate refuses an End date (today, YYYY-MM-DD) before the latest posting date of
// a posted journal ("" when none).
func CheckEndDate(lastPostedOn, today string) error {
	if lastPostedOn == "" || lastPostedOn <= today {
		return nil
	}
	return &Error{Code: CodeEndBeforeLastPosted, Detail: fmt.Sprintf("the end date %s is before the last journal posting date %s", today, lastPostedOn)}
}

// UnresolvedSerials is the number of consumed serials of serial-tracked components that
// as-built records leave unassigned (BR-21.14), summed over the components and rounded up
// per component.
func UnresolvedSerials(components []ComponentCoverage) int {
	n := 0
	for _, c := range components {
		if open := c.Consumed.Sub(c.Assigned); open.IsPositive() {
			n += int(open.Ceil().IntPart())
		}
	}
	return n
}

func findingOf(err error) EndFinding {
	var de *Error
	if errors.As(err, &de) {
		return EndFinding{Code: de.Code, Message: de.Detail}
	}
	return EndFinding{Code: CodeInvalidInput, Message: err.Error()}
}
