package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func testFund(budget string, overspend, closed bool) Fund {
	return Fund{Name: "FUND-A", Type: "TRADE", Currency: "USD", Owner: "tm", BudgetPerPeriod: d(budget), AllowOverspend: overspend, Closed: closed}
}

func TestCheckAttribution_TableDriven(t *testing.T) {
	tests := []struct {
		name    string
		fund    Fund
		used    string
		amount  string
		code    string
		wantErr bool
	}{
		{"within budget", testFund("1000", false, false), "400", "100", "", false},
		{"exact budget allowed", testFund("1000", false, false), "900", "100", "", false},
		{"over budget rejected", testFund("1000", false, false), "900", "100.01", CodeFundOverBudget, true},
		{"overspend allowed", testFund("1000", true, false), "900", "500", "", false},
		{"closed fund rejects", testFund("1000", true, true), "0", "1", CodeFundClosed, true},
		{"zero amount rejected", testFund("1000", false, false), "0", "0", CodeFundAmount, true},
		{"negative amount rejected", testFund("1000", false, false), "0", "-5", CodeFundAmount, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckAttribution(tt.fund, d(tt.used), d(tt.amount))
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var de *RuleError
			if !errors.As(err, &de) || de.Code != tt.code {
				t.Fatalf("want code %s, got %v", tt.code, err)
			}
		})
	}
}

func TestCheckAttribution_OverBudgetMessageNamesExcess(t *testing.T) {
	err := CheckAttribution(testFund("1000", false, false), d("950"), d("75.5"))
	if err == nil || err.Error() != "PRC-0012: fund FUND-A budget exceeded by 25.50" {
		t.Fatalf("unexpected message: %v", err)
	}
}

// Property: any accepted sequence of attributions keeps usage within budget when overspend is off.
func TestFundNeverExceedsBudgetWithoutOverspend(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		budget := rapid.Int64Range(0, 100000).Draw(t, "budget")
		f := testFund(decimal.NewFromInt(budget).String(), false, false)
		used := decimal.Zero
		for i := 0; i < 20; i++ {
			amt := decimal.NewFromInt(rapid.Int64Range(1, 5000).Draw(t, "amount"))
			if CheckAttribution(f, used, amt) == nil {
				used = used.Add(amt)
			}
		}
		if used.GreaterThan(f.BudgetPerPeriod) {
			t.Fatalf("usage %s exceeds budget %s", used, f.BudgetPerPeriod)
		}
		if Remaining(f, used).IsNegative() {
			t.Fatalf("remaining negative without overspend")
		}
	})
}

func TestValidPeriodAndSource(t *testing.T) {
	for _, p := range []string{"2027-01", "2027-12"} {
		if !ValidPeriod(p) {
			t.Errorf("%s should be valid", p)
		}
	}
	for _, p := range []string{"2027-13", "2027-00", "27-01", "2027-1"} {
		if ValidPeriod(p) {
			t.Errorf("%s should be invalid", p)
		}
	}
	if !ValidUsageSource(UsageClaim) || ValidUsageSource("OTHER") {
		t.Error("usage source validation wrong")
	}
}

func TestValidateFund(t *testing.T) {
	ok := testFund("10", false, false)
	if f := ValidateFund(ok); len(f) != 0 {
		t.Fatalf("valid fund flagged: %v", f)
	}
	bad := ok
	bad.Currency = "usd"
	bad.BudgetPerPeriod = d("-1")
	f := ValidateFund(bad)
	if f["currency"] == "" || f["budgetPerPeriod"] == "" || len(f) != 2 {
		t.Fatalf("expected currency and budget errors, got %v", f)
	}
}

func TestBaselineAveragesThirteenWeeks(t *testing.T) {
	weekly := make([]decimal.Decimal, BaselineWeeks)
	for i := range weekly {
		weekly[i] = d("10")
	}
	weekly[12] = d("23")
	got, err := Baseline(weekly)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(d("11")) {
		t.Fatalf("baseline = %s, want 11", got)
	}
	if _, err := Baseline(weekly[:12]); err == nil {
		t.Error("12 weeks must be rejected")
	}
	weekly[0] = d("-1")
	if _, err := Baseline(weekly); err == nil {
		t.Error("negative weekly units must be rejected")
	}
}

func TestEffectivenessAndForecast(t *testing.T) {
	base := d("100")
	tests := []struct {
		name  string
		units string
		weeks int
		want  string
	}{
		{"no lift", "400", 4, "0"},
		{"twenty percent", "480", 4, "20"},
		{"loss", "320", 4, "-20"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Effectiveness(base, PromotionActuals{Units: d(tt.units), Weeks: tt.weeks})
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(d(tt.want)) {
				t.Fatalf("effectiveness = %s, want %s", got, tt.want)
			}
		})
	}
	if _, err := Effectiveness(d("0"), PromotionActuals{Units: d("1"), Weeks: 1}); !errors.Is(err, ErrNoBaseline) {
		t.Fatalf("zero baseline: got %v", err)
	}
	if got := ForecastUnits(d("100"), d("25"), 2); !got.Equal(d("250")) {
		t.Fatalf("forecast = %s, want 250", got)
	}
}

func TestPlanWeeksCountsStartedWeeks(t *testing.T) {
	start := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		end  time.Time
		want int
	}{
		{start, 1},
		{start.AddDate(0, 0, 6), 1},
		{start.AddDate(0, 0, 7), 2},
		{start.AddDate(0, 0, -1), 0},
	}
	for _, tt := range tests {
		if got := PlanWeeks(start, tt.end); got != tt.want {
			t.Errorf("PlanWeeks(%s) = %d, want %d", tt.end.Format("2006-01-02"), got, tt.want)
		}
	}
}

func TestValidatePromotion(t *testing.T) {
	start := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	weekly := make([]decimal.Decimal, BaselineWeeks)
	for i := range weekly {
		weekly[i] = d("5")
	}
	p := PromotionPlan{Name: "Spring", ProductID: "P-1", Currency: "USD", Start: start, End: start.AddDate(0, 0, 14),
		BaselineWeekly: weekly, LiftPct: d("10"), PlannedSpend: d("500")}
	if f := ValidatePromotion(p); len(f) != 0 {
		t.Fatalf("valid promotion flagged: %v", f)
	}
	p.End = start.AddDate(0, 0, -1)
	p.LiftPct = d("-101")
	p.BaselineWeekly = weekly[:3]
	f := ValidatePromotion(p)
	for _, k := range []string{"endDate", "liftPct", "baselineWeeklyUnits"} {
		if f[k] == "" {
			t.Errorf("missing field error %s in %v", k, f)
		}
	}
}

func TestCheckRevision(t *testing.T) {
	tests := []struct {
		name      string
		fund      Fund
		budget    string
		overspend bool
		peak      string
		code      string
	}{
		{"raise budget", testFund("1000", false, false), "1500", false, "900", ""},
		{"lower to the busiest period", testFund("1000", false, false), "900", false, "900", ""},
		{"lower below usage", testFund("1000", false, false), "800", false, "900", CodeFundOverBudget},
		{"lower below usage with overspend", testFund("1000", false, false), "800", true, "900", ""},
		{"closed fund is frozen", testFund("1000", false, true), "2000", false, "0", CodeFundClosed},
		{"negative budget", testFund("1000", false, false), "-1", true, "0", CodeValidation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckRevision(tt.fund, d(tt.budget), tt.overspend, d(tt.peak))
			var de *RuleError
			switch {
			case tt.code == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.code != "" && (!errors.As(err, &de) || de.Code != tt.code):
				t.Fatalf("want %s, got %v", tt.code, err)
			}
		})
	}
	err := CheckRevision(testFund("1000", false, false), d("800"), false, d("900"))
	if err == nil || err.Error() != "PRC-0012: fund FUND-A budget exceeded by 100.00" {
		t.Fatalf("message must name the excess: %v", err)
	}
}

func TestCheckPromotionFund(t *testing.T) {
	fund := testFund("1000", false, false)
	fund.LegalEntityID = "le-1"
	plan := PromotionPlan{LegalEntityID: "le-1", Currency: "USD"}
	if fields, err := CheckPromotionFund(fund, plan); len(fields) != 0 || err != nil {
		t.Fatalf("matching open fund: %v %v", fields, err)
	}
	eur := plan
	eur.Currency = "EUR"
	if fields, _ := CheckPromotionFund(fund, eur); fields["currency"] == "" {
		t.Fatalf("currency mismatch must be a field error: %v", fields)
	}
	other := plan
	other.LegalEntityID = "le-2"
	if fields, _ := CheckPromotionFund(fund, other); fields["fundId"] == "" {
		t.Fatalf("another legal entity's fund must be a field error: %v", fields)
	}
	fund.Closed = true
	var de *RuleError
	if _, err := CheckPromotionFund(fund, plan); !errors.As(err, &de) || de.Code != CodeFundClosed {
		t.Fatalf("closed fund: want PRC-0011, got %v", err)
	}
}
