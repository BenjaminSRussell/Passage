package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

func domainCode(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestPlanActivation_Refusals(t *testing.T) {
	one := []PendingCost{{ProductID: "p1", SiteID: "s1", NewCost: dec("10")}}
	book := dec("120")
	tests := []struct {
		name      string
		allowZero bool
		pending   []PendingCost
		want      string
	}{
		{"no pending", false, nil, CodeNoPending},
		{"zero cost refused", false, []PendingCost{{ProductID: "p", SiteID: "s", NewCost: dec("0")}}, CodeZeroCost},
		{"zero cost allowed", true, []PendingCost{{ProductID: "p", SiteID: "s", NewCost: dec("0")}}, ""},
		{"first standard with stock needs a book value", false,
			[]PendingCost{{ProductID: "p", SiteID: "s", NewCost: dec("10"), OnHandQty: dec("12")}}, CodeBookValueMissing},
		{"first standard with stock and book value", false,
			[]PendingCost{{ProductID: "p", SiteID: "s", NewCost: dec("10"), OnHandQty: dec("12"), BookValue: &book}}, ""},
		{"first standard without stock", false, one, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := PlanActivation(tc.allowZero, 2, tc.pending)
			if got := domainCode(err); got != tc.want {
				t.Errorf("code = %q, want %q (err %v)", got, tc.want, err)
			}
		})
	}
}

func TestCheckActivatable(t *testing.T) {
	tests := []struct {
		name string
		v    VersionState
		want string
	}{
		{"standard, effective today", VersionState{Type: "STANDARD", EffectiveFrom: "2026-10-27"}, ""},
		{"standard, effective earlier", VersionState{Type: "STANDARD", EffectiveFrom: "2026-01-01"}, ""},
		{"blocked", VersionState{Type: "STANDARD", EffectiveFrom: "2026-01-01", Blocked: true}, CodeActivationBlocked},
		{"planned", VersionState{Type: "PLANNED", EffectiveFrom: "2026-01-01"}, CodeNotStandard},
		{"effective tomorrow", VersionState{Type: "STANDARD", EffectiveFrom: "2026-10-28"}, CodeFutureEffective},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domainCode(CheckActivatable(tc.v, "2026-10-27")); got != tc.want {
				t.Errorf("code = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPlanActivation_RevaluationArithmetic(t *testing.T) {
	book := dec("180")
	pending := []PendingCost{
		{ProductID: "p1", SiteID: "s1", NewCost: dec("11"), PreviousCost: dec("10"), HasPrevious: true, OnHandQty: dec("100")},
		// No previous standard: 40 units carried at 180.00 are revalued to 40 x 5 = 200.00.
		{ProductID: "p2", SiteID: "s1", NewCost: dec("5"), OnHandQty: dec("40"), BookValue: &book},
		{ProductID: "p3", SiteID: "s2", NewCost: dec("2.005"), PreviousCost: dec("2"), HasPrevious: true, OnHandQty: dec("1")},
		{ProductID: "p4", SiteID: "s2", NewCost: dec("9"), PreviousCost: dec("9"), HasPrevious: true, OnHandQty: dec("-3")},
	}
	plan, err := PlanActivation(false, 2, pending)
	if err != nil {
		t.Fatal(err)
	}
	// Lines are sorted by site then product.
	got := map[string]string{}
	for _, l := range plan.Lines {
		got[l.ProductID] = l.Revaluation.String()
	}
	want := map[string]string{"p1": "100", "p2": "20", "p3": "0", "p4": "0"}
	for p, w := range want {
		if got[p] != w {
			t.Errorf("revaluation %s = %s, want %s", p, got[p], w)
		}
	}
	if plan.Sites[0].SiteID != "s1" || !plan.Sites[0].Amount.Equal(dec("120")) {
		t.Errorf("site s1 total = %+v", plan.Sites[0])
	}
	if !plan.Total.Equal(dec("120")) {
		t.Errorf("total = %s, want 120", plan.Total)
	}
	if plan.Lines[1].ProductID != "p2" || plan.Lines[1].HasPrevious || !plan.Lines[1].BookValue.Equal(dec("180")) {
		t.Errorf("first-standard line = %+v, want p2 with book value 180", plan.Lines[1])
	}
}

func TestPlanActivation_RoundsHalfEvenPerLine(t *testing.T) {
	// 0.125 * 1 rounds half-even to 0.12 at scale 2; 0.375 rounds to 0.38.
	pending := []PendingCost{
		{ProductID: "a", SiteID: "s", NewCost: dec("0.125"), PreviousCost: dec("0"), HasPrevious: true, OnHandQty: dec("1")},
		{ProductID: "b", SiteID: "s", NewCost: dec("0.375"), PreviousCost: dec("0"), HasPrevious: true, OnHandQty: dec("1")},
	}
	plan, err := PlanActivation(false, 2, pending)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Lines[0].Revaluation.String() != "0.12" || plan.Lines[1].Revaluation.String() != "0.38" {
		t.Errorf("rounding = %s, %s", plan.Lines[0].Revaluation, plan.Lines[1].Revaluation)
	}
}

func TestTopDeltas_OrdersByAbsoluteValue(t *testing.T) {
	lines := []ActivationLine{
		{ProductID: "a", SiteID: "s", Revaluation: dec("5")},
		{ProductID: "b", SiteID: "s", Revaluation: dec("-50")},
		{ProductID: "c", SiteID: "s", Revaluation: dec("20")},
	}
	top := TopDeltas(lines, 2)
	if len(top) != 2 || top[0].ProductID != "b" || top[1].ProductID != "c" {
		t.Errorf("top = %+v", top)
	}
}

func TestCopyCost(t *testing.T) {
	tests := []struct {
		cost, pct, want string
		err             bool
	}{
		{"10", "5", "10.5", false},
		{"10", "-10", "9", false},
		{"3", "0", "3", false},
		{"10", "-100", "", true},
		{"10", "-150", "", true},
		{"1.000001", "50", "1.500002", false}, // 1.5000015 rounds half-even to 1.500002
		{"99999999999999", "1", "", true},     // would exceed 14 integer digits
	}
	for _, tc := range tests {
		got, err := CopyCost(dec(tc.cost), dec(tc.pct))
		if (err != nil) != tc.err {
			t.Fatalf("CopyCost(%s,%s) err = %v", tc.cost, tc.pct, err)
		}
		if err == nil && got.String() != tc.want {
			t.Errorf("CopyCost(%s,%s) = %s, want %s", tc.cost, tc.pct, got, tc.want)
		}
	}
}

func TestParseItemCostCSV(t *testing.T) {
	p, s := ids.New(), ids.New()
	// Columns in any order, header case-insensitive with a BOM, extra columns and blank lines ignored, ids lower-cased.
	good := "\ufeffUnitCost,note,SITEID,productId\n10.25,x," + s + "," + strings.ToUpper(p) + "\n\n4,y," + s + "," + ids.New() + "\n"
	rows, err := ParseItemCostCSV(strings.NewReader(good), 10)
	if err != nil || len(rows) != 2 || rows[0].UnitCost.String() != "10.25" || rows[0].ProductID != p || rows[0].SiteID != s || rows[0].Line != 2 || rows[1].Line != 4 {
		t.Fatalf("good file: rows=%+v err=%v", rows, err)
	}
	bad := map[string]string{
		"no header":      "",
		"missing column": "productId,siteId\n" + p + "," + s + "\n",
		"not a uuid":     "productId,siteId,unitCost\nnope," + s + ",1\n",
		"negative cost":  "productId,siteId,unitCost\n" + p + "," + s + ",-1\n",
		"not decimal":    "productId,siteId,unitCost\n" + p + "," + s + ",abc\n",
		"duplicate":      "productId,siteId,unitCost\n" + p + "," + s + ",1\n" + p + "," + s + ",2\n",
		"no rows":        "productId,siteId,unitCost\n",
		"seven decimals": "productId,siteId,unitCost\n" + p + "," + s + ",1.0000001\n",
		"too large":      "productId,siteId,unitCost\n" + p + "," + s + ",100000000000000\n",
		"short row":      "productId,siteId,unitCost\n" + p + "\n",
		"bad quoting":    "productId,siteId,unitCost\n\"" + p + "," + s + ",1\n",
		"over max":       "productId,siteId,unitCost\n" + p + "," + s + ",1\n" + ids.New() + "," + s + ",1\n",
	}
	for name, in := range bad {
		max := 10
		if name == "over max" {
			max = 1
		}
		if _, err := ParseItemCostCSV(strings.NewReader(in), max); domainCode(err) != CodeImportInvalid {
			t.Errorf("%s: err = %v, want %s", name, err, CodeImportInvalid)
		}
	}
}

// Property: the grand total equals the sum of site totals, and site totals sum
// their own lines, for any pending set. Planning the same set twice is identical.
func TestPlanActivation_TotalsReconcileProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 25).Draw(t, "n")
		var pending []PendingCost
		for i := 0; i < n; i++ {
			newCost := decimal.NewFromInt(rapid.Int64Range(1, 100000).Draw(t, "new")).Div(decimal.NewFromInt(1000))
			prev := decimal.NewFromInt(rapid.Int64Range(0, 100000).Draw(t, "prev")).Div(decimal.NewFromInt(1000))
			qty := decimal.NewFromInt(rapid.Int64Range(-500, 500).Draw(t, "qty"))
			book := decimal.NewFromInt(rapid.Int64Range(-50000, 50000).Draw(t, "book")).Div(decimal.NewFromInt(100))
			pending = append(pending, PendingCost{
				BookValue:    &book,
				ProductID:    rapid.StringMatching(`[a-z]{1,6}`).Draw(t, "p"),
				SiteID:       rapid.SampledFrom([]string{"s1", "s2", "s3"}).Draw(t, "s"),
				NewCost:      newCost,
				PreviousCost: prev,
				HasPrevious:  rapid.Bool().Draw(t, "hp"),
				OnHandQty:    qty,
			})
		}
		a, err := PlanActivation(true, 2, pending)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := PlanActivation(true, 2, pending)
		sum := decimal.Zero
		for _, s := range a.Sites {
			sum = sum.Add(s.Amount)
		}
		if !sum.Equal(a.Total) {
			t.Fatalf("site sum %s != total %s", sum, a.Total)
		}
		if len(a.Lines) != len(b.Lines) || !a.Total.Equal(b.Total) {
			t.Fatalf("plan is not deterministic")
		}
		// Every line is (new − old) × qty, or new × qty − book value, at two places.
		for _, l := range a.Lines {
			want := l.NewCost.Mul(l.OnHandQty).Sub(l.BookValue)
			if l.HasPrevious {
				want = l.NewCost.Sub(l.PreviousCost).Mul(l.OnHandQty)
			}
			if !l.Revaluation.Equal(want.RoundBank(2)) {
				t.Fatalf("line %+v revaluation %s, want %s", l, l.Revaluation, want.RoundBank(2))
			}
		}
	})
}
