package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func num(s string) Value { v := d(s); return Value{Num: &v} }

func date(s string) Value {
	t, _ := time.Parse("2006-01-02", s)
	return Value{Date: &t}
}

func text(s string) Value { return Value{Text: &s} }

// codeOf returns the domain code of err, or "" when err is not a domain error.
func codeOf(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

// AC-24.11: weighted-average potency of 200 kg at 40% and 300 kg at 45% is 43%.
func TestDerive_WeightedAverage_AC2411(t *testing.T) {
	got, err := Derive(Rule{Attribute: "potency", Aggregation: AggWeightedAverage, Decimals: 4}, []Sender{
		{BatchID: "a", Value: num("40"), Qty: d("200")},
		{BatchID: "b", Value: num("45"), Qty: d("300")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Value.Num == nil || got.Value.Num.StringFixed(4) != "43.0000" {
		t.Fatalf("potency = %v", got.Value)
	}
}

func TestDerive_Aggregations(t *testing.T) {
	early, late := "2027-02-01", "2027-03-01"
	cases := []struct {
		name    string
		rule    Rule
		senders []Sender
		want    Value
		missing int
		err     string
	}{
		{"min", Rule{Aggregation: AggMin}, []Sender{{"a", num("3"), d("1")}, {"b", num("2"), d("1")}}, num("2"), 0, ""},
		{"max", Rule{Aggregation: AggMax}, []Sender{{"a", num("3"), d("1")}, {"b", num("2"), d("1")}}, num("3"), 0, ""},
		{"earliest", Rule{Aggregation: AggEarliestDate}, []Sender{{"a", date(late), d("1")}, {"b", date(early), d("1")}}, date(early), 0, ""},
		{"latest", Rule{Aggregation: AggLatestDate}, []Sender{{"a", date(early), d("1")}, {"b", date(late), d("1")}}, date(late), 0, ""},
		{"copy single", Rule{Aggregation: AggCopy}, []Sender{{"a", text("X"), d("1")}}, text("X"), 0, ""},
		{"copy conflict", Rule{Attribute: "grade", Aggregation: AggCopy}, []Sender{{"a", text("X"), d("1")}, {"b", text("Y"), d("1")}}, Value{}, 0, CodeInvalid},
		{"fixed", Rule{Aggregation: AggFixed, Fixed: text("KOSHER")}, nil, text("KOSHER"), 0, ""},
		{"missing sender ignored", Rule{Aggregation: AggMin}, []Sender{{"a", Value{}, d("1")}, {"b", num("7"), d("1")}}, num("7"), 1, ""},
		{"no sender values", Rule{Aggregation: AggMax}, []Sender{{"a", Value{}, d("1")}}, Value{}, 1, ""},
		{"unknown aggregation", Rule{Aggregation: "median"}, nil, Value{}, 0, CodeInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Derive(c.rule, c.senders)
			if c.err != "" {
				if codeOf(err) != c.err {
					t.Fatalf("err = %v, want %s", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !sameValue(got.Value, c.want) && !(got.Value.Empty() && c.want.Empty()) {
				t.Fatalf("value = %+v, want %+v", got.Value, c.want)
			}
			if len(got.Missing) != c.missing {
				t.Fatalf("missing = %v", got.Missing)
			}
		})
	}
}

// Property: a weighted average lies between the smallest and largest sender value.
func TestDerive_WeightedAverageBounded(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 6).Draw(t, "n")
		var senders []Sender
		lo, hi := decimal.Decimal{}, decimal.Decimal{}
		for i := 0; i < n; i++ {
			v := decimal.NewFromInt(rapid.Int64Range(0, 10000).Draw(t, "v"))
			q := decimal.NewFromInt(rapid.Int64Range(1, 1000).Draw(t, "q"))
			if i == 0 || v.LessThan(lo) {
				lo = v
			}
			if i == 0 || v.GreaterThan(hi) {
				hi = v
			}
			senders = append(senders, Sender{BatchID: "b", Value: Value{Num: &v}, Qty: q})
		}
		got, err := Derive(Rule{Aggregation: AggWeightedAverage, Decimals: 4}, senders)
		if err != nil {
			t.Fatal(err)
		}
		if got.Value.Num.LessThan(lo) || got.Value.Num.GreaterThan(hi) {
			t.Fatalf("average %s outside [%s, %s]", got.Value.Num, lo, hi)
		}
	})
}

// AC-24.12: 100 kg expiring 2027-03-01 and 50 kg expiring 2027-02-01 merge into
// 150 kg expiring 2027-02-01 with both sources as parents.
func TestMerge_AC2412(t *testing.T) {
	a := Batch{ID: "A", ItemID: "i", WarehouseID: "w", Disposition: "available", Qty: d("100"), UnitCost: d("2"),
		Values: map[string]Value{"expiry": date("2027-03-01")}}
	b := Batch{ID: "B", ItemID: "i", WarehouseID: "w", Disposition: "available", Qty: d("50"), UnitCost: d("2"),
		Values: map[string]Value{"expiry": date("2027-02-01")}}
	got, err := Merge([]Batch{a, b}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Qty.Equal(d("150")) || got.Values["expiry"].Date.Format("2006-01-02") != "2027-02-01" {
		t.Fatalf("merge = %+v", got)
	}
	if len(got.Parents) != 2 || got.Parents[0] != "A" || got.Parents[1] != "B" {
		t.Fatalf("parents = %v", got.Parents)
	}
	if !got.UnitCost.Equal(d("2")) {
		t.Fatalf("unit cost = %s", got.UnitCost)
	}
}

func TestMerge_Refusals(t *testing.T) {
	base := func(id string, dispo string, grade string) Batch {
		return Batch{ID: id, ItemID: "i", WarehouseID: "w", Disposition: dispo, Qty: d("10"),
			Values: map[string]Value{"grade": text(grade)}}
	}
	cases := []struct {
		name     string
		srcs     []Batch
		override map[string]bool
		code     string
		wantText string
	}{
		{"single source", []Batch{base("A", "available", "X")}, nil, CodeInvalid, ""},
		{"text differs", []Batch{base("A", "available", "X"), base("B", "available", "Y")}, nil, CodeMergeRefused, ""},
		{"text differs with override takes largest", []Batch{base("A", "available", "X"), base("B", "available", "Y")}, map[string]bool{"grade": true}, "", "X"},
		{"disposition differs", []Batch{base("A", "available", "X"), base("B", "on-hold", "X")}, nil, CodeMergeRefused, ""},
		{"item differs", []Batch{base("A", "available", "X"), {ID: "B", ItemID: "j", WarehouseID: "w", Disposition: "available", Qty: d("1")}}, nil, CodeMergeRefused, ""},
		{"listed twice", []Batch{base("A", "available", "X"), base("A", "available", "X")}, nil, CodeInvalid, ""},
		{"kinds differ", []Batch{base("A", "available", "X"), {ID: "B", ItemID: "i", WarehouseID: "w", Disposition: "available", Qty: d("1"),
			Values: map[string]Value{"grade": num("3")}}}, nil, CodeMergeRefused, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Merge(c.srcs, c.override)
			if c.code != "" {
				if codeOf(err) != c.code {
					t.Fatalf("err = %v, want %s", err, c.code)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.wantText != "" && *got.Values["grade"].Text != c.wantText {
				t.Fatalf("grade = %s", *got.Values["grade"].Text)
			}
		})
	}
}

// Merging batches of different potency recomputes potency as the
// quantity-weighted average and cost per unit as the weighted average cost
// (FR-24.16, BR-24.11).
func TestMerge_WeightedPotencyAndCost(t *testing.T) {
	a := Batch{ID: "A", ItemID: "i", WarehouseID: "w", Disposition: "available", Qty: d("200"), UnitCost: d("3"),
		Values: map[string]Value{"potency": num("40")}}
	b := Batch{ID: "B", ItemID: "i", WarehouseID: "w", Disposition: "available", Qty: d("300"), UnitCost: d("4"),
		Values: map[string]Value{"potency": num("45")}}
	got, err := Merge([]Batch{a, b}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Values["potency"].Num.StringFixed(4) != "43.0000" || !got.UnitCost.Equal(d("3.6")) {
		t.Fatalf("potency %s, unit cost %s", got.Values["potency"].Num, got.UnitCost)
	}
}

func ymd(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

// AC-24.13: packed orders for 400 and 600 bottles at 0.5 kg each with a 100 kg
// formula multiple give one 500 kg bulk order with both packing orders pegged.
func TestConsolidate_AC2413(t *testing.T) {
	got, err := Consolidate([]PackedDemand{
		{OrderID: "P1", ItemID: "bottle", Units: d("400"), Due: ymd("2027-01-05")},
		{OrderID: "P2", ItemID: "bottle", Units: d("600"), Due: ymd("2027-01-03")},
	}, BulkPack{PackQtyPerUnit: d("0.5"), Multiple: d("100")})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Qty.Equal(d("500")) || len(got[0].Pegs) != 2 {
		t.Fatalf("bulk orders = %+v", got)
	}
	if got[0].Pegs[0].OrderID != "P2" || !got[0].Pegs[0].BulkQty.Equal(d("300")) || !got[0].Pegs[1].BulkQty.Equal(d("200")) {
		t.Fatalf("pegs by due date = %+v", got[0].Pegs)
	}
}

// BR-24.12: round up to the multiple; above the formula maximum split into
// ⌈qty / max⌉ orders and peg the earliest due packing orders first.
func TestConsolidate_RoundsUpSplitsAndPegsByDue(t *testing.T) {
	// 1,234 units × 0.5 = 617 kg → 700 kg with multiple 100.
	got, err := Consolidate([]PackedDemand{{OrderID: "P", Units: d("1234")}}, BulkPack{PackQtyPerUnit: d("0.5"), Multiple: d("100")})
	if err != nil || len(got) != 1 || !got[0].Qty.Equal(d("700")) || !got[0].Pegs[0].BulkQty.Equal(d("617")) {
		t.Fatalf("round up = %+v, %v", got, err)
	}
	// 1,500 + 500 kg with max 800 splits into 800, 800, 400. The early order
	// (500 kg) fills the first bulk order; the late one spans all three.
	split, err := Consolidate([]PackedDemand{
		{OrderID: "late", Units: d("3000"), Due: ymd("2027-02-01")},
		{OrderID: "early", Units: d("1000"), Due: ymd("2027-01-01")},
	}, BulkPack{PackQtyPerUnit: d("0.5"), Multiple: d("100"), Max: d("800")})
	if err != nil || len(split) != 3 || !split[0].Qty.Equal(d("800")) || !split[2].Qty.Equal(d("400")) {
		t.Fatalf("split = %+v, %v", split, err)
	}
	first := split[0].Pegs
	if len(first) != 2 || first[0].OrderID != "early" || !first[0].BulkQty.Equal(d("500")) || !first[1].BulkQty.Equal(d("300")) {
		t.Fatalf("first bulk order pegs = %+v", first)
	}
	if p := split[2].Pegs; len(p) != 1 || p[0].OrderID != "late" || !p[0].BulkQty.Equal(d("400")) {
		t.Fatalf("last bulk order pegs = %+v", p)
	}
}

func TestConsolidate_Refusals(t *testing.T) {
	rel := BulkPack{PackQtyPerUnit: d("1"), Multiple: d("100")}
	cases := []struct {
		name    string
		demands []PackedDemand
		rel     BulkPack
	}{
		{"max not a multiple", []PackedDemand{{OrderID: "P", Units: d("200")}}, BulkPack{PackQtyPerUnit: d("1"), Multiple: d("100"), Max: d("150")}},
		{"no demand", nil, rel},
		{"zero units", []PackedDemand{{OrderID: "P", Units: d("0")}}, rel},
		{"order twice", []PackedDemand{{OrderID: "P", Units: d("1")}, {OrderID: "P", Units: d("1")}}, rel},
		{"no multiple", []PackedDemand{{OrderID: "P", Units: d("1")}}, BulkPack{PackQtyPerUnit: d("1")}},
	}
	for _, c := range cases {
		if _, err := Consolidate(c.demands, c.rel); codeOf(err) != CodeInvalid {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
}

// Property: bulk orders sum to the rounded total, each is a multiple of the
// formula multiple and at most the maximum, and the pegs of every packing
// order add up to its bulk need.
func TestConsolidate_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 5).Draw(t, "orders")
		rel := BulkPack{PackQtyPerUnit: d("0.5"), Multiple: d("100"), Max: decimal.NewFromInt(100 * rapid.Int64Range(1, 20).Draw(t, "maxMult"))}
		demands := make([]PackedDemand, n)
		need := map[string]decimal.Decimal{}
		total := decimal.Zero
		for i := range demands {
			id := string(rune('A' + i))
			units := decimal.NewFromInt(rapid.Int64Range(1, 3000).Draw(t, "units"))
			demands[i] = PackedDemand{OrderID: id, Units: units, Due: ymd("2027-01-01").AddDate(0, 0, rapid.IntRange(0, 30).Draw(t, "due"))}
			need[id] = units.Mul(rel.PackQtyPerUnit)
			total = total.Add(need[id])
		}
		orders, err := Consolidate(demands, rel)
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		pegged := map[string]decimal.Decimal{}
		for _, o := range orders {
			if o.Qty.GreaterThan(rel.Max) || o.Qty.Mod(rel.Multiple).IsPositive() {
				t.Fatalf("size %s breaks max %s / multiple", o.Qty, rel.Max)
			}
			inOrder := decimal.Zero
			for _, p := range o.Pegs {
				pegged[p.OrderID] = pegged[p.OrderID].Add(p.BulkQty)
				inOrder = inOrder.Add(p.BulkQty)
			}
			if inOrder.GreaterThan(o.Qty) {
				t.Fatalf("pegs %s exceed bulk order %s", inOrder, o.Qty)
			}
			sum = sum.Add(o.Qty)
		}
		if want := total.DivRound(d("100"), 24).Ceil().Mul(d("100")); !sum.Equal(want) {
			t.Fatalf("sum %s, want %s", sum, want)
		}
		for id, q := range need {
			if !pegged[id].Equal(q) {
				t.Fatalf("order %s pegged %s, needs %s", id, pegged[id], q)
			}
		}
	})
}

func intp(n int) *int    { return &n }
func boolp(b bool) *bool { return &b }

func avail(id, qty, expiry string) Batch {
	return Batch{ID: id, Disposition: DispositionAvailable, Qty: d(qty), Values: map[string]Value{"expiry": date(expiry)}}
}

// AC-24.14: sellable days 60; batches expiring in 45 and 90 days from shipment.
// Only the 90-day batch qualifies.
func TestCheck_AC2414(t *testing.T) {
	ship := ymd("2027-01-01")
	req, err := Resolve([]Requirement{{Level: ScopeCustomer, SellableDays: intp(60)}})
	if err != nil {
		t.Fatal(err)
	}
	if r := Check(avail("short", "100", "2027-02-15"), req, ship); r == "" {
		t.Fatal("45-day batch must not qualify")
	}
	if r := Check(avail("long", "100", "2027-04-01"), req, ship); r != "" {
		t.Fatalf("90-day batch must qualify: %s", r)
	}
}

func TestCheck_ExclusionReasons(t *testing.T) {
	ship := ymd("2027-01-01")
	lo, hi := d("40"), d("50")
	none := Resolved{}
	withRange := Resolved{Ranges: map[string]Range{"potency": {Min: &lo, Max: &hi}}}
	onHold := avail("h", "1", "2028-01-01")
	onHold.Disposition = "on-hold"
	noExpiry := Batch{ID: "n", Disposition: DispositionAvailable, Qty: d("1")}
	potent := func(p string) Batch {
		b := avail("p", "1", "2028-01-01")
		b.Values["potency"] = num(p)
		return b
	}
	cases := []struct {
		name string
		b    Batch
		req  Resolved
		ok   bool
	}{
		{"on-hold is excluded (AC-24.10)", onHold, none, false},
		{"expired on ship date without sellable days", avail("x", "1", "2026-12-31"), none, false},
		{"expiring on the ship date still ships", avail("x", "1", "2027-01-01"), none, true},
		{"no expiry passes without sellable days", noExpiry, none, true},
		{"no expiry fails with sellable days", noExpiry, Resolved{SellableDays: 1}, false},
		{"potency inside range", potent("45"), withRange, true},
		{"potency above range", potent("55"), withRange, false},
		{"potency missing", avail("m", "1", "2028-01-01"), withRange, false},
	}
	for _, c := range cases {
		if got := Check(c.b, c.req, ship); (got == "") != c.ok {
			t.Errorf("%s: reason %q", c.name, got)
		}
	}
}

// BR-24.13 field by field: the customer row wins over the group row, which wins
// over the item default; a row that does not set a field leaves it to the
// less specific rows.
func TestResolve_Precedence(t *testing.T) {
	lo := d("40")
	got, err := Resolve([]Requirement{
		{Level: ScopeCustomer, SellableDays: intp(60)},
		{Level: ScopeItemDefault, SellableDays: intp(10), SameBatch: boolp(true)},
		{Level: ScopeGroup, SellableDays: intp(30), Attribute: "potency", Min: &lo},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.SellableDays != 60 || !got.SameBatch || got.Ranges["potency"].Min == nil {
		t.Fatalf("resolved %+v", got)
	}
	got, _ = Resolve([]Requirement{{Level: ScopeItemDefault, SellableDays: intp(10)}, {Level: ScopeGroup, SellableDays: intp(30)}})
	if got.SellableDays != 30 {
		t.Fatalf("group over item: %+v", got)
	}
	got, _ = Resolve(nil)
	if got.SellableDays != 0 || got.SameBatch || len(got.Ranges) != 0 {
		t.Fatalf("no rows: %+v", got)
	}
	for _, bad := range [][]Requirement{
		{{Level: 4}},
		{{Level: ScopeCustomer, SellableDays: intp(-1)}},
		{{Level: ScopeCustomer, Attribute: "potency"}},
		{{Level: ScopeCustomer, Min: &lo}},
	} {
		if _, err := Resolve(bad); codeOf(err) != CodeInvalid {
			t.Errorf("%+v: err = %v", bad, err)
		}
	}
}

// FR-24.19: a same-batch line takes the earliest-expiring batch that holds the
// whole quantity; when none does the result is PMF-0008 naming the largest.
func TestSelectSameBatch(t *testing.T) {
	pick, err := SelectSameBatch([]Batch{avail("late", "100", "2027-06-01"), avail("small", "40", "2027-02-01"), avail("early", "120", "2027-03-01")}, d("100"))
	if err != nil || pick.ID != "early" {
		t.Fatalf("pick = %v %v", pick.ID, err)
	}
	_, err = SelectSameBatch([]Batch{avail("a", "40", "2027-02-01"), avail("b", "60", "2027-02-01")}, d("100"))
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeSameBatch || de.Detail != "no single batch can supply 100 (largest 60)" {
		t.Fatalf("err = %v", err)
	}
	if _, err := SelectSameBatch(nil, d("0")); codeOf(err) != CodeInvalid {
		t.Fatalf("zero need: %v", err)
	}
}
