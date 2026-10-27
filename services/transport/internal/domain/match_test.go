package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestMatchInvoice_ToleranceTable(t *testing.T) {
	tests := []struct {
		name     string
		expected []Line
		invoiced []Line
		status   string
		variance string
	}{
		{"AC-34.4 $5.50 over is a variance", []Line{{"BASE", dec("412.50")}}, []Line{{"BASE", dec("418.00")}}, MatchVariance, "5.50"},
		{"exact match", []Line{{"BASE", dec("412.50")}}, []Line{{"BASE", dec("412.50")}}, MatchMatched, "0"},
		{"$4 over on $412.50 is inside the $5 limit", []Line{{"BASE", dec("412.50")}}, []Line{{"BASE", dec("416.50")}}, MatchMatched, "4"},
		{"2 % limit binds on a small charge", []Line{{"FUEL", dec("10.00")}}, []Line{{"FUEL", dec("10.15")}}, MatchMatched, "0.15"},
		{"2 % limit rejects a 0.25 overage on $10", []Line{{"FUEL", dec("10.00")}}, []Line{{"FUEL", dec("10.25")}}, MatchVariance, "0.25"},
		{"unexpected charge compares against zero", []Line{{"BASE", dec("100")}}, []Line{{"BASE", dec("100")}, {"DETENTION", dec("40")}}, MatchVariance, "40"},
		{"missing charge is a negative variance", []Line{{"BASE", dec("100")}, {"FUEL", dec("8")}}, []Line{{"BASE", dec("100")}}, MatchVariance, "-8"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := MatchInvoice(tc.expected, tc.invoiced, DefaultTolerance)
			if res.Status != tc.status {
				t.Fatalf("status = %s, want %s", res.Status, tc.status)
			}
			if !res.VarianceTotal.Equal(dec(tc.variance)) {
				t.Fatalf("variance = %s, want %s", res.VarianceTotal, tc.variance)
			}
		})
	}
}

func TestMatchInvoice_PerLineDetail(t *testing.T) {
	res := MatchInvoice([]Line{{"BASE", dec("412.50")}}, []Line{{"BASE", dec("418.00")}}, DefaultTolerance)
	if len(res.Lines) != 1 {
		t.Fatalf("lines = %d", len(res.Lines))
	}
	l := res.Lines[0]
	if !l.Variance.Equal(decimal.RequireFromString("5.50")) || !l.Allowed.Equal(decimal.NewFromInt(5)) || l.Within {
		t.Fatalf("line = %+v", l)
	}
}

func TestInvoiceFits(t *testing.T) {
	bill := FreightBill{CarrierID: "c1", Currency: "USD"}
	if f := InvoiceFits(bill, CarrierInvoice{CarrierID: "c1", Currency: "USD"}); len(f) != 0 {
		t.Fatalf("matching invoice reported %v", f)
	}
	f := InvoiceFits(bill, CarrierInvoice{CarrierID: "c2", Currency: "EUR"})
	if f["carrierId"] == "" || f["currency"] == "" {
		t.Fatalf("mismatch fields = %v, want carrierId and currency", f)
	}
}
