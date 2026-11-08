package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestUtilize(t *testing.T) {
	d := decimal.RequireFromString
	for _, tc := range []struct {
		name        string
		limitType   string
		limit       string
		exposure    Exposure
		wantBalance string
		wantPct     string // "" means no percentage
		wantStatus  string
		understated bool
	}{
		{"balance only counts open AR less unapplied", LimitBalance, "10000",
			Exposure{OpenAR: d("8000"), Unapplied: d("500"), OpenOrders: d("9000"), DeliveredNotInvoiced: d("700")}, "7500", "75", UtilWithin, false},
		{"packing slip adds delivered not invoiced", LimitBalancePackingSlip, "10000",
			Exposure{OpenAR: d("8000"), DeliveredNotInvoiced: d("700"), OpenOrders: d("9000")}, "8700", "87", UtilWithin, false},
		{"all counts open orders", LimitBalanceAll, "10000",
			Exposure{OpenAR: d("8000"), DeliveredNotInvoiced: d("700"), OpenOrders: d("1000")}, "9700", "97", UtilWithin, false},
		{"exactly at the limit is within", LimitBalance, "10000", Exposure{OpenAR: d("10000")}, "10000", "100", UtilWithin, false},
		// 10000 limit, 5% and 1000 tolerance: threshold 11000.
		{"above the limit warns", LimitBalance, "10000", Exposure{OpenAR: d("10500")}, "10500", "105", UtilOverLimit, false},
		{"at the threshold still warns", LimitBalance, "10000", Exposure{OpenAR: d("11000")}, "11000", "110", UtilOverLimit, false},
		{"above the threshold fails", LimitBalance, "10000", Exposure{OpenAR: d("11000.01")}, "11000.01", "110", UtilOverTolerance, false},
		{"percent rounds half even to two places", LimitBalance, "3", Exposure{OpenAR: d("1")}, "1", "33.33", UtilWithin, false},
		{"unapplied above open AR is a negative balance", LimitBalance, "10000", Exposure{OpenAR: d("100"), Unapplied: d("600")}, "-500", "-5", UtilWithin, false},
		{"no limit type has no percent", LimitNone, "10000", Exposure{OpenAR: d("99999")}, "99999", "", UtilNoLimit, false},
		{"a zero limit has no percent", LimitBalance, "0", Exposure{OpenAR: d("5")}, "5", "", UtilNoLimit, false},
		{"other currencies not counted understate an all-in balance", LimitBalanceAll, "10000",
			Exposure{OpenAR: d("1000"), OpenOrdersOtherCurrencies: []CurrencyAmount{{Currency: "EUR", Amount: d("50")}}}, "1000", "10", UtilWithin, true},
		{"other currency orders do not matter to a balance-only limit", LimitBalance, "10000",
			Exposure{OpenAR: d("1000"), OpenOrdersOtherCurrencies: []CurrencyAmount{{Currency: "EUR", Amount: d("50")}}}, "1000", "10", UtilWithin, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := CreditProfile{Limit: d(tc.limit), Currency: "USD", LimitType: tc.limitType, TolerancePct: d("5"), ToleranceAmount: d("1000")}
			u := Utilize(p, tc.exposure)
			if !u.Balance.Equal(d(tc.wantBalance)) || u.Status != tc.wantStatus || u.Understated != tc.understated {
				t.Fatalf("got balance %s status %s understated %v", u.Balance, u.Status, u.Understated)
			}
			switch {
			case tc.wantPct == "" && u.Pct != nil:
				t.Fatalf("want no percent, got %s", u.Pct)
			case tc.wantPct != "" && (u.Pct == nil || !u.Pct.Equal(d(tc.wantPct))):
				t.Fatalf("want percent %s, got %v", tc.wantPct, u.Pct)
			}
		})
	}
}

// The report and CR-1 must agree: a balance the report calls OVER_TOLERANCE
// fails CR-1 for an order of 0, and OVER_LIMIT warns.
func TestUtilizeAgreesWithCR1(t *testing.T) {
	d := decimal.RequireFromString
	p := CreditProfile{Limit: d("10000"), Currency: "USD", LimitType: LimitBalanceAll, TolerancePct: d("5"), ToleranceAmount: d("1000"), MaxDunningLevel: 9}
	for _, open := range []string{"9000", "10500", "11000", "11001"} {
		e := Exposure{OpenAR: d(open)}
		u := Utilize(p, e)
		ev := EvaluateCredit(CreditInput{Profile: &p, Exposure: e, Order: decimal.Zero, OrderCurrency: "USD", AsOf: asOf})
		cr1 := ruleResult(t, ev, "CR-1")
		want := map[string]string{UtilWithin: RulePass, UtilOverLimit: RuleWarn, UtilOverTolerance: RuleFail}[u.Status]
		if cr1.Result != want || cr1.Value != u.Balance.String() || cr1.Threshold != u.Threshold.String() {
			t.Fatalf("open %s: report %s, CR-1 %+v", open, u.Status, cr1)
		}
	}
}
