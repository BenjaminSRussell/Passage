package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var decisionT0 = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

func decisionPlan() Plan {
	return Plan{PlanID: "p1", T0: decisionT0, Packs: []Pack{PackOpenItems, PackGLBalances}, Entities: []string{"USMF", "DEMF"}, Version: 1}
}

// allPackFacts are production runs: started after the legacy freeze (T-6h).
func allPackFacts(status RunStatus) []PackFact {
	var out []PackFact
	for _, pk := range []Pack{PackOpenItems, PackGLBalances} {
		for _, e := range []string{"USMF", "DEMF"} {
			out = append(out, PackFact{Pack: pk, Company: e, RunID: string(pk) + "-" + e, Status: status, CreatedAt: decisionT0.Add(2 * time.Hour)})
		}
	}
	return out
}

func signedOff() []ParallelFact {
	return []ParallelFact{{ParallelRunID: "pr", Period: "2026-12", SourceSystem: "AX2012", State: "SIGNED_OFF"}}
}

func allAttested() []Attestation {
	var out []Attestation
	for _, it := range Checklist {
		if it.Source == ItemSourceAttested {
			out = append(out, Attestation{Code: it.Code, Confirmed: true, Reference: "https://evidence/" + it.Code})
		}
	}
	return out
}

// glWaived waives the staged gl_balances pack, which decisionPlan includes.
func glWaived() []Waiver {
	return []Waiver{{Pack: "gl_balances", Reason: "opening balances posted manually in the target"}}
}

func statuses(items []ItemResult) map[string]string {
	m := map[string]string{}
	for _, it := range items {
		m[it.Code] = it.Status
	}
	return m
}

func TestChecklist_HasFourteenItemsTwoEvaluatedByTheFactory(t *testing.T) {
	system := 0
	for _, it := range Checklist {
		if it.Source == ItemSourceSystem {
			system++
		}
	}
	if len(Checklist) != 14 || system != 2 {
		t.Fatalf("checklist has %d items, %d system", len(Checklist), system)
	}
}

func TestEvaluateChecklist_PacksNeedEveryPackAndEntityReconciled(t *testing.T) {
	facts := ReadinessFacts{Plan: decisionPlan(), Packs: allPackFacts(StatusReconciled), Parallel: signedOff()}
	items, err := EvaluateChecklist(facts, allAttested(), glWaived())
	if err != nil {
		t.Fatal(err)
	}
	if open := OpenItems(items); len(open) != 0 {
		t.Fatalf("open items %v", open)
	}
	if len(items[0].References) != 4 {
		t.Fatalf("references %v", items[0].References)
	}

	// One company missing and one mismatching.
	facts.Packs = facts.Packs[1:]
	facts.Packs[0].Status = StatusLoaded
	items, err = EvaluateChecklist(facts, allAttested(), glWaived())
	if err != nil {
		t.Fatal(err)
	}
	got := statuses(items)[ItemPacksReconciled]
	if got != ItemOpen || !strings.Contains(items[0].Detail, "no run for open_items/USMF") || !strings.Contains(items[0].Detail, "is LOADED") {
		t.Fatalf("packs item %s %q", got, items[0].Detail)
	}
}

// A RECONCILED run from a dry run or rehearsal before the legacy freeze does not count.
func TestEvaluateChecklist_PackRunBeforeTheFreezeIsStale(t *testing.T) {
	facts := ReadinessFacts{Plan: decisionPlan(), Packs: allPackFacts(StatusReconciled), Parallel: signedOff()}
	facts.Packs[3].CreatedAt = decisionT0.Add(-7 * 24 * time.Hour)
	items, _ := EvaluateChecklist(facts, nil, glWaived())
	if items[0].Status != ItemOpen || !strings.Contains(items[0].Detail, "before the legacy freeze 2026-12-31T18:00:00Z for gl_balances/DEMF") {
		t.Fatalf("stale run: %s %q", items[0].Status, items[0].Detail)
	}
	facts.Packs[3].CreatedAt = decisionT0.Add(LegacyFreezeOffsetMin * time.Minute)
	if items, _ = EvaluateChecklist(facts, nil, glWaived()); items[0].Status != ItemMet {
		t.Fatalf("run at the freeze: %s %q", items[0].Status, items[0].Detail)
	}
}

func TestPlan_ParallelPeriodIsTheMonthBeforeT0(t *testing.T) {
	for t0, want := range map[time.Time]string{
		decisionT0: "2026-12",
		time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC): "2027-02",
		time.Date(2027, 7, 15, 3, 0, 0, 0, time.UTC): "2027-06",
	} {
		if got := (Plan{T0: t0}).ParallelPeriod(); got != want {
			t.Errorf("T0 %s: %s, want %s", t0, got, want)
		}
	}
}

func TestEvaluateChecklist_ParallelRun(t *testing.T) {
	base := ReadinessFacts{Plan: decisionPlan(), Packs: allPackFacts(StatusReconciled)}
	for _, c := range []struct {
		name string
		p    []ParallelFact
		want string
		text string
	}{
		{"none for the period", nil, ItemOpen, "No parallel run exists for 2026-12"},
		{"in review", []ParallelFact{{ParallelRunID: "x", Period: "2026-12", SourceSystem: "AX2012", State: "REVIEW", UnexplainedCount: 3}}, ItemOpen, "AX2012 is REVIEW with 3 unexplained"},
		{"signed off", signedOff(), ItemMet, "SIGNED_OFF"},
		{"one of two legacy systems open", append(signedOff(), ParallelFact{ParallelRunID: "y", Period: "2026-12", SourceSystem: "SAPECC", State: "DIFFING"}), ItemOpen, "SAPECC is DIFFING"},
	} {
		f := base
		f.Parallel = c.p
		items, err := EvaluateChecklist(f, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := items[1]; got.Status != c.want || !strings.Contains(got.Detail, c.text) || len(got.References) != len(c.p) {
			t.Errorf("%s: %+v, want %s containing %q", c.name, got, c.want, c.text)
		}
	}
}

func TestEvaluateChecklist_RejectsBadAttestations(t *testing.T) {
	f := ReadinessFacts{Plan: decisionPlan()}
	for name, att := range map[string][]Attestation{
		"unknown code":        {{Code: "nope", Confirmed: true, Reference: "r"}},
		"system item":         {{Code: ItemPacksReconciled, Confirmed: true, Reference: "r"}},
		"duplicate":           {{Code: "load_test", Confirmed: true, Reference: "a"}, {Code: "load_test", Confirmed: false}},
		"confirmed no ref":    {{Code: "load_test", Confirmed: true, Reference: "  "}},
		"reference too large": {{Code: "load_test", Confirmed: true, Reference: strings.Repeat("x", 501)}},
	} {
		if _, err := EvaluateChecklist(f, att, nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	// Unconfirmed attestations are accepted and stay UNCONFIRMED.
	items, err := EvaluateChecklist(f, []Attestation{{Code: "load_test", Confirmed: false}}, nil)
	if err != nil || statuses(items)["load_test"] != ItemUnconfirmed {
		t.Fatalf("%v %v", err, statuses(items))
	}
}

func TestPrepareDecision_ApproversAreAuthenticatedUsers(t *testing.T) {
	pendingBy := func(who string, att []Attestation) ReadinessFacts {
		return ReadinessFacts{Plan: decisionPlan(), Pending: []GoApproval{{Round: 1, Approver: who, Attestations: ConfirmedAttestations(att)}}}
	}
	none := ReadinessFacts{Plan: decisionPlan()}
	cases := []struct {
		name     string
		in       DecisionInput
		facts    ReadinessFacts
		want     error
		text     string
		complete bool
		appr     []string
	}{
		{"first GO is an approval by the caller", DecisionInput{Decision: DecisionGo, By: "alice", Attestations: allAttested()}, none, nil, "", false, []string{"alice"}},
		{"second GO by another user records it", DecisionInput{Decision: DecisionGo, By: " bob "}, pendingBy("alice", allAttested()), nil, "", true, []string{"alice", "bob"}},
		{"second GO with the same attestations", DecisionInput{Decision: DecisionGo, By: "bob", Attestations: allAttested()}, pendingBy("alice", allAttested()), nil, "", true, []string{"alice", "bob"}},
		{"second GO with other attestations", DecisionInput{Decision: DecisionGo, By: "bob", Attestations: allAttested()[1:]}, pendingBy("alice", allAttested()), ErrInvalid, "CUT-1605", false, nil},
		{"the first approver cannot approve twice", DecisionInput{Decision: DecisionGo, By: "alice"}, pendingBy("alice", allAttested()), ErrTransition, "CUT-1605", false, nil},
		{"no anonymous decision", DecisionInput{Decision: DecisionNoGo, By: " ", Reason: "x"}, none, ErrInvalid, "identified person", false, nil},
		{"no service principal", DecisionInput{Decision: DecisionGo, By: "service:integration-hub"}, none, ErrInvalid, "not a service", false, nil},
		{"no-go is approved by the caller", DecisionInput{Decision: DecisionNoGo, By: "carol", Reason: " UAT open "}, pendingBy("alice", nil), nil, "", true, []string{"carol"}},
		{"no-go needs a reason", DecisionInput{Decision: DecisionNoGo, By: "carol", Reason: " "}, none, ErrInvalid, "reason", false, nil},
		{"unknown decision", DecisionInput{Decision: "MAYBE", By: "carol"}, none, ErrInvalid, "GO or NO_GO", false, nil},
		{"after a GO", DecisionInput{Decision: DecisionNoGo, By: "carol", Reason: "x"}, ReadinessFacts{Decisions: []Decision{{Decision: DecisionGo}}}, ErrTransition, "already recorded", false, nil},
		{"go after a no-go", DecisionInput{Decision: DecisionGo, By: "alice"}, ReadinessFacts{Decisions: []Decision{{Decision: DecisionNoGo}}}, nil, "", false, []string{"alice"}},
	}
	for _, c := range cases {
		plan := decisionPlan()
		step, err := plan.PrepareDecision(c.in, c.facts)
		if c.want != nil {
			if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.text) {
				t.Errorf("%s: %v, want %v containing %q", c.name, err, c.want, c.text)
			}
			continue
		}
		if err != nil || step.Complete != c.complete || strings.Join(step.Approvers, ",") != strings.Join(c.appr, ",") {
			t.Errorf("%s: %+v %v, want complete=%v approvers %v", c.name, step, err, c.complete, c.appr)
		}
	}
	// The second approver approves the first approver's attestations, not their own.
	step, _ := (&Plan{}).PrepareDecision(DecisionInput{Decision: DecisionGo, By: "bob"}, pendingBy("alice", allAttested()[:2]))
	if len(step.Attestations) != 2 {
		t.Fatalf("attestations evaluated %v", step.Attestations)
	}
	rolledBack := decisionPlan()
	rolledBack.RolledBack = true
	if _, err := rolledBack.PrepareDecision(DecisionInput{Decision: DecisionNoGo, By: "a", Reason: "x"}, none); !errors.Is(err, ErrTransition) {
		t.Fatalf("rolled back: %v", err)
	}
}

func TestRequireAllMet(t *testing.T) {
	ready, _ := EvaluateChecklist(ReadinessFacts{Plan: decisionPlan(), Packs: allPackFacts(StatusReconciled), Parallel: signedOff()}, allAttested(), glWaived())
	if err := RequireAllMet(ready); err != nil {
		t.Fatal(err)
	}
	notReady, _ := EvaluateChecklist(ReadinessFacts{Plan: decisionPlan()}, nil, nil)
	if err := RequireAllMet(notReady); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "CUT-1607") || !strings.Contains(err.Error(), "packs_reconciled") {
		t.Fatalf("%v", err)
	}
}

func TestSameAttestations_IgnoresOrderAndUnconfirmed(t *testing.T) {
	a := []Attestation{{Code: "load_test", Confirmed: true, Reference: "r1"}, {Code: "sod_clear", Confirmed: true, Reference: " r2 "}}
	b := []Attestation{{Code: "sod_clear", Confirmed: true, Reference: "r2"}, {Code: "uat_signed", Confirmed: false}, {Code: "load_test", Confirmed: true, Reference: "r1"}}
	if !SameAttestations(a, b) {
		t.Fatal("same confirmed set in another order")
	}
	b[2].Reference = "other"
	if SameAttestations(a, b) {
		t.Fatal("a different reference is a different set")
	}
}

// A staged-but-unposted pack stays open without a waiver and is met with one; the waiver is
// validated against the plan and the pack's status.
func TestEvaluateChecklist_WaiverForStagedPack(t *testing.T) {
	facts := ReadinessFacts{Plan: decisionPlan(), Packs: allPackFacts(StatusReconciled), Parallel: signedOff()}
	items, err := EvaluateChecklist(facts, allAttested(), nil)
	if err != nil || items[0].Status != ItemOpen || !strings.Contains(items[0].Detail, "gl_balances/USMF is RECONCILED but staged") {
		t.Fatalf("no waiver: %v %s %q", err, items[0].Status, items[0].Detail)
	}
	items, err = EvaluateChecklist(facts, allAttested(), glWaived())
	if err != nil || items[0].Status != ItemMet || !strings.Contains(items[0].Detail, "gl_balances waived") || !strings.Contains(items[0].Detail, "posted manually") {
		t.Fatalf("waived: %v %s %q", err, items[0].Status, items[0].Detail)
	}
	// A waiver does not excuse a run that is not RECONCILED.
	facts.Packs[3].Status = StatusLoaded
	if items, _ = EvaluateChecklist(facts, allAttested(), glWaived()); items[0].Status != ItemOpen || !strings.Contains(items[0].Detail, "is LOADED") {
		t.Fatalf("waived but loaded: %s %q", items[0].Status, items[0].Detail)
	}
	// Nor a RECONCILED run started before the legacy freeze (a rehearsal).
	facts.Packs[3].Status = StatusReconciled
	facts.Packs[3].CreatedAt = decisionT0.Add(-7 * 24 * time.Hour)
	if items, _ = EvaluateChecklist(facts, allAttested(), glWaived()); items[0].Status != ItemOpen || !strings.Contains(items[0].Detail, "before the legacy freeze") {
		t.Fatalf("waived but stale: %s %q", items[0].Status, items[0].Detail)
	}
	// The limit is 500 characters, not bytes.
	if _, err := EvaluateChecklist(facts, nil, []Waiver{{Pack: "gl_balances", Reason: strings.Repeat("é", 500)}}); err != nil {
		t.Fatalf("500 two-byte characters: %v", err)
	}
	for name, w := range map[string][]Waiver{
		"posted pack":  {{Pack: "open_items", Reason: "x"}},
		"not in plan":  {{Pack: "onhand", Reason: "x"}},
		"no reason":    {{Pack: "gl_balances", Reason: "  "}},
		"duplicate":    {{Pack: "gl_balances", Reason: "x"}, {Pack: "gl_balances", Reason: "y"}},
		"too long":     {{Pack: "gl_balances", Reason: strings.Repeat("x", 501)}},
		"unknown pack": {{Pack: "nope", Reason: "x"}},
	} {
		if _, err := EvaluateChecklist(facts, nil, w); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The second approver approves the waivers the first approved (CUT-1608): none or the same set.
func TestPrepareDecision_WaiversNeedTheSecondApprover(t *testing.T) {
	plan := decisionPlan()
	first, err := (&plan).PrepareDecision(DecisionInput{Decision: DecisionGo, By: "alice", Waivers: glWaived()}, ReadinessFacts{Plan: plan})
	if err != nil || first.Complete || len(first.Waivers) != 1 {
		t.Fatalf("first: %+v %v", first, err)
	}
	pending := ReadinessFacts{Plan: plan, Pending: []GoApproval{{Round: 1, Approver: "alice", Waivers: NormalizeWaivers(glWaived())}}}
	if _, err := (&plan).PrepareDecision(DecisionInput{Decision: DecisionGo, By: "bob", Waivers: []Waiver{{Pack: "gl_balances", Reason: "other"}}}, pending); !errors.Is(err, ErrInvalid) {
		t.Fatalf("different waiver: %v", err)
	}
	step, err := (&plan).PrepareDecision(DecisionInput{Decision: DecisionGo, By: "bob", Waivers: glWaived()}, pending)
	if err != nil || !step.Complete || len(step.Waivers) != 1 {
		t.Fatalf("same waiver: %+v %v", step, err)
	}
	step, err = (&plan).PrepareDecision(DecisionInput{Decision: DecisionGo, By: "bob"}, pending)
	if err != nil || !step.Complete || len(step.Waivers) != 1 {
		t.Fatalf("none sent takes the first approver's: %+v %v", step, err)
	}
	// A NO_GO carries no waivers: it does not go live, and it voids the pending approval's.
	noGo, err := (&plan).PrepareDecision(DecisionInput{Decision: DecisionNoGo, Reason: "bank smoke test failed", By: "bob", Waivers: glWaived()}, pending)
	if err != nil || !noGo.Complete || len(noGo.Waivers) != 0 {
		t.Fatalf("no-go: %+v %v", noGo, err)
	}
	if !SameWaivers(glWaived(), []Waiver{{Pack: "gl_balances", Reason: " opening balances posted manually in the target "}}) {
		t.Fatal("reasons compare trimmed")
	}
}
