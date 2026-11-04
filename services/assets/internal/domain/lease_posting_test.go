package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

var leaseAcc = LeaseAccounts{ROUAsset: "1700", ROUAccumulated: "1790", Liability: "2400", InterestExpense: "7200", DepreciationExpense: "6820", PaymentClearing: "2110", RemeasurementGain: "7120", TerminationGainLoss: "7130", ExemptLeaseExpense: "6830"}

func dd(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func sumNet(net map[string]decimal.Decimal) decimal.Decimal {
	s := decimal.Zero
	for _, v := range net {
		s = s.Add(v)
	}
	return s
}

func TestLeaseRecognitionNet_DebitsTheROUAndCreditsTheLiability(t *testing.T) {
	net := LeaseRecognitionNet(leaseAcc, dd("43294.77"), dd("43294.77"))
	if !net["1700"].Equal(dd("43294.77")) || !net["2400"].Equal(dd("-43294.77")) || !sumNet(net).IsZero() {
		t.Fatalf("%v", net)
	}
}

// AC-37.6: liability 43,294.77, Y5 interest 476.18 on a 10,000.00 payment; the period journal moves the
// liability by interest - payment and depreciates the ROU asset.
func TestLeasePeriodNet_BooksInterestPaymentAndDepreciation(t *testing.T) {
	net, err := LeasePeriodNet(leaseAcc, LeasePeriodAmounts{Interest: dd("476.18"), Payment: dd("10000.00"), ROUDepreciation: dd("8658.95")})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"7200": "476.18", "2400": "9523.82", "2110": "-10000", "6820": "8658.95", "1790": "-8658.95"}
	for acc, v := range want {
		if !net[acc].Equal(dd(v)) {
			t.Errorf("%s = %s, want %s", acc, net[acc], v)
		}
	}
	if !sumNet(net).IsZero() {
		t.Fatalf("journal does not balance: %v", net)
	}
}

func TestLeasePeriodNet_RemeasurementWithAndWithoutProfitOrLossExcess(t *testing.T) {
	up, err := LeasePeriodNet(leaseAcc, LeasePeriodAmounts{Remeasurement: dd("5446.49"), ROUAdjustment: dd("5446.49")})
	if err != nil || !up["1700"].Equal(dd("5446.49")) || !up["2400"].Equal(dd("-5446.49")) || !sumNet(up).IsZero() {
		t.Fatalf("increase: %v %v", up, err)
	}
	// Liability falls by 1,500.00 but the ROU asset had only 1,000.00 left: 500.00 goes to profit or loss.
	down, err := LeasePeriodNet(leaseAcc, LeasePeriodAmounts{Remeasurement: dd("-1500"), ROUAdjustment: dd("-1000"), PLExcess: dd("500")})
	if err != nil || !down["1700"].Equal(dd("-1000")) || !down["2400"].Equal(dd("1500")) || !down["7120"].Equal(dd("-500")) || !sumNet(down).IsZero() {
		t.Fatalf("decrease: %v %v", down, err)
	}
}

func TestLeasePeriodNet_RefusesARemeasurementThatDoesNotAddUp(t *testing.T) {
	_, err := LeasePeriodNet(leaseAcc, LeasePeriodAmounts{Remeasurement: dd("100"), ROUAdjustment: dd("90")})
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeLeaseRemeasurementMismatch {
		t.Fatalf("%v", err)
	}
}

func TestLeasePeriodEnd(t *testing.T) {
	d := func(s string) time.Time { v, _ := time.Parse("2006-01-02", s); return v }
	for _, c := range []struct {
		start string
		ppy   int
		n     int
		end   string
	}{
		{"2026-01-01", 12, 1, "2026-01-31"},
		{"2026-01-01", 12, 12, "2026-12-31"},
		{"2026-01-01", 4, 2, "2026-06-30"},
		{"2026-03-15", 2, 1, "2026-09-14"},
		{"2026-01-31", 12, 1, "2026-02-27"},
		{"2027-01-01", 1, 5, "2031-12-31"},
	} {
		if got := LeasePeriodEnd(d(c.start), c.ppy, c.n).Format("2006-01-02"); got != c.end {
			t.Errorf("%s ppy %d period %d = %s, want %s", c.start, c.ppy, c.n, got, c.end)
		}
	}
}
