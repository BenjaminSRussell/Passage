package domain

import (
	"errors"
	"os"
	"testing"
)

func loadRepoPacks(t *testing.T) map[string]Pack {
	t.Helper()
	packs, err := LoadPacks(os.DirFS("../../../../countrypacks"))
	if err != nil {
		t.Fatalf("load repo packs: %v", err)
	}
	return packs
}

func codeOf(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestLoadPacks_ShipsSevenFirstPacks(t *testing.T) {
	packs := loadRepoPacks(t)
	for _, code := range []string{"US", "UK", "DE", "FR", "IN", "MX", "CA"} {
		p, ok := packs[code]
		if !ok {
			t.Errorf("pack %s missing", code)
			continue
		}
		if len(p.StatutoryReports) == 0 {
			t.Errorf("pack %s ships no statutory report", code)
		}
	}
	if packs["IN"].FiscalYearStartMonth != 4 {
		t.Errorf("IN fiscal year must start in April")
	}
}

func TestValidatePack_Table(t *testing.T) {
	base := loadRepoPacks(t)["DE"]
	tests := []struct {
		name   string
		mutate func(*Pack)
		code   string
	}{
		{"valid", func(*Pack) {}, ""},
		{"unknown code", func(p *Pack) { p.Code = "ZZ" }, CodeInvalid},
		{"wrong country", func(p *Pack) { p.Country = "AT" }, CodeInvalid},
		{"bad version", func(p *Pack) { p.Version = "v1" }, CodeInvalid},
		{"bad month", func(p *Pack) { p.FiscalYearStartMonth = 13 }, CodeInvalid},
		{"unknown tax scheme", func(p *Pack) { p.TaxIDScheme = "XX" }, CodeInvalid},
		{"unknown bank format", func(p *Pack) { p.BankFormats = []string{"CHAPS"} }, CodeInvalid},
		{"no bank format", func(p *Pack) { p.BankFormats = nil }, CodeInvalid},
		{"negative retention", func(p *Pack) { p.RetentionMinYears["financial"] = -1 }, CodeInvalid},
		{"duplicate report", func(p *Pack) { p.StatutoryReports = append(p.StatutoryReports, p.StatutoryReports[0]) }, CodeInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			p.RetentionMinYears = map[string]int{"financial": 10}
			p.StatutoryReports = append([]StatutoryReport{}, base.StatutoryReports...)
			tc.mutate(&p)
			if got := codeOf(ValidatePack(p)); got != tc.code {
				t.Errorf("code = %q, want %q", got, tc.code)
			}
		})
	}
}

func TestCheckInstall_Table(t *testing.T) {
	de := loadRepoPacks(t)["DE"]
	tests := []struct {
		name     string
		country  string
		primary  int
		calendar bool
		code     string
	}{
		{"installable", "DE", 0, true, ""},
		{"wrong entity country", "FR", 0, true, CodeCountry},
		{"open periods incompatible", "DE", 0, false, CodeCalendar},
		{"second primary refused", "DE", 1, true, CodePrimary},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := codeOf(CheckInstall(de, tc.country, tc.primary, tc.calendar)); got != tc.code {
				t.Errorf("code = %q, want %q", got, tc.code)
			}
		})
	}
}

func TestCheckRetention_NeverBelowPackMinimum(t *testing.T) {
	de := loadRepoPacks(t)["DE"]
	if err := CheckRetention(de, "financial", 9); codeOf(err) != CodeRetention {
		t.Errorf("9 years for DE financial must be refused, got %v", err)
	}
	if err := CheckRetention(de, "financial", 10); err != nil {
		t.Errorf("10 years must pass: %v", err)
	}
	if err := CheckRetention(de, "unknown-class", 1); err != nil {
		t.Errorf("classes without a pack minimum are unconstrained: %v", err)
	}
}
