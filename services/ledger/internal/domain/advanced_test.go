package domain

import (
	"slices"
	"testing"
	"time"
)

var d2026 = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

// ac1118Structure is the expense structure with DEPT; the advanced rule of AC-11.18 adds PROJECT
// to accounts 5000..5999 when DEPT is D100.
func ac1118Structure() Structure {
	return Structure{ID: "s1", Name: "PL", Version: 1, EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Status: StructureActive,
		Segments: []string{"DEPT"}, AccountFilter: []string{"5000..5999"},
		AdvancedRules: []AdvancedRule{{ID: "r1", Name: "PROJECT_ON_D100", Accounts: []string{"5000..5999"}, Dimensions: map[string]string{"DEPT": "D100"},
			Segments: []string{"PROJECT"}, Constraints: []Constraint{{Segment: "PROJECT", Allowed: []string{"*"}}}}}}
}

func TestAC11_18_AdvancedRuleRequiresAppendedSegment(t *testing.T) {
	structs := []Structure{ac1118Structure()}
	faults, sid := checkStructure(structs, "5100", map[string]string{"DEPT": "D100"}, d2026)
	if sid != "s1" || len(faults) != 1 || faults[0].Code != CodeDimensionRequired || faults[0].Field != "lines.dimensions.PROJECT" {
		t.Fatalf("5100/D100 without PROJECT: %+v", faults)
	}
	if faults, _ := checkStructure(structs, "5100", map[string]string{"DEPT": "D100", "PROJECT": "P1"}, d2026); len(faults) != 0 {
		t.Fatalf("with PROJECT: %+v", faults)
	}
	if faults, _ := checkStructure(structs, "5100", map[string]string{"DEPT": "D200"}, d2026); len(faults) != 0 {
		t.Fatalf("5100/D200 must post without PROJECT: %+v", faults)
	}
	// Without the rule matching, PROJECT is not a segment of the structure.
	faults, _ = checkStructure(structs, "5100", map[string]string{"DEPT": "D200", "PROJECT": "P1"}, d2026)
	if len(faults) != 1 || faults[0].Code != CodeDimensionInvalid {
		t.Fatalf("5100/D200 with PROJECT: %+v", faults)
	}
	// Outside the account range the rule does not apply either.
	st := ac1118Structure()
	st.AccountFilter = nil
	if faults, _ := checkStructure([]Structure{st}, "6100", map[string]string{"DEPT": "D100"}, d2026); len(faults) != 0 {
		t.Fatalf("6100/D100: %+v", faults)
	}
}

func TestAdvancedRulesAppendInPriorityOrderAndCap(t *testing.T) {
	st := Structure{Segments: []string{"DEPT"}, AdvancedRules: []AdvancedRule{
		{Name: "B", Priority: 2, Segments: []string{"CC"}},
		{Name: "A", Priority: 1, Dimensions: map[string]string{"DEPT": "*"}, Segments: []string{"PROJECT", "CC"}},
		{Name: "N", Priority: 0, Dimensions: map[string]string{"DEPT": "blank"}, Segments: []string{"NOPE"}},
	}}
	got, ok := st.WithAdvancedRules("5100", map[string]string{"DEPT": "D1"})
	if !ok || !slices.Equal(got.Segments, []string{"DEPT", "PROJECT", "CC"}) {
		t.Fatalf("segments %v ok=%v", got.Segments, ok)
	}
	if !slices.Equal(st.Segments, []string{"DEPT"}) {
		t.Fatalf("the structure must not be modified: %v", st.Segments)
	}
	var many []string
	for i := range 20 {
		many = append(many, "A"+string(rune('A'+i)))
	}
	st.AdvancedRules = []AdvancedRule{{Name: "BIG", Segments: many}}
	if _, ok := st.WithAdvancedRules("5100", nil); ok {
		t.Fatal("21 segments must exceed the cap")
	}
	faults, _ := checkStructure([]Structure{{ID: "s", Name: "X", Status: StructureActive, EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Segments: st.Segments, AdvancedRules: st.AdvancedRules}}, "5100", nil, d2026)
	if len(faults) != 1 || faults[0].Code != CodeAdvancedRuleSegmentLimit {
		t.Fatalf("cap violation: %+v", faults)
	}
}

func TestValidateAdvancedRule(t *testing.T) {
	attrs := []string{"DEPT", "CC", "PROJECT"}
	st := Structure{Name: "PL", Segments: []string{"DEPT"}}
	ok := AdvancedRule{Name: "R1", Accounts: []string{"5000..5999"}, Dimensions: map[string]string{"DEPT": "D100"}, Segments: []string{"PROJECT"},
		Constraints: []Constraint{{Segment: "PROJECT", Under: map[string]string{"MAIN_ACCOUNT": "5100", "DEPT": "D100"}, Allowed: []string{"*", "blank"}}}}
	if vs := ValidateAdvancedRule(ok, st, attrs); len(vs) != 0 {
		t.Fatalf("valid rule: %+v", vs)
	}
	cases := map[string]func(r *AdvancedRule){
		"bad name":              func(r *AdvancedRule) { r.Name = "no spaces" },
		"negative priority":     func(r *AdvancedRule) { r.Priority = -1 },
		"blank account token":   func(r *AdvancedRule) { r.Accounts = []string{"blank"} },
		"unknown filter attr":   func(r *AdvancedRule) { r.Dimensions = map[string]string{"ZZ": "1"} },
		"bad filter token":      func(r *AdvancedRule) { r.Dimensions = map[string]string{"DEPT": "a b"} },
		"empty filter":          func(r *AdvancedRule) { r.Accounts, r.Dimensions = nil, nil },
		"no segments":           func(r *AdvancedRule) { r.Segments, r.Constraints = nil, nil },
		"unknown segment":       func(r *AdvancedRule) { r.Segments = []string{"ZZ"}; r.Constraints = nil },
		"segment in structure":  func(r *AdvancedRule) { r.Segments = []string{"DEPT"}; r.Constraints = nil },
		"duplicate segment":     func(r *AdvancedRule) { r.Segments = []string{"CC", "CC"}; r.Constraints = nil },
		"constraint off rule":   func(r *AdvancedRule) { r.Constraints[0].Segment = "CC" },
		"constraint own under":  func(r *AdvancedRule) { r.Constraints[0].Under = map[string]string{"PROJECT": "P1"} },
		"constraint unknown up": func(r *AdvancedRule) { r.Constraints[0].Under = map[string]string{"ZZ": "1"} },
		"no allowed":            func(r *AdvancedRule) { r.Constraints[0].Allowed = nil },
		"bad allowed":           func(r *AdvancedRule) { r.Constraints[0].Allowed = []string{"x y"} },
		"duplicate row": func(r *AdvancedRule) {
			r.Constraints = append(r.Constraints, r.Constraints[0])
		},
	}
	for name, mutate := range cases {
		r := ok
		r.Constraints = slices.Clone(ok.Constraints)
		r.Constraints[0].Under = map[string]string{"MAIN_ACCOUNT": "5100"}
		mutate(&r)
		if vs := ValidateAdvancedRule(r, st, attrs); len(vs) == 0 || vs[0].Code != CodeAdvancedRuleInvalid {
			t.Errorf("%s: want LEDGER_ADVANCED_RULE_INVALID, got %+v", name, vs)
		}
	}
	long := Structure{Name: "PL", Segments: make([]string, 20)}
	if vs := ValidateAdvancedRule(ok, long, attrs); !slices.ContainsFunc(vs, func(v Violation) bool { return v.Code == CodeAdvancedRuleSegmentLimit }) {
		t.Fatalf("segment cap at save: %+v", vs)
	}
}

// Overlapping filters: when several matching rules constrain the same added segment, the most
// specific row governs (FR-11.3 semantics); on a tie the rule applied first (lower priority, then
// name) wins, whatever order the rules were stored in.
func TestAdvancedRulesOverlappingFilters(t *testing.T) {
	st := Structure{ID: "s", Name: "PL", Status: StructureActive, EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Segments: []string{"DEPT"}, AdvancedRules: []AdvancedRule{
			{Name: "WIDE", Priority: 5, Accounts: []string{"5000..5999"}, Segments: []string{"PROJECT"},
				Constraints: []Constraint{{Segment: "PROJECT", Allowed: []string{"P2"}}}},
			{Name: "NARROW", Priority: 1, Dimensions: map[string]string{"DEPT": "D100"}, Segments: []string{"PROJECT"},
				Constraints: []Constraint{{Segment: "PROJECT", Allowed: []string{"P1"}}}},
			{Name: "SPECIFIC", Priority: 9, Dimensions: map[string]string{"DEPT": "D100"}, Segments: []string{"PROJECT"},
				Constraints: []Constraint{{Segment: "PROJECT", Under: map[string]string{MainAccountSegment: "5500"}, Allowed: []string{"P3"}}}},
		}}
	check := func(account, project string) []Violation {
		faults, _ := checkStructure([]Structure{st}, account, map[string]string{"DEPT": "D100", "PROJECT": project}, d2026)
		return faults
	}
	// Both WIDE and NARROW match 5100/D100 with an unconditioned row: NARROW (priority 1) is applied first and governs.
	if faults := check("5100", "P1"); len(faults) != 0 {
		t.Fatalf("P1 under the first rule: %+v", faults)
	}
	if faults := check("5100", "P2"); len(faults) != 1 || faults[0].Code != CodeDimensionInvalid {
		t.Fatalf("P2 is refused by the first rule's row: %+v", faults)
	}
	// On 5500 the SPECIFIC row (one condition) is more specific than either, despite its priority.
	if faults := check("5500", "P3"); len(faults) != 0 {
		t.Fatalf("P3 under the most specific row: %+v", faults)
	}
	// Outside DEPT D100 only WIDE matches.
	faults, _ := checkStructure([]Structure{st}, "5100", map[string]string{"DEPT": "D200", "PROJECT": "P2"}, d2026)
	if len(faults) != 0 {
		t.Fatalf("WIDE alone: %+v", faults)
	}
}
