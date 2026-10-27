package domain

import (
	"testing"
)

// balancedUnit is a unit whose balance sheet and retained earnings roll hold.
func balancedUnit() UnitFS {
	return UnitFS{
		TotalAssets:      d("1000"),
		TotalLiabilities: d("400"),
		TotalEquity:      d("600"),
		OpeningRetained:  d("200"),
		NetIncome:        d("150"),
		Dividends:        d("50"),
		ClosingRetained:  d("300"),
	}
}

// TestEvaluateRules_AC40_15 is AC-40.15: a unit whose balance sheet is out by 12.00
// ends its validate task in Error, and the group translation task is Blocked.
func TestEvaluateRules_AC40_15(t *testing.T) {
	fs := balancedUnit()
	fs.TotalAssets = d("1012")
	res, err := EvaluateRules(DefaultValidationRules(), fs)
	if err != nil {
		t.Fatal(err)
	}
	status := UnitTaskStatus(res)
	if status != TaskError {
		t.Fatalf("unit status = %s, want Error", status)
	}
	if !GroupTranslationBlocked([]TaskStatus{status}) {
		t.Fatal("group translation must be Blocked")
	}
	if !res[0].Failed || !res[0].Difference.Equal(d("12")) {
		t.Fatalf("balance sheet result = %+v", res[0])
	}
	if f := res[0].Failure("S1"); f == nil || f.Code != CodeValidationFailed {
		t.Fatalf("failure = %v, want %s", f, CodeValidationFailed)
	}
	if res[1].Failure("S1") != nil {
		t.Fatal("a passing rule has no failure")
	}
}

func TestEvaluateRules_Table(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*UnitFS)
		want   TaskStatus
	}{
		{"balanced unit completes", func(*UnitFS) {}, TaskCompleted},
		{"retained earnings roll within 1.00 passes", func(f *UnitFS) { f.ClosingRetained = d("300.90") }, TaskCompleted},
		{"retained earnings roll beyond 1.00 errors", func(f *UnitFS) { f.ClosingRetained = d("302") }, TaskError},
		{"balance sheet within 0.01 passes", func(f *UnitFS) { f.TotalAssets = d("1000.01") }, TaskCompleted},
		{"IC item without partner only warns", func(f *UnitFS) { f.ICItemsWithoutPartner = 2 }, TaskCompletedWithWarnings},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := balancedUnit()
			tc.mutate(&fs)
			res, err := EvaluateRules(DefaultValidationRules(), fs)
			if err != nil {
				t.Fatal(err)
			}
			if got := UnitTaskStatus(res); got != tc.want {
				t.Fatalf("status = %s, want %s", got, tc.want)
			}
		})
	}
	if _, err := EvaluateRules([]ValidationRule{{ID: "NOPE"}}, balancedUnit()); err == nil {
		t.Fatal("unknown rule must fail")
	}
	if GroupTranslationBlocked([]TaskStatus{TaskCompleted, TaskCompletedWithWarnings}) {
		t.Fatal("warnings must not block translation")
	}
	if !GroupTranslationBlocked([]TaskStatus{TaskCompleted, TaskBlocked}) {
		t.Fatal("a blocked prerequisite must block translation")
	}
	for _, bad := range []ValidationRule{
		{ID: RuleBalanceSheet, Severity: SeverityError, Tolerance: d("-0.01")},
		{ID: RuleBalanceSheet, Severity: "Info"},
	} {
		if _, err := EvaluateRules([]ValidationRule{bad}, balancedUnit()); err == nil {
			t.Fatalf("rule %+v must be rejected", bad)
		}
	}
}

func TestEliminateICPair(t *testing.T) {
	tests := []struct {
		name       string
		recv, pay  string
		threshold  string
		diff       string
		errCode    string
		wantCredit bool
	}{
		{name: "exact match has no difference line", recv: "1000", pay: "1000", threshold: "1000", diff: "0"},
		{name: "small positive difference debits the IC difference", recv: "1000", pay: "990", threshold: "1000", diff: "10"},
		{name: "small negative difference credits the IC difference", recv: "900", pay: "1000", threshold: "1000", diff: "-100", wantCredit: true},
		{name: "difference above threshold ends in error", recv: "1000", pay: "2500", threshold: "1000", errCode: CodeICThreshold},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries, diff, err := EliminateICPair(d(tc.recv), d(tc.pay), d(tc.threshold), "USD")
			if tc.errCode != "" {
				if de, ok := err.(*Error); !ok || de.Code != tc.errCode {
					t.Fatalf("want %s, got %v", tc.errCode, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !diff.Equal(d(tc.diff)) {
				t.Fatalf("difference = %s, want %s", diff, tc.diff)
			}
			if err := CheckBalanced(entries); err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if e.Account == AcctICDifference && tc.wantCredit != e.Credit.IsPositive() {
					t.Fatalf("difference side wrong: %+v", e)
				}
			}
		})
	}
	if _, _, err := EliminateICPair(d("1"), d("1"), d("-1"), "USD"); err == nil {
		t.Fatal("negative threshold must fail")
	}
}
