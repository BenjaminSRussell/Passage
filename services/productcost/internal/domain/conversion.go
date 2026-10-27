package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// Checklist steps of a standard cost conversion, in order (FR-39.12): select
// the items and sites, roll pending standards into the conversion version,
// validate, preview the revaluation, run the inventory close up to the
// conversion date, and activate.
const (
	StepSelect   = "select"
	StepRoll     = "roll"
	StepValidate = "validate"
	StepPreview  = "preview"
	StepClose    = "close"
	StepActivate = "activate"
)

// ConversionSteps is the checklist order.
var ConversionSteps = []string{StepSelect, StepRoll, StepValidate, StepPreview, StepClose, StepActivate}

// ConversionRun statuses (data model: Draft / Validated / Closed / Activated).
// Reversed belongs to the reverse conversion, which is not built yet.
const (
	ConversionDraft     = "Draft"
	ConversionValidated = "Validated"
	ConversionClosed    = "Closed"
	ConversionActivated = "Activated"
)

// StepDone is the status of a recorded checklist step.
const StepDone = "Done"

// Conversion blockers as configured per legal entity (costing.conversionBlockers).
const (
	BlockNegativeOnHand = "NegativeOnHand"
	BlockInTransit      = "InTransit"
	BlockStartedOrders  = "StartedOrders"
)

// DefaultConversionBlockers is the parameter default: all three conditions block.
var DefaultConversionBlockers = []string{BlockNegativeOnHand, BlockInTransit, BlockStartedOrders}

// Conversion codes. CST-0731 and CST-0732 are from the wave-39 catalog;
// CST-0733 to CST-0736 extend it for this slice (see README).
const (
	CodeConversionClose   = "CST-0731"
	CodeNegativeOnHand    = "CST-0732"
	CodeConversionInTrans = "CST-0733"
	CodeStartedOrders     = "CST-0734"
	CodeChecklistBlocked  = "CST-0735"
	CodeStepOrder         = "CST-0736"
)

const dateLayout = "2006-01-02"

// ConversionPosition is one item at one site at the conversion date.
// ActualValue is the on-hand value at the actual costing method after the
// inventory close; StandardUnit is the pending standard of the conversion
// version. InTransit counts open in-transit transfers and StartedOrders the
// production orders started but not ended.
type ConversionPosition struct {
	ItemID        string          `json:"itemId"`
	SiteID        string          `json:"siteId"`
	OnHand        decimal.Decimal `json:"onHand"`
	StandardUnit  decimal.Decimal `json:"standardUnit"`
	ActualValue   decimal.Decimal `json:"actualValue"`
	InTransit     int             `json:"inTransit"`
	StartedOrders int             `json:"startedOrders"`
}

// ConversionInput is what the checklist validates. Dates are YYYY-MM-DD.
// CloseThrough is the end date of the last completed inventory close; the
// close step and activation need it to be the day before ConversionDate
// (BR-39.11). Blockers nil means the default (all three block); an empty list
// means none blocks. Currency sets the posting precision (BR-39.1).
type ConversionInput struct {
	ConversionDate string               `json:"conversionDate"`
	CloseThrough   string               `json:"closeThrough,omitempty"`
	Currency       string               `json:"currency,omitempty"`
	Blockers       []string             `json:"blockers"`
	Positions      []ConversionPosition `json:"positions"`
}

// ConversionPreviewLine is one position with its revaluation.
type ConversionPreviewLine struct {
	ItemID       string          `json:"itemId"`
	SiteID       string          `json:"siteId"`
	OnHand       decimal.Decimal `json:"onHand"`
	ActualValue  decimal.Decimal `json:"actualValue"`
	StandardUnit decimal.Decimal `json:"standardUnit"`
	Amount       decimal.Decimal `json:"amount"`
}

// ConversionReport is the validation outcome and the revaluation preview.
// Blocked is true when an enabled blocker matches (validation fails);
// CloseComplete is true when the inventory close runs through the day before
// the conversion date. Activation needs both: not blocked and closed.
type ConversionReport struct {
	Currency      string                  `json:"currency,omitempty"`
	Blocked       bool                    `json:"blocked"`
	CloseComplete bool                    `json:"closeComplete"`
	Diagnostics   []Diagnostic            `json:"diagnostics,omitempty"`
	Lines         []ConversionPreviewLine `json:"lines"`
	Revaluation   decimal.Decimal         `json:"revaluation"`
	Journal       []JournalLine           `json:"journal"`
}

// ChecklistStep records one executed step with its user and timestamp.
type ChecklistStep struct {
	Step   string    `json:"step"`
	Status string    `json:"status"`
	User   string    `json:"user"`
	At     time.Time `json:"at"`
}

// ConversionRun is the checklist state of one conversion. Status follows from
// the steps recorded, so a caller cannot send a status its checklist does not
// support.
type ConversionRun struct {
	Status    string          `json:"status"`
	Checklist []ChecklistStep `json:"checklist"`
}

func hasBlocker(blockers []string, name string) bool {
	for _, b := range blockers {
		if b == name {
			return true
		}
	}
	return false
}

// ValidateConversion runs the checklist's validation and computes the
// revaluation preview (FR-39.12): Σ on-hand × standard − on-hand value at
// actual cost, rounded per position. Enabled blockers turn a condition into an
// error; a disabled one leaves a warning. A missing or wrong inventory close is
// a warning here, because the close is a later checklist step; the close step
// and activation refuse it (CST-0731).
func ValidateConversion(in ConversionInput) (ConversionReport, error) {
	conv, err := time.Parse(dateLayout, in.ConversionDate)
	if err != nil {
		return ConversionReport{}, fmt.Errorf("conversionDate must be YYYY-MM-DD")
	}
	dayBefore := conv.AddDate(0, 0, -1)
	rep := ConversionReport{Currency: in.Currency, Journal: []JournalLine{}, Lines: []ConversionPreviewLine{}}
	if in.CloseThrough != "" {
		closed, err := time.Parse(dateLayout, in.CloseThrough)
		if err != nil {
			return ConversionReport{}, fmt.Errorf("closeThrough must be YYYY-MM-DD")
		}
		rep.CloseComplete = closed.Equal(dayBefore)
	}
	if !rep.CloseComplete {
		rep.Diagnostics = append(rep.Diagnostics, warnDiag(CodeConversionClose,
			fmt.Sprintf("inventory close through %s required before conversion on %s", dayBefore.Format(dateLayout), in.ConversionDate), ""))
	}
	blockers := in.Blockers
	if blockers == nil {
		blockers = DefaultConversionBlockers
	}
	condition := func(blocker, code, msg, ref string) {
		d := errDiag(code, msg, ref)
		if !hasBlocker(blockers, blocker) {
			d.Severity = "warning"
		}
		rep.Diagnostics = append(rep.Diagnostics, d)
		if d.Severity == "error" {
			rep.Blocked = true
		}
	}

	lines := make([]ConversionLine, 0, len(in.Positions))
	for _, p := range in.Positions {
		ref := p.ItemID + "@" + p.SiteID
		lines = append(lines, ConversionLine{ItemID: p.ItemID, SiteID: p.SiteID, Qty: p.OnHand, StdUnit: p.StandardUnit, ActualValue: p.ActualValue})
		if p.OnHand.IsNegative() {
			condition(BlockNegativeOnHand, CodeNegativeOnHand, "negative physical on-hand blocks conversion", ref)
		}
		if p.InTransit > 0 {
			condition(BlockInTransit, CodeConversionInTrans, fmt.Sprintf("%d open in-transit transfers", p.InTransit), ref)
		}
		if p.StartedOrders > 0 {
			msg := fmt.Sprintf("%d production orders started but not ended", p.StartedOrders)
			if !hasBlocker(blockers, BlockStartedOrders) {
				msg += "; their variance is computed at the new standard"
			}
			condition(BlockStartedOrders, CodeStartedOrders, msg, ref)
		}
	}
	amounts, total, journal := ConversionAdjustment(lines, in.Currency)
	for i, p := range in.Positions {
		rep.Lines = append(rep.Lines, ConversionPreviewLine{
			ItemID: p.ItemID, SiteID: p.SiteID, OnHand: p.OnHand, ActualValue: p.ActualValue, StandardUnit: p.StandardUnit, Amount: amounts[i],
		})
	}
	rep.Revaluation, rep.Journal = total, journal
	return rep, nil
}

// CheckChecklist verifies that the recorded steps are a prefix of the
// checklist order, each done by a user. It returns the index of the first
// step that is not, or -1.
func CheckChecklist(run ConversionRun) int {
	for i, s := range run.Checklist {
		if i >= len(ConversionSteps) || s.Step != ConversionSteps[i] || s.Status != StepDone || s.User == "" {
			return i
		}
	}
	return -1
}

// NextStep returns the first checklist step not yet done, or "" when the
// checklist is complete. The checklist must pass CheckChecklist.
func NextStep(run ConversionRun) string {
	if len(run.Checklist) >= len(ConversionSteps) {
		return ""
	}
	return ConversionSteps[len(run.Checklist)]
}

// statusAfter is the run status once step is done.
func statusAfter(step, current string) string {
	switch step {
	case StepValidate:
		return ConversionValidated
	case StepClose:
		return ConversionClosed
	case StepActivate:
		return ConversionActivated
	}
	if current == "" {
		return ConversionDraft
	}
	return current
}

// Advance records step as done by user at the given time. Steps run in
// checklist order (CST-0736). Validate needs a report without blockers
// (CST-0735); close needs the inventory close through the day before the
// conversion date (CST-0731, BR-39.11); activate needs both.
func Advance(run ConversionRun, step, user string, at time.Time, rep ConversionReport) (ConversionRun, error) {
	if user == "" {
		return run, fmt.Errorf("checklist steps record the user who ran them")
	}
	if i := CheckChecklist(run); i >= 0 {
		return run, fmt.Errorf("checklist[%d] is not the next step in order, done by a user", i)
	}
	next := NextStep(run)
	if next == "" {
		return run, coded(CodeStepOrder, "the checklist is complete")
	}
	if next != step {
		return run, coded(CodeStepOrder, "step %s cannot run before %s", step, next)
	}
	blocked := func() error {
		return coded(CodeChecklistBlocked, "conversion is blocked by %d error(s)", countErrors(rep.Diagnostics))
	}
	notClosed := func() error {
		return coded(CodeConversionClose, "the inventory close through the day before the conversion date is required before %s", step)
	}
	switch step {
	case StepValidate:
		if rep.Blocked {
			return run, blocked()
		}
	case StepClose:
		if !rep.CloseComplete {
			return run, notClosed()
		}
	case StepActivate:
		if rep.Blocked {
			return run, blocked()
		}
		if !rep.CloseComplete {
			return run, notClosed()
		}
	}
	out := ConversionRun{Status: ConversionDraft}
	out.Checklist = append(append([]ChecklistStep{}, run.Checklist...), ChecklistStep{Step: step, Status: StepDone, User: user, At: at})
	for _, s := range out.Checklist {
		out.Status = statusAfter(s.Step, out.Status)
	}
	return out, nil
}

func countErrors(diags []Diagnostic) int {
	n := 0
	for _, d := range diags {
		if d.Severity == "error" {
			n++
		}
	}
	return n
}
