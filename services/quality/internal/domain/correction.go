package domain

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Correction (CAPA) statuses of the 8D workflow (FR-33.8).
const (
	CAPAOpen                 = "OPEN"
	CAPAEffectivenessPending = "EFFECTIVENESS_PENDING"
	CAPAClosed               = "CLOSED"
)

// Effectiveness check results.
const (
	Effective    = "EFFECTIVE"
	NotEffective = "NOT_EFFECTIVE"
)

// CAPA timers and limits (BR-33.9, effectiveness_check_days).
const (
	ContainmentBusinessDays  = 2  // D3 due after start
	CorrectiveActionDays     = 30 // D5 due after start
	DefaultEffectivenessDays = 90 // effectiveness check after D7 closes
	MinEffectivenessDays     = 30
	MaxEffectivenessDays     = 365
	MaxStepNotes             = 2000
	// StepClose is D8: it completes through the effectiveness check, not directly.
	StepClose = 8
)

// StepNames are the eight disciplines, D1 to D8.
var StepNames = [8]string{
	"Team", "Problem description", "Containment", "Root cause",
	"Corrective action", "Implement and validate", "Prevent recurrence", "Close and recognise",
}

// CorrectionStep is one discipline of the 8D.
type CorrectionStep struct {
	Step        int        `json:"step"`
	Name        string     `json:"name"`
	DueAt       *time.Time `json:"dueAt,omitempty"`
	Notes       string     `json:"notes,omitempty"`
	CompletedBy string     `json:"completedBy,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

// Correction is the 8D correction (CAPA) of one nonconformance. D1 to D7 are
// completed in order; D7 starts the effectiveness timer, and D8 completes with an
// EFFECTIVE check. A NOT_EFFECTIVE check reopens the 8D at D4 (root cause).
type Correction struct {
	ID                  string           `json:"id"`
	Status              string           `json:"status"`
	CurrentStep         int              `json:"currentStep"`
	Steps               []CorrectionStep `json:"steps"`
	StartedBy           string           `json:"startedBy"`
	StartedAt           time.Time        `json:"startedAt"`
	EffectivenessDays   int              `json:"effectivenessDays"`
	EffectivenessDueAt  *time.Time       `json:"effectivenessDueAt,omitempty"`
	EffectivenessResult string           `json:"effectivenessResult,omitempty"`
	EffectivenessNote   string           `json:"effectivenessNote,omitempty"`
	EffectivenessBy     string           `json:"effectivenessBy,omitempty"`
	EffectivenessAt     *time.Time       `json:"effectivenessAt,omitempty"`
	Reopened            int              `json:"reopened"`
	ClosedAt            *time.Time       `json:"closedAt,omitempty"`
	// OverdueSteps and EffectivenessCheckDue are computed when the correction is
	// read (WithComputed), not stored.
	OverdueSteps          []int `json:"overdueSteps"`
	EffectivenessCheckDue bool  `json:"effectivenessCheckDue"`
}

// AddBusinessDays moves t forward by n working days, skipping Saturday and
// Sunday. The site calendar (capa_calendar) is a handoff; weekends are the default.
func AddBusinessDays(t time.Time, n int) time.Time {
	for n > 0 {
		t = t.AddDate(0, 0, 1)
		if wd := t.Weekday(); wd != time.Saturday && wd != time.Sunday {
			n--
		}
	}
	return t
}

func ptr(t time.Time) *time.Time { return &t }

// NewCorrection opens an 8D at D1 with the BR-33.9 due dates: D3 containment two
// business days after start, D5 corrective action 30 days after start.
// effectivenessDays 0 means the default of 90.
func NewCorrection(user string, effectivenessDays int, now time.Time) (Correction, error) {
	if effectivenessDays == 0 {
		effectivenessDays = DefaultEffectivenessDays
	}
	if effectivenessDays < MinEffectivenessDays || effectivenessDays > MaxEffectivenessDays {
		return Correction{}, invalid("effectivenessCheckDays must be 30 to 365")
	}
	now = now.UTC()
	c := Correction{Status: CAPAOpen, CurrentStep: 1, StartedBy: user, StartedAt: now, EffectivenessDays: effectivenessDays}
	c.Steps = make([]CorrectionStep, len(StepNames))
	for i, name := range StepNames {
		c.Steps[i] = CorrectionStep{Step: i + 1, Name: name}
	}
	c.Steps[2].DueAt = ptr(AddBusinessDays(now, ContainmentBusinessDays))
	c.Steps[4].DueAt = ptr(now.AddDate(0, 0, CorrectiveActionDays))
	return c, nil
}

// CompleteStep records the notes of the current step (D1 to D7) and moves to the
// next. Completing D7 starts the effectiveness timer.
func (c Correction) CompleteStep(step int, notes, user string, now time.Time) (Correction, error) {
	notes = strings.TrimSpace(notes)
	if notes == "" || utf8.RuneCountInString(notes) > MaxStepNotes {
		return c, invalid("notes are required and at most 2000 characters")
	}
	if c.Status != CAPAOpen {
		return c, fail(CodeCAPAStep, "the correction is "+c.Status+"; no step can be completed")
	}
	if step != c.CurrentStep {
		return c, fail(CodeCAPAStep, "D"+strconv.Itoa(step)+" is not the current step; complete D"+strconv.Itoa(c.CurrentStep)+" first")
	}
	now = now.UTC()
	c.Steps = append([]CorrectionStep(nil), c.Steps...)
	c.Steps[step-1].Notes, c.Steps[step-1].CompletedBy, c.Steps[step-1].CompletedAt = notes, user, ptr(now)
	c.CurrentStep = step + 1
	if step == StepClose-1 {
		c.Status = CAPAEffectivenessPending
		c.EffectivenessDueAt = ptr(now.AddDate(0, 0, c.EffectivenessDays))
	}
	return c, nil
}

// CheckEffectiveness records the effectiveness check once it is due. The checker
// must not be the user who completed D7 (quality.capa.maintain segregation of duties).
func (c Correction) CheckEffectiveness(result, note, user string, now time.Time) (Correction, error) {
	note = strings.TrimSpace(note)
	if result != Effective && result != NotEffective {
		return c, invalid("result must be EFFECTIVE or NOT_EFFECTIVE")
	}
	if note == "" || utf8.RuneCountInString(note) > MaxStepNotes {
		return c, invalid("note is required and at most 2000 characters")
	}
	if c.Status != CAPAEffectivenessPending || c.EffectivenessDueAt == nil {
		return c, fail(CodeCAPAStep, "the effectiveness check follows D7; the correction is "+c.Status)
	}
	now = now.UTC()
	if now.Before(*c.EffectivenessDueAt) {
		return c, fail(CodeEffectivenessNotDue, "the effectiveness check is due on "+c.EffectivenessDueAt.Format(time.DateOnly))
	}
	if user == "" || user == c.Steps[StepClose-2].CompletedBy {
		return c, fail(CodeEffectivenessSoD, "the effectiveness check must be made by a user other than the one who completed D7")
	}
	c.Steps = append([]CorrectionStep(nil), c.Steps...)
	c.EffectivenessResult, c.EffectivenessNote, c.EffectivenessBy, c.EffectivenessAt = result, note, user, ptr(now)
	if result == Effective {
		c.Steps[StepClose-1].Notes, c.Steps[StepClose-1].CompletedBy, c.Steps[StepClose-1].CompletedAt = note, user, ptr(now)
		c.Status, c.ClosedAt = CAPAClosed, ptr(now)
		return c, nil
	}
	// Not effective: back to root cause, with a fresh corrective-action due date.
	for i := 3; i < StepClose-1; i++ {
		c.Steps[i].Notes, c.Steps[i].CompletedBy, c.Steps[i].CompletedAt = "", "", nil
	}
	c.Steps[4].DueAt = ptr(now.AddDate(0, 0, CorrectiveActionDays))
	c.Status, c.CurrentStep, c.EffectivenessDueAt = CAPAOpen, 4, nil
	c.Reopened++
	return c, nil
}

// Overdue lists the steps whose due date passed before they were completed
// (the QM-0110 escalation condition).
func (c Correction) Overdue(now time.Time) []int {
	out := []int{}
	for _, s := range c.Steps {
		if s.DueAt != nil && s.CompletedAt == nil && s.DueAt.Before(now) {
			out = append(out, s.Step)
		}
	}
	return out
}

// EffectivenessDue reports whether the effectiveness check can be made now: D7 is
// complete and the effectiveness period has elapsed (AC-33.5 task).
func (c Correction) EffectivenessDue(now time.Time) bool {
	return c.Status == CAPAEffectivenessPending && c.EffectivenessDueAt != nil && !now.Before(*c.EffectivenessDueAt)
}

// WithComputed fills the read-time fields.
func (c Correction) WithComputed(now time.Time) Correction {
	c.OverdueSteps, c.EffectivenessCheckDue = c.Overdue(now), c.EffectivenessDue(now)
	return c
}
