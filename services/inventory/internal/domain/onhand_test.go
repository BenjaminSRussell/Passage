package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestOnHandFormulas_table(t *testing.T) {
	o := OnHand{PostedQty: d("10"), Received: d("5"), Registered: d("2"), Deducted: d("3"), Picked: d("1"),
		ReservedPhysical: d("4"), Ordered: d("6"), Arrived: d("1"), OnOrder: d("2"), ReservedOrdered: d("3")}
	// physical = 10+5+2-3-1 = 13; avail physical = 13-4 = 9; avail ordered = 9+6+1-2-3 = 11
	if got := o.PhysicalInvent().String(); got != "13" {
		t.Errorf("physical = %s", got)
	}
	if got := o.AvailPhysical().String(); got != "9" {
		t.Errorf("avail physical = %s", got)
	}
	if got := o.AvailOrdered().String(); got != "11" {
		t.Errorf("avail ordered = %s", got)
	}
}

// Property: the formulas of FR-16.3 hold for random bucket vectors.
func TestOnHandFormulasProperty(t *testing.T) {
	val := func(t *rapid.T, name string) decimal.Decimal {
		return decimal.NewFromInt(rapid.Int64Range(-1000, 1000).Draw(t, name))
	}
	rapid.Check(t, func(t *rapid.T) {
		o := OnHand{PostedQty: val(t, "pq"), Received: val(t, "rc"), Registered: val(t, "rg"), Deducted: val(t, "dd"),
			Picked: val(t, "pk"), ReservedPhysical: val(t, "rp"), Ordered: val(t, "od"), Arrived: val(t, "ar"),
			OnOrder: val(t, "oo"), ReservedOrdered: val(t, "ro")}
		phys := o.PostedQty.Add(o.Received).Add(o.Registered).Sub(o.Deducted).Sub(o.Picked)
		if !o.PhysicalInvent().Equal(phys) {
			t.Fatal("physical_invent formula")
		}
		if !o.AvailPhysical().Equal(phys.Sub(o.ReservedPhysical)) {
			t.Fatal("avail_physical formula")
		}
		if !o.AvailOrdered().Equal(phys.Sub(o.ReservedPhysical).Add(o.Ordered).Add(o.Arrived).Sub(o.OnOrder).Sub(o.ReservedOrdered)) {
			t.Fatal("avail_ordered formula")
		}
	})
}

func TestPostIssueThenSoldKeepsPhysicalConstant(t *testing.T) {
	var o OnHand
	_ = o.Post(StatusReceived, d("10"), d("40"), 1)
	_ = o.Post(StatusDeducted, d("-4"), d("-16"), 1)
	before := o.PhysicalInvent()
	// Financial step DEDUCTED -> SOLD: remove the bucket, move to posted.
	_ = o.Post(StatusDeducted, d("-4"), d("-16"), -1)
	_ = o.Post(StatusSold, d("-4"), d("-16"), 1)
	if !o.PhysicalInvent().Equal(before) {
		t.Fatalf("financial step changed physical: %s -> %s", before, o.PhysicalInvent())
	}
	if !o.PostedQty.Equal(d("-4")) || !o.PostedValue.Equal(d("-16")) {
		t.Fatalf("posted = %s / %s", o.PostedQty, o.PostedValue)
	}
}

func TestCheckNegative(t *testing.T) {
	var zero OnHand
	short := OnHand{Received: d("1")}
	if err := short.CheckNegative(zero, NegativePolicy{}); err != nil {
		t.Fatalf("positive stock rejected: %v", err)
	}
	neg := OnHand{Received: d("1"), Deducted: d("3")}
	var ne *NegativeError
	err := neg.CheckNegative(OnHand{Received: d("1")}, NegativePolicy{})
	if !errors.As(err, &ne) || ne.Code != CodeNegativePhysical || !ne.Shortfall.Equal(d("2")) {
		t.Fatalf("physical negative: got %v", err)
	}
	if err := neg.CheckNegative(zero, NegativePolicy{PhysicalAllowed: true, FinancialAllowed: true}); err != nil {
		t.Fatalf("allowed negative rejected: %v", err)
	}
	fin := OnHand{Received: d("5"), ReservedOrdered: d("6")}
	if err := fin.CheckNegative(zero, NegativePolicy{PhysicalAllowed: true}); !errors.As(err, &ne) || ne.Code != CodeNegativeFinancial {
		t.Fatalf("financial negative: got %v", err)
	}
}

// A receipt into a row left negative while the policy allowed it raises
// availability, so a forbid policy must not reject it; a further issue must be.
func TestCheckNegative_ReceiptIntoNegativeRowAllowed(t *testing.T) {
	before := OnHand{PostedQty: d("-10")}
	after := OnHand{PostedQty: d("-4")}
	if err := after.CheckNegative(before, NegativePolicy{}); err != nil {
		t.Fatalf("receipt into negative row rejected: %v", err)
	}
	worse := OnHand{PostedQty: d("-11")}
	var ne *NegativeError
	if err := worse.CheckNegative(before, NegativePolicy{}); !errors.As(err, &ne) || !ne.Shortfall.Equal(d("11")) {
		t.Fatalf("issue from negative row: got %v", err)
	}
}

func TestIssueValue(t *testing.T) {
	row := OnHand{PostedQty: d("3"), PostedValue: d("10.00")}
	tests := []struct {
		name     string
		row      OnHand
		qty      string
		fallback string
		currency string
		want     string
	}{
		// 10.00 / 3 = 3.3333..; one unit rounds to 3.33.
		{"running average rounded to the minor unit", row, "-1", "99", "USD", "-3.33"},
		{"issue that empties the row takes the whole value", row, "-3", "99", "USD", "-10"},
		{"beyond posted quantity uses the fallback cost", row, "-5", "2.5", "USD", "-15"},
		{"no posted quantity uses the fallback cost", OnHand{}, "-2", "1.255", "USD", "-2.51"},
		{"zero-decimal currency", OnHand{PostedQty: d("3"), PostedValue: d("100")}, "-1", "0", "JPY", "-33"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.row.IssueValue(d(tt.qty), d(tt.fallback), tt.currency)
			if !got.Equal(d(tt.want)) {
				t.Fatalf("IssueValue = %s, want %s", got, tt.want)
			}
		})
	}
}

// Property: issuing a row empty in any number of steps removes exactly its
// posted value, so no rounding residue is left in posted_value.
func TestIssueValue_DrainsRowExactlyProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		qty := rapid.Int64Range(1, 500).Draw(t, "qty")
		o := OnHand{PostedQty: decimal.NewFromInt(qty), PostedValue: decimal.New(rapid.Int64Range(0, 10_000_000).Draw(t, "cents"), -2)}
		for o.PostedQty.Sign() > 0 {
			step := decimal.NewFromInt(rapid.Int64Range(1, o.PostedQty.IntPart()).Draw(t, "step"))
			v := o.IssueValue(step.Neg(), decimal.Zero, "USD")
			if !v.Equal(v.Round(2)) || v.Sign() > 0 {
				t.Fatalf("issue value %s is not a non-positive cent amount", v)
			}
			o.PostedQty = o.PostedQty.Sub(step)
			o.PostedValue = o.PostedValue.Add(v)
		}
		if !o.PostedValue.IsZero() {
			t.Fatalf("residue %s left after issuing everything", o.PostedValue)
		}
	})
}

// Property: the live projection built by stepping transactions equals the
// projection rebuilt from their final states (AC2 at unit level).
func TestRebuildEqualsLiveProperty(t *testing.T) {
	issueChain := []Status{StatusOnOrder, StatusReservedOrdered, StatusReservedPhysical, StatusPicked, StatusDeducted, StatusSold}
	receiptChain := []Status{StatusOrdered, StatusArrived, StatusRegistered, StatusReceived, StatusPurchased}
	rapid.Check(t, func(t *rapid.T) {
		var live OnHand
		var finals []Contribution
		for i := 0; i < rapid.IntRange(1, 15).Draw(t, "txns"); i++ {
			chain := issueChain
			qty := decimal.NewFromInt(-rapid.Int64Range(1, 50).Draw(t, "q"))
			if rapid.Bool().Draw(t, "receipt") {
				chain = receiptChain
				qty = qty.Neg()
			}
			value := qty.Mul(decimal.NewFromInt(3))
			cur := chain[0]
			if err := live.Post(cur, qty, value, 1); err != nil {
				t.Fatal(err)
			}
			for k := 1; k < len(chain); k++ {
				if rapid.Bool().Draw(t, "step") {
					continue
				}
				if err := Step(cur, chain[k]); err != nil {
					t.Fatal(err)
				}
				_ = live.Post(cur, qty, value, -1)
				_ = live.Post(chain[k], qty, value, 1)
				cur = chain[k]
			}
			finals = append(finals, Contribution{Status: cur, Qty: qty, Value: value})
		}
		rebuilt, err := Rebuild(finals)
		if err != nil {
			t.Fatal(err)
		}
		if !sameOnHand(rebuilt, live) {
			t.Fatalf("rebuild %+v != live %+v", rebuilt, live)
		}
	})
}

func sameOnHand(a, b OnHand) bool {
	pairs := [][2]decimal.Decimal{{a.PostedQty, b.PostedQty}, {a.PostedValue, b.PostedValue}, {a.Received, b.Received},
		{a.Registered, b.Registered}, {a.Arrived, b.Arrived}, {a.Ordered, b.Ordered}, {a.Deducted, b.Deducted}, {a.Picked, b.Picked},
		{a.ReservedPhysical, b.ReservedPhysical}, {a.ReservedOrdered, b.ReservedOrdered}, {a.OnOrder, b.OnOrder}}
	for _, p := range pairs {
		if !p[0].Equal(p[1]) {
			return false
		}
	}
	return true
}
