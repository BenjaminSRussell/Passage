package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func errCode(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func plannedStudy(t *testing.T) Study {
	t.Helper()
	st, err := NewStudy(StudySpec{ItemID: "11111111-1111-4111-8111-111111111111", BatchID: "B-1", Protocol: "ICH Q1A(R2)",
		Condition: "25C/60RH", StartDate: time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC), TestGroupID: "22222222-2222-4222-8222-222222222222",
		PullMonths: []int{0, 3, 6}, LeadDays: 7, WindowDays: 3})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// FR-33.16: PLANNED -> ACTIVE -> COMPLETED, or TERMINATED (with a reason) from either open state.
func TestStudyTransition(t *testing.T) {
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		from, to, reason string
		code             string
	}{
		{StudyPlanned, StudyActive, "", ""},
		{StudyPlanned, StudyTerminated, "batch recalled", ""},
		{StudyActive, StudyCompleted, "", ""},
		{StudyActive, StudyTerminated, "chamber failure", ""},
		{StudyPlanned, StudyCompleted, "", CodeStudyStatus},
		{StudyActive, StudyActive, "", CodeStudyStatus},
		{StudyCompleted, StudyTerminated, "late", CodeStudyStatus},
		{StudyTerminated, StudyActive, "", CodeStudyStatus},
		{StudyActive, StudyPlanned, "", CodeValidation},
		{StudyActive, "OPEN", "", CodeValidation},
		{StudyActive, StudyTerminated, "   ", CodeValidation},
		{StudyActive, StudyCompleted, strings.Repeat("x", 501), CodeValidation},
	}
	for _, c := range cases {
		st := plannedStudy(t)
		st.Status = c.from
		next, err := st.Transition(c.to, c.reason, "qa-1", now)
		if got := errCode(err); got != c.code {
			t.Fatalf("%s -> %s (%q): code %q, want %q", c.from, c.to, c.reason, got, c.code)
		}
		if c.code != "" {
			if next.Status != c.from {
				t.Fatalf("%s -> %s: a refused change must leave the status", c.from, c.to)
			}
			continue
		}
		if next.Status != c.to || next.StatusChangedBy != "qa-1" || next.StatusChangedAt == nil || !next.StatusChangedAt.Equal(now) ||
			next.StatusReason != strings.TrimSpace(c.reason) {
			t.Fatalf("%s -> %s: %+v", c.from, c.to, next)
		}
	}
}

func TestStudyTerminateCancelsPlannedPullsOnly(t *testing.T) {
	st := plannedStudy(t)
	st.Status = StudyActive
	st.Pulls[0].Status = PullPulled
	st.Pulls[1].Status = PullOrdered
	next, err := st.Transition(StudyTerminated, "chamber failure", "qa-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{PullPulled, PullOrdered, PullCancelled}
	for i, p := range next.Pulls {
		if p.Status != want[i] {
			t.Fatalf("pull %d months: %s, want %s", p.Months, p.Status, want[i])
		}
	}
	if st.Pulls[2].Status != PullPlanned {
		t.Fatal("Transition must not change the study it was called on")
	}
	done, err := st.Transition(StudyCompleted, "", "qa-1", time.Now())
	if err != nil || done.Pulls[2].Status != PullPlanned {
		t.Fatalf("completing a study keeps its pulls: %+v %v", done.Pulls, err)
	}
}

func TestValidateReceiptCheck(t *testing.T) {
	ok := ReceiptCheckInput{POLineID: "11111111-1111-4111-8111-111111111111", Batch: " B ", ReceiptLineID: "22222222-2222-4222-8222-222222222222",
		ReceivedQty: dec("10.5")}
	if f := ValidateReceiptCheck(ok); len(f) != 0 {
		t.Fatalf("valid: %v", f)
	}
	bad := ReceiptCheckInput{POLineID: "PO-1", Batch: strings.Repeat("b", 61), ReceivedQty: dec("0.0000001")}
	f := ValidateReceiptCheck(bad)
	for _, name := range []string{"poLineId", "receiptLineId", "batch", "receivedQty"} {
		if f[name] == "" {
			t.Errorf("%s must be refused: %v", name, f)
		}
	}
	if f := ValidateReceiptCheck(ReceiptCheckInput{POLineID: ok.POLineID, Batch: "B", ReceiptLineID: ok.ReceiptLineID, ReceivedQty: dec("-1")}); f["receivedQty"] == "" {
		t.Error("a negative receipt must be refused")
	}
}

// AC-33.14 and BR-33.16: an accepted source inspection of 1,000 for batch B covers
// a receipt of 1,000 of B; batch C gets a normal order; what one receipt used is
// not available to the next.
func TestCheckReceipt(t *testing.T) {
	in := func(batch, qty string) ReceiptCheckInput {
		return ReceiptCheckInput{POLineID: "PO", Batch: batch, ReceiptLineID: "R", ReceivedQty: dec(qty)}
	}
	now := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name                  string
		in                    ReceiptCheckInput
		accepted, before      string
		skip                  bool
		covered, normal, code string
	}{
		{"AC-33.14 batch B fully covered", in("B", "1000"), "1000", "0", true, "1000", "0", CodeSourceSkipped},
		{"AC-33.14 batch C not inspected", in("C", "1000"), "0", "0", false, "0", "1000", ""},
		{"excess over accepted", in("B", "1200"), "1000", "0", false, "1000", "200", ""},
		{"second receipt sees what is left", in("B", "600"), "1000", "600", false, "400", "200", ""},
		{"accepted used up", in("B", "10"), "1000", "1000", false, "0", "10", ""},
		{"fractional quantities", in("B", "2.5"), "2.75", "0.25", true, "2.5", "0", CodeSourceSkipped},
	}
	for _, c := range cases {
		got, err := CheckReceipt(c.in, dec(c.accepted), dec(c.before), "rcv-1", now)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.Skip != c.skip || !got.CoveredQty.Equal(dec(c.covered)) || !got.NormalQty.Equal(dec(c.normal)) || got.Code != c.code {
			t.Fatalf("%s: %+v", c.name, got)
		}
		if got.Message == "" || got.CheckedBy != "rcv-1" || !got.CheckedAt.Equal(now) {
			t.Fatalf("%s: message and audit fields: %+v", c.name, got)
		}
	}
	if _, err := CheckReceipt(in("B", "0"), dec("10"), dec("0"), "u", now); err == nil {
		t.Fatal("a zero receipt must be refused")
	}
	if _, err := CheckReceipt(in("B", "1"), dec("-1"), dec("0"), "u", now); err == nil {
		t.Fatal("a negative accepted quantity must be refused")
	}
}

// BR-33.16 as a property: covered + normal = received, the cover never exceeds what
// is left of the accepted quantity, and the receipt skips iff what is left covers it.
func TestCheckReceiptProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		acc := decimal.NewFromInt(rapid.Int64Range(0, 5000).Draw(t, "accepted"))
		before := decimal.NewFromInt(rapid.Int64Range(0, 5000).Draw(t, "coveredBefore"))
		recv := decimal.NewFromInt(rapid.Int64Range(1, 5000).Draw(t, "received"))
		got, err := CheckReceipt(ReceiptCheckInput{POLineID: "PO", Batch: "B", ReceiptLineID: "R", ReceivedQty: recv}, acc, before, "u", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		left := decimal.Max(acc.Sub(before), decimal.Zero)
		if !got.CoveredQty.Add(got.NormalQty).Equal(recv) || got.CoveredQty.GreaterThan(left) || got.Skip != left.GreaterThanOrEqual(recv) {
			t.Fatalf("accepted %s before %s received %s: %+v", acc, before, recv, got)
		}
	})
}

func closedOrderWithDecision() (Order, TestGroup, *Decision) {
	min, max := dec("6.5"), dec("7.5")
	g := TestGroup{ID: "g1", Code: "PH", Version: 2, Tests: []TestLine{
		{TestID: "t-ph", Name: "pH", Kind: KindVariable, Required: true, Decimals: 1, Min: &min, Max: &max},
		{TestID: "t-app", Name: "Appearance", Kind: KindAttribute, Required: true, PassingOutcomes: []string{"clear"}},
	}}
	closedAt := time.Date(2026, 4, 2, 9, 0, 0, 0, time.UTC)
	o := Order{ID: "o1", ItemID: "i1", Status: StatusClosed, UOM: "KG", LotQty: dec("100"), Dims: map[string]string{"batch": "B-7"},
		TestGroupID: g.ID, ClosedAt: &closedAt}
	d := &Decision{ID: "d1", OrderID: o.ID, DecidedBy: "qa-release", DecidedAt: closedAt,
		Lines: []DecisionLine{{Decision: DecisionAccept, Quantity: dec("90")}, {Decision: DecisionAcceptWithDeviation, Quantity: dec("10"), Reason: " colour off-white "}},
		Split: Split{Accept: dec("90"), Reject: decimal.Zero, Deviation: dec("10")}}
	return o, g, d
}

// BR-33.10: the CoA prints the results the accepting decision was taken on, the
// latest retest of each test and sample, in test group order, signed by the
// decision user; a later revision supersedes the previous one and needs a reason.
func TestBuildCoA(t *testing.T) {
	o, g, d := closedOrderWithDecision()
	at := d.DecidedAt
	v := func(s string) *decimal.Decimal { x := dec(s); return &x }
	results := []Result{
		{TestID: "t-app", SampleNo: 1, OutcomeID: "clear", Passed: true, RecordedAt: at.Add(-time.Hour)},
		{TestID: "t-ph", SampleNo: 2, Value: v("7.1"), Passed: true, RecordedAt: at.Add(-time.Hour)},
		{TestID: "t-ph", SampleNo: 1, Value: v("7.9"), Passed: false, RecordedAt: at.Add(-2 * time.Hour)},
		{TestID: "t-ph", SampleNo: 1, RetestNo: 1, Value: v("7.2"), Passed: true, InstrumentID: "ph-01", RecordedAt: at.Add(-time.Hour)},
		{TestID: "t-ph", SampleNo: 1, RetestNo: 2, Value: v("6.0"), Passed: false, RecordedAt: at.Add(time.Hour)},
		{TestID: "t-gone", SampleNo: 1, Passed: true, RecordedAt: at.Add(-time.Hour)},
	}
	now := at.Add(48 * time.Hour)
	doc, err := BuildCoA(o, g, d, results, nil, CoARequest{QualityOrderID: o.ID}, "clerk-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Revision != 1 || doc.Supersedes != "" || doc.SignedBy != "qa-release" || !doc.SignedAt.Equal(at) || doc.IssuedBy != "clerk-1" ||
		doc.Batch != "B-7" || !doc.AcceptQty.Equal(dec("90")) || !doc.DeviationQty.Equal(dec("10")) || doc.SpecificationSet != SpecDefault ||
		doc.TestGroupCode != "PH" || doc.TestGroupVersion != 2 || doc.DecisionID != "d1" {
		t.Fatalf("header: %+v", doc)
	}
	if len(doc.Deviations) != 1 || doc.Deviations[0] != "colour off-white" {
		t.Fatalf("deviation reasons: %v", doc.Deviations)
	}
	if len(doc.Lines) != 3 {
		t.Fatalf("one line per test and sample of the group: %+v", doc.Lines)
	}
	first := doc.Lines[0]
	if first.TestID != "t-ph" || first.SampleNo != 1 || first.RetestNo != 1 || !first.Value.Equal(dec("7.2")) || first.InstrumentID != "ph-01" ||
		!first.Min.Equal(dec("6.5")) || !first.Max.Equal(dec("7.5")) || first.TestName != "pH" {
		t.Fatalf("the retest the decision saw replaces the first result, a later one is not printed: %+v", first)
	}
	if doc.Lines[1].TestID != "t-ph" || doc.Lines[1].SampleNo != 2 || doc.Lines[2].TestID != "t-app" {
		t.Fatalf("lines follow the test group, then the sample: %+v", doc.Lines)
	}
	if _, err := BuildCoA(o, g, d, results, &doc, CoARequest{QualityOrderID: o.ID}, "clerk-1", now); errCode(err) != CodeCoAReasonRequired {
		t.Fatalf("a second revision without a reason: %v", err)
	}
	rev2, err := BuildCoA(o, g, d, results, &doc, CoARequest{QualityOrderID: o.ID, Reason: " customer asked for metric units "}, "clerk-2", now)
	if err != nil || rev2.Revision != 2 || rev2.Reason != "customer asked for metric units" {
		t.Fatalf("revision 2: %+v %v", rev2, err)
	}
	doc.ID = "coa-1"
	if rev2, _ = BuildCoA(o, g, d, results, &doc, CoARequest{QualityOrderID: o.ID, Reason: "x"}, "clerk-2", now); rev2.Supersedes != "coa-1" {
		t.Fatalf("revision 2 supersedes revision 1: %+v", rev2)
	}
}

func TestBuildCoA_RefusesWhatWasNotReleased(t *testing.T) {
	o, g, d := closedOrderWithDecision()
	req := CoARequest{QualityOrderID: o.ID}
	open := o
	open.Status = StatusPending
	if _, err := BuildCoA(open, g, nil, nil, nil, req, "u", time.Now()); errCode(err) != CodeCoANotReleasable {
		t.Fatalf("an open order: %v", err)
	}
	if _, err := BuildCoA(o, g, nil, nil, nil, req, "u", time.Now()); errCode(err) != CodeCoANotReleasable {
		t.Fatalf("a closed order without its decision: %v", err)
	}
	rejected := *d
	rejected.Lines = []DecisionLine{{Decision: DecisionReject, Quantity: dec("100")}}
	rejected.Split = Split{Accept: decimal.Zero, Reject: dec("100"), Deviation: decimal.Zero}
	if _, err := BuildCoA(o, g, &rejected, nil, nil, req, "u", time.Now()); errCode(err) != CodeCoANotReleasable {
		t.Fatalf("a fully rejected lot: %v", err)
	}
	deviationOnly := *d
	deviationOnly.Split = Split{Accept: decimal.Zero, Reject: dec("90"), Deviation: dec("10")}
	if _, err := BuildCoA(o, g, &deviationOnly, nil, nil, req, "u", time.Now()); err != nil {
		t.Fatalf("quantity accepted with deviation is released: %v", err)
	}
}

func TestValidateCoARequest(t *testing.T) {
	if f := ValidateCoARequest(CoARequest{QualityOrderID: "11111111-1111-4111-8111-111111111111"}); len(f) != 0 {
		t.Fatalf("valid: %v", f)
	}
	f := ValidateCoARequest(CoARequest{QualityOrderID: "o1", ShipmentID: "s", CustomerID: "c", Reason: strings.Repeat("r", 501)})
	for _, name := range []string{"qualityOrderId", "shipmentId", "customerId", "reason"} {
		if f[name] == "" {
			t.Errorf("%s must be refused: %v", name, f)
		}
	}
}
