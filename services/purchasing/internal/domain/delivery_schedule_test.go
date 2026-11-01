package domain

import (
	"math/rand"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func dayOf(t *testing.T, s string) Day {
	t.Helper()
	d, err := ParseDay(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestBuildSchedule_SortsByDateAndNumbers(t *testing.T) {
	rows, err := BuildSchedule([]ScheduleInput{
		{Qty: dec("40"), DeliveryDate: dayOf(t, "2027-03-01")},
		{Qty: dec("60"), DeliveryDate: dayOf(t, "2027-02-01")},
	}, dec("100"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Seq != 1 || rows[0].DeliveryDate.String() != "2027-02-01" || !rows[0].Qty.Equal(dec("60")) || rows[1].Seq != 2 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestBuildSchedule_Rejections(t *testing.T) {
	d := dayOf(t, "2027-02-01")
	cases := []struct {
		name string
		in   []ScheduleInput
		qty  string
		code string
	}{
		{"empty", nil, "10", CodeValidation},
		{"zero row", []ScheduleInput{{Qty: dec("0"), DeliveryDate: d}, {Qty: dec("10"), DeliveryDate: d}}, "10", CodeValidation},
		{"negative row", []ScheduleInput{{Qty: dec("-1"), DeliveryDate: d}, {Qty: dec("11"), DeliveryDate: d}}, "10", CodeValidation},
		{"seven places", []ScheduleInput{{Qty: dec("10.0000001"), DeliveryDate: d}}, "10.0000001", CodeValidation},
		{"short", []ScheduleInput{{Qty: dec("9"), DeliveryDate: d}}, "10", CodeScheduleSum},
		{"over", []ScheduleInput{{Qty: dec("6"), DeliveryDate: d}, {Qty: dec("5"), DeliveryDate: d}}, "10", CodeScheduleSum},
	}
	for _, c := range cases {
		_, err := BuildSchedule(c.in, dec(c.qty))
		de, ok := err.(*Error)
		if !ok || de.Code != c.code {
			t.Errorf("%s: err = %v, want code %s", c.name, err, c.code)
		}
	}
	tooMany := make([]ScheduleInput, MaxScheduleRows+1)
	for i := range tooMany {
		tooMany[i] = ScheduleInput{Qty: dec("1"), DeliveryDate: d}
	}
	if _, err := BuildSchedule(tooMany, dec("101")); err == nil {
		t.Error("101 rows accepted")
	}
}

func TestCheckScheduleTarget(t *testing.T) {
	for _, s := range []string{POStatusDraft, POStatusRejected} {
		if err := CheckScheduleTarget(s, "OPEN"); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []string{POStatusInReview, POStatusApproved, POStatusConfirmed, POStatusFinalized, POStatusCancelled} {
		if err := CheckScheduleTarget(s, "OPEN"); err == nil {
			t.Errorf("%s accepted", s)
		}
	}
	if err := CheckScheduleTarget(POStatusDraft, POLineCancelled); err == nil {
		t.Error("cancelled line accepted")
	}
	if err := CheckConfirmTarget(POStatusConfirmed, "OPEN"); err != nil {
		t.Error(err)
	}
	if err := CheckConfirmTarget(POStatusApproved, "OPEN"); err == nil {
		t.Error("confirmed date on an approved PO accepted")
	}
	if err := CheckConfirmTarget(POStatusConfirmed, POLineCancelled); err == nil {
		t.Error("confirmed date on a cancelled line accepted")
	}
}

func TestAdjustToQty(t *testing.T) {
	d := dayOf(t, "2027-02-01")
	rows := []ScheduleRow{{Seq: 1, Qty: dec("60"), DeliveryDate: d}, {Seq: 2, Qty: dec("40"), DeliveryDate: d}}
	up, _ := AdjustToQty(rows, dec("110"))
	if len(up) != 2 || !up[1].Qty.Equal(dec("50")) || !rows[1].Qty.Equal(dec("40")) {
		t.Errorf("increase: %+v (input %+v)", up, rows)
	}
	down, _ := AdjustToQty(rows, dec("70"))
	if len(down) != 2 || !down[1].Qty.Equal(dec("10")) {
		t.Errorf("decrease inside the last row: %+v", down)
	}
	gone, _ := AdjustToQty(rows, dec("30"))
	if len(gone) != 1 || !gone[0].Qty.Equal(dec("30")) {
		t.Errorf("decrease past the last row: %+v", gone)
	}
	exact, _ := AdjustToQty(rows, dec("60"))
	if len(exact) != 1 || !exact[0].Qty.Equal(dec("60")) {
		t.Errorf("decrease to exactly the first row: %+v", exact)
	}
	if _, err := AdjustToQty(rows, dec("0")); err == nil {
		t.Error("zero quantity accepted")
	}
}

func TestAdjustToQty_AlwaysBalances(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 12).Draw(t, "rows")
		rows := make([]ScheduleRow, n)
		for i := range rows {
			rows[i] = ScheduleRow{Seq: i + 1, Qty: decimal.New(rapid.Int64Range(1, 1_000_000).Draw(t, "qty"), -3)}
		}
		next := decimal.New(rapid.Int64Range(1, 20_000_000).Draw(t, "next"), -3)
		out, err := AdjustToQty(rows, next)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckScheduleBalanced(out, next); err != nil {
			t.Fatalf("rows %+v for %s: %v", out, next, err)
		}
		for _, r := range out {
			if !r.Qty.IsPositive() {
				t.Fatalf("non-positive row %+v", r)
			}
		}
	})
}

func TestBuildSchedule_SumIdentityOverRandomSplits(t *testing.T) {
	rng := rand.New(rand.NewSource(17))
	d := dayOf(t, "2027-02-01")
	for i := 0; i < 500; i++ {
		n := 1 + rng.Intn(20)
		total := decimal.Zero
		in := make([]ScheduleInput, n)
		for j := range in {
			q := decimal.New(int64(1+rng.Intn(100000)), -2)
			in[j] = ScheduleInput{Qty: q, DeliveryDate: Day{d.AddDate(0, 0, rng.Intn(90))}}
			total = total.Add(q)
		}
		rows, err := BuildSchedule(in, total)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckScheduleBalanced(rows, total); err != nil {
			t.Fatal(err)
		}
		for k := 1; k < len(rows); k++ {
			if rows[k].DeliveryDate.Before(rows[k-1].DeliveryDate.Time) {
				t.Fatalf("rows out of date order: %+v", rows)
			}
		}
	}
}

func TestBuildSchedule_TrailingZerosAreNotExtraPlaces(t *testing.T) {
	d := dayOf(t, "2027-02-01")
	if _, err := BuildSchedule([]ScheduleInput{{Qty: dec("2.500000000"), DeliveryDate: d}, {Qty: dec("7.5"), DeliveryDate: d}}, dec("10")); err != nil {
		t.Fatalf("2.500000000 is 2.5: %v", err)
	}
}

// A row whose quantity a line edit changes loses the vendor's confirmation; the rows left alone keep theirs.
func TestAdjustToQty_ClearsTheConfirmationOfAChangedRow(t *testing.T) {
	d, c := dayOf(t, "2027-02-01"), dayOf(t, "2027-02-03")
	confirmed := func(seq int, qty string) ScheduleRow {
		return ScheduleRow{Seq: seq, Qty: dec(qty), DeliveryDate: d, ConfirmedDate: &c, ConfirmedBy: "u"}
	}
	rows := []ScheduleRow{confirmed(1, "60"), confirmed(2, "40")}
	for _, next := range []string{"110", "70"} {
		out, _ := AdjustToQty(rows, dec(next))
		if out[0].ConfirmedDate == nil || out[1].ConfirmedDate != nil || out[1].ConfirmedBy != "" {
			t.Errorf("%s: %+v", next, out)
		}
	}
	if rows[1].ConfirmedDate == nil {
		t.Error("the input rows were changed")
	}
	out, _ := AdjustToQty(rows, dec("60"))
	if len(out) != 1 || out[0].ConfirmedDate == nil {
		t.Errorf("dropping the whole last row keeps the first one's confirmation: %+v", out)
	}
}

func TestCarryConfirmations(t *testing.T) {
	d1, d2, c := dayOf(t, "2027-02-01"), dayOf(t, "2027-03-01"), dayOf(t, "2027-02-03")
	old := []ScheduleRow{
		{Seq: 1, Qty: dec("5"), DeliveryDate: d1, ConfirmedDate: &c, ConfirmedBy: "u"},
		{Seq: 2, Qty: dec("5"), DeliveryDate: d2, ConfirmedDate: &c},
	}
	rows := CarryConfirmations(old, []ScheduleRow{
		{Seq: 1, Qty: dec("5.0"), DeliveryDate: d1},
		{Seq: 2, Qty: dec("5"), DeliveryDate: d1},
		{Seq: 3, Qty: dec("4"), DeliveryDate: d2},
	})
	if rows[0].ConfirmedDate == nil || rows[0].ConfirmedBy != "u" {
		t.Errorf("the repeated row lost its confirmation: %+v", rows[0])
	}
	if rows[1].ConfirmedDate != nil {
		t.Errorf("one old row confirmed two new rows: %+v", rows[1])
	}
	if rows[2].ConfirmedDate != nil {
		t.Errorf("a row with another quantity took a confirmation: %+v", rows[2])
	}
}

func TestCheckScheduleBudget(t *testing.T) {
	if err := CheckScheduleBudget(MaxScheduleRowsPerPO-100, 100); err != nil {
		t.Errorf("exactly the budget: %v", err)
	}
	if de, ok := CheckScheduleBudget(MaxScheduleRowsPerPO-99, 100).(*Error); !ok || de.Code != CodeScheduleBudget {
		t.Errorf("one over the budget = %v", de)
	}
}
