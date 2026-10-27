package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestCanChange_ByStatus(t *testing.T) {
	cases := map[string]bool{
		OrderDraft: true, OrderOpen: true, OrderConfirmed: true,
		"PARTIALLY_SHIPPED": false, "SHIPPED": false, "INVOICED": false, "CLOSED": false, "CANCELLED": false,
	}
	for status, want := range cases {
		if got := CanChange(status); got != want {
			t.Errorf("CanChange(%s) = %v, want %v", status, got, want)
		}
		if got := CanCancel(status); got != want {
			t.Errorf("CanCancel(%s) = %v, want %v", status, got, want)
		}
	}
}

func TestDiffLine_FieldLevel(t *testing.T) {
	base := LineState{Quantity: decimal.NewFromInt(5), UnitPrice: decimal.NewFromInt(10), DiscPct: decimal.Zero, RequestedShipDate: "2027-03-01"}
	cases := []struct {
		name  string
		after LineState
		want  []string
	}{
		{"unchanged", base, nil},
		{"quantity 5 to 8 on line 10", LineState{Quantity: decimal.NewFromInt(8), UnitPrice: decimal.NewFromInt(10), DiscPct: decimal.Zero, RequestedShipDate: "2027-03-01"}, []string{"quantity"}},
		{"price and date", LineState{Quantity: decimal.NewFromInt(5), UnitPrice: decimal.NewFromInt(11), DiscPct: decimal.Zero, RequestedShipDate: "2027-03-09"}, []string{"unitPrice", "requestedShipDate"}},
		{"numerically equal quantity has no diff", LineState{Quantity: decimal.RequireFromString("5.000000"), UnitPrice: decimal.NewFromInt(10), DiscPct: decimal.Zero, RequestedShipDate: "2027-03-01"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			diff := DiffLine(10, base, c.after)
			got := make([]string, 0, len(diff))
			for _, d := range diff {
				if d.LineNo != 10 {
					t.Fatalf("line number lost: %+v", d)
				}
				got = append(got, d.Field)
			}
			if len(got) != len(c.want) {
				t.Fatalf("fields %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("fields %v, want %v", got, c.want)
				}
			}
		})
	}
}

func TestDiffHeader(t *testing.T) {
	if d := DiffHeader("requestedShipDate", "2027-03-01", "2027-03-01"); d != nil {
		t.Fatalf("equal values must not diff: %+v", d)
	}
	d := DiffHeader("requestedShipDate", "2027-03-01", "2027-03-05")
	if len(d) != 1 || d[0].LineNo != 0 || d[0].Old != "2027-03-01" || d[0].New != "2027-03-05" {
		t.Fatalf("header diff %+v", d)
	}
}

func TestLineChangeable(t *testing.T) {
	cases := map[string]bool{LineOpen: true, LineReserved: true, "PICKED": false, "DELIVERED": false, "INVOICED": false, LineCancelled: false}
	for status, want := range cases {
		if got := LineChangeable(status); got != want {
			t.Errorf("LineChangeable(%s) = %v, want %v", status, got, want)
		}
	}
}

func TestRepromises_OnlyQuantityOrDate(t *testing.T) {
	base := LineState{Quantity: decimal.NewFromInt(5), UnitPrice: decimal.NewFromInt(10), DiscPct: decimal.Zero, RequestedShipDate: "2027-03-01"}
	price := base
	price.UnitPrice = decimal.NewFromInt(12)
	qty := base
	qty.Quantity = decimal.NewFromInt(8)
	date := base
	date.RequestedShipDate = "2027-03-09"
	if Repromises(base, price) {
		t.Error("a price change must not re-promise")
	}
	if !Repromises(base, qty) || !Repromises(base, date) {
		t.Error("a quantity or date change must re-promise")
	}
}
