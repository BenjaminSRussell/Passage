package domain

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func ymd(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// TestCheckEnrollmentWindow is 8.42.5: enrollment needs the open window or a
// life event within DefaultLifeEventWindowDays on or after the event.
func TestCheckEnrollmentWindow(t *testing.T) {
	start, end := ymd(2026, 11, 1), ymd(2026, 11, 30)
	lifeDec1 := ymd(2026, 12, 1)
	cases := []struct {
		name string
		eff  time.Time
		le   *time.Time
		ok   bool
	}{
		{"first day of window", ymd(2026, 11, 1), nil, true},
		{"last day of window", ymd(2026, 11, 30), nil, true},
		{"after window, no event", ymd(2026, 12, 1), nil, false},
		{"before window, no event", ymd(2026, 10, 31), nil, false},
		{"life event same day", ymd(2026, 12, 1), &lifeDec1, true},
		{"life event 30 days later", ymd(2026, 12, 31), &lifeDec1, true},
		{"life event 31 days later", ymd(2027, 1, 1), &lifeDec1, false},
		{"effective in window before life event", ymd(2026, 11, 30), &lifeDec1, true},
		{"effective before window and life event", ymd(2026, 10, 15), &lifeDec1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckEnrollmentWindow(c.eff, start, end, c.le)
			if (err == nil) != c.ok {
				t.Fatalf("err = %v, want ok=%v", err, c.ok)
			}
			if err != nil {
				var de *Error
				if !errors.As(err, &de) || de.Code != CodeBenefitWindow {
					t.Fatalf("err = %v, want %s", err, CodeBenefitWindow)
				}
			}
		})
	}
}

func TestServiceDaysSignedAndWholeDays(t *testing.T) {
	if got := ServiceDays(ymd(2026, 1, 10), ymd(2026, 1, 1)); got != -9 {
		t.Errorf("before hire = %d, want -9", got)
	}
	if got := ServiceDays(ymd(2026, 1, 1), ymd(2026, 3, 1)); got != 59 {
		t.Errorf("two months = %d, want 59", got)
	}
}

// TestEnrollmentValidTo is BR-42.6: a termination keeps coverage to the end
// of the termination month (including the December to January roll); any
// other reason ends after the last covered day.
func TestEnrollmentValidTo(t *testing.T) {
	cases := []struct {
		last   string
		reason string
		want   time.Time
	}{
		{"2026-03-15", EndTermination, ymd(2026, 4, 1)},
		{"2026-03-01", EndTermination, ymd(2026, 4, 1)},
		{"2026-03-31", EndTermination, ymd(2026, 4, 1)},
		{"2026-12-31", EndTermination, ymd(2027, 1, 1)},
		{"2026-03-15", EndLifeEvent, ymd(2026, 3, 16)},
		{"2026-12-31", EndPlanEnd, ymd(2027, 1, 1)},
	}
	for _, c := range cases {
		last, _ := time.Parse("2006-01-02", c.last)
		if got := EnrollmentValidTo(last, c.reason); !got.Equal(c.want) {
			t.Errorf("%s %s -> %s, want %s", c.reason, c.last, got.Format("2006-01-02"), c.want.Format("2006-01-02"))
		}
	}
}

// TestEvaluateWaitingPeriodThenRule is BR-42.6: the waiting period is counted
// from hire and checked before the rule; an unknown fact is not eligible and
// the reason names it.
func TestEvaluateWaitingPeriodThenRule(t *testing.T) {
	rule, err := CompileRule("fte >= 0.5")
	if err != nil {
		t.Fatal(err)
	}
	half := decimal.RequireFromString("0.50")
	cases := []struct {
		name    string
		waiting int
		facts   EligibilityFacts
		want    bool
		reason  string
	}{
		{"waiting not met", 30, EligibilityFacts{ServiceDays: 29, FTE: &half}, false, "waiting period of 30 days"},
		{"waiting met on the day", 30, EligibilityFacts{ServiceDays: 30, FTE: &half}, true, "rule true"},
		{"rule false", 0, EligibilityFacts{ServiceDays: 10, FTE: ptr(decimal.RequireFromString("0.40"))}, false, "rule false"},
		{"no primary position, so no fte", 0, EligibilityFacts{ServiceDays: 10}, false, "unknown identifier: fte"},
		{"before hire", 0, EligibilityFacts{ServiceDays: -1, FTE: &half}, false, "waiting period"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Evaluate(rule, c.waiting, c.facts)
			if got.Eligible != c.want || !strings.Contains(got.Reason, c.reason) {
				t.Fatalf("Evaluate = %+v, want eligible=%v and a reason containing %q", got, c.want, c.reason)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

// TestEligibleUnknownFactIsError: a rule that reads job.type while the job
// entity is not yet loaded must fail, not guess.
func TestEligibleUnknownFactIsError(t *testing.T) {
	rule, err := CompileRule("job.type == 'HOURLY'")
	if err != nil {
		t.Fatal(err)
	}
	one := decimal.NewFromInt(1)
	_, err = rule.Eval(EligibilityFacts{EmploymentType: "FULL_TIME", FTE: &one}.Env())
	if !errors.Is(err, ErrUnknownIdentifier) {
		t.Fatalf("err = %v, want ErrUnknownIdentifier", err)
	}
}

// benefitFixture is testdata/hr-benefit-100.json (PRD 8.42.9). Its expected
// sets were computed by an independent reference, not by this evaluator.
type benefitFixture struct {
	AsOf  string `json:"asOf"`
	Plans []struct {
		Code        string `json:"code"`
		Rule        string `json:"rule"`
		WaitingDays int    `json:"waitingDays"`
	} `json:"plans"`
	Employees []struct {
		ID             string  `json:"id"`
		EmploymentType string  `json:"employmentType"`
		FTE            *string `json:"fte"`
		HireDate       string  `json:"hireDate"`
		LegalEntity    string  `json:"legalEntity"`
	} `json:"employees"`
	Expected map[string][]string `json:"expected"`
}

// TestEligibilityFixtureHundred is AC-42.5 (and ticket AC3): across the 100
// fixture employees and 6 plans, including the PRD rule
// employment_type in ['FULL_TIME'] && service_days >= 30, the eligible set
// equals the fixture expectation exactly.
func TestEligibilityFixtureHundred(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/hr-benefit-100.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx benefitFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	asOf, err := time.Parse("2006-01-02", fx.AsOf)
	if err != nil {
		t.Fatal(err)
	}
	if len(fx.Employees) != 100 || len(fx.Plans) != 6 {
		t.Fatalf("fixture has %d employees and %d plans, want 100 and 6", len(fx.Employees), len(fx.Plans))
	}
	for _, plan := range fx.Plans {
		rule, err := CompileRule(plan.Rule)
		if err != nil {
			t.Fatalf("%s: %v", plan.Code, err)
		}
		var got []string
		for _, e := range fx.Employees {
			hire, err := time.Parse("2006-01-02", e.HireDate)
			if err != nil {
				t.Fatal(err)
			}
			facts := EligibilityFacts{EmploymentType: e.EmploymentType, ServiceDays: ServiceDays(hire, asOf), LegalEntityID: e.LegalEntity}
			if e.FTE != nil {
				d := decimal.RequireFromString(*e.FTE)
				facts.FTE = &d
			}
			if Evaluate(rule, plan.WaitingDays, facts).Eligible {
				got = append(got, e.ID)
			}
		}
		want := fx.Expected[plan.Code]
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: eligible %d %v, want %d %v", plan.Code, len(got), got, len(want), want)
		}
		if len(want) == 0 || len(want) == 100 {
			t.Errorf("%s: fixture expectation is degenerate (%d eligible)", plan.Code, len(want))
		}
	}
}
