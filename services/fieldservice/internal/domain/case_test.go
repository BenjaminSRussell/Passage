package domain

import (
	"reflect"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// gold is the BR-45.2 example target: priority 2, response 60 and resolution
// 480 business minutes.
var gold = Target{Priority: 2, ResponseMinutes: 60, ResolutionMinutes: 480}

func chicagoCal(t *testing.T) Calendar {
	t.Helper()
	return MondayToFriday(chicago(t), 480, 1080)
}

func at(cal Calendar, day, hour, minute int) time.Time {
	return time.Date(2026, 10, day, hour, minute, 0, 0, cal.Loc)
}

func mustMove(t *testing.T, c CaseClocks, to CaseStatus, now time.Time, cal Calendar, pauseOnParts bool) (CaseClocks, []SLAAlert) {
	t.Helper()
	next, alerts, ok := c.Transition(to, now, cal, gold, pauseOnParts)
	if !ok {
		t.Fatalf("%s -> %s refused", c.Status, to)
	}
	return next, alerts
}

func TestCanCaseTransition(t *testing.T) {
	tests := []struct {
		from, to CaseStatus
		want     bool
	}{
		{CaseNew, CaseInProgress, true},
		{CaseNew, CaseResolved, true},
		{CaseInProgress, CaseWaitingCustomer, true},
		{CaseWaitingCustomer, CaseWaitingParts, true},
		{CaseWaitingParts, CaseResolved, true},
		{CaseWaitingCustomer, CaseCanceled, true},
		{CaseResolved, CaseClosed, true},
		{CaseInProgress, CaseNew, false},
		{CaseInProgress, CaseClosed, false},
		{CaseResolved, CaseInProgress, false},
		{CaseClosed, CaseInProgress, false},
		{CaseCanceled, CaseNew, false},
	}
	for _, tc := range tests {
		if got := CanCaseTransition(tc.from, tc.to); got != tc.want {
			t.Errorf("%s -> %s: got %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestPausesResolution(t *testing.T) {
	tests := []struct {
		to    CaseStatus
		parts bool
		want  bool
	}{
		{CaseWaitingCustomer, false, true},
		{CaseWaitingParts, true, true},
		{CaseWaitingParts, false, false},
		{CaseInProgress, true, false},
		{CaseResolved, true, false},
	}
	for _, tc := range tests {
		if got := PausesResolution(tc.to, tc.parts); got != tc.want {
			t.Errorf("%s pauseOnParts=%v: got %v, want %v", tc.to, tc.parts, got, tc.want)
		}
	}
}

func TestWarningMinutesRoundsUp(t *testing.T) {
	for _, tc := range []struct{ target, pct, want int }{{480, 75, 360}, {60, 75, 45}, {61, 75, 46}, {1, 75, 1}, {100, 50, 50}} {
		if got := WarningMinutes(tc.target, tc.pct); got != tc.want {
			t.Errorf("WarningMinutes(%d, %d) = %d, want %d", tc.target, tc.pct, got, tc.want)
		}
	}
}

// AC-45.2 under the BR-45.2 formula: opened Fri 2026-10-23 16:00 Chicago, the
// 480-minute resolution target is due Mon 14:00; waiting on the customer Mon
// 10:00 to 12:00 adds 120 business minutes, so it is due Mon 16:00. The PRD's
// Tue 12:00 does not follow from its own calendar; see NOT-IMPLEMENTED.md.
func TestTransition_PauseMovesResolutionDue(t *testing.T) {
	cal := chicagoCal(t)
	c := StartClocks(at(cal, 23, 16, 0), cal, gold)
	if want := at(cal, 26, 14, 0); !c.ResolutionDue.Equal(want) {
		t.Fatalf("unpaused due %s, want %s", c.ResolutionDue, want)
	}
	if want := at(cal, 26, 12, 0); !c.ResolutionWarnAt.Equal(want) {
		t.Fatalf("warning at %s, want %s (360 business minutes)", c.ResolutionWarnAt, want)
	}

	c, _ = mustMove(t, c, CaseInProgress, at(cal, 26, 8, 30), cal, false)
	c, alerts := mustMove(t, c, CaseWaitingCustomer, at(cal, 26, 10, 0), cal, false)
	if c.State != SLAPaused || c.PausedAt == nil || len(alerts) != 0 {
		t.Fatalf("waiting: state %s pausedAt %v alerts %v, want PAUSED with an open pause", c.State, c.PausedAt, alerts)
	}
	c, _ = mustMove(t, c, CaseInProgress, at(cal, 26, 12, 0), cal, false)
	if c.State != SLARunning || c.PausedAt != nil || c.PausedMinutes != 120 {
		t.Fatalf("resumed: state %s pausedAt %v paused %d, want RUNNING and 120", c.State, c.PausedAt, c.PausedMinutes)
	}
	if want := at(cal, 26, 16, 0); !c.ResolutionDue.Equal(want) {
		t.Fatalf("paused due %s, want %s", c.ResolutionDue, want)
	}
	if want := at(cal, 26, 14, 0); !c.ResolutionWarnAt.Equal(want) {
		t.Fatalf("paused warning at %s, want %s", c.ResolutionWarnAt, want)
	}
}

// BR-45.2: leaving NEW is the response. Responding after the response due emits
// the response breach once; the resolution clock is unaffected.
func TestTransition_LateResponseBreachesResponseOnly(t *testing.T) {
	cal := chicagoCal(t)
	c := StartClocks(at(cal, 26, 8, 0), cal, gold)
	c, alerts := mustMove(t, c, CaseInProgress, at(cal, 26, 9, 30), cal, false)
	if c.RespondedAt == nil || !c.RespondedAt.Equal(at(cal, 26, 9, 30)) {
		t.Fatalf("respondedAt %v, want 09:30", c.RespondedAt)
	}
	want := []SLAAlert{{Clock: ClockResponse, Kind: AlertBreach, DueAt: at(cal, 26, 9, 0)}}
	if !reflect.DeepEqual(alerts, want) || c.State != SLARunning {
		t.Fatalf("alerts %v state %s, want %v and RUNNING", alerts, c.State, want)
	}
	// A later move does not respond again.
	_, alerts = mustMove(t, c, CaseWaitingCustomer, at(cal, 26, 10, 0), cal, false)
	if len(alerts) != 0 {
		t.Fatalf("second move alerts %v, want none", alerts)
	}
}

func TestTransition_PartsPauseFollowsProfile(t *testing.T) {
	cal := chicagoCal(t)
	start := StartClocks(at(cal, 26, 8, 0), cal, gold)
	off, _ := mustMove(t, start, CaseWaitingParts, at(cal, 26, 8, 30), cal, false)
	if off.State != SLARunning || off.PausedAt != nil {
		t.Fatalf("pauseOnParts=false: state %s pausedAt %v, want RUNNING", off.State, off.PausedAt)
	}
	on, _ := mustMove(t, start, CaseWaitingParts, at(cal, 26, 8, 30), cal, true)
	if on.State != SLAPaused || on.PausedAt == nil {
		t.Fatalf("pauseOnParts=true: state %s, want PAUSED", on.State)
	}
	// Switching from parts to customer keeps the clock stopped and banks the minutes so far.
	switched, _ := mustMove(t, on, CaseWaitingCustomer, at(cal, 26, 9, 0), cal, true)
	if switched.State != SLAPaused || switched.PausedMinutes != 30 || !switched.PausedAt.Equal(at(cal, 26, 9, 0)) {
		t.Fatalf("switched: %+v, want PAUSED with 30 minutes banked and a new pause at 09:00", switched)
	}
}

// A RUNNING clock already past its due is breached by the next transition, even
// one that would pause it: the pause cannot hide time that already ran out.
func TestTransition_OverdueClockBreachesInsteadOfPausing(t *testing.T) {
	cal := chicagoCal(t)
	c := StartClocks(at(cal, 26, 8, 0), cal, gold)
	c, _ = mustMove(t, c, CaseInProgress, at(cal, 26, 8, 10), cal, false)
	c, alerts := mustMove(t, c, CaseWaitingCustomer, at(cal, 26, 16, 30), cal, false)
	if c.State != SLABreached || c.PausedAt != nil {
		t.Fatalf("state %s pausedAt %v, want BREACHED and no pause", c.State, c.PausedAt)
	}
	if len(alerts) != 1 || alerts[0].Clock != ClockResolution || alerts[0].Kind != AlertBreach {
		t.Fatalf("alerts %v, want one resolution breach", alerts)
	}
	// Resolved later it stays BREACHED and emits nothing new (attainment counts it as missed).
	c, _ = mustMove(t, c, CaseInProgress, at(cal, 26, 17, 0), cal, false)
	c, alerts = mustMove(t, c, CaseResolved, at(cal, 26, 17, 30), cal, false)
	if c.State != SLABreached || len(alerts) != 0 || c.ResolvedAt == nil {
		t.Fatalf("resolved: state %s alerts %v resolvedAt %v, want BREACHED, none, set", c.State, alerts, c.ResolvedAt)
	}
}

func TestTransition_ResolveInTimeIsMet(t *testing.T) {
	cal := chicagoCal(t)
	c := StartClocks(at(cal, 26, 8, 0), cal, gold)
	c, _ = mustMove(t, c, CaseInProgress, at(cal, 26, 8, 15), cal, false)
	c, _ = mustMove(t, c, CaseWaitingCustomer, at(cal, 26, 9, 0), cal, false)
	c, alerts := mustMove(t, c, CaseResolved, at(cal, 26, 11, 0), cal, false)
	if c.State != SLAMet || len(alerts) != 0 || c.PausedMinutes != 120 || !c.ResolvedAt.Equal(at(cal, 26, 11, 0)) {
		t.Fatalf("resolved from waiting: %+v alerts %v, want MET with 120 paused minutes", c, alerts)
	}
	closed, _ := mustMove(t, c, CaseClosed, at(cal, 27, 9, 0), cal, false)
	if closed.State != SLAMet || closed.Status != CaseClosed {
		t.Fatalf("closed: state %s status %s", closed.State, closed.Status)
	}
	if _, _, ok := closed.Transition(CaseInProgress, at(cal, 27, 10, 0), cal, gold, false); ok {
		t.Fatal("a CLOSED case was reopened")
	}
}

func TestTransition_CancelIsNotAResponse(t *testing.T) {
	cal := chicagoCal(t)
	c, alerts := mustMove(t, StartClocks(at(cal, 26, 8, 0), cal, gold), CaseCanceled, at(cal, 26, 10, 0), cal, false)
	if c.RespondedAt != nil || len(alerts) != 0 || c.State != SLARunning {
		t.Fatalf("canceled: respondedAt %v alerts %v state %s", c.RespondedAt, alerts, c.State)
	}
	if _, more := c.DueAlerts(at(cal, 27, 8, 0)); len(more) != 0 {
		t.Fatalf("monitor alerted on a canceled case: %v", more)
	}
}

// AC-45.3: the monitor warns once at 75 % and breaches once past due; a clock
// first seen past due emits the breach only.
func TestDueAlerts(t *testing.T) {
	cal := chicagoCal(t)
	responded := func(c CaseClocks) CaseClocks { c, _ = c.Respond(at(cal, 26, 8, 5)); return c }
	c := responded(StartClocks(at(cal, 26, 8, 0), cal, gold))

	if _, alerts := c.DueAlerts(at(cal, 26, 13, 59)); len(alerts) != 0 {
		t.Fatalf("before 75 %%: %v", alerts)
	}
	c, alerts := c.DueAlerts(at(cal, 26, 14, 0))
	if len(alerts) != 1 || alerts[0].Kind != AlertWarning || alerts[0].Clock != ClockResolution {
		t.Fatalf("at 75 %%: %v, want one resolution warning", alerts)
	}
	if c, alerts = c.DueAlerts(at(cal, 26, 15, 0)); len(alerts) != 0 {
		t.Fatalf("warning repeated: %v", alerts)
	}
	if c, alerts = c.DueAlerts(at(cal, 26, 16, 0)); len(alerts) != 0 || c.State != SLARunning {
		t.Fatalf("at the due instant: %v state %s, want nothing yet", alerts, c.State)
	}
	c, alerts = c.DueAlerts(at(cal, 26, 16, 1))
	if len(alerts) != 1 || alerts[0].Kind != AlertBreach || c.State != SLABreached {
		t.Fatalf("past due: %v state %s, want one breach and BREACHED", alerts, c.State)
	}
	if _, alerts = c.DueAlerts(at(cal, 26, 17, 0)); len(alerts) != 0 {
		t.Fatalf("breach repeated: %v", alerts)
	}

	late := responded(StartClocks(at(cal, 26, 8, 0), cal, gold))
	late, alerts = late.DueAlerts(at(cal, 27, 9, 0))
	if len(alerts) != 1 || alerts[0].Kind != AlertBreach {
		t.Fatalf("first seen past due: %v, want the breach only", alerts)
	}
	if !reflect.DeepEqual(late.Alerts, []string{"RESOLUTION_BREACH", "RESOLUTION_WARNING"}) {
		t.Fatalf("recorded %v, want the breach and the suppressed warning", late.Alerts)
	}
}

func TestDueAlerts_ResponseClockUntilResponded(t *testing.T) {
	cal := chicagoCal(t)
	c := StartClocks(at(cal, 26, 8, 0), cal, gold)
	c, alerts := c.DueAlerts(at(cal, 26, 8, 45))
	if len(alerts) != 1 || alerts[0].Clock != ClockResponse || alerts[0].Kind != AlertWarning {
		t.Fatalf("45 of 60 minutes: %v, want a response warning", alerts)
	}
	c, alerts = c.DueAlerts(at(cal, 26, 9, 1))
	if len(alerts) != 1 || alerts[0].Clock != ClockResponse || alerts[0].Kind != AlertBreach || c.State != SLARunning {
		t.Fatalf("past response due: %v state %s, want a response breach with resolution still RUNNING", alerts, c.State)
	}
	// Responding late after the monitor's breach does not emit it again.
	if _, alerts = c.Respond(at(cal, 26, 9, 30)); len(alerts) != 0 {
		t.Fatalf("respond after monitor breach: %v", alerts)
	}
	paused, _ := mustMove(t, c, CaseWaitingCustomer, at(cal, 26, 9, 30), cal, false)
	if _, alerts = paused.DueAlerts(at(cal, 27, 9, 0)); len(alerts) != 0 {
		t.Fatalf("monitor alerted on a paused clock: %v", alerts)
	}
}

// Property (BR-45.2): after any pause, the resolution due lies exactly target
// unpaused business minutes after opening, and the warning instant never lies
// before 75 % of the target has elapsed.
func TestTransition_PropertyDueHonoursPauses(t *testing.T) {
	cal := chicagoCal(t)
	base := time.Date(2026, 10, 19, 0, 0, 0, 0, cal.Loc)
	rapid.Check(t, func(rt *rapid.T) {
		target := Target{Priority: 1, ResponseMinutes: 1, ResolutionMinutes: rapid.IntRange(1, 2400).Draw(rt, "target")}
		opened := base.Add(time.Duration(rapid.IntRange(0, 14*24*60).Draw(rt, "open")) * time.Minute)
		pauseAt := opened.Add(time.Duration(rapid.IntRange(0, 3*24*60).Draw(rt, "pauseAfter")) * time.Minute)
		resumeAt := pauseAt.Add(time.Duration(rapid.IntRange(0, 3*24*60).Draw(rt, "pauseFor")) * time.Minute)

		c := StartClocks(opened, cal, target)
		c, _, _ = c.Transition(CaseInProgress, opened, cal, target, false)
		c, _, _ = c.Transition(CaseWaitingCustomer, pauseAt, cal, target, false)
		c, _, _ = c.Transition(CaseInProgress, resumeAt, cal, target, false)
		if c.State == SLABreached {
			return // the clock ran out before the pause; nothing to move
		}
		if c.PausedMinutes != cal.Between(pauseAt, resumeAt) {
			rt.Fatalf("paused %d, want %d", c.PausedMinutes, cal.Between(pauseAt, resumeAt))
		}
		if got := cal.Between(opened, c.ResolutionDue) - c.PausedMinutes; got != target.ResolutionMinutes {
			rt.Fatalf("business minutes to due less pauses = %d, want %d", got, target.ResolutionMinutes)
		}
		if got := cal.Between(opened, c.ResolutionWarnAt) - c.PausedMinutes; got*100 < DefaultWarningPercent*target.ResolutionMinutes {
			rt.Fatalf("warning after %d of %d minutes, below %d %%", got, target.ResolutionMinutes, DefaultWarningPercent)
		}
	})
}

func TestPercent(t *testing.T) {
	tests := []struct {
		part, whole int64
		want        string // "" means nil
	}{
		{0, 0, ""},
		{1, -4, ""},
		{0, 5, "0.00"},
		{3, 4, "75.00"},
		{1, 3, "33.33"},
		{2, 3, "66.67"},
		{1, 8, "12.50"},
		{1, 800, "0.12"}, // 0.125 rounds half-even down
		{3, 800, "0.38"}, // 0.375 rounds half-even up
	}
	for _, tc := range tests {
		got := Percent(tc.part, tc.whole)
		switch {
		case tc.want == "" && got != nil:
			t.Errorf("Percent(%d, %d) = %s, want nil", tc.part, tc.whole, got)
		case tc.want != "" && (got == nil || got.StringFixed(2) != tc.want):
			t.Errorf("Percent(%d, %d) = %v, want %s", tc.part, tc.whole, got, tc.want)
		}
	}
	if p := Percent(2, 3); !p.Equal(decimal.RequireFromString("66.67")) {
		t.Errorf("Percent is not decimal-exact: %s", p)
	}
}

func TestCanFollowUp(t *testing.T) {
	for s, want := range map[WOStatus]bool{WOCompleted: true, WOPosted: true, WOInProgress: false, WOOnHold: false, WOCanceled: false, WOUnscheduled: false} {
		if got := CanFollowUp(s); got != want {
			t.Errorf("CanFollowUp(%s) = %v, want %v", s, got, want)
		}
	}
}
