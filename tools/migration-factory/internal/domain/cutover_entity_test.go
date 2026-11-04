package domain

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// fixtureStep is one line of the `fx-cutover-plan` golden file.
type fixtureStep struct {
	Seq    int    `json:"seq"`
	Offset int    `json:"offsetMin"`
	Name   string `json:"name"`
	Pack   string `json:"pack,omitempty"`
	Entity string `json:"entity,omitempty"`
	GoNoGo bool   `json:"goNoGo,omitempty"`
}

func fixturePlan(t *testing.T) Plan {
	t.Helper()
	p, err := GeneratePlan(PlanInput{PlanID: "fx-cutover-plan", T0: t0, Packs: Packs, Entities: []string{"USMF", "DEMF", "JPMF"}, ExpandByEntity: true})
	if err != nil {
		t.Fatalf("GeneratePlan: %v", err)
	}
	return p
}

// TestFxCutoverPlan_Has84StepsPerEntity pins the fixture of D5a: 3 entities and the 9 packs expand
// to 84 steps, compared line by line with testdata/fx-cutover-plan.json (UPDATE_FIXTURE=1 rewrites it).
func TestFxCutoverPlan_Has84StepsPerEntity(t *testing.T) {
	p := fixturePlan(t)
	if len(p.Steps) != 84 {
		t.Fatalf("steps = %d, want 84", len(p.Steps))
	}
	got := make([]fixtureStep, len(p.Steps))
	for i, s := range p.Steps {
		got[i] = fixtureStep{Seq: s.Seq, Offset: s.OffsetMin, Name: s.Name, Pack: s.Pack, Entity: s.Entity, GoNoGo: s.GoNoGo}
	}
	const path = "testdata/fx-cutover-plan.json"
	if os.Getenv("UPDATE_FIXTURE") == "1" {
		b, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want []fixtureStep
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(got) {
		t.Fatalf("fixture has %d steps, plan %d", len(want), len(got))
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("step %d: plan %+v, fixture %+v", i+1, got[i], want[i])
		}
	}
}

func TestExpandByEntity_StepsPerEntityAndGates(t *testing.T) {
	p := fixturePlan(t)
	names := map[string]bool{}
	perEntity := map[string]int{}
	goNoGo := 0
	for i, s := range p.Steps {
		if names[s.Name] {
			t.Fatalf("duplicate step name %q", s.Name)
		}
		names[s.Name] = true
		if s.Seq != i+1 || (i > 0 && s.OffsetMin < p.Steps[i-1].OffsetMin) {
			t.Fatalf("step %d is out of order: %+v", i+1, s)
		}
		if s.Entity != "" {
			perEntity[s.Entity]++
			if s.GoNoGo {
				t.Fatalf("per-entity step %q must not be a go/no-go step", s.Name)
			}
			if !strings.Contains(s.Name, s.Entity) {
				t.Fatalf("step %q does not name its entity %q", s.Name, s.Entity)
			}
		}
		if s.GoNoGo {
			goNoGo++
		}
	}
	for _, e := range []string{"USMF", "DEMF", "JPMF"} {
		if perEntity[e] != 23 {
			t.Fatalf("entity %s has %d steps, want 23 (freeze, extract, 9 loads, 9 reconciles, reconcile, sign-off, hypercare)", e, perEntity[e])
		}
	}
	if goNoGo != 2 || len(p.OpenGoNoGo()) != 2 {
		t.Fatalf("go/no-go steps = %d, want the 2 plan-wide ones", goNoGo)
	}
	// The plan-wide pack load steps are replaced, not duplicated.
	if _, ok := p.StepByName("Load pack open_items"); ok {
		t.Fatal("an expanded plan has no plan-wide pack load step")
	}
	if s, ok := p.StepByName("Load pack gl_balances for DEMF"); !ok || s.OffsetMin != 4*MinutesPerHour || s.Pack != "gl_balances" {
		t.Fatalf("balance pack load: %+v %v", s, ok)
	}
	if s, ok := p.StepByName("Reconcile pack gl_balances for DEMF"); !ok || s.OffsetMin != 12*MinutesPerHour {
		t.Fatalf("balance pack reconcile: %+v %v", s, ok)
	}
}

func TestExpandByEntity_ScalesWithEntitiesAndPacksAndCarriesRehearsal(t *testing.T) {
	for _, tc := range []struct{ entities, packs int }{{1, 1}, {2, 3}, {5, 9}} {
		ents := make([]string, tc.entities)
		for i := range ents {
			ents[i] = fmt.Sprintf("E%d", i)
		}
		p, err := GeneratePlan(PlanInput{PlanID: "p", T0: t0, Packs: Packs[:tc.packs], Entities: ents, ExpandByEntity: true})
		if err != nil {
			t.Fatal(err)
		}
		if want := 15 + tc.entities*tc.packs*2 + tc.entities*5; len(p.Steps) != want {
			t.Fatalf("%d entities, %d packs: %d steps, want %d", tc.entities, tc.packs, len(p.Steps), want)
		}
	}
	p, err := GeneratePlan(PlanInput{PlanID: "p", T0: t0, Packs: []Pack{PackOpenItems}, Entities: []string{"USMF"}, ExpandByEntity: true,
		Rehearsal: DurationOverrides{"Load pack open_items for USMF": 95}})
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := p.StepByName("Load pack open_items for USMF"); s.PlannedDurationMin != 95 || p.RehearsalCarried != 1 {
		t.Fatalf("rehearsal duration not carried: %+v carried %d", s, p.RehearsalCarried)
	}
	// Without the flag nothing changes.
	q := newPlan(t)
	for _, s := range q.Steps {
		if s.Entity != "" {
			t.Fatalf("plain plan has a per-entity step %q", s.Name)
		}
	}
}

// completeByName completes the named step, failing the test when it is refused.
func completeByName(t *testing.T, p *Plan, name string) {
	t.Helper()
	s, ok := p.StepByName(name)
	if !ok {
		t.Fatalf("no step %q", name)
	}
	if err := p.Complete(s.Seq, 10); err != nil {
		t.Fatalf("complete %q: %v", name, err)
	}
}

// refused asserts the named step can neither start nor complete, naming CUT-1609 and the step it waits for.
func refused(t *testing.T, p *Plan, name, waitsFor string) {
	t.Helper()
	s, _ := p.StepByName(name)
	for _, err := range []error{p.Start(s.Seq, t0, "engineer"), p.Complete(s.Seq, 10)} {
		if err == nil || !strings.Contains(err.Error(), "CUT-1609") || !strings.Contains(err.Error(), waitsFor) {
			t.Fatalf("%q: err = %v, want CUT-1609 waiting for %q", name, err, waitsFor)
		}
	}
}

func TestExpandByEntity_StepsWaitForTheirOwnEntityChain(t *testing.T) {
	p, err := GeneratePlan(PlanInput{PlanID: "p", T0: t0, Packs: []Pack{PackGLBalances, PackOpenItems}, Entities: []string{"USMF", "DEMF"}, ExpandByEntity: true})
	if err != nil {
		t.Fatal(err)
	}
	refused(t, &p, "Extract entity USMF", "Freeze legacy company USMF")
	refused(t, &p, "Load pack gl_balances for USMF", "Freeze legacy company USMF")
	completeByName(t, &p, "Freeze legacy company USMF")
	refused(t, &p, "Load pack gl_balances for USMF", "Extract entity USMF")
	completeByName(t, &p, "Extract entity USMF")
	// Another entity's freeze and extract do not release USMF's load, and USMF's do not release DEMF's.
	refused(t, &p, "Load pack gl_balances for DEMF", "Freeze legacy company DEMF")
	refused(t, &p, "Reconcile pack gl_balances for USMF", "Load pack gl_balances for USMF")
	completeByName(t, &p, "Load pack gl_balances for USMF")
	completeByName(t, &p, "Reconcile pack gl_balances for USMF")
	refused(t, &p, "Reconcile entity USMF", "Reconcile pack open_items for USMF")
	completeByName(t, &p, "Load pack open_items for USMF")
	completeByName(t, &p, "Reconcile pack open_items for USMF")
	refused(t, &p, "Controller sign-off USMF", "Reconcile entity USMF")
	completeByName(t, &p, "Reconcile entity USMF")
	refused(t, &p, "Hypercare check USMF", "Controller sign-off USMF")
	completeByName(t, &p, "Controller sign-off USMF")
	completeByName(t, &p, "Hypercare check USMF")
	// The plan-wide go/no-go steps wait for every entity's sign-off, so user access stays closed and
	// the abort deadline still sees open go/no-go items.
	refused(t, &p, StepReconcile, "Controller sign-off DEMF")
	if must, _ := p.AbortDue(p.AbortOffsetMin); !must {
		t.Fatal("an entity not signed off must leave the go/no-go items open at the abort deadline")
	}
	for _, name := range []string{"Freeze legacy company DEMF", "Extract entity DEMF", "Load pack gl_balances for DEMF", "Reconcile pack gl_balances for DEMF",
		"Load pack open_items for DEMF", "Reconcile pack open_items for DEMF", "Reconcile entity DEMF", "Controller sign-off DEMF", StepReconcile, StepGoNoGo, StepOpenAccess} {
		completeByName(t, &p, name)
	}
}

func TestPlainPlan_KeepsItsGates(t *testing.T) {
	p := newPlan(t)
	// A plan-wide runbook has no per-entity prerequisites: its pack loads and the reconciliation
	// go/no-go step complete in any order the go/no-go sequencing allows.
	completeByName(t, p, "Load pack open_items")
	completeByName(t, p, StepReconcile)
}

func TestExpandByEntity_CapsTheEntities(t *testing.T) {
	ents := make([]string, MaxExpandedEntities+1)
	for i := range ents {
		ents[i] = fmt.Sprintf("E%03d", i)
	}
	if _, err := GeneratePlan(PlanInput{PlanID: "p", T0: t0, Packs: Packs, Entities: ents, ExpandByEntity: true}); err == nil || !strings.Contains(err.Error(), "at most 50") {
		t.Fatalf("err = %v, want the entity cap", err)
	}
	if _, err := GeneratePlan(PlanInput{PlanID: "p", T0: t0, Packs: Packs, Entities: ents}); err != nil {
		t.Fatalf("a plan-wide runbook is not capped: %v", err)
	}
}
