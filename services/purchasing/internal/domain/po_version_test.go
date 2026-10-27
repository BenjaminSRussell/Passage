package domain

import (
	"reflect"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func snapLine(no int, qty, price int64, date string) SnapshotLine {
	q, p := decimal.NewFromInt(qty), decimal.NewFromInt(price)
	d, err := ParseDay(date)
	if err != nil {
		panic(err)
	}
	return SnapshotLine{LineNo: no, ProductID: "p", Unit: "EA", Qty: q, UnitPrice: p,
		PriceUnit: decimal.NewFromInt(1), DiscountPct: decimal.Zero, LineAmount: q.Mul(p),
		TaxAmount: decimal.Zero, DeliveryDate: d, Status: POLineOpen}
}

func snap(total int64, lines ...SnapshotLine) Snapshot {
	return NewSnapshot("USD", decimal.NewFromInt(total), lines, nil)
}

// TestChangeOrder_QtyIncreaseNeedsReapproval is AC-17.2: qty 100 -> 110 under
// "qty increase > 5 % requires re-approval" gives a patch on /lines/0/qty and IN_REVIEW.
func TestChangeOrder_QtyIncreaseNeedsReapproval(t *testing.T) {
	base := snap(1000, snapLine(1, 100, 10, "2027-01-05"))
	next := snap(1100, snapLine(1, 110, 10, "2027-01-05"))
	patch, err := VersionDiff(base, next)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, op := range patch {
		if op.Op == "replace" && op.Path == "/lines/0/qty" {
			found = true
		}
	}
	if !found {
		t.Fatalf("patch = %+v, want replace /lines/0/qty", patch)
	}
	if !ReapprovalRequired(base.LineStates(), next.LineStates(), DefaultReapprovalRules()) {
		t.Fatal("qty increase above 5 % must require re-approval")
	}
}

// TestChangeOrder_DeliveryDateMoveNoReapproval is AC-17.3: a one-day move under
// the default rules is a new version only.
func TestChangeOrder_DeliveryDateMoveNoReapproval(t *testing.T) {
	base := snap(1000, snapLine(1, 100, 10, "2027-01-05"))
	next := snap(1000, snapLine(1, 100, 10, "2027-01-06"))
	patch, err := VersionDiff(base, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(patch) != 1 || patch[0].Path != "/lines/0/deliveryDate" {
		t.Fatalf("patch = %+v", patch)
	}
	if ReapprovalRequired(base.LineStates(), next.LineStates(), DefaultReapprovalRules()) {
		t.Fatal("delivery date move must not require re-approval under default rules")
	}
}

func TestChangeOrder_IdenticalSnapshotsHaveEmptyPatch(t *testing.T) {
	s := snap(10, snapLine(1, 1, 10, "2027-01-05"))
	patch, err := VersionDiff(s, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(patch) != 0 {
		t.Fatalf("patch = %+v, want none", patch)
	}
}

// TestVersionPatch_ApplyRoundTrip checks apply(patch, v_n) = v_{n+1} over random
// line sets (the version-diff test in the PRD test plan).
func TestVersionPatch_ApplyRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		mk := func(n int) Snapshot {
			lines := make([]SnapshotLine, 0, n)
			for i := 1; i <= n; i++ {
				q := rapid.Int64Range(1, 500).Draw(t, "qty")
				pr := rapid.Int64Range(1, 500).Draw(t, "price")
				d := rapid.SampledFrom([]string{"2027-01-05", "2027-01-12", "2027-02-01"}).Draw(t, "date")
				lines = append(lines, snapLine(i, q, pr, d))
			}
			return NewSnapshot("USD", decimal.NewFromInt(int64(n)), lines, nil)
		}
		a := mk(rapid.IntRange(1, 5).Draw(t, "nA"))
		b := mk(rapid.IntRange(1, 5).Draw(t, "nB"))
		patch, err := VersionDiff(a, b)
		if err != nil {
			t.Fatal(err)
		}
		na, err := Normalize(a)
		if err != nil {
			t.Fatal(err)
		}
		applied, err := Apply(na, patch)
		if err != nil {
			t.Fatal(err)
		}
		want, err := Normalize(b)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(applied, want) {
			t.Fatalf("apply = %v\nwant    %v", applied, want)
		}
	})
}

func TestCheckEditable(t *testing.T) {
	cases := map[string]bool{
		POStatusDraft: true, POStatusInReview: true, POStatusConfirmed: true, POStatusRejected: true,
		POStatusApproved: false, POStatusCancelled: false, POStatusFinalized: false,
	}
	for status, ok := range cases {
		err := CheckEditable(status)
		if (err == nil) != ok {
			t.Errorf("CheckEditable(%s) error = %v, want editable=%v", status, err, ok)
		}
	}
}

func TestSnapshotLineStates_ParsesDates(t *testing.T) {
	s := snap(0, snapLine(1, 1, 1, "2027-03-09"))
	got := s.LineStates()[0].DeliveryDate.Time
	want := time.Date(2027, 3, 9, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("delivery date = %v, want %v", got, want)
	}
}

// TestChangeOrder_CancelledLineKeepsIndexes is BR-17.9: removing line 1 of two
// leaves it in the version as CANCELLED, so the patch is one status replace and
// line 2 keeps index 1.
func TestChangeOrder_CancelledLineKeepsIndexes(t *testing.T) {
	base := snap(1100, snapLine(1, 100, 10, "2027-01-05"), snapLine(2, 10, 10, "2027-01-05"))
	gone := snapLine(1, 100, 10, "2027-01-05")
	gone.Status = POLineCancelled
	next := snap(100, gone, snapLine(2, 10, 10, "2027-01-05"))
	patch, err := VersionDiff(base, next)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"/lines/0/status": POLineCancelled, "/total": "100"}
	if len(patch) != len(want) {
		t.Fatalf("patch = %+v, want %v", patch, want)
	}
	for _, op := range patch {
		if op.Op != "replace" || want[op.Path] != op.Value {
			t.Fatalf("unexpected op %+v", op)
		}
	}
	if ChangeNeedsReapproval(base, next, patch, DefaultReapprovalRules()) {
		t.Fatal("cancelling a line is not a reapproval trigger under the default rules")
	}
	if !ChangeNeedsReapproval(base, next, patch, []ReapprovalRule{{Kind: RuleAnyChange}}) {
		t.Fatal("ANY_CHANGE must fire on a cancelled line")
	}
}

// TestChangeNeedsReapproval_AnyChangeSeesCharges: a charge-only change has no
// line delta, so only ANY_CHANGE sends it back for approval.
func TestChangeNeedsReapproval_AnyChangeSeesCharges(t *testing.T) {
	base := snap(1000, snapLine(1, 100, 10, "2027-01-05"))
	next := base
	next.Charges = []SnapshotCharge{{Calc: CalcFixed, Value: decimal.NewFromInt(25), Amount: decimal.NewFromInt(25)}}
	next.Total = decimal.NewFromInt(1025)
	patch, err := VersionDiff(base, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(patch) == 0 {
		t.Fatal("a new charge must produce a patch")
	}
	if ChangeNeedsReapproval(base, next, patch, DefaultReapprovalRules()) {
		t.Fatal("default rules ignore charges")
	}
	if !ChangeNeedsReapproval(base, next, patch, []ReapprovalRule{{Kind: RuleAnyChange}}) {
		t.Fatal("ANY_CHANGE must fire on a charge change")
	}
	if ChangeNeedsReapproval(base, base, nil, []ReapprovalRule{{Kind: RuleAnyChange}}) {
		t.Fatal("no patch, no reapproval")
	}
}
