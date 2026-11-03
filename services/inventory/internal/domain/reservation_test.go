package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestCheckReservable(t *testing.T) {
	tests := []struct {
		name, qty, avail, code string
	}{
		{"fits exactly", "10", "10", ""},
		{"over-reservation", "11", "10", CodeReserveShortfall},
		{"zero qty", "0", "10", CodeInvalid},
		{"negative qty", "-1", "10", CodeInvalid},
		{"negative available", "1", "-3", CodeReserveShortfall},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domainCode(CheckReservable(d(tc.qty), d(tc.avail))); got != tc.code {
				t.Fatalf("code %q, want %q", got, tc.code)
			}
		})
	}
}

func TestPlanPolicyErrors(t *testing.T) {
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	exp := day.AddDate(0, 0, 2)
	stock := []BatchStock{{BatchID: "B1", Key: "k1", Expiry: &exp, ReceiptDate: day, Available: d("5"), Status: StockAvailable}}
	tests := []struct {
		name    string
		policy  ReservationPolicy
		stock   []BatchStock
		qty     string
		margin  int
		partial bool
		code    string
		short   string
	}{
		{"manual is not a policy", PolicyManual, stock, "1", 0, false, CodeInvalid, ""},
		{"negative margin", PolicyFEFO, stock, "1", -1, false, CodeInvalid, ""},
		{"fefo batch expires inside margin (INV-RES-002)", PolicyFEFO, stock, "1", 3, false, CodeReserveNoEligible, ""},
		{"shortfall without partial", PolicyFIFO, stock, "8", 0, false, CodeReserveShortfall, ""},
		{"no stock at all", PolicyFIFO, nil, "1", 0, true, CodeReserveShortfall, ""},
		{"partial returns the shortfall", PolicyFIFO, stock, "8", 0, true, "", "3"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sel, err := PlanPolicy(tc.policy, tc.stock, d(tc.qty), day, tc.margin, tc.partial)
			if got := domainCode(err); got != tc.code {
				t.Fatalf("code %q, want %q (err %v)", got, tc.code, err)
			}
			if tc.short != "" && !sel.Shortfall.Equal(d(tc.short)) {
				t.Fatalf("shortfall %s, want %s", sel.Shortfall, tc.short)
			}
		})
	}
}

// Property: allocations never exceed the available quantity of their dimension
// set, never touch restricted stock, use each set once, and allocated plus
// shortfall equals the request.
func TestSelectBatchesConservesQuantity(t *testing.T) {
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 12).Draw(t, "n")
		batches := make([]BatchStock, 0, n)
		avail := map[string]decimal.Decimal{}
		for i := 0; i < n; i++ {
			b := BatchStock{BatchID: fmt.Sprintf("B%02d", rapid.IntRange(0, 20).Draw(t, "batch")), Key: fmt.Sprintf("k%02d", i),
				ReceiptDate: day.AddDate(0, 0, rapid.IntRange(0, 30).Draw(t, "receipt")),
				Available:   decimal.NewFromInt(int64(rapid.IntRange(-2, 10).Draw(t, "avail"))), Status: StockAvailable}
			if rapid.Bool().Draw(t, "expires") {
				e := day.AddDate(0, 0, rapid.IntRange(0, 60).Draw(t, "expiry"))
				b.Expiry = &e
			}
			if rapid.IntRange(0, 5).Draw(t, "restricted") == 0 {
				b.Status = StockBlocked
			} else {
				avail[b.Key] = b.Available
			}
			batches = append(batches, b)
		}
		qty := decimal.NewFromInt(int64(rapid.IntRange(1, 40).Draw(t, "qty")))
		policy := rapid.SampledFrom([]ReservationPolicy{PolicyFEFO, PolicyFIFO, PolicyLIFO}).Draw(t, "policy")
		sel := SelectBatches(policy, batches, qty, day, rapid.IntRange(0, 5).Draw(t, "margin"))
		total := decimal.Zero
		seen := map[string]bool{}
		for _, a := range sel.Allocations {
			if seen[a.Key] {
				t.Fatalf("set %s allocated twice", a.Key)
			}
			seen[a.Key] = true
			if a.Qty.Sign() <= 0 || a.Qty.GreaterThan(avail[a.Key]) {
				t.Fatalf("allocation %s of %s exceeds available %s (restricted stock has none)", a.Qty, a.Key, avail[a.Key])
			}
			total = total.Add(a.Qty)
		}
		if !total.Add(sel.Shortfall).Equal(qty) {
			t.Fatalf("allocated %s + shortfall %s != requested %s", total, sel.Shortfall, qty)
		}
	})
}

func TestCheckReservableOrderedClaimsOnlyUnreservedExpectedReceipt(t *testing.T) {
	oh := OnHand{Ordered: decimal.NewFromInt(30), Arrived: decimal.NewFromInt(10), ReservedOrdered: decimal.NewFromInt(15)}
	if err := CheckReservableOrdered(decimal.NewFromInt(25), oh); err != nil {
		t.Fatalf("25 of 25 unreserved: %v", err)
	}
	var e *Error
	if err := CheckReservableOrdered(decimal.NewFromInt(26), oh); !errors.As(err, &e) || e.Code != CodeReserveShortfall {
		t.Fatalf("26 of 25 unreserved: %v", err)
	}
	// Open demand against the row lowers availability below the expected receipt.
	oh.OnOrder = decimal.NewFromInt(20)
	if err := CheckReservableOrdered(decimal.NewFromInt(10), oh); !errors.As(err, &e) || e.Code != CodeReserveShortfall {
		t.Fatalf("availability 5 after demand: %v", err)
	}
	if err := CheckReservableOrdered(decimal.Zero, oh); !errors.As(err, &e) || e.Code != CodeInvalid {
		t.Fatalf("zero quantity: %v", err)
	}
}
