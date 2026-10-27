package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func stDay(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

// stRules has two active structures: a specific one for 6000..6999 (DEPT
// required, CC allowed values under D100) and an open catch-all at priority 20.
func stRules() Rules {
	return Rules{
		ChartCode:           "GL",
		Currency:            "USD",
		DimensionAttributes: []string{"DEPT", "CC"},
		FutureDays:          366,
		Structures: []Structure{
			{ID: "s-expense", Name: "EXPENSE", Version: 1, Priority: 10, EffectiveFrom: stDay("2026-01-01"), Status: StructureActive,
				Segments: []string{"DEPT", "CC"}, AccountFilter: []string{"6000..6999"},
				Constraints: []Constraint{
					{Segment: "DEPT", Allowed: []string{"*"}},
					{Segment: "CC", Under: map[string]string{"DEPT": "D100"}, Allowed: []string{"C200", "C210"}},
				}},
			{ID: "s-open", Name: "OPEN", Version: 1, Priority: 20, EffectiveFrom: stDay("2026-01-01"), Status: StructureActive,
				Segments: []string{"DEPT", "CC"}},
		},
	}
}

func stAccounts() map[string]Account {
	return map[string]Account{
		"6010": {ID: "a-6010", Number: "6010", Postable: true},
		"2100": {ID: "a-2100", Number: "2100", Postable: true},
	}
}

// stEntry is one balanced two-line entry: debit on acct with dims, credit 2100.
func stEntry(acct string, dims map[string]string) []LineInput {
	amt := decimal.RequireFromString("100.00")
	return []LineInput{
		{Account: acct, Dimensions: dims, Side: SideDebit, Amount: amt, Currency: "USD", Text: "x"},
		{Account: "2100", Side: SideCredit, Amount: amt, Currency: "USD", Text: "x"},
	}
}

func stCodes(vs []Violation) string {
	codes := make([]string, 0, len(vs))
	for _, v := range vs {
		codes = append(codes, v.Code)
	}
	return strings.Join(codes, ",")
}

func TestStructure_ValidateEntryAgainstStructures(t *testing.T) {
	suspended := DimensionValue{Attribute: "DEPT", Value: "D100", Suspended: true}
	cases := []struct {
		name  string
		rules func() Rules
		acct  string
		dims  map[string]string
		want  string
	}{
		{"complete expense line", stRules, "6010", map[string]string{"DEPT": "D100", "CC": "C200"}, ""},
		{"missing required DEPT", stRules, "6010", map[string]string{"CC": "C200"}, "LEDGER_DIMENSION_REQUIRED"},
		{"CC outside allowed values under D100", stRules, "6010", map[string]string{"DEPT": "D100", "CC": "C999"}, "LEDGER_DIMENSION_INVALID"},
		{"CC blank under D100", stRules, "6010", map[string]string{"DEPT": "D100"}, "LEDGER_DIMENSION_REQUIRED"},
		{"CC free under other DEPT", stRules, "6010", map[string]string{"DEPT": "D200", "CC": "C999"}, ""},
		{"no structure covers the account", func() Rules {
			r := stRules()
			r.Structures = r.Structures[:1]
			return r
		}, "6010", map[string]string{"DEPT": "D100", "CC": "C200"}, "LEDGER_STRUCTURE_NOT_FOUND"},
		{"open ledger accepts any format-valid line", func() Rules {
			r := stRules()
			r.Structures = nil
			return r
		}, "6010", map[string]string{"DEPT": "ZZ9"}, ""},
		{"dimension that is not a segment of the structure", func() Rules {
			r := stRules()
			r.Structures[0].Segments = []string{"DEPT"}
			r.Structures[0].Constraints = r.Structures[0].Constraints[:1]
			return r
		}, "6010", map[string]string{"DEPT": "D100", "CC": "C200"}, "LEDGER_DIMENSION_INVALID"},
		{"empty value counts as blank, not as an extra dimension", func() Rules {
			r := stRules()
			r.Structures[0].Segments = []string{"DEPT"}
			r.Structures[0].Constraints = r.Structures[0].Constraints[:1]
			return r
		}, "6010", map[string]string{"DEPT": "D100", "CC": ""}, ""},
		{"suspended registered value", func() Rules {
			r := stRules()
			r.DimensionValues = map[string]DimensionValue{ValueKey("DEPT", "D100"): suspended}
			return r
		}, "6010", map[string]string{"DEPT": "D100", "CC": "C200"}, "LEDGER_DIMENSION_INACTIVE"},
		{"registered value before its active_from", func() Rules {
			from := stDay("2026-06-01")
			r := stRules()
			r.DimensionValues = map[string]DimensionValue{ValueKey("DEPT", "D100"): {Attribute: "DEPT", Value: "D100", ActiveFrom: &from}}
			return r
		}, "6010", map[string]string{"DEPT": "D100", "CC": "C200"}, "LEDGER_DIMENSION_INACTIVE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rules := tc.rules()
			res := ValidateEntry(rules, stAccounts(), stDay("2026-03-31"), stDay("2026-03-31"), stEntry(tc.acct, tc.dims))
			if got := stCodes(res.Violations); got != tc.want {
				t.Fatalf("codes = %q, want %q (violations %+v)", got, tc.want, res.Violations)
			}
			if tc.want == "" && len(rules.Structures) > 0 {
				for _, l := range res.Lines {
					if l.Account.Number == tc.acct && l.StructureID == "" {
						t.Fatalf("line on %s has no structure id", tc.acct)
					}
				}
			}
		})
	}
}

func TestStructure_ViolationsCarryLineIndex(t *testing.T) {
	res := ValidateEntry(stRules(), stAccounts(), stDay("2026-03-31"), stDay("2026-03-31"), stEntry("6010", nil))
	if len(res.Violations) != 1 || res.Violations[0].LineIndex == nil || *res.Violations[0].LineIndex != 0 {
		t.Fatalf("want one DEPT violation on line 0, got %+v", res.Violations)
	}
	if res.Violations[0].Field != "lines.dimensions.DEPT" {
		t.Fatalf("field = %q", res.Violations[0].Field)
	}
}

func TestStructure_SelectStructure(t *testing.T) {
	base := Structure{Name: "A", Version: 1, Priority: 5, EffectiveFrom: stDay("2026-01-01"), Status: StructureActive,
		Segments: []string{"DEPT"}}
	cases := []struct {
		name    string
		structs []Structure
		account string
		date    string
		wantID  string
	}{
		{"lowest priority wins", []Structure{
			withID(base, "p5"), withName(withPriority(withID(base, "p1"), 1), "B")}, "6010", "2026-03-31", "p1"},
		{"equal priority falls back to name", []Structure{
			withName(withID(base, "z"), "Z"), withName(withID(base, "b"), "B")}, "6010", "2026-03-31", "b"},
		{"a newer version replaces the older one even when its filter no longer matches", []Structure{
			withFilter(withID(base, "v1"), "*"),
			withVersion(withEffective(withFilter(withID(base, "v2"), "2100"), "2026-02-01"), 2)}, "6010", "2026-03-31", ""},
		{"the older version still governs dates before the newer one", []Structure{
			withFilter(withID(base, "v1"), "*"),
			withVersion(withEffective(withFilter(withID(base, "v2"), "2100"), "2026-02-01"), 2)}, "6010", "2026-01-15", "v1"},
		{"a superseded version's priority does not matter", []Structure{
			withPriority(withID(base, "a1"), 0),
			withPriority(withVersion(withEffective(withID(base, "a2"), "2026-02-01"), 2), 9),
			withName(withPriority(withID(base, "b1"), 5), "B")}, "6010", "2026-03-31", "b1"},
		{"same effective date: highest version wins", []Structure{
			withID(base, "v1"), withVersion(withID(base, "v3"), 3)}, "6010", "2026-03-31", "v3"},
		{"future effective date is skipped", []Structure{
			withEffective(withID(base, "future"), "2027-01-01")}, "6010", "2026-03-31", ""},
		{"draft and retired never govern", []Structure{
			withStatus(withID(base, "d"), StructureDraft), withStatus(withID(base, "r"), StructureRetired)}, "6010", "2026-03-31", ""},
		{"account filter exact", []Structure{
			withFilter(withID(base, "x"), "2100")}, "6010", "2026-03-31", ""},
		{"account filter range", []Structure{
			withFilter(withID(base, "r"), "6000..6999")}, "6010", "2026-03-31", "r"},
		{"account filter wildcard", []Structure{
			withFilter(withID(base, "w"), "*")}, "6010", "2026-03-31", "w"},
		{"latest effective wins within a name", []Structure{
			withEffective(withID(base, "old"), "2026-01-01"), withEffective(withID(base, "new"), "2026-02-01")}, "6010", "2026-03-31", "new"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := SelectStructure(tc.structs, tc.account, stDay(tc.date))
			if (tc.wantID != "") != ok || got.ID != tc.wantID {
				t.Fatalf("SelectStructure = (%q, %v), want %q", got.ID, ok, tc.wantID)
			}
		})
	}
}

func withID(s Structure, id string) Structure     { s.ID = id; return s }
func withName(s Structure, name string) Structure { s.Name = name; return s }
func withVersion(s Structure, v int) Structure    { s.Version = v; return s }
func withPriority(s Structure, p int) Structure {
	s.Priority = p
	return s
}
func withEffective(s Structure, d string) Structure {
	s.EffectiveFrom = stDay(d)
	return s
}
func withStatus(s Structure, st string) Structure { s.Status = st; return s }
func withFilter(s Structure, toks ...string) Structure {
	s.AccountFilter = toks
	return s
}

func TestStructure_ValidateStructure(t *testing.T) {
	attrs := []string{"DEPT", "CC"}
	good := Structure{Name: "EXPENSE", Version: 1, Status: StructureDraft, Segments: []string{"DEPT", "CC"},
		AccountFilter: []string{"6000..6999"},
		Constraints:   []Constraint{{Segment: "CC", Under: map[string]string{"DEPT": "D100"}, Allowed: []string{"C200", "blank"}}}}
	cases := []struct {
		name   string
		mutate func(Structure) Structure
		ok     bool
	}{
		{"valid", func(s Structure) Structure { return s }, true},
		{"bad name", func(s Structure) Structure { s.Name = "bad name"; return s }, false},
		{"version zero", func(s Structure) Structure { s.Version = 0; return s }, false},
		{"unknown status", func(s Structure) Structure { s.Status = "live"; return s }, false},
		{"main account listed as segment", func(s Structure) Structure {
			s.Segments = []string{"MAIN_ACCOUNT", "DEPT"}
			return s
		}, false},
		{"segment not on ledger", func(s Structure) Structure { s.Segments = []string{"PROJECT"}; return s }, false},
		{"duplicate segment", func(s Structure) Structure { s.Segments = []string{"DEPT", "DEPT"}; return s }, false},
		{"constraint on unlisted segment", func(s Structure) Structure {
			s.Constraints = []Constraint{{Segment: "PROJECT", Allowed: []string{"*"}}}
			return s
		}, false},
		{"under must be an earlier segment", func(s Structure) Structure {
			s.Constraints = []Constraint{{Segment: "DEPT", Under: map[string]string{"CC": "C200"}, Allowed: []string{"*"}}}
			return s
		}, false},
		{"constraint without tokens", func(s Structure) Structure {
			s.Constraints = []Constraint{{Segment: "DEPT"}}
			return s
		}, false},
		{"bad filter token", func(s Structure) Structure { s.AccountFilter = []string{"blank"}; return s }, false},
		{"reversed range", func(s Structure) Structure { s.AccountFilter = []string{"6999..6000"}; return s }, false},
		{"main account only (no segments)", func(s Structure) Structure {
			s.Segments, s.Constraints = nil, nil
			return s
		}, true},
		{"under the main account with a range", func(s Structure) Structure {
			s.Constraints = []Constraint{{Segment: "DEPT", Under: map[string]string{"MAIN_ACCOUNT": "6000..6099"}, Allowed: []string{"*"}}}
			return s
		}, true},
		{"constraint on the main account", func(s Structure) Structure {
			s.Constraints = []Constraint{{Segment: "MAIN_ACCOUNT", Allowed: []string{"*"}}}
			return s
		}, false},
		{"duplicate rows are ambiguous", func(s Structure) Structure {
			s.Constraints = []Constraint{
				{Segment: "CC", Under: map[string]string{"DEPT": "D100"}, Allowed: []string{"C200"}},
				{Segment: "CC", Under: map[string]string{"DEPT": "D100", "MAIN_ACCOUNT": "*"}, Allowed: []string{"C300"}},
			}
			return s
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vs := ValidateStructure(tc.mutate(good), attrs)
			if (len(vs) == 0) != tc.ok {
				t.Fatalf("ok = %v, want %v (violations %+v)", len(vs) == 0, tc.ok, vs)
			}
		})
	}
}

func TestStructure_ValidateDimensionValue(t *testing.T) {
	from, to := stDay("2026-06-01"), stDay("2026-01-01")
	cases := []struct {
		name string
		v    DimensionValue
		ok   bool
	}{
		{"valid", DimensionValue{Attribute: "DEPT", Value: "D100", Name: "Sales"}, true},
		{"unknown attribute", DimensionValue{Attribute: "PROJECT", Value: "P1"}, false},
		{"bad value", DimensionValue{Attribute: "DEPT", Value: "D 100"}, false},
		{"active_to before active_from", DimensionValue{Attribute: "DEPT", Value: "D100", ActiveFrom: &from, ActiveTo: &to}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(ValidateDimensionValue(tc.v, []string{"DEPT", "CC"})) == 0; got != tc.ok {
				t.Fatalf("ok = %v, want %v", got, tc.ok)
			}
		})
	}
}

func TestStructure_DimensionValueActiveOn(t *testing.T) {
	from, to := stDay("2026-02-01"), stDay("2026-02-28")
	cases := []struct {
		name string
		v    DimensionValue
		date string
		want bool
	}{
		{"open ended", DimensionValue{}, "2000-01-01", true},
		{"before from", DimensionValue{ActiveFrom: &from}, "2026-01-31", false},
		{"on from", DimensionValue{ActiveFrom: &from}, "2026-02-01", true},
		{"after to", DimensionValue{ActiveTo: &to}, "2026-03-01", false},
		{"on to", DimensionValue{ActiveTo: &to}, "2026-02-28", true},
		{"suspended overrides dates", DimensionValue{Suspended: true}, "2026-02-10", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.v.ActiveOn(stDay(tc.date)); got != tc.want {
				t.Fatalf("ActiveOn(%s) = %v, want %v", tc.date, got, tc.want)
			}
		})
	}
}

// TestStructure_AC11_4 is AC-11.4: account 6010 requires DEPT under the active
// structure, written as a constraint row under MAIN_ACCOUNT, while other
// accounts of the same structure may leave DEPT blank.
func TestStructure_AC11_4(t *testing.T) {
	r := Rules{ChartCode: "GL", Currency: "USD", DimensionAttributes: []string{"DEPT", "CC"}, FutureDays: 366,
		Structures: []Structure{{ID: "s1", Name: "PL", Version: 1, EffectiveFrom: stDay("2026-01-01"), Status: StructureActive,
			Segments: []string{"DEPT", "CC"},
			Constraints: []Constraint{
				{Segment: "DEPT", Under: map[string]string{"MAIN_ACCOUNT": "6010"}, Allowed: []string{"*"}},
				{Segment: "DEPT", Allowed: []string{"*", "blank"}},
			}}}}
	accounts := stAccounts()
	accounts["6020"] = Account{ID: "a-6020", Number: "6020", Postable: true}
	res := ValidateEntry(r, accounts, stDay("2026-03-31"), stDay("2026-03-31"), stEntry("6010", map[string]string{"CC": "C200"}))
	if len(res.Violations) != 1 {
		t.Fatalf("want one violation, got %+v", res.Violations)
	}
	v := res.Violations[0]
	if v.Code != CodeDimensionRequired || v.LineIndex == nil || *v.LineIndex != 0 || v.Field != "lines.dimensions.DEPT" {
		t.Fatalf("want LEDGER_DIMENSION_REQUIRED on line 0 DEPT, got %+v", v)
	}
	if !strings.Contains(v.Message, "6010") {
		t.Fatalf("message should name the account: %q", v.Message)
	}
	if res := ValidateEntry(r, accounts, stDay("2026-03-31"), stDay("2026-03-31"), stEntry("6020", nil)); !res.Valid() {
		t.Fatalf("6020 may leave DEPT blank: %+v", res.Violations)
	}
}

// TestStructure_EveryViolationInOnePass checks that structure faults are
// reported alongside other line faults instead of hiding behind them.
func TestStructure_EveryViolationInOnePass(t *testing.T) {
	lines := stEntry("6010", map[string]string{"CC": "C200"})
	lines[0].Currency = "EUR"
	r := stRules()
	r.DimensionValues = map[string]DimensionValue{ValueKey("CC", "C200"): {Attribute: "CC", Value: "C200", Suspended: true}}
	res := ValidateEntry(r, stAccounts(), stDay("2026-03-31"), stDay("2026-03-31"), lines)
	if got := stCodes(res.Violations); got != "LEDGER_CURRENCY_MISMATCH,LEDGER_DIMENSION_INACTIVE,LEDGER_DIMENSION_REQUIRED" {
		t.Fatalf("codes = %q", got)
	}
}

// TestStructure_CombinationCarriesStructureVersion is FR-11.4: the same tuple
// under two structure versions is two combinations, and a line on a ledger
// without structures keeps the structure-less hash.
func TestStructure_CombinationCarriesStructureVersion(t *testing.T) {
	attrs := []string{"DEPT", "CC"}
	dims := map[string]string{"DEPT": "D100"}
	plain, display := CombinationID(attrs, "GL", "6010", dims)
	none, _ := CombinationIDFor("", attrs, "GL", "6010", dims)
	v1, displayV1 := CombinationIDFor("s1", attrs, "GL", "6010", dims)
	v2, _ := CombinationIDFor("s2", attrs, "GL", "6010", dims)
	if string(plain) != string(none) {
		t.Fatal("an empty structure id must give the structure-less hash")
	}
	if string(v1) == string(plain) || string(v1) == string(v2) {
		t.Fatal("the structure version must be part of the combination hash")
	}
	if display != "6010-D100" || displayV1 != display {
		t.Fatalf("display names %q, %q", display, displayV1)
	}
	r := stRules()
	res := ValidateEntry(r, stAccounts(), stDay("2026-03-31"), stDay("2026-03-31"), stEntry("6010", map[string]string{"DEPT": "D100", "CC": "C200"}))
	if !res.Valid() || res.Lines[0].StructureID != "s-expense" || res.Lines[1].StructureID != "s-open" {
		t.Fatalf("lines: %+v", res.Lines)
	}
	want, _ := CombinationIDFor("s-expense", r.DimensionAttributes, r.ChartCode, "6010", map[string]string{"DEPT": "D100", "CC": "C200"})
	if string(res.Lines[0].CombinationID) != string(want) {
		t.Fatal("the posted line must carry the structure-scoped combination")
	}
}

func TestStructure_EditAllowed(t *testing.T) {
	base := Structure{ID: "s1", Name: "EXPENSE", Version: 1, EffectiveFrom: stDay("2026-01-01"), Status: StructureDraft,
		Segments: []string{"DEPT"}, Constraints: []Constraint{{Segment: "DEPT", Under: map[string]string{"MAIN_ACCOUNT": "6010"}, Allowed: []string{"*"}}}}
	with := func(s Structure, mut func(*Structure)) Structure {
		s.Constraints = append([]Constraint(nil), s.Constraints...)
		mut(&s)
		return s
	}
	active := with(base, func(s *Structure) { s.Status = StructureActive })
	cases := []struct {
		name       string
		prev, next Structure
		want       bool
	}{
		{"draft changes freely and activates", base, with(base, func(s *Structure) { s.Segments = []string{"CC"}; s.Status = StructureActive }), true},
		{"active re-sent unchanged", active, active, true},
		{"active to retired with same content", active, with(base, func(s *Structure) { s.Status = StructureRetired }), true},
		{"active to retired with changed content", active, with(base, func(s *Structure) { s.Status = StructureRetired; s.Priority = 3 }), false},
		{"active content change refused", active, with(active, func(s *Structure) { s.Segments = []string{"CC"} }), false},
		{"active constraint change refused", active, with(active, func(s *Structure) {
			s.Constraints[0] = Constraint{Segment: "DEPT", Under: map[string]string{"MAIN_ACCOUNT": "6020"}, Allowed: []string{"*"}}
		}), false},
		{"active back to draft refused", active, base, false},
		{"retired cannot be reactivated", with(base, func(s *Structure) { s.Status = StructureRetired }), active, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StructureEditAllowed(tc.prev, tc.next); got != tc.want {
				t.Fatalf("StructureEditAllowed = %v, want %v", got, tc.want)
			}
		})
	}
}
