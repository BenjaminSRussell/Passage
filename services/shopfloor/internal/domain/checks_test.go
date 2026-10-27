package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// lim returns a check limit pointer for table rows.
func lim(s string) *decimal.Decimal {
	d := decimal.RequireFromString(s)
	return &d
}

func TestEvaluateCheck_Table(t *testing.T) {
	numeric := Check{Code: "len", Name: "Length", Kind: CheckNumeric, Min: lim("10"), Max: lim("12")}
	passFail := Check{Code: "visual", Name: "Visual", Kind: CheckPassFail}
	cases := []struct {
		name   string
		check  Check
		value  string
		pass   bool
		reason string
	}{
		{"inside limits", numeric, "11.5", true, ""},
		{"lower bound inclusive", numeric, "10", true, ""},
		{"upper bound inclusive", numeric, "12.000", true, ""},
		{"above limit fails", numeric, "13", false, ""},
		{"below limit fails", numeric, "9.99", false, ""},
		{"not a number", numeric, "abc", false, "must be a number"},
		{"open upper bound", Check{Code: "x", Kind: CheckNumeric, Min: lim("1")}, "1000000", true, ""},
		{"pass word", passFail, "PASS", true, ""},
		{"fail word", passFail, "fail", false, ""},
		{"unknown word", passFail, "maybe", false, "must be pass or fail"},
		{"text is recorded", Check{Code: "note", Kind: CheckText}, " burr on edge ", true, ""},
		{"blank text", Check{Code: "note", Kind: CheckText}, "  ", false, "must not be blank"},
		{"unknown kind", Check{Code: "x", Kind: "enum"}, "x", false, "unknown check kind"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := EvaluateCheck(tc.check, tc.value)
			if got.Pass != tc.pass || reason != tc.reason {
				t.Fatalf("EvaluateCheck(%q) = pass %v reason %q; want pass %v reason %q", tc.value, got.Pass, reason, tc.pass, tc.reason)
			}
		})
	}
}

func TestValidateCheck_Table(t *testing.T) {
	cases := []struct {
		name  string
		check Check
		field string
	}{
		{"valid numeric", Check{Code: "len", Name: "Len", Kind: CheckNumeric, Min: lim("1"), Max: lim("2")}, ""},
		{"empty code", Check{Name: "Len", Kind: CheckNumeric}, "code"},
		{"empty name", Check{Code: "len", Kind: CheckNumeric}, "name"},
		{"min above max", Check{Code: "len", Name: "Len", Kind: CheckNumeric, Min: lim("3"), Max: lim("2")}, "max"},
		{"pass-fail with limits", Check{Code: "v", Name: "V", Kind: CheckPassFail, Min: lim("1")}, "kind"},
		{"text with limits", Check{Code: "v", Name: "V", Kind: CheckText, Max: lim("1")}, "kind"},
		{"code with blanks", Check{Code: " v", Name: "V", Kind: CheckText}, "code"},
		{"unknown kind", Check{Code: "v", Name: "V", Kind: "enum"}, "kind"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := ValidateCheck(tc.check)
			if tc.field == "" {
				if len(fields) != 0 {
					t.Fatalf("unexpected field errors %v", fields)
				}
				return
			}
			if _, ok := fields[tc.field]; !ok {
				t.Fatalf("want error on %q, got %v", tc.field, fields)
			}
		})
	}
}

func TestFinishBlockers_Table(t *testing.T) {
	checks := []Check{
		{Code: "len", Kind: CheckNumeric, Mandatory: true},
		{Code: "visual", Kind: CheckPassFail, Mandatory: true},
		{Code: "note", Kind: CheckPassFail, Mandatory: false},
	}
	pass := map[string]CheckResult{"len": {Code: "len", Pass: true}, "visual": {Code: "visual", Pass: true}}
	failLen := map[string]CheckResult{"len": {Code: "len", Pass: false}, "visual": {Code: "visual", Pass: true}}
	cases := []struct {
		name       string
		results    map[string]CheckResult
		overridden bool
		want       []string
	}{
		{"all passing", pass, false, nil},
		{"failing mandatory blocks", failLen, false, []string{"len"}},
		{"missing mandatory blocks", map[string]CheckResult{}, false, []string{"len", "visual"}},
		{"override clears", failLen, true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FinishBlockers(checks, tc.results, tc.overridden)
			if len(got) != len(tc.want) {
				t.Fatalf("blockers = %v; want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("blockers = %v; want %v", got, tc.want)
				}
			}
		})
	}
}

func TestFinishError_IsSF0005(t *testing.T) {
	err := FinishError([]string{"len"})
	var de *Error
	if !errors.As(err, &de) || de.Code != "SF-0005" {
		t.Fatalf("want SF-0005 domain error, got %v", err)
	}
}

// Property: a numeric check passes exactly when its value lies in [min, max].
func TestEvaluateCheck_Property_NumericWithinLimits(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		lo := rapid.IntRange(-1000, 1000).Draw(t, "lo")
		span := rapid.IntRange(0, 1000).Draw(t, "span")
		v := rapid.IntRange(-2000, 2000).Draw(t, "v")
		c := Check{Code: "p", Kind: CheckNumeric,
			Min: lim(decimal.NewFromInt(int64(lo)).String()),
			Max: lim(decimal.NewFromInt(int64(lo + span)).String())}
		got, reason := EvaluateCheck(c, decimal.NewFromInt(int64(v)).String())
		want := v >= lo && v <= lo+span
		if reason != "" || got.Pass != want {
			t.Fatalf("v=%d in [%d,%d]: pass=%v reason=%q, want %v", v, lo, lo+span, got.Pass, reason, want)
		}
	})
}
