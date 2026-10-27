package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestValidateApply(t *testing.T) {
	discountBy := day("2027-03-10")
	tests := []struct {
		name      string
		value     time.Time
		unapplied string
		allocs    []Allocation
		residual  string
		wantCode  string
	}{
		{
			name:      "exact settlement balances",
			value:     day("2027-03-05"),
			unapplied: "500.00",
			allocs:    []Allocation{{OpenItemID: "a", Open: amt("500.00"), Amount: amt("500.00")}},
			residual:  "0",
		},
		{
			name:      "residual on account balances the payment",
			value:     day("2027-03-05"),
			unapplied: "500.00",
			allocs:    []Allocation{{OpenItemID: "a", Open: amt("300.00"), Amount: amt("300.00")}},
			residual:  "200.00",
		},
		{
			name:      "sum short of the payment is AR-0001",
			value:     day("2027-03-05"),
			unapplied: "500.00",
			allocs:    []Allocation{{OpenItemID: "a", Open: amt("300.00"), Amount: amt("300.00")}},
			residual:  "0",
			wantCode:  CodeApplyMismatch,
		},
		{
			name:      "over-settling an item is rejected",
			value:     day("2027-03-05"),
			unapplied: "500.00",
			allocs:    []Allocation{{OpenItemID: "a", Open: amt("300.00"), Amount: amt("500.00")}},
			residual:  "0",
			wantCode:  CodeApplyMismatch,
		},
		{
			name:      "the same item twice is rejected",
			value:     day("2027-03-05"),
			unapplied: "100.00",
			allocs: []Allocation{
				{OpenItemID: "a", Open: amt("300.00"), Amount: amt("50.00")},
				{OpenItemID: "a", Open: amt("300.00"), Amount: amt("50.00")},
			},
			residual: "0",
			wantCode: CodeApplyMismatch,
		},
		{
			name:      "discount after its date is rejected",
			value:     day("2027-03-12"),
			unapplied: "980.00",
			allocs:    []Allocation{{OpenItemID: "a", Open: amt("1000.00"), Amount: amt("980.00"), CashDiscount: amt("20.00"), DiscountDate: &discountBy}},
			residual:  "0",
			wantCode:  CodeApplyMismatch,
		},
		{
			name:      "discount above the terms is rejected",
			value:     day("2027-03-05"),
			unapplied: "950.00",
			allocs:    []Allocation{{OpenItemID: "a", Open: amt("1000.00"), Amount: amt("950.00"), CashDiscount: amt("50.00"), DiscountDate: &discountBy, DiscountAvailable: amt("20.00")}},
			residual:  "0",
			wantCode:  CodeApplyMismatch,
		},
		{
			name:      "write-off tolerance is a share of the original item, not what is left",
			value:     day("2027-03-05"),
			unapplied: "92.00",
			allocs:    []Allocation{{OpenItemID: "a", Open: amt("100.00"), ItemAmount: amt("4000.00"), Amount: amt("92.00"), WriteOff: amt("8.00")}},
			residual:  "0",
		},
		{
			name:      "write-off within tolerance passes",
			value:     day("2027-03-05"),
			unapplied: "995.00",
			allocs:    []Allocation{{OpenItemID: "a", Open: amt("1000.00"), Amount: amt("995.00"), WriteOff: amt("5.00")}},
			residual:  "0",
		},
		{
			name:      "write-off above tolerance is AR-0006",
			value:     day("2027-03-05"),
			unapplied: "900.00",
			allocs:    []Allocation{{OpenItemID: "a", Open: amt("1000.00"), Amount: amt("900.00"), WriteOff: amt("50.00")}},
			residual:  "0",
			wantCode:  CodeWriteOffTolerance,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateApply(tc.value, amt(tc.unapplied), tc.allocs, amt(tc.residual))
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var de *Error
			if !errors.As(err, &de) || de.Code != tc.wantCode {
				t.Fatalf("error = %v, want code %s", err, tc.wantCode)
			}
		})
	}
}

// AC-30.2: 1,000.00 at 2% 10 net 30, 980.00 paid on day 8 settles fully with 20.00 discount.
func TestSettlement_EarlyPaymentSettlesInFull(t *testing.T) {
	discountBy := day("2027-03-10")
	a := Allocation{OpenItemID: "inv", Open: amt("1000.00"), Amount: amt("980.00"), CashDiscount: amt("20.00"), DiscountDate: &discountBy, DiscountAvailable: amt("20.00")}
	if err := ValidateApply(day("2027-03-08"), amt("980.00"), []Allocation{a}, decimal.Zero); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !a.NewOpen().IsZero() {
		t.Fatalf("open after settlement = %s, want 0", a.NewOpen())
	}
	if ApplyStatus(a.NewOpen()) != "SETTLED" {
		t.Fatal("status must be SETTLED at zero open amount")
	}
}

func TestWriteOffTolerance(t *testing.T) {
	tests := map[string]string{
		"100.00":   "5.00",  // 0.5% is 0.50, floor applies
		"2000.00":  "10.00", // 0.5% is 10.00
		"150000.0": "750.00",
	}
	for open, want := range tests {
		if got := WriteOffTolerance(amt(open)); !got.Equal(amt(want)) {
			t.Errorf("tolerance(%s) = %s, want %s", open, got, want)
		}
	}
}

func TestValidateApply_PropertyBalancesOrRejects(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		open := decimal.New(rapid.Int64Range(100, 1000000).Draw(t, "open"), -2)
		cash := decimal.New(rapid.Int64Range(0, 1000000).Draw(t, "cash"), -2)
		residual := decimal.New(rapid.Int64Range(0, 1000000).Draw(t, "residual"), -2)
		a := Allocation{OpenItemID: "x", Open: open, Amount: cash}
		unapplied := cash.Add(residual)
		err := ValidateApply(day("2027-03-01"), unapplied, []Allocation{a}, residual)
		over := cash.GreaterThan(open)
		if cash.IsZero() {
			return
		}
		if over && err == nil {
			t.Fatalf("over-settlement %s of %s accepted", cash, open)
		}
		if !over && err != nil {
			t.Fatalf("valid settlement %s of %s rejected: %v", cash, open, err)
		}
	})
}
