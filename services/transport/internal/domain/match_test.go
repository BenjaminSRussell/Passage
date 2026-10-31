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

// TestMatchInvoice_PercentLimitIsNotRoundedUp is a regression test: 2 % of $123.45
// is $2.469, so a $2.47 overage is 2.0008 % and outside tolerance. The allowance was
// rounded half-up to $2.47 first, which let that charge match.
func TestMatchInvoice_PercentLimitIsNotRoundedUp(t *testing.T) {
	tests := []struct {
		invoiced, status string
	}{
		{"125.92", MatchVariance}, // +2.47 > 2.469
		{"120.98", MatchVariance}, // -2.47
		{"125.91", MatchMatched},  // +2.46
		{"120.99", MatchMatched},  // -2.46
	}
	for _, tc := range tests {
		res := MatchInvoice([]Line{{"BASE", dec("123.45")}}, []Line{{"BASE", dec(tc.invoiced)}}, DefaultTolerance)
		if res.Status != tc.status {
			t.Errorf("123.45 invoiced %s: status %s, want %s (allowed %s)", tc.invoiced, res.Status, tc.status, res.Lines[0].Allowed)
		}
		if !res.Lines[0].Allowed.Equal(dec("2.469")) {
			t.Errorf("allowed = %s, want the exact 2 %% 2.469", res.Lines[0].Allowed)
		}
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
