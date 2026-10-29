package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

func newPlan(t *testing.T, packs ...Pack) *Plan {
	t.Helper()
	if len(packs) == 0 {
		packs = []Pack{PackOpenItems, PackGLBalances}
	}
	p, err := GeneratePlan(PlanInput{PlanID: "plan-1", T0: t0, Packs: packs, Entities: []string{"USMF"}})
	if err != nil {
		t.Fatalf("GeneratePlan: %v", err)
	}
	return &p
}

func TestGeneratePlan_TimelineOrderAndAnchors(t *testing.T) {
	p := newPlan(t)
	for i := 1; i < len(p.Steps); i++ {
		if p.Steps[i].OffsetMin < p.Steps[i-1].OffsetMin {
			t.Fatalf("steps not sorted by offset at seq %d", p.Steps[i].Seq)
		}
	}
	for i, s := range p.Steps {
		if s.Seq != i+1 {
			t.Fatalf("seq %d at index %d", s.Seq, i)
		}
	}
	open, ok := p.StepByName(StepOpenAccess)
	if !ok || open.OffsetMin != 30*MinutesPerHour {
		t.Fatalf("open user access must be at T+30h, got %+v", open)
	}
	if open.OffsetMin >= p.AbortOffsetMin {
		t.Fatalf("open user access must come before the T+36h abort deadline")
	}
	if p.AbortOffsetMin != 36*MinutesPerHour {
		t.Fatalf("default abort offset is 36h, got %d", p.AbortOffsetMin)
	}
	// AC-49.6: T-0 through open user access inside 48 h.
	if open.OffsetMin > 48*MinutesPerHour {
		t.Fatalf("cutover window exceeds 48 h")
	}
}

func TestGeneratePlan_PackStepsAndRehearsalDurations(t *testing.T) {
	p, err := GeneratePlan(PlanInput{
		PlanID: "p", T0: t0, Packs: []Pack{PackFixedAssets}, Entities: []string{"USMF"},
		Rehearsal: DurationOverrides{"Load pack fixed_assets": 77, StepOpenAccess: 15},
	})
	if err != nil {
		t.Fatal(err)
	}
	pack, ok := p.StepByName("Load pack fixed_assets")
	if !ok || pack.PlannedDurationMin != 77 || pack.Pack != "fixed_assets" || pack.OffsetMin != 4*MinutesPerHour {
		t.Fatalf("pack step %+v", pack)
	}
	open, _ := p.StepByName(StepOpenAccess)
	if open.PlannedDurationMin != 15 {
		t.Fatalf("rehearsal override not applied to open access: %d", open.PlannedDurationMin)
	}
}

func TestGeneratePlan_Validation(t *testing.T) {
	base := PlanInput{PlanID: "p", T0: t0, Packs: []Pack{PackOpenItems}, Entities: []string{"USMF"}}
	cases := []struct {
		name string
		mut  func(*PlanInput)
	}{
		{"no t0", func(i *PlanInput) { i.T0 = time.Time{} }},
		{"no packs", func(i *PlanInput) { i.Packs = nil }},
		{"no entities", func(i *PlanInput) { i.Entities = nil }},
		{"abort below 12h", func(i *PlanInput) { i.AbortOffsetH = 6 }},
		{"abort above 72h", func(i *PlanInput) { i.AbortOffsetH = 80 }},
		{"pack twice", func(i *PlanInput) { i.Packs = []Pack{PackOpenItems, PackOpenItems} }},
		{"entity twice", func(i *PlanInput) { i.Entities = []string{"USMF", "USMF"} }},
		{"blank entity", func(i *PlanInput) { i.Entities = []string{" "} }},
		{"negative rehearsal duration", func(i *PlanInput) { i.Rehearsal = DurationOverrides{StepOpenAccess: -1} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := base
			c.mut(&in)
			if _, err := GeneratePlan(in); !errors.Is(err, ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}

func TestComplete_GoNoGoOrderingAndOpenAccessGate(t *testing.T) {
	p := newPlan(t)
	open, _ := p.StepByName(StepOpenAccess)
	if err := p.Complete(open.Seq, 0); err == nil || !strings.Contains(err.Error(), "CUT-1601") {
		t.Fatalf("open access must wait for go/no-go items, got %v", err)
	}
	reconcile, _ := p.StepByName(StepReconcile)
	goNoGo, _ := p.StepByName(StepGoNoGo)
	// Completing the meeting before the reconciliation item is out of sequence.
	if err := p.Complete(goNoGo.Seq, 10); err == nil || !strings.Contains(err.Error(), "CUT-1601") {
		t.Fatalf("go/no-go out of sequence must be rejected, got %v", err)
	}
	if err := p.Complete(reconcile.Seq, 200); err != nil {
		t.Fatal(err)
	}
	if err := p.Complete(goNoGo.Seq, 10); err != nil {
		t.Fatal(err)
	}
	if err := p.Complete(open.Seq, 5); err != nil {
		t.Fatalf("open access should start once go/no-go items are done: %v", err)
	}
	if err := p.Complete(open.Seq, 5); err == nil {
		t.Fatal("completing a step twice must fail")
	}
	if got := p.Steps[open.Seq-1].ActualDurationMin; got != 5 {
		t.Fatalf("actual duration not recorded: %d", got)
	}
}

func TestComplete_UnknownStep(t *testing.T) {
	p := newPlan(t)
	if err := p.Complete(9999, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown step must be ErrInvalid, got %v", err)
	}
}

func TestAbortDue_BR499(t *testing.T) {
	p := newPlan(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	rec, _ := p.StepByName(StepReconcile)
	must(p.Complete(rec.Seq, 1))
	// 35 h: one hour before the deadline with a go/no-go item open -> warning only.
	mustRB, warn := p.AbortDue(35 * MinutesPerHour)
	if mustRB || !strings.Contains(warn, "CUT-1604") {
		t.Fatalf("at T+35h expect warning, got rollback=%v warn=%q", mustRB, warn)
	}
	// 36 h with the meeting still open -> rollback required.
	if mustRB, _ := p.AbortDue(36 * MinutesPerHour); !mustRB {
		t.Fatal("at T+36h with open go/no-go items rollback must be due")
	}
	gn, _ := p.StepByName(StepGoNoGo)
	must(p.Complete(gn.Seq, 1))
	if mustRB, _ := p.AbortDue(36 * MinutesPerHour); mustRB {
		t.Fatal("no rollback when all go/no-go items are complete")
	}
}

func TestRollback_RequiresTwoDistinctApprovers(t *testing.T) {
	cases := []struct {
		name      string
		approvers []string
		ok        bool
	}{
		{"none", nil, false},
		{"one", []string{"alice"}, false},
		{"same person twice", []string{"alice", "alice"}, false},
		{"blank counts as absent", []string{"alice", ""}, false},
		{"two distinct", []string{"alice", "bob"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newPlan(t)
			err := p.Rollback(c.approvers...)
			if (err == nil) != c.ok {
				t.Fatalf("err=%v want ok=%v", err, c.ok)
			}
			if err == nil && !p.RolledBack {
				t.Fatal("RolledBack not set")
			}
		})
	}
}

func TestGeneratePlan_AbortStepFollowsAbortOffset(t *testing.T) {
	for _, h := range []int{0, 12, 48, 72} {
		p, err := GeneratePlan(PlanInput{PlanID: "p", T0: t0, Packs: []Pack{PackOpenItems}, Entities: []string{"USMF"}, AbortOffsetH: h})
		if err != nil {
			t.Fatal(err)
		}
		want := h
		if want == 0 {
			want = 36
		}
		abort, ok := p.StepByName(StepAbort)
		if !ok || abort.OffsetMin != want*MinutesPerHour || p.AbortOffsetMin != abort.OffsetMin {
			t.Fatalf("abort %dh: step %+v, plan offset %d", h, abort, p.AbortOffsetMin)
		}
		for i := 1; i < len(p.Steps); i++ {
			if p.Steps[i].OffsetMin < p.Steps[i-1].OffsetMin {
				t.Fatalf("abort %dh: steps not ordered by offset", h)
			}
		}
	}
}

func TestPlanVersionAndRolledBackState(t *testing.T) {
	p := newPlan(t)
	if p.Version != 1 {
		t.Fatalf("new plan version %d", p.Version)
	}
	rec, _ := p.StepByName(StepReconcile)
	if err := p.Complete(rec.Seq, -1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative duration: %v", err)
	}
	if p.Version != 1 {
		t.Fatal("a rejected completion must not bump the version")
	}
	if err := p.Complete(rec.Seq, 10); err != nil || p.Version != 2 {
		t.Fatalf("complete: %v, version %d", err, p.Version)
	}
	if err := p.Rollback(" alice ", "bob", "alice"); err != nil {
		t.Fatal(err)
	}
	if p.Version != 3 || len(p.Approvers) != 2 || p.Approvers[0] != "alice" {
		t.Fatalf("rollback version %d approvers %q", p.Version, p.Approvers)
	}
	if err := p.Rollback("alice", "bob"); !errors.Is(err, ErrTransition) {
		t.Fatalf("second rollback: %v, want ErrTransition", err)
	}
	gn, _ := p.StepByName(StepGoNoGo)
	if err := p.Complete(gn.Seq, 1); !errors.Is(err, ErrTransition) {
		t.Fatalf("complete after rollback: %v, want ErrTransition", err)
	}
}
