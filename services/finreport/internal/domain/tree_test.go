package domain

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func fixtureTree() Tree {
	return Tree{Code: "PC-TREE", Name: "Profit centers", Attribute: "profit_center", Units: []TreeUnit{
		{Code: "ALL", Name: "Group"},
		{Code: "EMEA", Name: "EMEA", Parent: "ALL"},
		{Code: "DE", Name: "Germany", Parent: "EMEA", Values: []string{"PC10", "PC11"}},
		{Code: "FR", Name: "France", Parent: "EMEA", Values: []string{"PC20"}},
		{Code: "US", Name: "United States", Parent: "ALL", Values: []string{"PC30"}},
	}}
}

func TestTreeValidate_AcceptsAFixtureTree(t *testing.T) {
	if issues := fixtureTree().Validate(); len(issues) != 0 {
		t.Fatalf("unexpected issues: %+v", issues)
	}
}

func TestTreeValidate_Rejects(t *testing.T) {
	cases := map[string]struct {
		edit  func(*Tree)
		field string
		text  string
	}{
		"bad code":        {func(x *Tree) { x.Code = "x" }, "code", "2 to 30"},
		"no name":         {func(x *Tree) { x.Name = "" }, "name", "1 to 120"},
		"no attribute":    {func(x *Tree) { x.Attribute = "" }, "attribute", "dimension attribute"},
		"no units":        {func(x *Tree) { x.Units = nil }, "units", "at least one"},
		"too many units":  {func(x *Tree) { x.Units = make([]TreeUnit, MaxTreeUnits+1) }, "units", "at most"},
		"duplicate unit":  {func(x *Tree) { x.Units[3].Code = "DE" }, "units[3].code", "duplicate"},
		"bad unit code":   {func(x *Tree) { x.Units[3].Code = "fr" }, "units[3].code", "upper-case"},
		"unknown parent":  {func(x *Tree) { x.Units[3].Parent = "NOPE" }, "units[3].parent", "does not exist"},
		"own parent":      {func(x *Tree) { x.Units[3].Parent = "FR" }, "units[3].parent", "own parent"},
		"empty leaf":      {func(x *Tree) { x.Units[3].Values = nil }, "units[3].values", "at least one value"},
		"overlap":         {func(x *Tree) { x.Units[3].Values = []string{"PC10"} }, "units[3].values[0]", "already belongs to unit DE"},
		"twice":           {func(x *Tree) { x.Units[2].Values = []string{"PC10", "PC10"} }, "units[2].values[1]", "listed twice"},
		"blank value":     {func(x *Tree) { x.Units[2].Values = []string{" "} }, "units[2].values[0]", "without leading"},
		"cycle":           {func(x *Tree) { x.Units[0].Parent = "DE" }, "units[0].parent", "cycle"},
		"too many values": {func(x *Tree) { x.Units[2].Values = make([]string, MaxUnitValues+1) }, "units[2].values", "at most"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tr := fixtureTree()
			tr.Units = append([]TreeUnit(nil), tr.Units...)
			c.edit(&tr)
			for _, is := range tr.Validate() {
				if is.Field == c.field && strings.Contains(is.Detail, c.text) {
					return
				}
			}
			t.Fatalf("no issue %s containing %q in %+v", c.field, c.text, tr.Validate())
		})
	}
}

func TestTreeValidate_DepthLimit(t *testing.T) {
	tr := Tree{Code: "DEEP", Name: "Deep", Attribute: "pc"}
	parent := ""
	for i := 0; i <= MaxTreeDepth; i++ {
		u := TreeUnit{Code: "U" + string(rune('A'+i)), Name: "u", Parent: parent}
		if i == MaxTreeDepth {
			u.Values = []string{"v"}
		}
		tr.Units = append(tr.Units, u)
		parent = u.Code
	}
	found := false
	for _, is := range tr.Validate() {
		found = found || strings.Contains(is.Detail, "deeper")
	}
	if !found {
		t.Fatalf("depth %d not refused: %+v", MaxTreeDepth+1, tr.Validate())
	}
}

func TestTreeCoverage_RollsUpAndRefusesUnknownUnits(t *testing.T) {
	tr := fixtureTree()
	for unit, want := range map[string]string{"DE": "PC10,PC11", "EMEA": "PC10,PC11,PC20", "ALL": "PC10,PC11,PC20,PC30", "": "PC10,PC11,PC20,PC30"} {
		got, err := tr.Coverage(unit)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(strings.Split(want, ",")) {
			t.Errorf("unit %q covers %v, want %s", unit, got, want)
		}
		for _, v := range strings.Split(want, ",") {
			if !got[v] {
				t.Errorf("unit %q misses %s", unit, v)
			}
		}
	}
	if _, err := tr.Coverage("NOPE"); err == nil || !strings.Contains(err.Error(), CodeTreeRun) {
		t.Fatalf("unknown unit: %v", err)
	}
}

func TestLimitToUnit_FiltersByAttributeAndReportsWhatIsLeftOut(t *testing.T) {
	tr := fixtureTree()
	bal := func(pc string, n int64) Balance {
		b := Balance{Account: "4000", Net: decimal.NewFromInt(n), Dims: map[string]string{}}
		if pc != "" {
			b.Dims["profit_center"] = pc
		}
		return b
	}
	all := []Balance{bal("PC10", 1), bal("PC20", 2), bal("PC30", 4), bal("PC99", 8), bal("", 16)}
	kept, scope, err := LimitToUnit(tr, "EMEA", all)
	if err != nil || len(kept) != 2 {
		t.Fatalf("EMEA keeps %d, %v", len(kept), err)
	}
	if c := TreeRunCheck(tr, "EMEA", scope); c != nil {
		t.Fatalf("a unit run carries no outside warning: %+v", c)
	}
	kept, scope, err = LimitToUnit(tr, "", all)
	if err != nil || len(kept) != 3 || !scope.Blank || len(scope.Outside) != 1 || scope.Outside[0] != "PC99" {
		t.Fatalf("whole tree: kept %d scope %+v %v", len(kept), scope, err)
	}
	c := TreeRunCheck(tr, "", scope)
	if c == nil || c.Code != CodeTreeOutside || !strings.Contains(c.Detail, "PC99") || !strings.Contains(c.Detail, "no value for profit_center") {
		t.Fatalf("whole-tree check: %+v", c)
	}
	if _, _, err := LimitToUnit(tr, "NOPE", all); err == nil {
		t.Fatal("unknown unit is refused")
	}
}

// A run's viewer sees the unit the run used and the units below it, not the
// rest of the tree.
func TestTreeSubtree_KeepsTheUnitAndItsDescendants(t *testing.T) {
	tr := fixtureTree()
	codes := func(x Tree) string {
		var out []string
		for _, u := range x.Units {
			out = append(out, u.Code)
		}
		return strings.Join(out, ",")
	}
	for unit, want := range map[string]string{"EMEA": "EMEA,DE,FR", "DE": "DE", "": codes(tr)} {
		if got := codes(tr.Subtree(unit)); got != want {
			t.Errorf("subtree %q = %s, want %s", unit, got, want)
		}
	}
	if sub := tr.Subtree("EMEA"); sub.Code != tr.Code || sub.Attribute != tr.Attribute || len(tr.Units) != 5 {
		t.Fatalf("subtree changed the tree or lost its header: %+v", sub)
	}
}
