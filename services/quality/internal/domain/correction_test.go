package domain

import (
	"strings"
	"testing"
	"time"
)

func TestAddBusinessDays_SkipsWeekends(t *testing.T) {
	cases := []struct {
		start string
		n     int
		want  string
	}{
		{"2026-03-02", 2, "2026-03-04"}, // Monday -> Wednesday
		{"2026-03-05", 2, "2026-03-09"}, // Thursday -> Monday
		{"2026-03-06", 2, "2026-03-10"}, // Friday -> Tuesday
		{"2026-03-07", 2, "2026-03-10"}, // Saturday -> Tuesday
		{"2026-03-08", 1, "2026-03-09"}, // Sunday -> Monday
		{"2026-03-02", 0, "2026-03-02"},
	}
	for _, tc := range cases {
		start, _ := time.Parse(time.DateOnly, tc.start)
		if got := AddBusinessDays(start, tc.n).Format(time.DateOnly); got != tc.want {
			t.Errorf("%s + %d business days = %s, want %s", tc.start, tc.n, got, tc.want)
		}
	}
}

// BR-33.9: D3 containment two business days after start, D5 corrective action 30
// days after start, effectiveness check the configured days after D7.
func TestNewCorrection_DueDates(t *testing.T) {
	friday := time.Date(2026, 3, 6, 15, 0, 0, 0, time.UTC)
	c, err := NewCorrection("qe-1", 0, friday)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != CAPAOpen || c.CurrentStep != 1 || len(c.Steps) != 8 || c.EffectivenessDays != 90 {
		t.Fatalf("new correction: %+v", c)
	}
	if got := c.Steps[2].DueAt.Format(time.DateOnly); got != "2026-03-10" {
		t.Errorf("D3 due %s, want 2026-03-10 (two business days after a Friday)", got)
	}
	if got := c.Steps[4].DueAt.Format(time.DateOnly); got != "2026-04-05" {
		t.Errorf("D5 due %s, want 2026-04-05", got)
	}
	for i, s := range c.Steps {
		if s.Step != i+1 || s.Name != StepNames[i] || (s.DueAt != nil) != (i == 2 || i == 4) {
			t.Errorf("step %d: %+v", i+1, s)
		}
	}
	for _, days := range []int{29, 366, -1} {
		if _, err := NewCorrection("qe-1", days, friday); code(err) != CodeValidation {
			t.Errorf("effectiveness days %d: %v", days, err)
		}
	}
}

func completeThrough(t *testing.T, c Correction, last int, user string, at time.Time) Correction {
	t.Helper()
	for step := c.CurrentStep; step <= last; step++ {
		var err error
		c, err = c.CompleteStep(step, "D"+string(rune('0'+step))+" notes", user, at)
		if err != nil {
			t.Fatalf("D%d: %v", step, err)
		}
	}
	return c
}

func TestCompleteStep_InOrderAndD7StartsTimer(t *testing.T) {
	start := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	c, _ := NewCorrection("qe-1", 60, start)
	if _, err := c.CompleteStep(2, "problem", "qe-1", start); code(err) != CodeCAPAStep {
		t.Fatalf("D2 before D1: %v", err)
	}
	if _, err := c.CompleteStep(1, "   ", "qe-1", start); code(err) != CodeValidation {
		t.Fatalf("empty notes: %v", err)
	}
	if _, err := c.CompleteStep(1, strings.Repeat("x", MaxStepNotes+1), "qe-1", start); code(err) != CodeValidation {
		t.Fatalf("long notes: %v", err)
	}
	one, err := c.CompleteStep(1, "  team: QE, buyer  ", "qe-1", start)
	if err != nil || one.CurrentStep != 2 || one.Steps[0].Notes != "team: QE, buyer" || one.Steps[0].CompletedBy != "qe-1" {
		t.Fatalf("D1: %+v %v", one, err)
	}
	if c.Steps[0].CompletedAt != nil {
		t.Fatal("completing a step must not change the original correction")
	}
	d7 := start.AddDate(0, 0, 20)
	done := completeThrough(t, one, 7, "qe-1", d7)
	if done.Status != CAPAEffectivenessPending || done.CurrentStep != 8 || !done.EffectivenessDueAt.Equal(d7.AddDate(0, 0, 60)) {
		t.Fatalf("after D7: %+v", done)
	}
	if _, err := done.CompleteStep(8, "close", "qe-1", d7); code(err) != CodeCAPAStep {
		t.Fatalf("D8 completes only through the effectiveness check: %v", err)
	}
}

// AC-33.5: once D7 closes and the period elapses the check is due, and an
// EFFECTIVE check by another user completes D8 and closes the correction.
func TestCheckEffectiveness_DueSoDAndClose(t *testing.T) {
	start := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	c, _ := NewCorrection("qe-1", 0, start)
	d7 := start.AddDate(0, 0, 25)
	c = completeThrough(t, c, 7, "qe-1", d7)
	due := d7.AddDate(0, 0, 90)

	if c.EffectivenessDue(due.Add(-time.Second)) || !c.EffectivenessDue(due) {
		t.Fatal("the check falls due exactly 90 days after D7")
	}
	if _, err := c.CheckEffectiveness(Effective, "no recurrence", "qa-2", due.Add(-time.Hour)); code(err) != CodeEffectivenessNotDue {
		t.Fatalf("check before due: %v", err)
	}
	if _, err := c.CheckEffectiveness(Effective, "no recurrence", "qe-1", due); code(err) != CodeEffectivenessSoD {
		t.Fatalf("D7 owner checks: %v", err)
	}
	if _, err := c.CheckEffectiveness("MAYBE", "x", "qa-2", due); code(err) != CodeValidation {
		t.Fatalf("unknown result: %v", err)
	}
	if _, err := c.CheckEffectiveness(Effective, "", "qa-2", due); code(err) != CodeValidation {
		t.Fatalf("empty note: %v", err)
	}
	closed, err := c.CheckEffectiveness(Effective, "no recurrence in 90 days", "qa-2", due)
	if err != nil || closed.Status != CAPAClosed || closed.ClosedAt == nil || closed.Steps[7].CompletedBy != "qa-2" || closed.EffectivenessResult != Effective {
		t.Fatalf("effective: %+v %v", closed, err)
	}
	if _, err := closed.CheckEffectiveness(Effective, "again", "qa-2", due); code(err) != CodeCAPAStep {
		t.Fatalf("check a closed correction: %v", err)
	}
}

func TestCheckEffectiveness_NotEffectiveReopensAtD4(t *testing.T) {
	start := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	c, _ := NewCorrection("qe-1", 30, start)
	c = completeThrough(t, c, 7, "qe-1", start)
	at := start.AddDate(0, 0, 31)
	open, err := c.CheckEffectiveness(NotEffective, "defect recurred on lot 7", "qa-2", at)
	if err != nil {
		t.Fatal(err)
	}
	if open.Status != CAPAOpen || open.CurrentStep != 4 || open.Reopened != 1 || open.EffectivenessDueAt != nil || open.EffectivenessResult != NotEffective {
		t.Fatalf("reopen: %+v", open)
	}
	for i := 0; i < 8; i++ {
		completed := open.Steps[i].CompletedAt != nil
		if completed != (i < 3) {
			t.Errorf("D%d completed=%v after reopening at D4", i+1, completed)
		}
	}
	if !open.Steps[4].DueAt.Equal(at.AddDate(0, 0, CorrectiveActionDays)) {
		t.Errorf("D5 due %v, want 30 days after the failed check", open.Steps[4].DueAt)
	}
	again := completeThrough(t, open, 7, "qe-1", at)
	if again.Status != CAPAEffectivenessPending {
		t.Fatalf("second pass: %+v", again)
	}
}

func TestOverdue(t *testing.T) {
	start := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC) // D3 due 03-04, D5 due 04-01
	c, _ := NewCorrection("qe-1", 0, start)
	if got := c.Overdue(start.AddDate(0, 0, 1)); len(got) != 0 {
		t.Fatalf("nothing overdue on day 1: %v", got)
	}
	if got := c.Overdue(start.AddDate(0, 0, 3)); len(got) != 1 || got[0] != 3 {
		t.Fatalf("D3 overdue on day 3: %v", got)
	}
	if got := c.Overdue(start.AddDate(0, 0, 31)); len(got) != 2 || got[1] != 5 {
		t.Fatalf("D3 and D5 overdue on day 31: %v", got)
	}
	c = completeThrough(t, c, 3, "qe-1", start)
	if got := c.Overdue(start.AddDate(0, 0, 31)); len(got) != 1 || got[0] != 5 {
		t.Fatalf("a completed D3 is not overdue: %v", got)
	}
	computed := c.WithComputed(start.AddDate(0, 0, 31))
	if len(computed.OverdueSteps) != 1 || computed.EffectivenessCheckDue {
		t.Fatalf("computed: %+v", computed)
	}
}
