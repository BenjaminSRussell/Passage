package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestWithhold_Table(t *testing.T) {
	lower5 := d("5")
	lower25 := d("2.5")
	lower12 := d("12")
	tooHigh := d("101")
	tests := []struct {
		name   string
		in     WithholdInput
		rate   string
		base   string
		amount string
		reason string
		code   string
	}{
		{"194J above annual threshold with PAN", WithholdInput{Section: "194J", Amount: d("60000"), PANPresent: true}, "10", "60000", "6000.00", ReasonThreshold, ""},
		// AC-48.21: 194J at 10% with no PAN is withheld at 20% (206AA).
		{"194J no PAN is 20 percent", WithholdInput{Section: "194J", Amount: d("100000")}, "20", "100000", "20000.00", ReasonNoPAN, ""},
		{"194J below annual threshold", WithholdInput{Section: "194J", Amount: d("10000"), PANPresent: true}, "10", "0", "0.00", ReasonBelowThreshold, ""},
		// Crossing the aggregate catches up the earlier untaxed 45,000 as well.
		{"194J crossing the aggregate catches up earlier credits", WithholdInput{Section: "194J", Amount: d("10000"), YTDPaid: d("45000"), PANPresent: true}, "10", "55000", "5500.00", ReasonThreshold, ""},
		{"194J after the catch-up only the payment", WithholdInput{Section: "194J", Amount: d("10000"), YTDPaid: d("55000"), YTDTaxed: d("55000"), PANPresent: true}, "10", "10000", "1000.00", ReasonThreshold, ""},
		{"194C individual single payment", WithholdInput{Section: "194C", PayeeType: PayeeIndividualHUF, Amount: d("40000"), PANPresent: true}, "1", "40000", "400.00", ReasonThreshold, ""},
		{"194C company rate", WithholdInput{Section: "194C", PayeeType: PayeeOther, Amount: d("40000"), PANPresent: true}, "2", "40000", "800.00", ReasonThreshold, ""},
		{"194C under both thresholds", WithholdInput{Section: "194C", PayeeType: PayeeOther, Amount: d("20000"), PANPresent: true}, "2", "0", "0.00", ReasonBelowThreshold, ""},
		// 90,000 so far of which the 40,000 single payment was taxed; 20,000 more crosses 1,00,000.
		{"194C aggregate taxes the untaxed small payments", WithholdInput{Section: "194C", PayeeType: PayeeOther, Amount: d("20000"), YTDPaid: d("90000"), YTDTaxed: d("40000"), PANPresent: true}, "2", "70000", "1400.00", ReasonThreshold, ""},
		{"194C needs payee type", WithholdInput{Section: "194C", Amount: d("40000"), PANPresent: true}, "", "", "", "", CodeTDSPayee},
		{"194I monthly rent over 50,000", WithholdInput{Section: "194I", Amount: d("60000"), PANPresent: true}, "10", "60000", "6000.00", ReasonThreshold, ""},
		{"194I monthly rent at 50,000 is not taxed", WithholdInput{Section: "194I", Amount: d("50000"), YTDPaid: d("500000"), PANPresent: true}, "10", "0", "0.00", ReasonBelowThreshold, ""},
		{"194H commission at 2 percent", WithholdInput{Section: "194H", Amount: d("25000"), PANPresent: true}, "2", "25000", "500.00", ReasonThreshold, ""},
		{"194H at the 20,000 threshold is not taxed", WithholdInput{Section: "194H", Amount: d("20000"), PANPresent: true}, "2", "0", "0.00", ReasonBelowThreshold, ""},
		{"194Q only the excess over 50 lakh", WithholdInput{Section: "194Q", Amount: d("1000000"), YTDPaid: d("4800000"), PANPresent: true}, "0.1", "800000", "800.00", ReasonExcessOnly, ""},
		{"194Q after the excess began", WithholdInput{Section: "194Q", Amount: d("100000"), YTDPaid: d("5800000"), YTDTaxed: d("800000"), PANPresent: true}, "0.1", "100000", "100.00", ReasonExcessOnly, ""},
		{"194Q below 50 lakh", WithholdInput{Section: "194Q", Amount: d("1000000"), YTDPaid: d("1000000"), PANPresent: true}, "0.1", "0", "0.00", ReasonBelowThreshold, ""},
		// 206AA proviso: 194Q without PAN is 5%, not 20%.
		{"194Q without PAN is 5 percent", WithholdInput{Section: "194Q", Amount: d("1000000"), YTDPaid: d("4800000")}, "5", "800000", "40000.00", ReasonNoPAN, ""},
		{"lower deduction certificate lowers the rate", WithholdInput{Section: "194J", Amount: d("60000"), PANPresent: true, LowerRatePct: &lower5}, "5", "60000", "3000.00", ReasonLowerDeduction, ""},
		{"a certificate above the section rate is not used", WithholdInput{Section: "194J", Amount: d("60000"), PANPresent: true, LowerRatePct: &lower12}, "10", "60000", "6000.00", ReasonThreshold, ""},
		{"lower certificate ignored without PAN", WithholdInput{Section: "194J", Amount: d("60000"), LowerRatePct: &lower5}, "20", "60000", "12000.00", ReasonNoPAN, ""},
		{"lower certificate below 206AA floor is still floored without PAN", WithholdInput{Section: "194C", PayeeType: PayeeIndividualHUF, Amount: d("40000"), LowerRatePct: &lower25}, "20", "40000", "8000.00", ReasonNoPAN, ""},
		{"rounds to the paisa once", WithholdInput{Section: "194C", PayeeType: PayeeIndividualHUF, Amount: d("30000.55"), PANPresent: true}, "1", "30000.55", "300.01", ReasonThreshold, ""},
		{"section code is case-insensitive", WithholdInput{Section: " 194j ", Amount: d("60000"), PANPresent: true}, "10", "60000", "6000.00", ReasonThreshold, ""},
		{"unknown section", WithholdInput{Section: "999Z", Amount: d("1")}, "", "", "", "", CodeTDSSection},
		{"negative amount", WithholdInput{Section: "194J", Amount: d("-1")}, "", "", "", "", CodeTDSAmount},
		{"zero amount", WithholdInput{Section: "194J", Amount: d("0")}, "", "", "", "", CodeTDSAmount},
		{"taxed more than paid", WithholdInput{Section: "194J", Amount: d("1"), YTDPaid: d("10"), YTDTaxed: d("11")}, "", "", "", "", CodeTDSAmount},
		{"certificate rate over 100", WithholdInput{Section: "194J", Amount: d("1"), PANPresent: true, LowerRatePct: &tooHigh}, "", "", "", "", CodeTDSAmount},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Withhold(tc.in)
			if tc.code != "" {
				if codeOf(err) != tc.code {
					t.Fatalf("want %s, got %q (%v)", tc.code, codeOf(err), err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.RatePct.Equal(d(tc.rate)) {
				t.Fatalf("rate: want %s got %s", tc.rate, got.RatePct)
			}
			if !got.TaxableAmount.Equal(d(tc.base)) {
				t.Fatalf("base: want %s got %s", tc.base, got.TaxableAmount)
			}
			if got.TDSAmount.StringFixed(2) != tc.amount {
				t.Fatalf("tds: want %s got %s", tc.amount, got.TDSAmount.StringFixed(2))
			}
			if got.Reason != tc.reason {
				t.Fatalf("reason: want %s got %s", tc.reason, got.Reason)
			}
			if got.ReturnForm != "26Q" || got.ChallanForm != "ITNS 281" || got.Currency != "INR" {
				t.Fatalf("forms: %+v", got)
			}
		})
	}
}

func TestWithhold_PropertyNoPANAndBase(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		sections := []string{"194C", "194J", "194I", "194H", "194Q"}
		sec := sections[rapid.IntRange(0, len(sections)-1).Draw(t, "section")]
		amt := rapid.Int64Range(1, 10_000_000).Draw(t, "amount")
		ytd := rapid.Int64Range(0, 10_000_000).Draw(t, "ytd")
		taxed := rapid.Int64Range(0, ytd).Draw(t, "taxed")
		in := WithholdInput{Section: sec, PayeeType: PayeeOther, Amount: decimal.NewFromInt(amt), YTDPaid: decimal.NewFromInt(ytd), YTDTaxed: decimal.NewFromInt(taxed)}
		got, err := Withhold(in)
		if err != nil {
			t.Fatalf("valid input rejected: %v", err)
		}
		floor := TDSSections()[sec].NoPANFloorPct
		if got.RatePct.LessThan(floor) {
			t.Fatalf("no PAN must never withhold below %s%%: %+v", floor, got)
		}
		withPAN := in
		withPAN.PANPresent = true
		pan, _ := Withhold(withPAN)
		if got.TDSAmount.LessThan(pan.TDSAmount) {
			t.Fatalf("no PAN withholds less than with PAN: %s < %s", got.TDSAmount, pan.TDSAmount)
		}
		if got.TDSAmount.GreaterThan(got.TaxableAmount) {
			t.Fatalf("withheld more than the taxable base: %+v", got)
		}
		if limit := in.Amount.Add(in.YTDPaid).Sub(in.YTDTaxed); got.TaxableAmount.GreaterThan(limit) {
			t.Fatalf("base %s exceeds the untaxed aggregate %s", got.TaxableAmount, limit)
		}
	})
}

func TestTDSSections_ReturnsCopy(t *testing.T) {
	s := TDSSections()
	delete(s, "194J")
	if _, ok := TDSSections()["194J"]; !ok {
		t.Fatal("TDSSections must return a copy")
	}
}
