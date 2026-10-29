package domain

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// plan builds content from nodes; deps are (pred, succ) FS links.
func plan(nodes []WBSNodeInput, deps ...[2]string) map[string]NodeContent {
	out := map[string]NodeContent{}
	for _, n := range nodes {
		if n.ConstraintType == "" {
			n.ConstraintType = ConstraintASAP
		}
		out[n.WBSCode] = NodeContent{Node: n}
	}
	for _, d := range deps {
		c := out[d[1]]
		c.Predecessors = append(c.Predecessors, WBSDependencyInput{PredecessorWBSCode: d[0], SuccessorWBSCode: d[1], Type: DepFS})
		out[d[1]] = c
	}
	return out
}

// ac4115 is the AC-41.15 project: 1 with tasks 1.1 to 1.4.
func ac4115() []WBSNodeInput {
	return []WBSNodeInput{
		{WBSCode: "1", Name: "Project"},
		{WBSCode: "1.1", ParentWBSCode: ptr("1"), Name: "Design", DurationDays: 5},
		{WBSCode: "1.2", ParentWBSCode: ptr("1"), Name: "Build", DurationDays: 10},
		{WBSCode: "1.3", ParentWBSCode: ptr("1"), Name: "Test", DurationDays: 4},
		{WBSCode: "1.4", ParentWBSCode: ptr("1"), Name: "Handover", DurationDays: 1},
	}
}

func codes(cs []NodeChange) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.WBSCode)
	}
	return out
}

func TestContentByCode_ParentsAndPredecessorsByCode(t *testing.T) {
	nodes := []WBSNode{
		{ID: "a", WBSCode: "1", Name: "Root", ConstraintType: ConstraintASAP},
		{ID: "b", WBSCode: "1.1", ParentID: ptr("a"), Name: "A", DurationDays: 2, ConstraintType: ConstraintASAP, EarlyStart: ptr("2026-10-01")},
		{ID: "c", WBSCode: "1.2", ParentID: ptr("a"), Name: "B", DurationDays: 3, ConstraintType: ConstraintASAP},
	}
	links := []WBSLink{{PredecessorID: "b", SuccessorID: "c", Type: DepSS, LagDays: 2}}
	got := ContentByCode(nodes, links)
	if p := got["1.2"].Node.ParentWBSCode; p == nil || *p != "1" {
		t.Fatalf("parent of 1.2 = %v", p)
	}
	want := []WBSDependencyInput{{PredecessorWBSCode: "1.1", SuccessorWBSCode: "1.2", Type: DepSS, LagDays: 2}}
	if !reflect.DeepEqual(got["1.2"].Predecessors, want) {
		t.Fatalf("predecessors of 1.2 = %+v", got["1.2"].Predecessors)
	}
	// Ids and scheduler output are not content: the same plan under new ids
	// and without dates fingerprints alike.
	nodes2 := []WBSNode{
		{ID: "x", WBSCode: "1", Name: "Root", ConstraintType: ConstraintASAP},
		{ID: "y", WBSCode: "1.1", ParentID: ptr("x"), Name: "A", DurationDays: 2, ConstraintType: ConstraintASAP},
		{ID: "z", WBSCode: "1.2", ParentID: ptr("x"), Name: "B", DurationDays: 3, ConstraintType: ConstraintASAP},
	}
	links2 := []WBSLink{{PredecessorID: "y", SuccessorID: "z", Type: DepSS, LagDays: 2}}
	if !reflect.DeepEqual(Fingerprints(got), Fingerprints(ContentByCode(nodes2, links2))) {
		t.Fatal("same content under new ids fingerprints differently")
	}
}

func TestFingerprint_WhatCountsAsAChange(t *testing.T) {
	base := NodeContent{Node: WBSNodeInput{WBSCode: "1.2", Name: "Build", EffortHours: ptr("8"), DurationDays: 10, ConstraintType: ConstraintASAP}}
	same := base
	same.Node.EffortHours = ptr("8.00")
	same.Node.ConstraintType = ""
	if Fingerprint(base) != Fingerprint(same) {
		t.Fatal("8 and 8.00 hours, or an empty and an ASAP constraint, must be the same content")
	}
	changes := map[string]func(c *NodeContent){
		"name":        func(c *NodeContent) { c.Node.Name = "Build v2" },
		"duration":    func(c *NodeContent) { c.Node.DurationDays = 11 },
		"effort":      func(c *NodeContent) { c.Node.EffortHours = ptr("9") },
		"constraint":  func(c *NodeContent) { c.Node.ConstraintType, c.Node.ConstraintDate = ConstraintSNET, ptr("2026-11-02") },
		"parent":      func(c *NodeContent) { c.Node.ParentWBSCode = ptr("2") },
		"milestone":   func(c *NodeContent) { c.Node.IsBillableMilestone = true },
		"predecessor": func(c *NodeContent) { c.Predecessors = []WBSDependencyInput{{PredecessorWBSCode: "1.1", Type: DepFS}} },
	}
	for name, change := range changes {
		c := base
		change(&c)
		if Fingerprint(c) == Fingerprint(base) {
			t.Errorf("a %s change is not detected", name)
		}
	}
}

// AC-41.15: a SIMULATION copied from WORKING where node 1.2 was later moved in
// WORKING and node 1.4 only in the simulation. Moving 1.2 in WORKING alone is
// not a conflict (BR-41.14: conflicts are nodes changed in both), so the
// simulation moves 1.2 too, differently.
func TestDiffSimulation_AC4115(t *testing.T) {
	base := Fingerprints(plan(ac4115(), [2]string{"1.1", "1.2"}, [2]string{"1.2", "1.3"}, [2]string{"1.3", "1.4"}))

	wk := ac4115()
	wk[2].ConstraintType, wk[2].ConstraintDate = ConstraintSNET, ptr("2026-11-09") // 1.2 moved in WORKING
	sim := ac4115()
	sim[2].DurationDays = 14 // 1.2 also changed in the simulation
	sim[4].DurationDays = 3  // 1.4 changed only in the simulation
	working := plan(wk, [2]string{"1.1", "1.2"}, [2]string{"1.2", "1.3"}, [2]string{"1.3", "1.4"})
	simulation := plan(sim, [2]string{"1.1", "1.2"}, [2]string{"1.2", "1.3"}, [2]string{"1.3", "1.4"})

	d := DiffSimulation(base, working, simulation, nil)
	if got := codes(d.Applied); !reflect.DeepEqual(got, []string{"1.4"}) {
		t.Fatalf("applied = %v, want [1.4]", got)
	}
	if got := codes(d.Conflicts); !reflect.DeepEqual(got, []string{"1.2"}) {
		t.Fatalf("conflicts = %v, want [1.2]", got)
	}
	err := CheckTransfer("SIM", d)
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeSimConflict || !strings.Contains(de.Detail, "1 nodes") || !strings.Contains(de.Detail, "1.2") {
		t.Fatalf("unresolved transfer: %v", err)
	}

	for _, tc := range []struct {
		resolution string
		want       int // duration of 1.2 after the merge
		wantSNET   bool
	}{
		{ResolveKeepWorking, 10, true},
		{ResolveTakeSimulation, 14, false},
	} {
		d := DiffSimulation(base, working, simulation, map[string]string{"1.2": tc.resolution, "9.9": ResolveTakeSimulation})
		if err := CheckTransfer("SIM", d); err != nil {
			t.Fatalf("%s: %v", tc.resolution, err)
		}
		if d.Conflicts[0].Resolution != tc.resolution {
			t.Fatalf("%s: resolution not reported: %+v", tc.resolution, d.Conflicts)
		}
		merged := map[string]WBSNodeInput{}
		for _, n := range d.Nodes {
			merged[n.WBSCode] = n
		}
		if merged["1.4"].DurationDays != 3 {
			t.Fatalf("%s: 1.4 not applied: %+v", tc.resolution, merged["1.4"])
		}
		if merged["1.2"].DurationDays != tc.want || (merged["1.2"].ConstraintType == ConstraintSNET) != tc.wantSNET {
			t.Fatalf("%s: 1.2 = %+v", tc.resolution, merged["1.2"])
		}
		if len(d.Nodes) != 5 || len(d.Dependencies) != 3 {
			t.Fatalf("%s: merged %d nodes, %d dependencies", tc.resolution, len(d.Nodes), len(d.Dependencies))
		}
	}
}

func TestDiffSimulation_AddsRemovesAndKeepsWorkingOnlyChanges(t *testing.T) {
	baseNodes := ac4115()
	base := Fingerprints(plan(baseNodes, [2]string{"1.1", "1.2"}))

	wk := append(ac4115(), WBSNodeInput{WBSCode: "1.6", ParentWBSCode: ptr("1"), Name: "Working add", DurationDays: 1})
	wk[1].Name = "Design (working)"                                           // changed only in WORKING: kept
	sim := []WBSNodeInput{ac4115()[0], ac4115()[1], ac4115()[2], ac4115()[4], // 1.3 removed in the simulation
		{WBSCode: "1.5", ParentWBSCode: ptr("1"), Name: "Sim add", DurationDays: 2}}
	working := plan(wk, [2]string{"1.1", "1.2"})
	simulation := plan(sim, [2]string{"1.1", "1.2"}, [2]string{"1.5", "1.4"})

	d := DiffSimulation(base, working, simulation, nil)
	if len(d.Conflicts) != 0 {
		t.Fatalf("conflicts = %+v", d.Conflicts)
	}
	kinds := map[string]string{}
	for _, c := range d.Applied {
		kinds[c.WBSCode] = c.Change
	}
	// 1.4 gained a predecessor in the simulation, so it changed too.
	want := map[string]string{"1.3": ChangeRemoved, "1.4": ChangeChanged, "1.5": ChangeAdded}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("applied = %v, want %v", kinds, want)
	}
	merged := map[string]WBSNodeInput{}
	for _, n := range d.Nodes {
		merged[n.WBSCode] = n
	}
	if _, ok := merged["1.3"]; ok {
		t.Fatal("1.3 removed in the simulation is still in the merge")
	}
	if merged["1.1"].Name != "Design (working)" || merged["1.6"].Name != "Working add" {
		t.Fatalf("WORKING-only changes lost: %+v", merged)
	}
	if !reflect.DeepEqual(d.Dependencies, []WBSDependencyInput{
		{PredecessorWBSCode: "1.1", SuccessorWBSCode: "1.2", Type: DepFS},
		{PredecessorWBSCode: "1.5", SuccessorWBSCode: "1.4", Type: DepFS},
	}) {
		t.Fatalf("dependencies = %+v", d.Dependencies)
	}
	if err := CheckTransfer("SIM", d); err != nil {
		t.Fatal(err)
	}
}

func TestDiffSimulation_SameChangeOnBothSidesIsNoConflict(t *testing.T) {
	base := Fingerprints(plan(ac4115()))
	wk, sim := ac4115(), ac4115()
	wk[3].DurationDays, sim[3].DurationDays = 7, 7
	d := DiffSimulation(base, plan(wk), plan(sim), nil)
	if len(d.Applied) != 0 || len(d.Conflicts) != 0 {
		t.Fatalf("applied %v, conflicts %v", codes(d.Applied), codes(d.Conflicts))
	}
}

func TestCheckTransfer_RefusesAMergeThatIsNotAWBS(t *testing.T) {
	// The simulation removes summary 2; WORKING gave 2 a new child 2.1.
	nodes := []WBSNodeInput{{WBSCode: "1", Name: "A"}, {WBSCode: "2", Name: "B"}}
	base := Fingerprints(plan(nodes))
	wk := append([]WBSNodeInput{}, nodes...)
	wk = append(wk, WBSNodeInput{WBSCode: "2.1", ParentWBSCode: ptr("2"), Name: "B child"})
	sim := nodes[:1]
	d := DiffSimulation(base, plan(wk), plan(sim), nil)
	err := CheckTransfer("SIM", d)
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeSimConflict || !strings.Contains(de.Detail, "node 2.1.parentWbsCode") {
		t.Fatalf("got %v", err)
	}
}

func TestCheckTransfer_MergedCycleIsWBS001(t *testing.T) {
	nodes := []WBSNodeInput{{WBSCode: "1", Name: "A"}, {WBSCode: "2", Name: "B"}}
	base := Fingerprints(plan(nodes))
	working := plan(nodes, [2]string{"1", "2"}) // WORKING: 1 → 2
	sim := plan(nodes, [2]string{"2", "1"})     // simulation: 2 → 1
	d := DiffSimulation(base, working, sim, nil)
	if len(d.Conflicts) != 0 || len(d.Dependencies) != 2 {
		t.Fatalf("each side changed one node: conflicts %v, dependencies %+v", codes(d.Conflicts), d.Dependencies)
	}
	var de *Error
	if err := CheckTransfer("SIM", d); !errors.As(err, &de) || de.Code != CodeWBSCycle {
		t.Fatalf("got %v", err)
	}
}

// Property: transferring an untouched simulation changes nothing, and taking
// the simulation on every conflict makes WORKING equal to the simulation.
func TestDiffSimulation_Properties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(t, "n")
		gen := func(label string) []WBSNodeInput {
			out := []WBSNodeInput{}
			for i := 1; i <= n; i++ {
				if rapid.Bool().Draw(t, label+"keep") {
					out = append(out, WBSNodeInput{WBSCode: strconv.Itoa(i), Name: "N",
						DurationDays: rapid.IntRange(0, 3).Draw(t, label+"d")})
				}
			}
			return out
		}
		base, wk, sim := plan(gen("b")), plan(gen("w")), plan(gen("s"))
		baseFP := Fingerprints(base)

		same := DiffSimulation(baseFP, wk, base, nil)
		if len(same.Applied) != 0 {
			t.Fatalf("untouched simulation applies %v", codes(same.Applied))
		}

		all := map[string]string{}
		for c := range wk {
			all[c] = ResolveTakeSimulation
		}
		for c := range sim {
			all[c] = ResolveTakeSimulation
		}
		d := DiffSimulation(baseFP, wk, sim, all)
		if len(d.Unresolved()) != 0 {
			t.Fatalf("unresolved %v", d.Unresolved())
		}
		got := map[string]string{}
		for _, nd := range d.Nodes {
			got[nd.WBSCode] = Fingerprint(NodeContent{Node: nd})
		}
		// Nodes the simulation did not change keep WORKING's version, so
		// equality holds on the nodes the simulation changed or added.
		for c, sc := range sim {
			if Fingerprint(sc) != baseFP[c] && got[c] != Fingerprint(sc) {
				t.Fatalf("node %s not taken from the simulation", c)
			}
		}
		for c := range base {
			if _, inSim := sim[c]; !inSim {
				if _, ok := got[c]; ok {
					t.Fatalf("node %s removed in the simulation survives", c)
				}
			}
		}
	})
}

func TestCompareWBSCodes(t *testing.T) {
	ordered := []string{"1", "1.2", "1.10", "2", "10"}
	for i := 0; i+1 < len(ordered); i++ {
		if CompareWBSCodes(ordered[i], ordered[i+1]) >= 0 || CompareWBSCodes(ordered[i+1], ordered[i]) <= 0 {
			t.Fatalf("%s should sort before %s", ordered[i], ordered[i+1])
		}
	}
	if CompareWBSCodes("1.2", "1.2") != 0 {
		t.Fatal("equal codes")
	}
}

func TestCheckNewPlan_SimulationCap(t *testing.T) {
	working := &PlanVersion{ID: "w", Kind: KindWorking}
	existing := []PlanKind{KindWorking}
	for i := 0; i < MaxSimulations-1; i++ {
		existing = append(existing, KindSimulation)
	}
	if _, err := CheckNewPlan(KindSimulation, working, existing); err != nil {
		t.Fatalf("simulation %d: %v", MaxSimulations, err)
	}
	existing = append(existing, KindSimulation)
	var de *Error
	if _, err := CheckNewPlan(KindSimulation, working, existing); !errors.As(err, &de) || de.Code != CodePlanKind {
		t.Fatalf("simulation %d: %v", MaxSimulations+1, err)
	}
}
