package domain

import (
	"fmt"
	"testing"

	"pgregory.net/rapid"
)

func accountHierarchy(strict bool, leaves ...HierarchyNode) Hierarchy {
	nodes := append([]HierarchyNode{{Code: "ROOT", Label: "Root"}}, leaves...)
	for i := range nodes[1:] {
		nodes[i+1].Parent = "ROOT"
	}
	return Hierarchy{Code: "HIER-AC", Name: "Accounts", Subject: SubjectAccount, Strict: strict, Nodes: nodes}
}

func leaf(code, from, to string) HierarchyNode {
	return HierarchyNode{Code: code, Label: code, Rules: []Rule{{From: from, To: to}}}
}

func TestHierarchy_Validate(t *testing.T) {
	deep := Hierarchy{Code: "HIER-DEEP", Name: "Deep", Subject: SubjectAccount}
	for i := 0; i < 13; i++ {
		n := HierarchyNode{Code: fmt.Sprintf("N%d", i), Label: "n"}
		if i > 0 {
			n.Parent = fmt.Sprintf("N%d", i-1)
		}
		deep.Nodes = append(deep.Nodes, n)
	}
	cycle := Hierarchy{Code: "HIER-CYC", Name: "Cycle", Subject: SubjectAccount, Nodes: []HierarchyNode{
		{Code: "A", Label: "a", Parent: "B"}, {Code: "B", Label: "b", Parent: "A"}}}
	tests := []struct {
		name string
		h    Hierarchy
		want bool // true when a problem is expected
	}{
		{"valid account hierarchy", accountHierarchy(true, leaf("ASSET", "100000", "199999")), false},
		{"depth over 12 levels", deep, true},
		{"cycle", cycle, true},
		{"rules on a non-leaf", Hierarchy{Code: "HIER-NL", Name: "x", Subject: SubjectAccount, Nodes: []HierarchyNode{
			{Code: "ROOT", Label: "r", Rules: []Rule{{Values: []string{"*"}}}},
			{Code: "CHILD", Label: "c", Parent: "ROOT"}}}, true},
		{"unknown parent", Hierarchy{Code: "HIER-UP", Name: "x", Subject: SubjectAccount, Nodes: []HierarchyNode{
			{Code: "ROOT", Label: "r", Parent: "MISSING"}}}, true},
		{"dimension hierarchy without attribute", Hierarchy{Code: "HIER-DIM", Name: "x", Subject: SubjectDimension,
			Nodes: []HierarchyNode{{Code: "ROOT", Label: "r"}}}, true},
		{"range with from after to", accountHierarchy(true, leaf("BAD", "9", "1")), true},
		{"unknown subject", Hierarchy{Code: "HIER-SU", Name: "x", Subject: "ledger", Nodes: []HierarchyNode{{Code: "ROOT", Label: "r"}}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := len(tc.h.Validate()) > 0
			if got != tc.want {
				t.Fatalf("problems = %+v, want problem %v", tc.h.Validate(), tc.want)
			}
		})
	}
}

func TestHierarchy_Materialize(t *testing.T) {
	h := accountHierarchy(true, leaf("ASSET", "100000", "199999"), leaf("LIAB", "200000", "299999"))
	overlapping := accountHierarchy(true, leaf("ASSET", "100000", "199999"), leaf("CURRENT", "100000", "100500"))
	tests := []struct {
		name       string
		h          Hierarchy
		members    []Member
		wantCode   string
		assigned   map[string]string
		unassigned []string
	}{
		{"all assigned", h, []Member{{ID: "m1", Key: "100100"}, {ID: "m2", Key: "250000"}},
			"", map[string]string{"m1": "ASSET", "m2": "LIAB"}, []string{}},
		{"no category is unassigned", h, []Member{{ID: "m1", Key: "100100"}, {ID: "m9", Key: "400000"}},
			"", map[string]string{"m1": "ASSET"}, []string{"m9"}},
		{"member matching two leaves is an overlap", overlapping, []Member{{ID: "m1", Key: "100100"}},
			CodeHierOverlap, nil, nil},
		{"duplicate member id", h, []Member{{ID: "m1", Key: "100100"}, {ID: "m1", Key: "200100"}},
			ShapeCode, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := tc.h.Materialize(tc.members)
			if tc.wantCode != "" {
				de, ok := err.(*Error)
				if !ok || de.Code != tc.wantCode {
					t.Fatalf("err = %v, want %s", err, tc.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(m.Assigned) != fmt.Sprint(tc.assigned) || fmt.Sprint(m.Unassigned) != fmt.Sprint(tc.unassigned) {
				t.Fatalf("membership = %+v, want assigned %v unassigned %v", m, tc.assigned, tc.unassigned)
			}
		})
	}
}

func TestHierarchy_CheckPublishStrictAndNonStrict(t *testing.T) {
	m := Membership{Assigned: map[string]string{}, Unassigned: []string{"a1", "a2"}}
	strict := accountHierarchy(true, leaf("ASSET", "100000", "199999"))
	err := strict.CheckPublish(m)
	if err == nil || err.Code != CodeHierUnassigned {
		t.Fatalf("strict publish = %v, want %s", err, CodeHierUnassigned)
	}
	loose := accountHierarchy(false, leaf("ASSET", "100000", "199999"))
	if err := loose.CheckPublish(m); err != nil {
		t.Fatalf("non-strict publish = %v, want nil", err)
	}
	if err := strict.CheckPublish(Membership{}); err != nil {
		t.Fatalf("fully assigned strict publish = %v, want nil", err)
	}
}

// Property: every member is assigned to exactly one leaf whose range contains
// its key, or appears once in the unassigned list (FR-15.13 membership).
func TestHierarchy_MaterializePartitionsMembers(t *testing.T) {
	leaves := []HierarchyNode{leaf("LOW", "000", "399"), leaf("MID", "400", "699"), leaf("HIGH", "700", "899")}
	h := accountHierarchy(false, leaves...)
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 60).Draw(t, "n")
		members := make([]Member, n)
		for i := range members {
			key := rapid.IntRange(0, 999).Draw(t, "key")
			members[i] = Member{ID: fmt.Sprintf("m%03d", i), Key: fmt.Sprintf("%03d", key)}
		}
		m, err := h.Materialize(members)
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Assigned)+len(m.Unassigned) != n {
			t.Fatalf("assigned %d + unassigned %d != members %d", len(m.Assigned), len(m.Unassigned), n)
		}
		for _, mem := range members {
			leafCode, ok := m.Assigned[mem.ID]
			if !ok {
				continue
			}
			for _, lf := range leaves {
				if lf.Code == leafCode && !(lf.Rules[0].From <= mem.Key && mem.Key <= lf.Rules[0].To) {
					t.Fatalf("member %s key %s assigned to %s outside its range", mem.ID, mem.Key, leafCode)
				}
			}
		}
	})
}

func TestHierarchy_RulesWildcardsAndAttributes(t *testing.T) {
	h := Hierarchy{Code: "HIER-CC", Name: "Cost centers", Subject: SubjectDimension, DimensionAttribute: "COST_CENTER", Nodes: []HierarchyNode{
		{Code: "ROOT", Label: "All"},
		{Code: "EMEA", Label: "EMEA", Parent: "ROOT", Rules: []Rule{{Attribute: "region", Values: []string{"EMEA"}}}},
		{Code: "US", Label: "US", Parent: "ROOT", Rules: []Rule{{Values: []string{"US*"}}}},
	}}
	if issues := h.Validate(); len(issues) > 0 {
		t.Fatalf("valid hierarchy refused: %+v", issues)
	}
	m, err := h.Materialize([]Member{
		{ID: "c1", Key: "DE01", Attributes: map[string]string{"region": "EMEA"}},
		{ID: "c2", Key: "US10"},
		{ID: "c3", Key: "U10"},  // "US*" is a prefix, not a substring
		{ID: "c4", Key: "FR02"}, // no region attribute: matches nothing
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Assigned["c1"] != "EMEA" || m.Assigned["c2"] != "US" || fmt.Sprint(m.Unassigned) != "[c3 c4]" {
		t.Fatalf("membership = %+v", m)
	}
}

func TestHierarchy_MaterializeRefusesMembersWithoutKey(t *testing.T) {
	h := accountHierarchy(false, leaf("ASSET", "100000", "199999"))
	_, err := h.Materialize([]Member{{ID: "m1"}})
	if de, ok := err.(*Error); !ok || de.Code != ShapeCode {
		t.Fatalf("err = %v, want %s", err, ShapeCode)
	}
}

func TestHierarchy_OverlapNamesMemberAndBothLeaves(t *testing.T) {
	h := accountHierarchy(true, leaf("ASSET", "100000", "199999"), HierarchyNode{Code: "BANK", Label: "Bank", Rules: []Rule{{Values: []string{"1001*"}}}})
	_, err := h.Materialize([]Member{{ID: "acct-100100", Key: "100100"}})
	de, ok := err.(*Error)
	if !ok || de.Code != CodeHierOverlap || de.Detail != "Member acct-100100 matches leaves ASSET and BANK." {
		t.Fatalf("err = %v", err)
	}
}

func TestHierarchy_NodesNeedLabels(t *testing.T) {
	h := accountHierarchy(true, HierarchyNode{Code: "ASSET", Rules: []Rule{{From: "1", To: "2"}}})
	issues := h.Validate()
	if len(issues) != 1 || issues[0].Field != "nodes[1].label" {
		t.Fatalf("issues = %+v, want one on nodes[1].label", issues)
	}
}
