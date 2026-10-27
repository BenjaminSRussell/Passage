package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

const (
	steelID   = "11111111-1111-1111-1111-111111111111"
	paintID   = "22222222-2222-2222-2222-222222222222"
	latheID   = "33333333-3333-3333-3333-333333333333"
	paintBoth = "44444444-4444-4444-4444-444444444444"
)

// editFixture is an order of 100: line 10 needs 3.05 kg per unit (305 kg), and two
// operations, the second with 5% scrap, so operation 10 starts 106 units (BR-21.8).
func editFixture() ([]OrderLine, []Operation) {
	lines := []OrderLine{{LineNo: "10", ItemID: steelID, QtyPer: d("3.05"), SeriesSize: d("1"), ScrapPct: d("0"),
		QtyRequired: d("305"), UnitCost: d("10"), Flushing: FlushManual, QtyConsumed: d("0")}}
	ops, err := PlanOperations([]Operation{
		{OperNumber: 10, ResourceID: latheID, RunHPer: d("0.2"), LaborRate: d("30"), Status: OperOpen},
		{OperNumber: 20, ResourceID: paintBoth, RunHPer: d("0.1"), ScrapPct: d("5"), LaborRate: d("25"), Status: OperOpen},
	}, d("100"))
	if err != nil {
		panic(err)
	}
	return lines, ops
}

func ptr[T any](v T) *T { return &v }

func dp(s string) *decimal.Decimal { return ptr(d(s)) }

func TestApplyOrderEdits_Rules(t *testing.T) {
	cases := []struct {
		name  string
		edits OrderEdits
		inUse InUse
		prep  func(lines []OrderLine, ops []Operation)
		code  string
		check func(t *testing.T, r EditResult)
	}{
		{name: "no edits", code: CodeInvalidInput},
		{name: "add a line with scrap and the default flushing", edits: OrderEdits{Lines: []LineEdit{{Op: EditAdd, LineNo: "20", ItemID: ptr(paintID), QtyPer: dp("0.5"), ScrapPct: dp("10")}}},
			check: func(t *testing.T, r EditResult) {
				l := r.Lines[1]
				// BR-21.2: 0.5 × 100 / 1 × 1.10 = 55.
				if l.LineNo != "20" || !l.QtyRequired.Equal(d("55")) || l.Flushing != FlushManual || !l.SeriesSize.Equal(d("1")) || !l.QtyConsumed.IsZero() {
					t.Fatalf("added line = %+v", l)
				}
				if a := r.Audit[0]; a.Target != TargetLine || a.Op != EditAdd || a.LineBefore != nil || a.LineAfter == nil || a.LineAfter.ItemID != paintID {
					t.Fatalf("audit = %+v", a)
				}
			}},
		{name: "add a line that exists", edits: OrderEdits{Lines: []LineEdit{{Op: EditAdd, LineNo: "10", ItemID: ptr(paintID), QtyPer: dp("1")}}}, code: CodeEditTargetExists},
		{name: "add a line without quantity", edits: OrderEdits{Lines: []LineEdit{{Op: EditAdd, LineNo: "20", ItemID: ptr(paintID)}}}, code: CodeInvalidInput},
		{name: "add a line with scrap 100", edits: OrderEdits{Lines: []LineEdit{{Op: EditAdd, LineNo: "20", ItemID: ptr(paintID), QtyPer: dp("1"), ScrapPct: dp("100")}}}, code: CodeInvalidInput},
		{name: "add a line with series 0", edits: OrderEdits{Lines: []LineEdit{{Op: EditAdd, LineNo: "20", ItemID: ptr(paintID), QtyPer: dp("1"), SeriesSize: dp("0")}}}, code: CodeInvalidInput},
		{name: "add a line with an unknown flushing", edits: OrderEdits{Lines: []LineEdit{{Op: EditAdd, LineNo: "20", ItemID: ptr(paintID), QtyPer: dp("1"), Flushing: ptr(Flushing("weekly"))}}}, code: CodeInvalidInput},
		{name: "change quantity recomputes the requirement", edits: OrderEdits{Lines: []LineEdit{{Op: EditChange, LineNo: "10", QtyPer: dp("4"), ScrapPct: dp("2")}}},
			check: func(t *testing.T, r EditResult) {
				// 4 × 100 × 1.02 = 408; the item and cost are kept.
				if l := r.Lines[0]; !l.QtyRequired.Equal(d("408")) || l.ItemID != steelID || !l.UnitCost.Equal(d("10")) {
					t.Fatalf("changed line = %+v", l)
				}
				if a := r.Audit[0]; !a.LineBefore.QtyRequired.Equal(d("305")) || !a.LineAfter.QtyRequired.Equal(d("408")) {
					t.Fatalf("audit before/after = %+v / %+v", a.LineBefore, a.LineAfter)
				}
			}},
		{name: "change with no field", edits: OrderEdits{Lines: []LineEdit{{Op: EditChange, LineNo: "10"}}}, code: CodeInvalidInput},
		{name: "change the item of a journaled line", edits: OrderEdits{Lines: []LineEdit{{Op: EditChange, LineNo: "10", ItemID: ptr(paintID)}}},
			inUse: InUse{Lines: map[string]bool{"10": true}}, code: CodeEditTargetInUse},
		{name: "change the quantity of a journaled line", edits: OrderEdits{Lines: []LineEdit{{Op: EditChange, LineNo: "10", QtyPer: dp("3")}}},
			inUse: InUse{Lines: map[string]bool{"10": true}}},
		{name: "requirement below consumption", edits: OrderEdits{Lines: []LineEdit{{Op: EditChange, LineNo: "10", QtyPer: dp("3")}}},
			prep: func(lines []OrderLine, _ []Operation) { lines[0].QtyConsumed = d("301") }, code: CodeEditTargetInUse},
		{name: "remove an unused line", edits: OrderEdits{Lines: []LineEdit{{Op: EditRemove, LineNo: "10"}}},
			check: func(t *testing.T, r EditResult) {
				if len(r.Lines) != 0 || r.Audit[0].LineBefore == nil || r.Audit[0].LineAfter != nil {
					t.Fatalf("lines %+v audit %+v", r.Lines, r.Audit)
				}
			}},
		{name: "remove a consumed line", edits: OrderEdits{Lines: []LineEdit{{Op: EditRemove, LineNo: "10"}}},
			prep: func(lines []OrderLine, _ []Operation) { lines[0].QtyConsumed = d("1") }, code: CodeEditTargetInUse},
		{name: "remove a missing line", edits: OrderEdits{Lines: []LineEdit{{Op: EditRemove, LineNo: "99"}}}, code: CodeEditTargetMissing},
		{name: "unknown op", edits: OrderEdits{Lines: []LineEdit{{Op: "swap", LineNo: "10"}}}, code: CodeInvalidInput},
		{name: "missing line number", edits: OrderEdits{Lines: []LineEdit{{Op: EditRemove}}}, code: CodeInvalidInput},
		{name: "add an operation between two re-plans the route", edits: OrderEdits{Operations: []OperationEdit{{Op: EditAdd, OperNumber: 15, ResourceID: ptr(latheID), RunHPer: dp("0.05"), ScrapPct: dp("10")}}},
			check: func(t *testing.T, r EditResult) {
				// BR-21.8 backward: op 20 starts ⌈100/0.95⌉₆ = 105.263158, op 15 that / 0.9 = 116.959065, op 10 the same.
				if len(r.Operations) != 3 || r.Operations[1].OperNumber != 15 {
					t.Fatalf("operations = %+v", r.Operations)
				}
				if !r.Operations[1].ProcessQty.Equal(d("116.959065")) || !r.Operations[0].ProcessQty.Equal(d("116.959065")) ||
					!r.Operations[2].ProcessQty.Equal(d("105.263158")) {
					t.Fatalf("process quantities = %s, %s, %s", r.Operations[0].ProcessQty, r.Operations[1].ProcessQty, r.Operations[2].ProcessQty)
				}
				if a := r.Audit[0]; a.Target != TargetOperation || a.Key != "15" || a.OperAfter == nil || !a.OperAfter.ProcessQty.Equal(d("116.959065")) || a.OperAfter.Status != OperOpen {
					t.Fatalf("audit = %+v", a)
				}
			}},
		{name: "add an operation that exists", edits: OrderEdits{Operations: []OperationEdit{{Op: EditAdd, OperNumber: 10, ResourceID: ptr(latheID)}}}, code: CodeEditTargetExists},
		{name: "add an operation without resource", edits: OrderEdits{Operations: []OperationEdit{{Op: EditAdd, OperNumber: 30}}}, code: CodeInvalidInput},
		{name: "change an operation's resource and time", edits: OrderEdits{Operations: []OperationEdit{{Op: EditChange, OperNumber: 10, ResourceID: ptr(paintBoth), SetupH: dp("1.5")}}},
			check: func(t *testing.T, r EditResult) {
				if op := r.Operations[0]; op.ResourceID != paintBoth || !op.SetupH.Equal(d("1.5")) || !op.RunHPer.Equal(d("0.2")) {
					t.Fatalf("changed operation = %+v", op)
				}
			}},
		{name: "change the resource of an operation with feedback", edits: OrderEdits{Operations: []OperationEdit{{Op: EditChange, OperNumber: 10, ResourceID: ptr(paintBoth)}}},
			prep: func(_ []OrderLine, ops []Operation) { ops[0].HoursReported = d("2") }, code: CodeEditTargetInUse},
		{name: "negative run time", edits: OrderEdits{Operations: []OperationEdit{{Op: EditChange, OperNumber: 10, RunHPer: dp("-1")}}}, code: CodeInvalidInput},
		{name: "remove a journaled operation", edits: OrderEdits{Operations: []OperationEdit{{Op: EditRemove, OperNumber: 20}}},
			inUse: InUse{Operations: map[int]bool{20: true}}, code: CodeEditTargetInUse},
		{name: "remove an operation", edits: OrderEdits{Operations: []OperationEdit{{Op: EditRemove, OperNumber: 20}}},
			check: func(t *testing.T, r EditResult) {
				// Without op 20's scrap, op 10 starts exactly the order quantity.
				if len(r.Operations) != 1 || !r.Operations[0].ProcessQty.Equal(d("100")) {
					t.Fatalf("operations = %+v", r.Operations)
				}
			}},
		{name: "remove every operation", edits: OrderEdits{Operations: []OperationEdit{{Op: EditRemove, OperNumber: 10}, {Op: EditRemove, OperNumber: 20}}}, code: CodeInvalidInput},
		{name: "edits apply in order", edits: OrderEdits{Lines: []LineEdit{
			{Op: EditAdd, LineNo: "20", ItemID: ptr(paintID), QtyPer: dp("1")},
			{Op: EditChange, LineNo: "20", RoundUp: ptr(true), QtyPer: dp("0.333")},
			{Op: EditRemove, LineNo: "10"},
		}}, check: func(t *testing.T, r EditResult) {
			// 0.333 × 100 = 33.3, rounded up to a whole unit.
			if len(r.Lines) != 1 || r.Lines[0].LineNo != "20" || !r.Lines[0].QtyRequired.Equal(d("34")) || len(r.Audit) != 3 {
				t.Fatalf("lines %+v audit %d", r.Lines, len(r.Audit))
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines, ops := editFixture()
			if tc.prep != nil {
				tc.prep(lines, ops)
			}
			r, err := ApplyOrderEdits(lines, ops, d("100"), tc.edits, tc.inUse)
			if tc.code != "" {
				if got := codeOf(err); got != tc.code {
					t.Fatalf("error = %v, want code %s", err, tc.code)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(r.Audit) != tc.edits.Count() {
				t.Fatalf("audit rows = %d, want one per edit (%d)", len(r.Audit), tc.edits.Count())
			}
			if tc.check != nil {
				tc.check(t, r)
			}
		})
	}
}

func TestCheckOrderEditable(t *testing.T) {
	for _, s := range []Status{Created, Estimated, Scheduled, Released} {
		if err := CheckOrderEditable(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []Status{Started, ReportedAsFinished, Ended, Cancelled} {
		if got := codeOf(CheckOrderEditable(s)); got != CodeEditNotAllowed {
			t.Errorf("%s: code %q, want %s", s, got, CodeEditNotAllowed)
		}
	}
}

// Property (FR-21.18, BR-21.2, BR-21.8): any accepted quantity change leaves the
// line at exactly RequiredQty of its new values, never below what was consumed,
// the operations planned as PlanOperations would plan them, and the inputs untouched.
func TestApplyOrderEdits_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		lines, ops := editFixture()
		lines[0].QtyConsumed = decimal.New(int64(rapid.IntRange(0, 50000).Draw(t, "consumed")), -2)
		qtyPer := decimal.New(int64(rapid.IntRange(0, 100000).Draw(t, "qtyPer")), -4)
		scrap := decimal.New(int64(rapid.IntRange(0, 9999).Draw(t, "scrap")), -2)
		roundUp := rapid.Bool().Draw(t, "roundUp")
		opScrap := decimal.New(int64(rapid.IntRange(0, 9999).Draw(t, "opScrap")), -2)
		edits := OrderEdits{
			Lines:      []LineEdit{{Op: EditChange, LineNo: "10", QtyPer: &qtyPer, ScrapPct: &scrap, RoundUp: &roundUp}},
			Operations: []OperationEdit{{Op: EditChange, OperNumber: 10, ScrapPct: &opScrap}},
		}
		r, err := ApplyOrderEdits(lines, ops, d("100"), edits, InUse{})
		if !lines[0].QtyPer.Equal(d("3.05")) || !ops[0].ScrapPct.IsZero() {
			t.Fatal("inputs were modified")
		}
		want, _ := RequiredQty(qtyPer, d("100"), d("1"), scrap, roundUp)
		if err != nil {
			if codeOf(err) != CodeEditTargetInUse || !want.LessThan(lines[0].QtyConsumed) {
				t.Fatalf("refused %v although the requirement %s covers the consumed %s", err, want, lines[0].QtyConsumed)
			}
			return
		}
		if !r.Lines[0].QtyRequired.Equal(want) || r.Lines[0].QtyRequired.LessThan(lines[0].QtyConsumed) {
			t.Fatalf("required %s, want %s (consumed %s)", r.Lines[0].QtyRequired, want, lines[0].QtyConsumed)
		}
		changed := append([]Operation(nil), ops...)
		changed[0].ScrapPct = opScrap
		plan, err := PlanOperations(changed, d("100"))
		if err != nil {
			t.Fatal(err)
		}
		for i := range plan {
			if !plan[i].ProcessQty.Equal(r.Operations[i].ProcessQty) {
				t.Fatalf("operation %d process qty %s, want %s", plan[i].OperNumber, r.Operations[i].ProcessQty, plan[i].ProcessQty)
			}
		}
	})
}

func TestVariancePct(t *testing.T) {
	cases := []struct {
		amount, planned string
		want            string
	}{
		{"50", "3000", "1.67"},     // BR-21.7 lot-size: 50 / 3,000
		{"-120", "1380", "-8.70"},  // favourable labor
		{"61", "3050", "2"},        // exact
		{"0.005", "100", "0.01"},   // half away from zero
		{"-0.005", "100", "-0.01"}, // half away from zero
	}
	for _, tc := range cases {
		got := VariancePct(d(tc.amount), d(tc.planned))
		if got == nil || !got.Equal(d(tc.want)) {
			t.Errorf("VariancePct(%s, %s) = %v, want %s", tc.amount, tc.planned, got, tc.want)
		}
	}
	if VariancePct(d("10"), decimal.Zero) != nil {
		t.Error("a variance with nothing planned has no percentage")
	}
}
