package domain

import (
	"errors"
	"strconv"
	"testing"
	"time"
)

var slice4T0 = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

func slice4Plan(t *testing.T, abortH int) *Plan {
	t.Helper()
	p, err := GeneratePlan(PlanInput{PlanID: "plan-4", T0: slice4T0, Packs: []Pack{PackOpenItems}, Entities: []string{"USMF"}, AbortOffsetH: abortH})
	if err != nil {
		t.Fatal(err)
	}
	return &p
}

func mustStep(t *testing.T, p *Plan, name string) *Step {
	t.Helper()
	s, ok := p.StepByName(name)
	if !ok {
		t.Fatalf("plan has no step %q", name)
	}
	return s
}

// FR-49.9: the rollback plan starts with the decision at the abort deadline, then stops Passage
// access, restores legacy write access, resumes the coexistence adapters (AC-49.7), exports the
// postings since T-0 and re-keys them, each step planned to start when the one before ends.
func TestRollbackPlan_StartsAtTheAbortDeadlineInExecutionOrder(t *testing.T) {
	for _, abortH := range []int{12, 36, 72} {
		p := slice4Plan(t, abortH)
		steps := p.RollbackPlan()
		want := []string{RollbackDecision, RollbackStopAccess, RollbackRestoreLegacy, RollbackResumeAdapters, RollbackExport, RollbackRekey}
		if len(steps) != len(want) {
			t.Fatalf("T+%dh: %d rollback steps, want %d", abortH, len(steps), len(want))
		}
		at := abortH * MinutesPerHour
		for i, s := range steps {
			if s.Seq != i+1 || s.Name != want[i] || s.Owner == "" || s.PlannedDurationMin <= 0 {
				t.Fatalf("T+%dh step %d = %+v, want %q", abortH, i, s, want[i])
			}
			if s.OffsetMin != at {
				t.Fatalf("T+%dh step %q at %d min, want %d (previous step's planned end)", abortH, s.Name, s.OffsetMin, at)
			}
			if s.DecisionPoint != (i == 0) {
				t.Fatalf("T+%dh: only the first step is the decision point, step %d = %+v", abortH, i, s)
			}
			at += s.PlannedDurationMin
		}
	}
	// The plan is derived on every call: a caller changing one copy does not change the next.
	p := slice4Plan(t, 36)
	p.RollbackPlan()[0].Name = "changed"
	if p.RollbackPlan()[0].Name != RollbackDecision {
		t.Fatal("RollbackPlan must not share its steps between calls")
	}
}

func TestPlanStart_GateOnceAndNotAfterDone(t *testing.T) {
	p := slice4Plan(t, 36)
	first := p.Steps[0].Seq
	at := slice4T0.Add(-14 * 24 * time.Hour)
	if err := p.Start(first, time.Time{}, "owner"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero start time: %v", err)
	}
	if err := p.Start(999, at, "owner"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown step: %v", err)
	}
	if err := p.Start(first, at, "owner"); err != nil {
		t.Fatal(err)
	}
	if s := p.Steps[0]; s.StartedAt == nil || !s.StartedAt.Equal(at) || s.StartedBy != "owner" || !s.Running() || p.Version != 2 {
		t.Fatalf("started step %+v, plan version %d", s, p.Version)
	}
	if err := p.Start(first, at.Add(time.Minute), "owner"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a step starts once: %v", err)
	}
	if err := p.Complete(first, 200); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(first, at, "owner"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a done step cannot start: %v", err)
	}

	// CUT-1601 applies to starting: the go/no-go meeting waits for reconciliation, open access
	// waits for every go/no-go item.
	meeting := mustStep(t, p, StepGoNoGo)
	open := mustStep(t, p, StepOpenAccess)
	for _, seq := range []int{meeting.Seq, open.Seq} {
		if err := p.Start(seq, at, "owner"); !errors.Is(err, ErrInvalid) || meeting.StartedAt != nil || open.StartedAt != nil {
			t.Fatalf("step %d started with go/no-go items open: %v", seq, err)
		}
	}
	if err := p.Complete(mustStep(t, p, StepReconcile).Seq, 240); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(meeting.Seq, at, "owner"); err != nil {
		t.Fatalf("meeting after reconciliation: %v", err)
	}
	if err := p.Start(open.Seq, at, "owner"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("open access while the meeting is open: %v", err)
	}

	if err := p.Rollback("alice", "bob"); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(p.Steps[len(p.Steps)-1].Seq, at, "owner"); !errors.Is(err, ErrTransition) {
		t.Fatalf("a rolled-back plan's steps cannot start: %v", err)
	}
}

func TestMeasuredDuration_FromStartInWholeMinutes(t *testing.T) {
	p := slice4Plan(t, 36)
	seq := p.Steps[0].Seq
	if _, err := p.MeasuredDuration(seq, slice4T0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a step never started has no measurement: %v", err)
	}
	start := slice4T0.Add(-14 * 24 * time.Hour)
	if err := p.Start(seq, start, "owner"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		after time.Duration
		want  int
	}{{0, 0}, {59 * time.Second, 0}, {90 * time.Second, 1}, {4*time.Hour + 30*time.Second, 240}, {-time.Hour, 0}} {
		got, err := p.MeasuredDuration(seq, start.Add(c.after))
		if err != nil || got != c.want {
			t.Fatalf("after %s: %d %v, want %d", c.after, got, err, c.want)
		}
	}
}

// 9.49.6: the runbook shows elapsed against planned, amber at 110 % and red at 125 %, and a
// running step past amber carries CUT-1602.
func TestStepTiming_RunningDoneAndNotStarted(t *testing.T) {
	start := slice4T0
	s := Step{Name: "Load pack open_items", PlannedDurationMin: 100, Status: StepPending}
	if e, tone, warn := s.Timing(start, 110, 125); e != 0 || tone != "" || warn != "" {
		t.Fatalf("a step not started has no timing: %d %q %q", e, tone, warn)
	}
	s.StartedAt = &start
	cases := []struct {
		min  int
		tone string
		warn bool
	}{{0, ToneOK, false}, {109, ToneOK, false}, {110, ToneAmber, true}, {124, ToneAmber, true}, {125, ToneRed, true}, {300, ToneRed, true}}
	for _, c := range cases {
		e, tone, warn := s.Timing(start.Add(time.Duration(c.min)*time.Minute), 110, 125)
		if e != c.min || tone != c.tone || (warn != "") != c.warn {
			t.Fatalf("running %d min: %d %q %q", c.min, e, tone, warn)
		}
		if c.warn && warn != `CUT-1602 Step "Load pack open_items" at `+strconv.Itoa(c.min)+`% of planned duration.` {
			t.Fatalf("warning text %q", warn)
		}
	}
	s.Status, s.ActualDurationMin = StepDone, 126
	if e, tone, warn := s.Timing(start.Add(time.Minute), 110, 125); e != 126 || tone != ToneRed || warn != "" {
		t.Fatalf("a done step reports its recorded duration without a warning: %d %q %q", e, tone, warn)
	}
	zero := Step{Name: StepAbort, Status: StepPending, StartedAt: &start}
	if _, tone, warn := zero.Timing(start.Add(time.Hour), 110, 125); tone != ToneOK || warn != "" {
		t.Fatalf("a step planned at 0 min never overruns: %q %q", tone, warn)
	}
}

func TestPackCatalog_NinePacksAllSystemsAllAvailable(t *testing.T) {
	cat := PackCatalog()
	if len(cat) != len(Packs) {
		t.Fatalf("catalog has %d packs, want %d", len(cat), len(Packs))
	}
	var available []Pack
	for i, e := range cat {
		if e.Pack != Packs[i] {
			t.Fatalf("entry %d is %s, want FR-49.1 order %s", i, e.Pack, Packs[i])
		}
		if len(e.Systems) != 4 || e.ControlTotals == "" {
			t.Fatalf("pack %s: systems %v, totals %q", e.Pack, e.Systems, e.ControlTotals)
		}
		if e.Available != PackAvailable(e.Pack) {
			t.Fatalf("pack %s availability disagrees with PackAvailable", e.Pack)
		}
		if e.Available {
			available = append(available, e.Pack)
		}
	}
	// Slice 17 makes wip_prod, fixed_assets and payroll_ytd available (staged only).
	if len(available) != len(Packs) {
		t.Fatalf("available packs %v, want all nine", available)
	}
	cat[0].Systems[0] = "X"
	if PackCatalog()[0].Systems[0] != SourceAX2012 {
		t.Fatal("catalog entries must not share their systems slice")
	}
}
