package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func dd(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func ocmOrder(t *testing.T) ([]OrderLine, []Operation) {
	t.Helper()
	lines, err := Explode([]BOMComponent{
		{LineNo: "10", ItemID: "11111111-1111-4111-8111-111111111111", QtyPer: dd("2"), SeriesSize: dd("1"), UnitCost: dd("10")},
		{LineNo: "20", ItemID: "22222222-2222-4222-8222-222222222222", QtyPer: dd("1"), SeriesSize: dd("1"), UnitCost: dd("4")},
	}, dd("100"))
	if err != nil {
		t.Fatal(err)
	}
	ops, err := PlanOperations([]Operation{
		{OperNumber: 10, ResourceID: "33333333-3333-4333-8333-333333333333", RunHPer: dd("0.5"), LaborRate: dd("30"), OverheadRate: dd("10")},
		{OperNumber: 20, ResourceID: "44444444-4444-4444-8444-444444444444", RunHPer: dd("0.1"), LaborRate: dd("20")},
	}, dd("100"))
	if err != nil {
		t.Fatal(err)
	}
	return lines, ops
}

// FR-21.16: the delta between an order and the new BOM and route is applied as
// order edits, so the result equals exploding the new snapshot directly.
func TestDiffSnapshot_AppliedEditsReachTheNewSnapshot(t *testing.T) {
	lines, ops := ocmOrder(t)
	nextLines, err := Explode([]BOMComponent{
		{LineNo: "10", ItemID: "11111111-1111-4111-8111-111111111111", QtyPer: dd("3"), SeriesSize: dd("1"), UnitCost: dd("11")}, // changed
		{LineNo: "30", ItemID: "55555555-5555-4555-8555-555555555555", QtyPer: dd("1"), SeriesSize: dd("1"), UnitCost: dd("2")},  // added; 20 removed
	}, dd("100"))
	if err != nil {
		t.Fatal(err)
	}
	nextOps := []Operation{
		{OperNumber: 10, ResourceID: "33333333-3333-4333-8333-333333333333", RunHPer: dd("0.4"), LaborRate: dd("30"), OverheadRate: dd("10")},
		{OperNumber: 30, ResourceID: "44444444-4444-4444-8444-444444444444", RunHPer: dd("0.2"), LaborRate: dd("20")},
	}
	edits, delta := DiffSnapshot(lines, ops, nextLines, nextOps, true, true)
	want := ChangeDelta{LinesAdded: 1, LinesRemoved: 1, LinesChanged: 1, OperationsAdded: 1, OperationsRemoved: 1, OperationsChanged: 1}
	if delta != want || delta.Total() != 6 || edits.Count() != 6 {
		t.Fatalf("delta %+v, %d edits", delta, edits.Count())
	}
	res, err := ApplyOrderEdits(lines, ops, dd("100"), edits, InUse{Lines: map[string]bool{}, Operations: map[int]bool{}})
	if err != nil {
		t.Fatal(err)
	}
	got, wantRows := PlannedTotal(Estimate(res.Lines, res.Operations)), PlannedTotal(Estimate(nextLines, mustPlan(t, nextOps)))
	if !got.Equal(wantRows) {
		t.Fatalf("planned after the edits %s, after exploding the new snapshot %s", got, wantRows)
	}
}

func mustPlan(t *testing.T, ops []Operation) []Operation {
	t.Helper()
	out, err := PlanOperations(ops, dd("100"))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDiffSnapshot_NoDeltaAndPartialSnapshots(t *testing.T) {
	lines, ops := ocmOrder(t)
	if edits, delta := DiffSnapshot(lines, ops, lines, ops, true, true); edits.Count() != 0 || delta.Total() != 0 {
		t.Fatalf("identical snapshot gave %d edits", edits.Count())
	}
	// A BOM-only ECO leaves the route alone even though the route list is empty.
	if edits, _ := DiffSnapshot(lines, ops, lines[:1], nil, true, false); len(edits.Operations) != 0 || len(edits.Lines) != 1 || edits.Lines[0].Op != EditRemove {
		t.Fatalf("BOM-only edits: %+v", edits)
	}
	// An empty flushing principle is manual.
	same := append([]OrderLine(nil), lines...)
	same[0].Flushing = ""
	if edits, _ := DiffSnapshot(lines, ops, same, ops, true, true); edits.Count() != 0 {
		t.Fatal("empty flushing must equal manual")
	}
}

// A line that consumption references cannot be removed, so the change is refused.
func TestDiffSnapshot_ConsumedLineIsNeverRemoved(t *testing.T) {
	lines, ops := ocmOrder(t)
	lines[1].QtyConsumed = dd("1")
	edits, _ := DiffSnapshot(lines, ops, lines[:1], ops, true, true)
	_, err := ApplyOrderEdits(lines, ops, dd("100"), edits, InUse{Lines: map[string]bool{}, Operations: map[int]bool{}})
	if de, ok := err.(*Error); !ok || de.Code != CodeEditTargetInUse {
		t.Fatalf("want %s, got %v", CodeEditTargetInUse, err)
	}
}

func TestResolveOCMProfile(t *testing.T) {
	site := "55555555-5555-4555-8555-555555555555"
	profiles := []OCMProfile{
		{ItemGroup: AllKey, SiteID: AllKey, Mode: OCMNone},
		{ItemGroup: AllKey, SiteID: site, Mode: OCMAutomatic},
		{ItemGroup: "FG", SiteID: AllKey, Mode: OCMManual},
	}
	for _, c := range []struct {
		group, site string
		want        OCMMode
	}{{"FG", site, OCMManual}, {"RM", site, OCMAutomatic}, {"RM", "other", OCMNone}, {"", "other", OCMNone}} {
		if got := ResolveOCMProfile(profiles, c.group, c.site).Mode; got != c.want {
			t.Errorf("%s × %s: %s, want %s", c.group, c.site, got, c.want)
		}
	}
	if p := ResolveOCMProfile(nil, "FG", site); p.Mode != OCMManual || !p.SurchargeFixed.IsZero() {
		t.Fatalf("default profile: %+v", p)
	}
}

// BR-21.12: change cost = new − old planned + surcharge; a negative delta still carries the fixed surcharge.
func TestComputeChangeCost(t *testing.T) {
	row := func(a string) []CostRow { return []CostRow{{Group: GroupMaterial, Key: "x", Amount: dd(a)}} }
	p := OCMProfile{SurchargeFixed: dd("25"), SurchargePct: dd("10")}
	up := ComputeChangeCost(row("1000"), row("1300"), p, 2)
	if !up.Delta.Equal(dd("300")) || !up.Surcharge.Equal(dd("55")) || !up.Total.Equal(dd("355")) {
		t.Fatalf("increase: %+v", up)
	}
	down := ComputeChangeCost(row("1000"), row("900"), p, 2)
	if !down.Delta.Equal(dd("-100")) || !down.Surcharge.Equal(dd("25")) || !down.Total.Equal(dd("-75")) {
		t.Fatalf("decrease: %+v", down)
	}
	// Amounts round to the minor unit and the total is the sum of the rounded parts:
	// delta 0.01 (0.004 → 0.00, 0.014 → 0.01), surcharge 25 + 10% of 0.01 = 25.001 → 25.00.
	odd := ComputeChangeCost(row("0.004"), row("0.014"), p, 2)
	if !odd.Delta.Equal(dd("0.01")) || !odd.Surcharge.Equal(dd("25")) || !odd.Total.Equal(odd.Delta.Add(odd.Surcharge)) {
		t.Fatalf("rounding: %+v", odd)
	}
	// A zero-decimal currency rounds to whole units.
	if yen := ComputeChangeCost(row("100"), row("100.6"), OCMProfile{SurchargePct: dd("50")}, 0); !yen.Total.Equal(dd("2")) {
		t.Fatalf("zero decimals: %+v", yen)
	}
}

func TestAutoApplies(t *testing.T) {
	if !AutoApplies(OCMAutomatic, false, CmdRelease) {
		t.Fatal("an automatic profile applies at release")
	}
	for name, ok := range map[string]bool{
		"manual":      AutoApplies(OCMManual, false, CmdRelease),
		"hand-edited": AutoApplies(OCMAutomatic, true, CmdRelease),
		"cancel":      AutoApplies(OCMAutomatic, false, CmdCancel),
	} {
		if ok {
			t.Errorf("%s must not apply automatically", name)
		}
	}
}

func TestCheckChangeEligible(t *testing.T) {
	for _, s := range []Status{Created, Estimated, Scheduled, Released} {
		if err := CheckChangeEligible(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []Status{Started, ReportedAsFinished, Ended, Cancelled} {
		if de, ok := CheckChangeEligible(s).(*Error); !ok || de.Code != CodeChangeNotEligible {
			t.Errorf("%s must be refused", s)
		}
	}
}

// An ECO that changes only the queue or transit hours of an operation is a change, and applying it
// carries the new hours to the order (they date the schedule).
func TestDiffSnapshot_QueueAndTransitHours(t *testing.T) {
	lines, ops := ocmOrder(t)
	next := append([]Operation(nil), ops...)
	next[1].QueueBeforeH, next[1].TransitH = dd("2"), dd("0.5")
	edits, delta := DiffSnapshot(lines, ops, lines, next, false, true)
	if delta.OperationsChanged != 1 || edits.Count() != 1 {
		t.Fatalf("delta %+v", delta)
	}
	res, err := ApplyOrderEdits(lines, ops, dd("100"), edits, InUse{Lines: map[string]bool{}, Operations: map[int]bool{}})
	if err != nil || !res.Operations[1].QueueBeforeH.Equal(dd("2")) || !res.Operations[1].TransitH.Equal(dd("0.5")) || !res.Operations[1].QueueAfterH.IsZero() {
		t.Fatalf("%+v %v", res.Operations, err)
	}
}
