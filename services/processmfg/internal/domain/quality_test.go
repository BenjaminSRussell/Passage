package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func dp(s string) *decimal.Decimal { v := d(s); return &v }

func potencyDef() AttributeDef {
	return AttributeDef{Name: "potency", Kind: AttrNumeric, Target: dp("50"), Min: dp("40"), Max: dp("60")}
}

func TestCheckAttributes_LimitsFlagWithoutRefusing(t *testing.T) {
	defs := []AttributeDef{
		potencyDef(),
		{Name: "grade", Kind: AttrEnum, Options: []string{"A", "B"}},
		{Name: "note", Kind: AttrText},
		{Name: "expiry", Kind: AttrDate},
	}
	got, err := CheckAttributes(defs, map[string]Value{
		"potency": num("61"), "grade": text("B"), "note": text("ok"), "expiry": date("2027-06-01"),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []AttributeCheck{
		{Name: "expiry", Pass: true},
		{Name: "grade", Pass: true},
		{Name: "note", Pass: true},
		{Name: "potency", Pass: false, Detail: "potency 61 is above the maximum 60"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("result %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCheckAttributes_NumericBoundsAreInclusive(t *testing.T) {
	for _, tc := range []struct {
		v    string
		pass bool
	}{{"40", true}, {"60", true}, {"39.9999", false}, {"60.0001", false}} {
		got, err := CheckAttributes([]AttributeDef{potencyDef()}, map[string]Value{"potency": num(tc.v)})
		if err != nil {
			t.Fatal(err)
		}
		if got[0].Pass != tc.pass {
			t.Fatalf("potency %s: pass = %v, want %v", tc.v, got[0].Pass, tc.pass)
		}
	}
}

func TestCheckAttributes_Refusals(t *testing.T) {
	enum := AttributeDef{Name: "grade", Kind: AttrEnum, Options: []string{"A"}}
	cases := []struct {
		name   string
		defs   []AttributeDef
		values map[string]Value
	}{
		{"undefined attribute", []AttributeDef{potencyDef()}, map[string]Value{"colour": text("red")}},
		{"text for a number", []AttributeDef{potencyDef()}, map[string]Value{"potency": text("high")}},
		{"empty value", []AttributeDef{potencyDef()}, map[string]Value{"potency": {}}},
		{"enum value not an option", []AttributeDef{enum}, map[string]Value{"grade": text("C")}},
		{"date for an enum", []AttributeDef{enum}, map[string]Value{"grade": date("2027-01-01")}},
		{"number for a date", []AttributeDef{{Name: "expiry", Kind: AttrDate}}, map[string]Value{"expiry": num("1")}},
		{"unknown kind", []AttributeDef{{Name: "x", Kind: "colour"}}, nil},
		{"defined twice", []AttributeDef{potencyDef(), potencyDef()}, nil},
		{"min above max", []AttributeDef{{Name: "p", Kind: AttrNumeric, Min: dp("5"), Max: dp("4")}}, nil},
		{"limits on text", []AttributeDef{{Name: "n", Kind: AttrText, Max: dp("4")}}, nil},
		{"enum without options", []AttributeDef{{Name: "g", Kind: AttrEnum}}, nil},
		{"options on numeric", []AttributeDef{{Name: "p", Kind: AttrNumeric, Options: []string{"A"}}}, nil},
		{"unnamed", []AttributeDef{{Kind: AttrText}}, nil},
		{"empty option", []AttributeDef{{Name: "g", Kind: AttrEnum, Options: []string{"A", " "}}}, nil},
		{"repeated option", []AttributeDef{{Name: "g", Kind: AttrEnum, Options: []string{"A", "A"}}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := CheckAttributes(tc.defs, tc.values); codeOf(err) != CodeInvalid {
				t.Fatalf("err = %v, want %s", err, CodeInvalid)
			}
		})
	}
}

func TestCheckRole(t *testing.T) {
	for _, tc := range []struct {
		kind, role string
		ok         bool
	}{
		{AttrNumeric, RoleGeneric, true},
		{AttrNumeric, RolePotency, true},
		{AttrText, RoleGeneric, true},
		{AttrEnum, RolePotency, false},
		{AttrDate, RolePotency, false},
		{AttrNumeric, "assay", false},
		{AttrNumeric, "", false},
	} {
		err := CheckRole("a", tc.kind, tc.role)
		if (err == nil) != tc.ok || (err != nil && codeOf(err) != CodeInvalid) {
			t.Errorf("%s/%q: err = %v, want ok=%v", tc.kind, tc.role, err, tc.ok)
		}
	}
}

func batchWith(id, disposition, qty string, values map[string]Value) Batch {
	return Batch{ID: id, Disposition: disposition, Qty: d(qty), Values: values}
}

// AC-24.4, AC-24.10, BR-24.10 and FR-24.3 at pick time.
func TestCheckPickable(t *testing.T) {
	line := PickLine{PostingDate: ymd("2027-01-15")}
	banded := PickLine{PostingDate: line.PostingDate, Potency: &PotencyBand{Attribute: "potency", Min: d("40"), Max: d("60")}}
	expired := map[string]Value{ExpiryAttribute: date("2027-01-14")}
	held := ExpiryOverride{DutyHeld: true, Reason: "QA approved use"}
	cases := []struct {
		name       string
		batch      Batch
		line       PickLine
		ov         ExpiryOverride
		code       string
		overridden bool
	}{
		{"available and unexpired", batchWith("B1", "available", "1", map[string]Value{ExpiryAttribute: date("2027-06-01")}), line, ExpiryOverride{}, "", false},
		{"on-hold is excluded (AC-24.10)", batchWith("B2", "on-hold", "1", nil), line, ExpiryOverride{}, CodeNotPickable, false},
		{"quarantine is excluded", batchWith("B3", "quarantine", "1", nil), line, ExpiryOverride{}, CodeNotPickable, false},
		{"disposition beats the override", batchWith("B3b", "rejected", "1", expired), line, held, CodeNotPickable, false},
		{"expired without the duty (AC-24.4)", batchWith("B4", "available", "1", expired), line, ExpiryOverride{Reason: "QA approved use"}, CodeBatchExpired, false},
		{"expiring on the posting date is still good", batchWith("B5", "available", "1", map[string]Value{ExpiryAttribute: date("2027-01-15")}), line, ExpiryOverride{}, "", false},
		{"expired with duty and reason is an override", batchWith("B6", "available", "1", expired), line, held, "", true},
		{"reason of ten characters after trimming", batchWith("B6b", "available", "1", expired), line, ExpiryOverride{DutyHeld: true, Reason: "  0123456789  "}, "", true},
		{"short reason is malformed", batchWith("B7", "available", "1", expired), line, ExpiryOverride{DutyHeld: true, Reason: "  short   "}, CodeInvalid, false},
		{"nine multibyte characters are too short", batchWith("B7b", "available", "1", expired), line, ExpiryOverride{DutyHeld: true, Reason: "ééééééééé"}, CodeInvalid, false},
		{"potency inside band", batchWith("B8", "available", "1", map[string]Value{"potency": num("45")}), banded, ExpiryOverride{}, "", false},
		{"potency at the band edge", batchWith("B9", "available", "1", map[string]Value{"potency": num("60")}), banded, ExpiryOverride{}, "", false},
		{"potency above band", batchWith("B10", "available", "1", map[string]Value{"potency": num("60.5")}), banded, ExpiryOverride{}, CodePotencyRange, false},
		{"potency not recorded", batchWith("B11", "available", "1", nil), banded, ExpiryOverride{}, CodePotencyRange, false},
		{"no band ignores potency", batchWith("B12", "available", "1", map[string]Value{"potency": num("1")}), line, ExpiryOverride{}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			overridden, err := CheckPickable(tc.batch, tc.line, tc.ov)
			if codeOf(err) != tc.code || (tc.code == "" && err != nil) {
				t.Fatalf("err = %v, want code %q", err, tc.code)
			}
			if overridden != tc.overridden {
				t.Fatalf("overridden = %v, want %v", overridden, tc.overridden)
			}
		})
	}
}

func TestProposePicking_FEFOAndExclusions(t *testing.T) {
	line := PickLine{PostingDate: ymd("2027-01-15")}
	cands := []Batch{
		batchWith("late", "available", "100", map[string]Value{ExpiryAttribute: date("2027-09-01")}),
		batchWith("none", "available", "100", nil),
		batchWith("hold", "on-hold", "100", map[string]Value{ExpiryAttribute: date("2027-02-01")}),
		batchWith("early", "available", "30", map[string]Value{ExpiryAttribute: date("2027-03-01")}),
		batchWith("old", "available", "50", map[string]Value{ExpiryAttribute: date("2026-12-31")}),
	}
	got, err := ProposePicking(cands, line, d("80"), ExpiryOverride{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Picked) != 2 || got.Picked[0].BatchID != "early" || !got.Picked[0].Qty.Equal(d("30")) ||
		got.Picked[1].BatchID != "late" || !got.Picked[1].Qty.Equal(d("50")) {
		t.Fatalf("picked %+v", got.Picked)
	}
	if !got.Shortfall.IsZero() {
		t.Fatalf("shortfall %s", got.Shortfall)
	}
	if len(got.Excluded) != 2 || got.Excluded[0].BatchID != "hold" || got.Excluded[0].Code != CodeNotPickable ||
		got.Excluded[1].BatchID != "old" || got.Excluded[1].Code != CodeBatchExpired {
		t.Fatalf("excluded %+v", got.Excluded)
	}

	// With the override the expired batch is consumed first and flagged.
	got, err = ProposePicking(cands, line, d("400"), ExpiryOverride{DutyHeld: true, Reason: "QA approved use"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Picked[0].BatchID != "old" || !got.Picked[0].ExpiredOverride || got.Picked[1].ExpiredOverride {
		t.Fatalf("picked %+v", got.Picked)
	}
	if got.Picked[len(got.Picked)-1].BatchID != "none" || !got.Shortfall.Equal(d("120")) {
		t.Fatalf("batches without expiry go last; shortfall %s, picked %+v", got.Shortfall, got.Picked)
	}
}

func TestProposePicking_Refusals(t *testing.T) {
	line := PickLine{PostingDate: ymd("2027-01-15")}
	a := batchWith("a", "available", "1", nil)
	if _, err := ProposePicking([]Batch{a}, line, d("0"), ExpiryOverride{}); codeOf(err) != CodeInvalid {
		t.Fatalf("zero need: %v", err)
	}
	if _, err := ProposePicking([]Batch{a, a}, line, d("1"), ExpiryOverride{}); codeOf(err) != CodeInvalid {
		t.Fatalf("duplicate batch: %v", err)
	}
	old := batchWith("old", "available", "1", map[string]Value{ExpiryAttribute: date("2026-01-01")})
	if _, err := ProposePicking([]Batch{old}, line, d("1"), ExpiryOverride{DutyHeld: true, Reason: "short"}); codeOf(err) != CodeInvalid {
		t.Fatalf("short reason: %v", err)
	}
}

// Property: the proposal never takes more than a batch holds, only takes
// pickable batches, and picked plus shortfall equals the need.
func TestProposePicking_Conserves(t *testing.T) {
	dispositions := []string{DispositionAvailable, DispositionOnHold, DispositionQuarantine, DispositionRejected}
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 8).Draw(t, "batches")
		cands := make([]Batch, n)
		for i := range cands {
			vals := map[string]Value{}
			if rapid.Bool().Draw(t, "hasExpiry") {
				vals[ExpiryAttribute] = date(ymd("2027-01-01").AddDate(0, 0, rapid.IntRange(-30, 30).Draw(t, "days")).Format("2006-01-02"))
			}
			cands[i] = Batch{ID: string(rune('a' + i)), Disposition: rapid.SampledFrom(dispositions).Draw(t, "disp"),
				Qty: decimal.NewFromInt(rapid.Int64Range(1, 500).Draw(t, "qty")), Values: vals}
		}
		need := decimal.NewFromInt(rapid.Int64Range(1, 2000).Draw(t, "need"))
		got, err := ProposePicking(cands, PickLine{PostingDate: ymd("2027-01-01")}, need, ExpiryOverride{})
		if err != nil {
			t.Fatal(err)
		}
		byID := map[string]Batch{}
		for _, b := range cands {
			byID[b.ID] = b
		}
		sum := decimal.Zero
		for _, p := range got.Picked {
			b := byID[p.BatchID]
			if _, err := CheckPickable(b, PickLine{PostingDate: ymd("2027-01-01")}, ExpiryOverride{}); err != nil {
				t.Fatalf("picked unpickable batch %s: %v", b.ID, err)
			}
			if p.Qty.GreaterThan(b.Qty) || !p.Qty.IsPositive() {
				t.Fatalf("took %s from %s holding %s", p.Qty, b.ID, b.Qty)
			}
			sum = sum.Add(p.Qty)
		}
		if !sum.Add(got.Shortfall).Equal(need) || got.Shortfall.IsNegative() {
			t.Fatalf("picked %s + shortfall %s != need %s", sum, got.Shortfall, need)
		}
		if len(got.Picked)+len(got.Excluded) > n {
			t.Fatalf("more rows than candidates")
		}
	})
}

// AC-24.18 (planning part): rework of B-77 with the defaults keeps the batch
// number, outputs to quarantine, needs a quality order and lists B-77 as parent.
func TestPlanRework_AC2418Defaults(t *testing.T) {
	src := batchWith("B-77", "on-hold", "500", nil)
	plan, err := PlanRework(src, d("500"), d("480"), DefaultReworkParams())
	if err != nil {
		t.Fatal(err)
	}
	if plan.OutputBatchID != "B-77" || plan.OutputDisposition != DispositionQuarantine || !plan.QualityOrderRequired ||
		plan.VarianceType != VarianceRework || len(plan.Parents) != 1 || plan.Parents[0] != "B-77" || !plan.OutputQty.Equal(d("480")) {
		t.Fatalf("plan %+v", plan)
	}
}

func TestPlanRework_Parameters(t *testing.T) {
	src := batchWith("B-78", "rejected", "100", nil)
	plan, err := PlanRework(src, d("40"), d("40"), ReworkParams{OutputDisposition: DispositionAvailable, Capitalize: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.OutputBatchID != "" || plan.QualityOrderRequired || plan.VarianceType != VarianceCapitalized || !plan.InputQty.Equal(d("40")) {
		t.Fatalf("plan %+v", plan)
	}
}

func TestPlanRework_Refusals(t *testing.T) {
	src := batchWith("B-79", "available", "100", nil)
	def := DefaultReworkParams()
	cases := []struct {
		name    string
		src     Batch
		in, out string
		p       ReworkParams
	}{
		{"more than the batch holds", src, "100.000001", "1", def},
		{"zero input", src, "0", "1", def},
		{"zero output", src, "10", "0", def},
		{"empty batch", batchWith("B-0", "available", "0", nil), "1", "1", def},
		{"available output with a quality order", src, "10", "10", ReworkParams{RequireQualityOrder: true, OutputDisposition: DispositionAvailable}},
		{"rejected output", src, "10", "10", ReworkParams{OutputDisposition: DispositionRejected}},
		{"unknown output disposition", src, "10", "10", ReworkParams{OutputDisposition: "scrapped"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := PlanRework(tc.src, d(tc.in), d(tc.out), tc.p); codeOf(err) != CodeInvalid {
				t.Fatalf("err = %v, want %s", err, CodeInvalid)
			}
		})
	}
}
