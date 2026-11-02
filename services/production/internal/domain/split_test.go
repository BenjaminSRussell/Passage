package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var de *Error
	if !errors.As(err, &de) || de.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

// splitFixture is an order of 100: 3.05 kg per unit with 0 % scrap, a
// round-up line of 1 box per 30 units, and one operation of 2 h setup and
// 0.2 h per unit with 5 % scrap.
func splitFixture() ([]OrderLine, []Operation) {
	lines := []OrderLine{
		{LineNo: "10", ItemID: "rm-kg", QtyPer: d("3.05"), SeriesSize: d("1"), QtyRequired: d("305"), Flushing: FlushStart},
		{LineNo: "20", ItemID: "box", QtyPer: d("1"), SeriesSize: d("30"), RoundUp: true, QtyRequired: d("4")},
	}
	ops := []Operation{{OperNumber: 10, ResourceID: "res-1", SetupH: d("2"), RunHPer: d("0.2"), ScrapPct: d("5"), ProcessQty: d("105.263158"),
		Status: OperOpen}}
	return lines, ops
}

func TestSplitOrder_ReexplodesBothOrders(t *testing.T) {
	lines, ops := splitFixture()
	plan, err := SplitOrder(d("100"), d("40"), decimal.Zero, decimal.Zero, lines, ops)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.OriginalQty.Equal(d("60")) || !plan.SplitQty.Equal(d("40")) {
		t.Fatalf("quantities = %s/%s, want 60/40", plan.OriginalQty, plan.SplitQty)
	}
	// The variable requirement is shared in proportion: 3.05 × 60 = 183, 3.05 × 40 = 122.
	if !plan.OriginalLines[0].QtyRequired.Equal(d("183")) || !plan.NewLines[0].QtyRequired.Equal(d("122")) {
		t.Fatalf("kg = %s/%s, want 183/122", plan.OriginalLines[0].QtyRequired, plan.NewLines[0].QtyRequired)
	}
	// BR-21.2 round-up applies per order: ceil(60/30) = 2, ceil(40/30) = 2.
	if !plan.OriginalLines[1].QtyRequired.Equal(d("2")) || !plan.NewLines[1].QtyRequired.Equal(d("2")) {
		t.Fatalf("boxes = %s/%s, want 2/2", plan.OriginalLines[1].QtyRequired, plan.NewLines[1].QtyRequired)
	}
	// BR-21.8 per order: ceil6(60 / 0.95) = 63.157895, ceil6(40 / 0.95) = 42.105264; each keeps its setup.
	if !plan.OriginalOps[0].ProcessQty.Equal(d("63.157895")) || !plan.NewOps[0].ProcessQty.Equal(d("42.105264")) {
		t.Fatalf("process qty = %s/%s", plan.OriginalOps[0].ProcessQty, plan.NewOps[0].ProcessQty)
	}
	if !plan.OriginalOps[0].SetupH.Equal(d("2")) || !plan.NewOps[0].SetupH.Equal(d("2")) {
		t.Fatalf("setup = %s/%s, want 2/2", plan.OriginalOps[0].SetupH, plan.NewOps[0].SetupH)
	}
	if plan.NewLines[0].Flushing != FlushStart || plan.NewLines[0].ItemID != "rm-kg" || plan.NewOps[0].ResourceID != "res-1" {
		t.Fatalf("snapshot not carried: %+v %+v", plan.NewLines[0], plan.NewOps[0])
	}
}

func TestSplitOrder_NewOrderStartsWithoutFeedback(t *testing.T) {
	lines, ops := splitFixture()
	lines[0].QtyConsumed = d("12")
	ops[0].HoursReported, ops[0].QtyGood, ops[0].Status = d("1.5"), d("3"), OperStarted
	plan, err := SplitOrder(d("100"), d("50"), decimal.Zero, decimal.Zero, lines, ops)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.NewLines[0].QtyConsumed.IsZero() || !plan.NewOps[0].HoursReported.IsZero() || !plan.NewOps[0].QtyGood.IsZero() || plan.NewOps[0].Status != OperOpen {
		t.Fatalf("new order inherited feedback: %+v %+v", plan.NewLines[0], plan.NewOps[0])
	}
	if !plan.OriginalLines[0].QtyConsumed.Equal(d("12")) || !plan.OriginalOps[0].HoursReported.Equal(d("1.5")) {
		t.Fatalf("original lost its feedback: %+v %+v", plan.OriginalLines[0], plan.OriginalOps[0])
	}
	if !lines[0].QtyRequired.Equal(d("305")) || !ops[0].HoursReported.Equal(d("1.5")) {
		t.Fatal("SplitOrder changed its input")
	}
}

func TestCheckSplitQty(t *testing.T) {
	cases := []struct {
		name, split, min, multiple string
		ok                         bool
	}{
		{"zero", "0", "0", "0", false},
		{"negative", "-5", "0", "0", false},
		{"whole order", "100", "0", "0", false},
		{"above the order", "120", "0", "0", false},
		{"below the minimum", "5", "10", "0", false},
		{"at the minimum", "10", "10", "0", true},
		{"not a multiple", "15", "0", "10", false},
		{"a decimal multiple", "7.5", "0", "2.5", true},
		{"minimum and multiple", "20", "10", "10", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckSplitQty(d("100"), d(tc.split), d(tc.min), d(tc.multiple))
			if tc.ok {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			wantCode(t, err, CodeSplitQuantity)
		})
	}
}

func TestCheckSplittable(t *testing.T) {
	for _, s := range []Status{Created, Estimated, Scheduled, Released, Started} {
		if err := CheckSplittable(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []Status{ReportedAsFinished, Ended, Cancelled, Status("Bogus")} {
		wantCode(t, CheckSplittable(s), CodeIllegalTransition)
	}
}

func TestCheckMergeable(t *testing.T) {
	for _, s := range []Status{Created, Estimated, Scheduled, Released} {
		if err := CheckMergeable(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []Status{Started, ReportedAsFinished, Ended, Cancelled, Status("Bogus")} {
		wantCode(t, CheckMergeable(s), CodeIllegalTransition)
	}
}

// The FR-21.15 invariant: the two orders add up to the quantity at split time,
// and a line without rounding effects is shared to within one rounding step.
func TestSplitOrder_ConservesQuantity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		qty := decimal.New(rapid.Int64Range(2, 5_000_000).Draw(t, "qty"), -3)
		split := decimal.New(rapid.Int64Range(1, qty.Shift(3).IntPart()-1).Draw(t, "split"), -3)
		per := decimal.New(rapid.Int64Range(1, 100_000).Draw(t, "per"), -4)
		lines := []OrderLine{{LineNo: "10", ItemID: "i", QtyPer: per, SeriesSize: d("1")}}
		ops := []Operation{{OperNumber: 10, ResourceID: "r"}}
		plan, err := SplitOrder(qty, split, decimal.Zero, decimal.Zero, lines, ops)
		if err != nil {
			t.Fatal(err)
		}
		if !plan.OriginalQty.Add(plan.SplitQty).Equal(qty) || !plan.OriginalQty.IsPositive() {
			t.Fatalf("%s + %s != %s", plan.OriginalQty, plan.SplitQty, qty)
		}
		whole, _ := RequiredQty(per, qty, d("1"), decimal.Zero, false)
		sum := plan.OriginalLines[0].QtyRequired.Add(plan.NewLines[0].QtyRequired)
		if sum.LessThan(whole) || sum.Sub(whole).GreaterThan(d("0.000002")) {
			t.Fatalf("lines %s + %s, whole order %s", plan.OriginalLines[0].QtyRequired, plan.NewLines[0].QtyRequired, whole)
		}
		if !plan.OriginalOps[0].ProcessQty.Equal(plan.OriginalQty) || !plan.NewOps[0].ProcessQty.Equal(plan.SplitQty) {
			t.Fatalf("process quantities %s/%s", plan.OriginalOps[0].ProcessQty, plan.NewOps[0].ProcessQty)
		}
	})
}

func mergeOrder(id, qty string, status Status) MergeInput {
	lines := []OrderLine{{LineNo: "10", ItemID: "rm", QtyPer: d("2"), SeriesSize: d("1"), QtyRequired: d("2").Mul(d(qty))}}
	ops := []Operation{{OperNumber: 10, ResourceID: "res-1", SetupH: d("1.5"), RunHPer: d("0.5"), ProcessQty: d(qty)}}
	return MergeInput{ID: id, LegalEntityID: "le", ProductID: "fg", SiteID: "site", WarehouseID: "wh", Currency: "USD",
		Status: status, Quantity: d(qty), Lines: lines, Operations: ops}
}

func TestMergeOrders_SumsAndReexplodes(t *testing.T) {
	m, err := MergeOrders([]MergeInput{mergeOrder("a", "40", Released), mergeOrder("b", "60", Created), mergeOrder("c", "0.5", Scheduled)})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Quantity.Equal(d("100.5")) || !m.Lines[0].QtyRequired.Equal(d("201")) || !m.Operations[0].ProcessQty.Equal(d("100.5")) {
		t.Fatalf("merged %s, line %s, process %s", m.Quantity, m.Lines[0].QtyRequired, m.Operations[0].ProcessQty)
	}
	if !m.Operations[0].SetupH.Equal(d("1.5")) {
		t.Fatalf("setup %s, want the survivor's 1.5 once", m.Operations[0].SetupH)
	}
}

func TestMergeOrders_Refusals(t *testing.T) {
	variant := func(change func(*MergeInput)) MergeInput {
		o := mergeOrder("b", "60", Released)
		change(&o)
		return o
	}
	a := mergeOrder("a", "40", Released)
	cases := map[string][]MergeInput{
		"a single order":       {a},
		"the same order twice": {a, mergeOrder("a", "40", Released)},
		"a started order":      {a, mergeOrder("b", "60", Started)},
		"a cancelled order":    {a, mergeOrder("b", "60", Cancelled)},
		"another product":      {a, variant(func(o *MergeInput) { o.ProductID = "other" })},
		"another site":         {a, variant(func(o *MergeInput) { o.SiteID = "other" })},
		"another warehouse":    {a, variant(func(o *MergeInput) { o.WarehouseID = "other" })},
		"another currency":     {a, variant(func(o *MergeInput) { o.Currency = "EUR" })},
		"another item group":   {a, variant(func(o *MergeInput) { o.ItemGroup = "FG" })},
		"another legal entity": {a, variant(func(o *MergeInput) { o.LegalEntityID = "other" })},
		"a rework order":       {a, variant(func(o *MergeInput) { o.ReworkReference = &ReworkRef{Type: ReworkRefBatch, ID: "B-1"} })},
		"another run time":     {a, variant(func(o *MergeInput) { o.Operations[0].RunHPer = d("0.9") })},
		"another setup":        {a, variant(func(o *MergeInput) { o.Operations[0].SetupH = d("3") })},
		"another component":    {a, variant(func(o *MergeInput) { o.Lines[0].ItemID = "other" })},
		"an extra BOM line":    {a, variant(func(o *MergeInput) { o.Lines = append(o.Lines, o.Lines[0]) })},
		"another co-product":   {a, variant(func(o *MergeInput) { o.CoProducts = []CoProduct{{LineNo: "C1", ItemID: "x", QtyPer: d("0.2")}} })},
		"a quantity too large": {a, variant(func(o *MergeInput) { o.Quantity = d("999999999999.999999") })},
	}
	for name, orders := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := MergeOrders(orders)
			wantCode(t, err, CodeMergeRefused)
		})
	}
}

func TestMergeOrders_SameCoProductsMerge(t *testing.T) {
	co := []CoProduct{{LineNo: "C1", Type: CoProductTypeCo, ItemID: "x", QtyPer: d("0.2")}}
	a, b := mergeOrder("a", "40", Released), mergeOrder("b", "60", Released)
	a.CoProducts, b.CoProducts = co, co
	if _, err := MergeOrders([]MergeInput{a, b}); err != nil {
		t.Fatal(err)
	}
	b.CoProducts = []CoProduct{{LineNo: "C1", Type: CoProductTypeBy, ItemID: "x", QtyPer: d("0.2")}}
	_, err := MergeOrders([]MergeInput{a, b})
	wantCode(t, err, CodeMergeRefused)
}

func TestMergeOrders_SameReworkReference(t *testing.T) {
	ref := &ReworkRef{Type: ReworkRefBatch, ID: "B-1"}
	a, b := mergeOrder("a", "4", Created), mergeOrder("b", "6", Created)
	a.ReworkReference, b.ReworkReference = ref, &ReworkRef{Type: ReworkRefBatch, ID: "B-1"}
	if _, err := MergeOrders([]MergeInput{a, b}); err != nil {
		t.Fatalf("rework orders of the same batch: %v", err)
	}
}

func TestBulkRules(t *testing.T) {
	for _, cmd := range []Command{CmdEstimate, CmdSchedule, CmdRelease, CmdStart, CmdEnd, CmdCancel} {
		if !BulkCommand(cmd) {
			t.Errorf("%s should be allowed in bulk", cmd)
		}
	}
	for _, cmd := range []Command{CmdReportFinished, CmdResetStatus, CmdSplit, CmdMerge} {
		if BulkCommand(cmd) {
			t.Errorf("%s needs per-order input and must not run in bulk", cmd)
		}
	}
	ids := make([]string, MaxBulkOrders+1)
	for i := range ids {
		ids[i] = string(rune('a'+i%26)) + decimal.NewFromInt(int64(i)).String()
	}
	if err := ValidateBulk(ids[:MaxBulkOrders]); err != nil {
		t.Fatalf("500 orders: %v", err)
	}
	wantCode(t, ValidateBulk(ids), CodeInvalidInput)
	wantCode(t, ValidateBulk(nil), CodeInvalidInput)
	wantCode(t, ValidateBulk([]string{"x", "y", "x"}), CodeInvalidInput)
}
