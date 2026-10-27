package domain

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func codeOf(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestValidateGroup_IDsNameAndType(t *testing.T) {
	cases := []struct {
		group ProjectGroup
		field string
	}{
		{ProjectGroup{ID: "ENG", Name: "Engineering"}, ""},
		{ProjectGroup{ID: "ENG-SVC-01", Name: "x", DefaultProjectType: TypeFixedPrice}, ""},
		{ProjectGroup{ID: "*", Name: "Wildcard"}, "id"},
		{ProjectGroup{ID: "eng", Name: "x"}, "id"},
		{ProjectGroup{ID: "ENGINEERING", Name: "x"}, "id"},
		{ProjectGroup{ID: "ENG", Name: ""}, "name"},
		{ProjectGroup{ID: "ENG", Name: strings.Repeat("é", 120)}, ""},
		{ProjectGroup{ID: "ENG", Name: strings.Repeat("é", 121)}, "name"},
		{ProjectGroup{ID: "ENG", Name: "x", DefaultProjectType: "GRANT"}, "defaultProjectType"},
	}
	for _, c := range cases {
		fields := ValidateGroup(c.group)
		if c.field == "" && len(fields) > 0 || c.field != "" && fields[c.field] == "" {
			t.Errorf("ValidateGroup(%+v) = %v, want error on %q", c.group, fields, c.field)
		}
	}
}

func profile(group, category, property string) PostingProfile {
	return PostingProfile{Key: ProfileKey{group, category, property}, CostAccount: "5100", WIPAccount: "1400",
		AccruedRevenueAccount: "1410", InvoicedRevenueAccount: "4000", OnAccountAccount: "2400"}
}

func TestValidateProfile_OnlyResolvableKeys(t *testing.T) {
	cases := []struct {
		p     PostingProfile
		field string
	}{
		{profile("ENG", "LABOR", "CHARGEABLE"), ""},
		{profile("ENG", "*", "*"), ""},
		{profile("*", "*", "*"), ""},
		{profile("ENG", "*", "CHARGEABLE"), "lineProperty"},
		{profile("ENG", "LABOR", "*"), "lineProperty"},
		{profile("*", "LABOR", "CHARGEABLE"), "category"},
		{profile("*", "*", "NON_CHARGEABLE"), "lineProperty"},
		{profile("eng", "LABOR", "CHARGEABLE"), "projectGroup"},
		{profile("ENG", "", "CHARGEABLE"), "category"},
		{profile("ENG", "LABOR", "MAYBE"), "lineProperty"},
	}
	for _, c := range cases {
		fields := ValidateProfile(c.p)
		if c.field == "" && len(fields) > 0 || c.field != "" && fields[c.field] == "" {
			t.Errorf("ValidateProfile(%v) = %v, want error on %q", c.p.Key, fields, c.field)
		}
	}
	noWIP := profile("ENG", "*", "*")
	noWIP.WIPAccount = ""
	if ValidateProfile(noWIP)["wipAccount"] == "" {
		t.Error("a missing WIP account must be rejected")
	}
}

// Every key ValidateProfile accepts is one Resolve can return, so no stored row is dead.
func TestValidateProfile_PropertyAcceptedKeysAreResolvable(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		group := rapid.SampledFrom([]string{"ENG", "OPS", Wildcard}).Draw(t, "group")
		category := rapid.SampledFrom([]string{"LABOR", "TRAVEL", Wildcard}).Draw(t, "category")
		property := rapid.SampledFrom([]string{LineChargeable, LineNonChargeable, Wildcard}).Draw(t, "property")
		p := profile(group, category, property)
		if len(ValidateProfile(p)) > 0 {
			return
		}
		lookup := ProfileKey{ProjectGroup: group, Category: category, LineProperty: property}
		if group == Wildcard {
			lookup.ProjectGroup = "ANY"
		}
		if category == Wildcard {
			lookup.Category, lookup.LineProperty = "LABOR", LineChargeable
		}
		got, err := Resolve([]PostingProfile{p}, lookup)
		if err != nil || got.Key != p.Key {
			t.Fatalf("accepted key %v is not resolved by %v: %v", p.Key, lookup, err)
		}
	})
}

func TestCheckNewPlan_KindsAndSources(t *testing.T) {
	working := &PlanVersion{ID: "w", Kind: KindWorking}
	baseline := &PlanVersion{ID: "b", Kind: KindBaseline, Locked: true}
	simulation := &PlanVersion{ID: "s", Kind: KindSimulation}
	cases := []struct {
		name     string
		kind     PlanKind
		source   *PlanVersion
		existing []PlanKind
		locked   bool
		code     string
	}{
		{"first empty working", KindWorking, nil, nil, false, ""},
		{"working copied from baseline", KindWorking, baseline, []PlanKind{KindBaseline}, false, ""},
		{"second working", KindWorking, nil, []PlanKind{KindWorking}, false, CodePlanKind},
		{"baseline from working is locked", KindBaseline, working, []PlanKind{KindWorking}, true, ""},
		{"baseline needs a source", KindBaseline, nil, []PlanKind{KindWorking}, false, CodePlanKind},
		{"baseline from a simulation", KindBaseline, simulation, []PlanKind{KindWorking, KindSimulation}, false, CodePlanKind},
		{"second baseline", KindBaseline, working, []PlanKind{KindWorking, KindBaseline}, false, CodePlanKind},
		{"simulation from working", KindSimulation, working, []PlanKind{KindWorking}, false, ""},
		{"simulation from baseline", KindSimulation, baseline, []PlanKind{KindWorking, KindBaseline}, false, ""},
		{"many simulations", KindSimulation, working, []PlanKind{KindWorking, KindSimulation, KindSimulation}, false, ""},
		{"simulation needs a source", KindSimulation, nil, nil, false, CodePlanKind},
		{"simulation of a simulation", KindSimulation, simulation, nil, false, CodePlanKind},
	}
	for _, c := range cases {
		locked, err := CheckNewPlan(c.kind, c.source, c.existing)
		if codeOf(err) != c.code || (err == nil && locked != c.locked) {
			t.Errorf("%s: locked=%v err=%v, want locked=%v code %q", c.name, locked, err, c.locked, c.code)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func node(code string, parent *string, days int) WBSNodeInput {
	return WBSNodeInput{WBSCode: code, ParentWBSCode: parent, Name: "Task " + code, DurationDays: days, ConstraintType: ConstraintASAP}
}

func TestValidateWBS_FieldErrors(t *testing.T) {
	good := []WBSNodeInput{node("1", nil, 0), node("1.1", ptr("1"), 3), node("1.2", ptr("1"), 2)}
	cases := []struct {
		name  string
		edit  func(n []WBSNodeInput, d []WBSDependencyInput) ([]WBSNodeInput, []WBSDependencyInput)
		field string
	}{
		{"valid", func(n []WBSNodeInput, d []WBSDependencyInput) ([]WBSNodeInput, []WBSDependencyInput) { return n, d }, ""},
		{"code with letters", func(n []WBSNodeInput, d []WBSDependencyInput) ([]WBSNodeInput, []WBSDependencyInput) {
			n[1].WBSCode = "1.a"
			return n, d
		}, "nodes[1].wbsCode"},
		{"negative duration", func(n []WBSNodeInput, d []WBSDependencyInput) ([]WBSNodeInput, []WBSDependencyInput) {
			n[1].DurationDays = -1
			return n, d
		}, "nodes[1].durationDays"},
		{"effort with three decimals", func(n []WBSNodeInput, d []WBSDependencyInput) ([]WBSNodeInput, []WBSDependencyInput) {
			n[1].EffortHours = ptr("1.125")
			return n, d
		}, "nodes[1].effortHours"},
		{"negative effort", func(n []WBSNodeInput, d []WBSDependencyInput) ([]WBSNodeInput, []WBSDependencyInput) {
			n[1].EffortHours = ptr("-4")
			return n, d
		}, "nodes[1].effortHours"},
		{"role is not a uuid", func(n []WBSNodeInput, d []WBSDependencyInput) ([]WBSNodeInput, []WBSDependencyInput) {
			n[1].RoleID = ptr("engineer")
			return n, d
		}, "nodes[1].roleId"},
		{"SNET with a bad date", func(n []WBSNodeInput, d []WBSDependencyInput) ([]WBSNodeInput, []WBSDependencyInput) {
			n[1].ConstraintType, n[1].ConstraintDate = ConstraintSNET, ptr("2026-13-01")
			return n, d
		}, "nodes[1].constraintDate"},
		{"FNLT is refused until scheduled", func(n []WBSNodeInput, d []WBSDependencyInput) ([]WBSNodeInput, []WBSDependencyInput) {
			n[1].ConstraintType, n[1].ConstraintDate = ConstraintFNLT, ptr("2026-11-01")
			return n, d
		}, "nodes[1].constraintType"},
		{"parent cycle", func(n []WBSNodeInput, d []WBSDependencyInput) ([]WBSNodeInput, []WBSDependencyInput) {
			n[0].ParentWBSCode = ptr("1.1")
			return n, d
		}, "nodes[0].parentWbsCode"},
		{"link to a summary node", func(n []WBSNodeInput, d []WBSDependencyInput) ([]WBSNodeInput, []WBSDependencyInput) {
			return n, append(d, WBSDependencyInput{PredecessorWBSCode: "1", SuccessorWBSCode: "1.2", Type: DepFS})
		}, "dependencies[0]"},
		{"unknown dependency type", func(n []WBSNodeInput, d []WBSDependencyInput) ([]WBSNodeInput, []WBSDependencyInput) {
			return n, append(d, WBSDependencyInput{PredecessorWBSCode: "1.1", SuccessorWBSCode: "1.2", Type: "XX"})
		}, "dependencies[0].type"},
		{"lag beyond ten years", func(n []WBSNodeInput, d []WBSDependencyInput) ([]WBSNodeInput, []WBSDependencyInput) {
			return n, append(d, WBSDependencyInput{PredecessorWBSCode: "1.1", SuccessorWBSCode: "1.2", Type: DepFS, LagDays: 4000})
		}, "dependencies[0].lagDays"},
	}
	for _, c := range cases {
		nodes, deps := c.edit(append([]WBSNodeInput(nil), good...), nil)
		fields, err := ValidateWBS(nodes, deps)
		if err != nil {
			t.Errorf("%s: unexpected rule error %v", c.name, err)
			continue
		}
		if c.field == "" && len(fields) > 0 || c.field != "" && fields[c.field] == "" {
			t.Errorf("%s: fields %v, want error on %q", c.name, fields, c.field)
		}
	}
}

func TestValidateWBS_DuplicateCodeAndCycle(t *testing.T) {
	nodes := []WBSNodeInput{node("1", nil, 1), node("2", nil, 1), node("3", nil, 1), node("2", nil, 1)}
	if _, err := ValidateWBS(nodes, nil); codeOf(err) != CodeWBSDuplicate || !strings.Contains(err.Error(), "2") {
		t.Fatalf("duplicate code: %v", err)
	}
	nodes = []WBSNodeInput{node("1", nil, 1), node("2", nil, 1), node("3", nil, 1), node("4", nil, 1)}
	deps := []WBSDependencyInput{
		{PredecessorWBSCode: "1", SuccessorWBSCode: "2", Type: DepFS},
		{PredecessorWBSCode: "2", SuccessorWBSCode: "3", Type: DepFS},
		{PredecessorWBSCode: "3", SuccessorWBSCode: "2", Type: DepSS},
		{PredecessorWBSCode: "3", SuccessorWBSCode: "4", Type: DepFS},
	}
	_, err := ValidateWBS(nodes, deps)
	// The named dependency is on the 2 ⇄ 3 loop, not the 3 → 4 link downstream of it.
	if codeOf(err) != CodeWBSCycle || !(strings.Contains(err.Error(), "2 → 3") || strings.Contains(err.Error(), "3 → 2")) {
		t.Fatalf("cycle: %v", err)
	}
}

func TestValidateWBS_SizeCaps(t *testing.T) {
	nodes := make([]WBSNodeInput, MaxWBSNodes+1)
	if fields, _ := ValidateWBS(nodes, nil); fields["nodes"] == "" {
		t.Fatalf("over %d nodes must be refused: %v", MaxWBSNodes, fields)
	}
}

func dates(n WBSNode) string {
	return fmt.Sprintf("%s..%s late %s..%s slack %d critical %v", *n.EarlyStart, *n.EarlyFinish, *n.LateStart, *n.LateFinish, *n.SlackDays, *n.Critical)
}

func TestScheduleWBS_SummaryNodesRollUp(t *testing.T) {
	// 1 Design (summary) → 1.1 Draft (3d) FS+2 → 1.2 Review (2d); 1.3 Side (1d) floats.
	// 2 Build (5d) SNET Monday 12 Oct.
	nodes := []WBSNode{
		{ID: "n1", WBSCode: "1", DurationDays: 9, ConstraintType: ConstraintASAP},
		{ID: "n11", WBSCode: "1.1", ParentID: ptr("n1"), DurationDays: 3, ConstraintType: ConstraintASAP},
		{ID: "n12", WBSCode: "1.2", ParentID: ptr("n1"), DurationDays: 2, ConstraintType: ConstraintASAP},
		{ID: "n13", WBSCode: "1.3", ParentID: ptr("n1"), DurationDays: 1, ConstraintType: ConstraintASAP},
		{ID: "n2", WBSCode: "2", DurationDays: 5, ConstraintType: ConstraintSNET, ConstraintDate: ptr("2026-10-12")},
	}
	links := []WBSLink{{PredecessorID: "n11", SuccessorID: "n12", Type: DepFS, LagDays: 2}}
	sched, err := ScheduleWBS(StandardCalendar(), "2026-10-01", nodes, links)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		// Thu 1, Fri 2, Mon 5 Oct; Mon 5 + 2 calendar days = Wed 7, next day Thu 8.
		"1.1": "2026-10-01..2026-10-05 late 2026-10-08..2026-10-12 slack 5 critical false",
		"1.2": "2026-10-08..2026-10-09 late 2026-10-15..2026-10-16 slack 5 critical false",
		"1.3": "2026-10-01..2026-10-01 late 2026-10-16..2026-10-16 slack 11 critical false",
		// The summary spans its children; its own 9-day duration is ignored.
		"1": "2026-10-01..2026-10-09 late 2026-10-08..2026-10-16 slack 5 critical false",
		"2": "2026-10-12..2026-10-16 late 2026-10-12..2026-10-16 slack 0 critical true",
	}
	for _, n := range nodes {
		if got := dates(n); got != want[n.WBSCode] {
			t.Errorf("node %s: %s, want %s", n.WBSCode, got, want[n.WBSCode])
		}
	}
	if sched.FinishDate.Format(time.DateOnly) != "2026-10-16" || fmt.Sprint(sched.CriticalPath) != "[n2]" {
		t.Fatalf("finish %s critical %v", sched.FinishDate.Format(time.DateOnly), sched.CriticalPath)
	}
}

func TestScheduleWBS_RejectsBadStoredDates(t *testing.T) {
	if _, err := ScheduleWBS(StandardCalendar(), "01/10/2026", nil, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad project start: %v", err)
	}
	nodes := []WBSNode{{ID: "a", WBSCode: "1", DurationDays: 1, ConstraintType: ConstraintSNET, ConstraintDate: ptr("soon")}}
	if _, err := ScheduleWBS(StandardCalendar(), "2026-10-01", nodes, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad constraint date: %v", err)
	}
}
