package domain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

var asOf = time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)

func usdProfile() *CreditProfile {
	return &CreditProfile{Limit: decimal.NewFromInt(10000), Currency: "USD", LimitType: LimitBalance,
		TolerancePct: decimal.NewFromInt(5), ToleranceAmount: decimal.NewFromInt(1000),
		OldestItemDaysMax: 30, MaxDunningLevel: 3}
}

func ruleResult(t *testing.T, ev CreditEvaluation, id string) RuleResult {
	t.Helper()
	for _, r := range ev.Rules {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("rule %s not evaluated: %+v", id, ev.Rules)
	return RuleResult{}
}

func TestEvaluateCredit_TableDriven(t *testing.T) {
	ar85 := Exposure{OpenAR: decimal.NewFromInt(8500)}
	tests := []struct {
		name        string
		profile     func() *CreditProfile
		mandatory   bool
		exposure    Exposure
		order       int64
		asOf        time.Time
		rule        string
		wantResult  string
		wantOutcome string
	}{
		{name: "AC-27.3 2000 order within both tolerances warns only", profile: usdProfile, exposure: ar85, order: 2000,
			asOf: asOf, rule: "CR-1", wantResult: RuleWarn, wantOutcome: RuleWarn},
		{name: "AC-27.3 3000 order exceeds both tolerances", profile: usdProfile, exposure: ar85, order: 3000,
			asOf: asOf, rule: "CR-1", wantResult: RuleFail, wantOutcome: RuleFail},
		{name: "tolerance edge: exposure at the percentage threshold but under the amount tolerance warns", profile: usdProfile,
			exposure: Exposure{OpenAR: decimal.NewFromInt(9500)}, order: 1000, asOf: asOf, rule: "CR-1",
			wantResult: RuleWarn, wantOutcome: RuleWarn},
		{name: "packing slip limit type adds delivered-not-invoiced", profile: func() *CreditProfile {
			p := usdProfile()
			p.LimitType = LimitBalancePackingSlip
			return p
		}, exposure: Exposure{OpenAR: decimal.NewFromInt(8500), DeliveredNotInvoiced: decimal.NewFromInt(2000)}, order: 1000,
			asOf: asOf, rule: "CR-1", wantResult: RuleFail, wantOutcome: RuleFail},
		{name: "BALANCE_ALL adds open orders", profile: func() *CreditProfile {
			p := usdProfile()
			p.LimitType = LimitBalanceAll
			return p
		}, exposure: Exposure{OpenAR: decimal.NewFromInt(8000), OpenOrders: decimal.NewFromInt(2000)}, order: 1500,
			asOf: asOf, rule: "CR-1", wantResult: RuleFail, wantOutcome: RuleFail},
		{name: "limit type NONE skips CR-1", profile: func() *CreditProfile {
			p := usdProfile()
			p.LimitType = LimitNone
			return p
		}, exposure: ar85, order: 90000, asOf: asOf, rule: "CR-1", wantResult: "", wantOutcome: RulePass},
		{name: "CR-2 oldest item over the maximum fails", profile: usdProfile,
			exposure: Exposure{OldestItemDays: 31}, asOf: asOf, rule: "CR-2", wantResult: RuleFail, wantOutcome: RuleFail},
		{name: "CR-2 oldest item at the maximum passes", profile: usdProfile,
			exposure: Exposure{OldestItemDays: 30}, asOf: asOf, rule: "CR-2", wantResult: RulePass, wantOutcome: RulePass},
		{name: "CR-3 dunning at the maximum level fails", profile: usdProfile,
			exposure: Exposure{DunningLevel: 3}, asOf: asOf, rule: "CR-3", wantResult: RuleFail, wantOutcome: RuleFail},
		{name: "CR-3 below the maximum passes", profile: usdProfile,
			exposure: Exposure{DunningLevel: 2}, asOf: asOf, rule: "CR-3", wantResult: RulePass, wantOutcome: RulePass},
		{name: "CR-4 mandatory check with passed review date fails", profile: func() *CreditProfile {
			p := usdProfile()
			p.MandatoryCheck = true
			yesterday := asOf.AddDate(0, 0, -1)
			p.ReviewDate = &yesterday
			return p
		}, asOf: asOf, rule: "CR-4", wantResult: RuleFail, wantOutcome: RuleFail},
		{name: "CR-4 mandatory check with missing profile fails", profile: func() *CreditProfile { return nil },
			mandatory: true, asOf: asOf, rule: "CR-4", wantResult: RuleFail, wantOutcome: RuleFail},
		{name: "CR-4 missing profile without mandatory check passes", profile: func() *CreditProfile { return nil },
			asOf: asOf, rule: "CR-4", wantResult: RulePass, wantOutcome: RulePass},
		{name: "CR-5 overdue above a quarter of open AR warns", profile: usdProfile,
			exposure: Exposure{OpenAR: decimal.NewFromInt(1000), OverdueAR: decimal.NewFromInt(300)}, asOf: asOf,
			rule: "CR-5", wantResult: RuleWarn, wantOutcome: RuleWarn},
		{name: "CR-6 young customer above 50000 USD fails", profile: func() *CreditProfile {
			p := usdProfile()
			p.Limit = decimal.NewFromInt(1000000)
			since := asOf.AddDate(0, 0, -30)
			p.CustomerSince = &since
			return p
		}, order: 50001, asOf: asOf, rule: "CR-6", wantResult: RuleFail, wantOutcome: RuleFail},
		{name: "CR-6 customer older than 90 days is not checked", profile: func() *CreditProfile {
			p := usdProfile()
			p.Limit = decimal.NewFromInt(1000000)
			since := asOf.AddDate(0, 0, -200)
			p.CustomerSince = &since
			return p
		}, order: 60000, asOf: asOf, rule: "CR-1", wantResult: RulePass, wantOutcome: RulePass},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := EvaluateCredit(CreditInput{Profile: tc.profile(), EntityMandatory: tc.mandatory,
				Exposure: tc.exposure, Order: decimal.NewFromInt(tc.order), AsOf: tc.asOf})
			if ev.Outcome != tc.wantOutcome {
				t.Fatalf("outcome %s want %s (%+v)", ev.Outcome, tc.wantOutcome, ev.Rules)
			}
			if tc.wantResult == "" {
				for _, r := range ev.Rules {
					if r.ID == tc.rule {
						t.Fatalf("rule %s must not apply", tc.rule)
					}
				}
				return
			}
			if got := ruleResult(t, ev, tc.rule).Result; got != tc.wantResult {
				t.Fatalf("%s result %s want %s", tc.rule, got, tc.wantResult)
			}
		})
	}
}

func TestEvaluateCredit_RecordsValueAndThreshold(t *testing.T) {
	ev := EvaluateCredit(CreditInput{Profile: usdProfile(), Exposure: Exposure{OpenAR: decimal.NewFromInt(8500)},
		Order: decimal.NewFromInt(3000), AsOf: asOf})
	r := ruleResult(t, ev, "CR-1")
	if r.Value != "11500" || r.Threshold != "11000" {
		t.Fatalf("value %s threshold %s; want 11500 and 11000", r.Value, r.Threshold)
	}
}

// creditFixture is one case of testdata/credit-40.json (PRD §5.27.9 "credit-40":
// 40/40 outcomes including tolerance edges). Expected results were worked out
// by hand from BR-27.3; a null rule result means the rule must not run.
type creditFixture struct {
	ID              string             `json:"id"`
	Note            string             `json:"note"`
	EntityMandatory bool               `json:"entityMandatory"`
	Order           string             `json:"order"`
	OrderCurrency   string             `json:"orderCurrency"`
	AsOf            string             `json:"asOf"`
	ExpectOutcome   string             `json:"expectOutcome"`
	ExpectRules     map[string]*string `json:"expectRules"`
	Exposure        Exposure           `json:"exposure"`
	Profile         *struct {
		CreditProfile
		ReviewDate    *string `json:"reviewDate"`
		CustomerSince *string `json:"customerSince"`
	} `json:"profile"`
}

func fixtureTime(t *testing.T, s string) time.Time {
	t.Helper()
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if v, err := time.Parse(layout, s); err == nil {
			return v
		}
	}
	t.Fatalf("bad time %q", s)
	return time.Time{}
}

func TestEvaluateCredit_Credit40Fixtures(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "credit-40.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []creditFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 40 {
		t.Fatalf("want 40 fixtures, got %d", len(fixtures))
	}
	for _, fx := range fixtures {
		t.Run(fx.ID, func(t *testing.T) {
			in := CreditInput{EntityMandatory: fx.EntityMandatory, Exposure: fx.Exposure, Order: decimal.RequireFromString(fx.Order),
				OrderCurrency: fx.OrderCurrency, AsOf: fixtureTime(t, fx.AsOf)}
			if fx.Profile != nil {
				p := fx.Profile.CreditProfile
				if fx.Profile.ReviewDate != nil {
					v := fixtureTime(t, *fx.Profile.ReviewDate)
					p.ReviewDate = &v
				}
				if fx.Profile.CustomerSince != nil {
					v := fixtureTime(t, *fx.Profile.CustomerSince)
					p.CustomerSince = &v
				}
				in.Profile = &p
			}
			ev := EvaluateCredit(in)
			if ev.Outcome != fx.ExpectOutcome {
				t.Fatalf("%s: outcome %s want %s (%+v)", fx.Note, ev.Outcome, fx.ExpectOutcome, ev.Rules)
			}
			for id, want := range fx.ExpectRules {
				var got *RuleResult
				for i := range ev.Rules {
					if ev.Rules[i].ID == id {
						got = &ev.Rules[i]
					}
				}
				switch {
				case want == nil && got != nil:
					t.Fatalf("%s: rule %s must not run, got %+v", fx.Note, id, *got)
				case want != nil && got == nil:
					t.Fatalf("%s: rule %s did not run (%+v)", fx.Note, id, ev.Rules)
				case want != nil && got.Result != *want:
					t.Fatalf("%s: %s = %s want %s (value %s threshold %s)", fx.Note, id, got.Result, *want, got.Value, got.Threshold)
				}
			}
		})
	}
}

// Open orders in another currency cannot be converted yet (w12); BALANCE_ALL
// must not pass silently on an exposure that leaves them out.
func TestEvaluateCredit_FlagsOpenOrdersInOtherCurrencies(t *testing.T) {
	p := usdProfile()
	p.LimitType = LimitBalanceAll
	other := []CurrencyAmount{{Currency: "EUR", Amount: decimal.NewFromInt(4000)}}
	ev := EvaluateCredit(CreditInput{Profile: p, Exposure: Exposure{OpenAR: decimal.NewFromInt(1000), OpenOrdersOtherCurrencies: other},
		Order: decimal.NewFromInt(1000), AsOf: asOf})
	r := ruleResult(t, ev, "CR-1")
	if r.Result != RuleWarn || r.Value != "2000" || r.Note != "open orders not counted for want of an FX rate: 4000 EUR" || ev.Outcome != RuleWarn {
		t.Fatalf("a pass leaving out EUR open orders must warn with a note: %+v outcome %s", r, ev.Outcome)
	}
	// A failing exposure stays a failure, with the note.
	ev = EvaluateCredit(CreditInput{Profile: p, Exposure: Exposure{OpenAR: decimal.NewFromInt(12000), OpenOrdersOtherCurrencies: other},
		Order: decimal.NewFromInt(1000), AsOf: asOf})
	if r := ruleResult(t, ev, "CR-1"); r.Result != RuleFail || r.Note == "" {
		t.Fatalf("fail keeps fail: %+v", r)
	}
	// BALANCE does not count open orders, so nothing is missing.
	p.LimitType = LimitBalance
	ev = EvaluateCredit(CreditInput{Profile: p, Exposure: Exposure{OpenAR: decimal.NewFromInt(1000), OpenOrdersOtherCurrencies: other},
		Order: decimal.NewFromInt(1000), AsOf: asOf})
	if r := ruleResult(t, ev, "CR-1"); r.Result != RulePass || r.Note != "" {
		t.Fatalf("BALANCE ignores open orders: %+v", r)
	}
}
