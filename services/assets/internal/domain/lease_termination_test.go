package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestComputeTermination_GainAndLoss(t *testing.T) {
	cases := []struct {
		name                string
		liab, acc, rem, pay string
		gain                string
	}{
		{"liability above ROU is a gain", "10000.00", "4000.00", "8000.00", "0", "2000.00"},
		{"penalty can turn it into a loss", "10000.00", "4000.00", "8000.00", "3500.00", "-1500.00"},
		{"exact", "8000.00", "1.00", "8000.00", "0", "0.00"},
	}
	for _, c := range cases {
		got, err := ComputeTermination(TerminationInput{Currency: "USD", Liability: d(c.liab), ROUAccumulated: d(c.acc), ROURemaining: d(c.rem), Payment: d(c.pay)})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !got.GainLoss.Equal(d(c.gain)) {
			t.Fatalf("%s: gain/loss %s, want %s", c.name, got.GainLoss, c.gain)
		}
		if !got.ROUGross.Equal(d(c.acc).Add(d(c.rem))) {
			t.Fatalf("%s: gross %s", c.name, got.ROUGross)
		}
	}
}

func TestComputeTermination_Rejects(t *testing.T) {
	for _, in := range []TerminationInput{
		{Currency: "US", Liability: d("1")},
		{Currency: "USD", Liability: d("1"), Payment: d("-1")},
		{Currency: "USD", Liability: d("-1")},
	} {
		if _, err := ComputeTermination(in); err == nil {
			t.Fatalf("%+v accepted", in)
		}
	}
}

// The journal balances for any gain or loss: debits equal credits.
func TestLeaseTerminationNet_Balances(t *testing.T) {
	acc := LeaseAccounts{ROUAsset: "1700", ROUAccumulated: "1790", Liability: "2400", PaymentClearing: "2110", RemeasurementGain: "7120", TerminationGainLoss: "7130", InterestExpense: "7200", DepreciationExpense: "6820"}
	for _, pay := range []string{"0", "3500", "12000"} {
		tm, err := ComputeTermination(TerminationInput{Currency: "USD", Liability: d("10000"), ROUAccumulated: d("4000"), ROURemaining: d("8000"), Payment: d(pay)})
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		net := LeaseTerminationNet(acc, tm)
		if _, onRemeasurement := net["7120"]; onRemeasurement || !net["7130"].Equal(tm.GainLoss.Neg()) {
			t.Fatalf("payment %s: the gain or loss is not on the termination account: %v", pay, net)
		}
		for _, v := range net {
			sum = sum.Add(v)
		}
		if !sum.IsZero() {
			t.Fatalf("payment %s: journal off by %s", pay, sum)
		}
	}
}

func TestValidTerminationPeriod(t *testing.T) {
	for _, c := range []struct {
		after, n int
		ok       bool
	}{{0, 5, false}, {1, 5, true}, {4, 5, true}, {5, 5, false}} {
		if err := ValidTerminationPeriod(c.after, c.n); (err == nil) != c.ok {
			t.Fatalf("%+v: %v", c, err)
		}
	}
	if got := LeaseTerminationDate(time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC), 4, 1); got.Format("2006-01-02") != "2026-04-29" {
		t.Fatalf("date %s", got)
	}
}

// Period afterPeriod must have started by asOf (its first day counts), whatever the hour; a quarterly lease from 31 January starts its
// second period on 30 April (the 29 April end of period 1 plus a day).
func TestTerminationNotAhead(t *testing.T) {
	jan31 := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		after int
		asOf  string
		ok    bool
	}{{1, "2026-01-31T00:00:00Z", true}, {1, "2026-01-30T23:59:00Z", false}, {2, "2026-04-29T23:00:00Z", false}, {2, "2026-04-30T00:00:00Z", true}, {3, "2026-05-15T12:00:00Z", false}} {
		asOf, _ := time.Parse(time.RFC3339, c.asOf)
		if err := TerminationNotAhead(jan31, 4, c.after, asOf); (err == nil) != c.ok {
			t.Errorf("after %d on %s: %v", c.after, c.asOf, err)
		}
	}
}

func TestComputeTermination_RejectsSubCentPayment(t *testing.T) {
	if _, err := ComputeTermination(TerminationInput{Currency: "USD", Liability: d("100"), Payment: d("1.005")}); err == nil {
		t.Fatal("1.005 USD accepted")
	}
	if _, err := ComputeTermination(TerminationInput{Currency: "JPY", Liability: d("100"), Payment: d("1.5")}); err == nil {
		t.Fatal("1.5 JPY accepted")
	}
}
