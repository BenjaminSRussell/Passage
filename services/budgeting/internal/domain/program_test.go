package domain

import (
	"errors"
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// errCode returns the domain code of err, or "" when err is nil or not a domain error.
func errCode(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

// TestDistribute_AC38_13 is the acceptance example of FR-38.21: a program of
// 5,000,000.00 with children distributed 4,800,000.00 rejects a further 300,000.00
// with CO-0641 and names the 100,000.00 excess; 200,000.00 still fits.
func TestDistribute_AC38_13(t *testing.T) {
	children := []Child{{ID: "a", Budget: d("4800000")}, {ID: "b", Budget: d("0")}}
	_, err := Distribute(d("5000000"), children, "b", d("300000"))
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeDistributionExceeds {
		t.Fatalf("got %v, want %s", err, CodeDistributionExceeds)
	}
	if de.Detail != "distribution exceeds parent position budget by 100000.00" {
		t.Errorf("detail %q does not name the excess", de.Detail)
	}
	out, err := Distribute(d("5000000"), children, "b", d("200000"))
	if err != nil {
		t.Fatalf("200,000.00 fits: %v", err)
	}
	if !Total(out).Equal(d("5000000")) {
		t.Errorf("total %s, want 5000000", Total(out))
	}
	if !Total(children).Equal(d("4800000")) {
		t.Errorf("input slice was modified: %s", Total(children))
	}
}

func TestDistribute_Rules(t *testing.T) {
	kids := []Child{{ID: "a", Budget: d("60"), Distributed: d("40")}, {ID: "b", Budget: d("30")}}
	cases := []struct {
		name   string
		target string
		amount string
		code   string
	}{
		{"within parent", "b", "40", ""},
		{"exactly the parent", "a", "70", ""},
		{"exceeds parent", "b", "41", CodeDistributionExceeds},
		{"reduce to own distributed", "a", "40", ""},
		{"below own children", "a", "39.99", CodeBelowDistributed},
		{"unknown position", "zz", "1", CodePositionNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Distribute(d("100"), kids, c.target, d(c.amount))
			if errCode(err) != c.code {
				t.Fatalf("got %v, want code %q", err, c.code)
			}
		})
	}
}

func TestValidateLevel_Bounds(t *testing.T) {
	for level, ok := range map[int]bool{0: false, 1: true, 6: true, 7: false} {
		if (ValidateLevel(level) == nil) != ok {
			t.Errorf("level %d: ok=%v", level, ok)
		}
		if !ok && errCode(ValidateLevel(level)) != CodeLevelExceeded {
			t.Errorf("level %d: code %q", level, errCode(ValidateLevel(level)))
		}
	}
}

func TestValidCategoryAndObjectType(t *testing.T) {
	for c, ok := range map[string]bool{"Capex": true, "Opex-related": true, "Contingency": true, "Opex": false, "": false} {
		if ValidCategory(c) != ok {
			t.Errorf("category %q: want %v", c, ok)
		}
	}
	for c, ok := range map[string]bool{"CapexRequest": true, "AssetUnderConstruction": true, "Project": true, "Asset": false} {
		if ValidObjectType(c) != ok {
			t.Errorf("object type %q: want %v", c, ok)
		}
	}
}

func TestUnused(t *testing.T) {
	cases := []struct{ budget, distributed, actual, commitment, want string }{
		{"100", "0", "0", "0", "100"},
		{"100", "0", "50", "25", "25"},
		{"100", "0", "60", "60", "0"}, // overrun carries nothing
		{"100", "70", "0", "0", "30"}, // only the undistributed slice of a parent
		{"100", "100", "0", "0", "0"},
		{"100", "40", "50", "0", "10"},
	}
	for _, c := range cases {
		got := Unused(d(c.budget), d(c.distributed), d(c.actual), d(c.commitment))
		if !got.Equal(d(c.want)) {
			t.Errorf("Unused(%s, %s, %s, %s) = %s, want %s", c.budget, c.distributed, c.actual, c.commitment, got, c.want)
		}
	}
}

// TestUnused_NoDoubleCount: a parent of 100 that distributed 100 to two leaves
// with nothing spent carries 100 in total, not 200.
func TestUnused_NoDoubleCount(t *testing.T) {
	total := Unused(d("100"), d("100"), d("0"), d("0")).Add(Unused(d("60"), d("0"), d("0"), d("0"))).Add(Unused(d("40"), d("0"), d("0"), d("0")))
	if !total.Equal(d("100")) {
		t.Fatalf("carried %s, want 100", total)
	}
}

func TestRollup_SumsDescendants(t *testing.T) {
	a := func(p, ac, c string) Amounts { return Amounts{Plan: d(p), Actual: d(ac), Commitment: d(c)} }
	nodes := []Node{
		{ID: "root", Own: a("0", "0", "0")},
		{ID: "plant", ParentID: "root", Own: a("10", "1", "2")},
		{ID: "line1", ParentID: "plant", Own: a("100", "40", "5")},
		{ID: "line2", ParentID: "plant", Own: a("50", "0", "10")},
		{ID: "it", ParentID: "root", Own: a("7", "7", "0")},
	}
	got := Rollup(nodes)
	want := map[string]Amounts{
		"root":  a("167", "48", "17"),
		"plant": a("160", "41", "17"),
		"line1": a("100", "40", "5"),
		"line2": a("50", "0", "10"),
		"it":    a("7", "7", "0"),
	}
	for id, w := range want {
		g := got[id]
		if !g.Plan.Equal(w.Plan) || !g.Actual.Equal(w.Actual) || !g.Commitment.Equal(w.Commitment) {
			t.Errorf("%s: got %+v, want %+v", id, g, w)
		}
	}
}

// TestDistribute_InvariantProperty: whatever sequence of requests is accepted,
// Σ children ≤ parent holds and no child drops below its own distributed amount;
// a rejected request returns no state (BR-38.15).
func TestDistribute_InvariantProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		parent := decimal.NewFromInt(rapid.Int64Range(0, 10000).Draw(t, "parent"))
		n := rapid.IntRange(1, 5).Draw(t, "children")
		children := make([]Child, n)
		for i := range children {
			children[i] = Child{ID: fmt.Sprintf("p%d", i), Distributed: decimal.NewFromInt(rapid.Int64Range(0, 100).Draw(t, "distributed"))}
		}
		steps := rapid.IntRange(1, 30).Draw(t, "steps")
		for s := 0; s < steps; s++ {
			idx := rapid.IntRange(0, n-1).Draw(t, "target")
			amount := decimal.NewFromInt(rapid.Int64Range(0, 4000).Draw(t, "amount"))
			out, err := Distribute(parent, children, children[idx].ID, amount)
			if err != nil {
				if out != nil {
					t.Fatalf("rejected request returned state")
				}
				continue
			}
			children = out
			if Total(children).GreaterThan(parent) {
				t.Fatalf("children total %s exceeds parent %s", Total(children), parent)
			}
			if c := children[idx]; c.Budget.LessThan(c.Distributed) {
				t.Fatalf("child %s accepted budget %s below its distributed %s", c.ID, c.Budget, c.Distributed)
			}
		}
	})
}
