package domain

import (
	"strings"
	"testing"
	"time"
)

var (
	derivDay  = time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	attrNames = []string{"COST_CENTER", "FUNC_AREA", "SEGMENT"}
)

func lookupSteps() []DerivationStep {
	return []DerivationStep{{ID: "s1", Sequence: 10, Kind: StepTableLookup, SourceFields: []string{FieldAccount, "COST_CENTER"},
		Target: "FUNC_AREA", LookupTableID: "t1"}}
}

func TestDerive_AC11_15_LookupFillsFunctionalArea(t *testing.T) {
	rows := map[string][]LookupRow{"t1": {{Source: []string{"5010", "C200"}, Target: "PROD", ValidFrom: "2026-01-01"}}}
	dims, trace := Derive(lookupSteps(), rows, derivDay, Account{Number: "5010", Type: "expense"}, map[string]string{"COST_CENTER": "C200"})
	if dims["FUNC_AREA"] != "PROD" || dims["COST_CENTER"] != "C200" {
		t.Fatalf("dims = %v", dims)
	}
	if len(trace) != 1 || trace[0].StepID != "s1" || trace[0].Before != "" || trace[0].After != "PROD" || len(trace[0].MatchedRow) != 2 {
		t.Fatalf("trace = %+v", trace)
	}
	if ids := StepIDs(trace); len(ids) != 1 || ids[0] != "s1" {
		t.Fatalf("step ids = %v", ids)
	}
}

func TestDerive_ExplicitValueWinsEvenOverOverwrite(t *testing.T) {
	// FR-11.22 precedence: explicit line value > step with overwrite > step without overwrite.
	rows := map[string][]LookupRow{"t1": {{Source: []string{"5010", "C200"}, Target: "PROD", ValidFrom: "2026-01-01"}}}
	in := map[string]string{"COST_CENTER": "C200", "FUNC_AREA": "ADMIN"}
	steps := lookupSteps()
	steps[0].Overwrite = true
	steps = append(steps, DerivationStep{ID: "c", Sequence: 20, Kind: StepClear, Target: "FUNC_AREA", Overwrite: true})
	if dims, trace := Derive(steps, rows, derivDay, Account{Number: "5010"}, in); dims["FUNC_AREA"] != "ADMIN" || len(trace) != 0 {
		t.Fatalf("explicit value changed by a step: %v %+v", dims, trace)
	}
	if in["FUNC_AREA"] != "ADMIN" {
		t.Fatal("Derive changed its input")
	}
	// A blank value in the request is not explicit: steps fill it.
	if dims, _ := Derive(lookupSteps(), rows, derivDay, Account{Number: "5010"}, map[string]string{"COST_CENTER": "C200", "FUNC_AREA": ""}); dims["FUNC_AREA"] != "PROD" {
		t.Fatalf("blank request value not derived: %v", dims)
	}
}

func TestDerive_OverwriteReplacesOnlyEarlierSteps(t *testing.T) {
	base := []DerivationStep{
		{ID: "a", Sequence: 10, Kind: StepConstant, Target: "SEGMENT", Value: "FIRST"},
		{ID: "b", Sequence: 20, Kind: StepConstant, Target: "SEGMENT", Value: "SECOND"},
	}
	dims, trace := Derive(base, nil, derivDay, Account{Number: "5010"}, nil)
	if dims["SEGMENT"] != "FIRST" || len(trace) != 1 || trace[0].StepID != "a" {
		t.Fatalf("a later step without overwrite replaced an earlier one: %v %+v", dims, trace)
	}
	base[1].Overwrite = true
	dims, trace = Derive(base, nil, derivDay, Account{Number: "5010"}, nil)
	if dims["SEGMENT"] != "SECOND" || len(trace) != 2 || trace[1].Before != "FIRST" || trace[1].After != "SECOND" {
		t.Fatalf("overwrite did not replace the earlier step: %v %+v", dims, trace)
	}
	if ids := StepIDs(trace); len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("step ids = %v", ids)
	}
}

func TestLookupMatch_MostSpecificRowThenLatestValidFrom(t *testing.T) {
	to := "2026-02-28"
	rows := []LookupRow{
		{Source: []string{"*", "*"}, Target: "ANY", ValidFrom: "2026-01-01"},
		{Source: []string{"5010", "*"}, Target: "ACC", ValidFrom: "2026-01-01"},
		{Source: []string{"5010", "C200"}, Target: "OLD", ValidFrom: "2025-01-01"},
		{Source: []string{"5010", "C200"}, Target: "NEW", ValidFrom: "2026-02-01"},
		{Source: []string{"5010", "C200"}, Target: "EXPIRED", ValidFrom: "2026-01-01", ValidTo: &to},
		{Source: []string{"5010", "C200"}, Target: "FUTURE", ValidFrom: "2026-04-01"},
	}
	if got, _, ok := LookupMatch(rows, []string{"5010", "C200"}, derivDay); !ok || got != "NEW" {
		t.Fatalf("exact row: %q %v", got, ok)
	}
	if got, _, _ := LookupMatch(rows, []string{"5010", "C300"}, derivDay); got != "ACC" {
		t.Fatalf("one wildcard: %q", got)
	}
	if got, _, _ := LookupMatch(rows, []string{"6000", ""}, derivDay); got != "ANY" {
		t.Fatalf("blank matches only wildcard: %q", got)
	}
	if _, _, ok := LookupMatch(rows[2:3], []string{"5010", ""}, derivDay); ok {
		t.Fatal("a blank value must not match an exact row")
	}
}

func TestDerive_StepKindsConditionsAndOrder(t *testing.T) {
	steps := []DerivationStep{
		{ID: "c", Sequence: 30, Kind: StepClear, Target: "SEGMENT", Overwrite: true,
			Condition: &StepCondition{Field: FieldAccountType, Op: CondEq, Values: []string{"revenue"}}},
		{ID: "k", Sequence: 10, Kind: StepConstant, Target: "SEGMENT", Value: "MFG"},
		{ID: "m", Sequence: 20, Kind: StepMove, SourceFields: []string{"COST_CENTER"}, Target: "FUNC_AREA",
			Condition: &StepCondition{Field: FieldAccount, Op: CondIn, Values: []string{"5010", "5020"}}},
	}
	dims, trace := Derive(steps, nil, derivDay, Account{Number: "5010", Type: "expense"}, map[string]string{"COST_CENTER": "C1"})
	if dims["SEGMENT"] != "MFG" || dims["FUNC_AREA"] != "C1" || len(trace) != 2 || trace[0].StepID != "k" || trace[1].StepID != "m" {
		t.Fatalf("expense line: %v %+v", dims, trace)
	}
	dims, trace = Derive(steps, nil, derivDay, Account{Number: "4000", Type: "revenue"}, map[string]string{"COST_CENTER": "C1"})
	if _, ok := dims["SEGMENT"]; ok || dims["FUNC_AREA"] != "" || len(trace) != 2 || trace[1].Kind != StepClear || trace[1].After != "" {
		t.Fatalf("revenue line: %v %+v", dims, trace)
	}
}

func TestConditionOps(t *testing.T) {
	for _, tc := range []struct {
		c    StepCondition
		v    string
		want bool
	}{
		{StepCondition{Op: CondEq, Values: []string{"a"}}, "a", true},
		{StepCondition{Op: CondNe, Values: []string{"a"}}, "a", false},
		{StepCondition{Op: CondIn, Values: []string{"a", "b"}}, "b", true},
		{StepCondition{Op: CondBlank}, "", true},
		{StepCondition{Op: CondNotBlank}, "", false},
		{StepCondition{Op: "bogus"}, "a", false},
	} {
		if got := conditionHolds(&tc.c, tc.v); got != tc.want {
			t.Errorf("%+v on %q = %v", tc.c, tc.v, got)
		}
	}
}

func TestValidateStrategy_BR11_14(t *testing.T) {
	tables := map[string]LookupTable{"t1": {ID: "t1", Name: "F", SourceFields: []string{FieldAccount, "COST_CENTER"}, Target: "FUNC_AREA"}}
	ok := DerivationStrategy{Steps: lookupSteps()}
	if d := ValidateStrategy(ok, attrNames, tables, true); len(d) != 0 {
		t.Fatalf("valid strategy refused: %v", d)
	}
	many := DerivationStrategy{}
	for i := 1; i <= MaxDerivationSteps+1; i++ {
		many.Steps = append(many.Steps, DerivationStep{ID: "x", Sequence: i, Kind: StepConstant, Target: "SEGMENT", Value: "A"})
	}
	cases := map[string]DerivationStrategy{
		"at most 50 steps":                 many,
		"at least one step":                {},
		"which it also writes":             {Steps: []DerivationStep{{Sequence: 1, Kind: StepMove, SourceFields: []string{"SEGMENT"}, Target: "SEGMENT"}}},
		"condition reads SEGMENT":          {Steps: []DerivationStep{{Sequence: 1, Kind: StepConstant, Value: "A", Target: "SEGMENT", Condition: &StepCondition{Field: "SEGMENT", Op: CondBlank}}}},
		"not a dimension attribute":        {Steps: []DerivationStep{{Sequence: 1, Kind: StepConstant, Value: "A", Target: "ACCOUNT"}}},
		"clear needs overwrite":            {Steps: []DerivationStep{{Sequence: 1, Kind: StepClear, Target: "SEGMENT"}}},
		"must be greater than":             {Steps: []DerivationStep{{Sequence: 2, Kind: StepConstant, Value: "A", Target: "SEGMENT"}, {Sequence: 2, Kind: StepConstant, Value: "B", Target: "FUNC_AREA"}}},
		"does not exist":                   {Steps: []DerivationStep{{Sequence: 1, Kind: StepTableLookup, SourceFields: []string{FieldAccount}, Target: "FUNC_AREA", LookupTableID: "gone"}}},
		"must equal those of lookup table": {Steps: []DerivationStep{{Sequence: 1, Kind: StepTableLookup, SourceFields: []string{FieldAccount}, Target: "FUNC_AREA", LookupTableID: "t1"}}},
		"must be table_lookup":             {Steps: []DerivationStep{{Sequence: 1, Kind: "expression", Target: "SEGMENT"}}},
		"needs exactly one value":          {Steps: []DerivationStep{{Sequence: 1, Kind: StepConstant, Value: "A", Target: "SEGMENT", Condition: &StepCondition{Field: FieldAccount, Op: CondEq}}}},
		"exactly one source field":         {Steps: []DerivationStep{{Sequence: 1, Kind: StepMove, Target: "SEGMENT"}}},
		"constant value":                   {Steps: []DerivationStep{{Sequence: 1, Kind: StepConstant, Value: "bad value", Target: "SEGMENT"}}},
		"is unknown":                       {Steps: []DerivationStep{{Sequence: 1, Kind: StepMove, SourceFields: []string{"NOPE"}, Target: "SEGMENT"}}},
		"used by an earlier step":          {Steps: []DerivationStep{{ID: "x", Sequence: 1, Kind: StepConstant, Value: "A", Target: "SEGMENT"}, {ID: "x", Sequence: 2, Kind: StepConstant, Value: "B", Target: "FUNC_AREA"}}},
	}
	for want, s := range cases {
		details := ValidateStrategy(s, attrNames, tables, true)
		if !strings.Contains(strings.Join(details, "|"), want) {
			t.Errorf("%s: details %v", want, details)
		}
	}
	// A draft save does not resolve table references.
	if d := ValidateStrategy(cases["does not exist"], attrNames, nil, false); len(d) != 0 {
		t.Errorf("draft save resolved tables: %v", d)
	}
}

func TestValidateLookupTable(t *testing.T) {
	good := LookupTable{Name: "F", SourceFields: []string{FieldAccount, "COST_CENTER"}, Target: "FUNC_AREA",
		Rows: []LookupRow{{Source: []string{"5010", "*"}, Target: "PROD", ValidFrom: "2026-01-01"}}}
	if p := ValidateLookupTable(good, attrNames); len(p) != 0 {
		t.Fatalf("valid table refused: %v", p)
	}
	to := "2025-12-31"
	bad := map[string]LookupTable{
		"sourceFields": {Name: "F", SourceFields: []string{"NOPE"}, Target: "FUNC_AREA"},
		"target":       {Name: "F", SourceFields: []string{FieldAccount}, Target: "ACCOUNT"},
		"name":         {Name: "", SourceFields: []string{FieldAccount}, Target: "FUNC_AREA"},
		"rows[0]":      {Name: "F", SourceFields: []string{FieldAccount}, Target: "FUNC_AREA", Rows: []LookupRow{{Source: []string{"a", "b"}, Target: "P", ValidFrom: "2026-01-01"}}},
		"rows[1]":      {Name: "F", SourceFields: []string{FieldAccount}, Target: "FUNC_AREA", Rows: []LookupRow{{Source: []string{"a"}, Target: "P", ValidFrom: "2026-01-01"}, {Source: []string{"a"}, Target: "Q", ValidFrom: "2026-01-01"}}},
		"rows":         {Name: "F", SourceFields: []string{FieldAccount}, Target: "FUNC_AREA", Rows: make([]LookupRow, MaxLookupRows+1)},
	}
	bad["rows[0]"].Rows[0].ValidTo = &to
	for field, tbl := range bad {
		if p := ValidateLookupTable(tbl, attrNames); p[field] == "" {
			t.Errorf("%s: %v", field, p)
		}
	}
}
