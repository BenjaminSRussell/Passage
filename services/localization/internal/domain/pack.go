// Package domain holds the localization rules: country pack metadata, install
// checks, tax-id validators and statutory fixture checks. It is pure.
package domain

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
)

const (
	CodeCalendar  = "LOC-0501"
	CodeRetention = "LOC-0502"
	CodeCountry   = "LOC-0503"
	CodePrimary   = "LOC-0504"
	CodeInvalid   = "LOC-0505"
)

// The seven packs of FR-48.2. Pack codes are the ISO-like short codes used in
// the API path, mapped to the ISO 3166 country they localize.
var packCountry = map[string]string{
	"US": "US", "UK": "GB", "DE": "DE", "FR": "FR", "IN": "IN", "MX": "MX", "CA": "CA",
}

var (
	versionPattern = regexp.MustCompile(`^\d{4}\.\d+$`)
	knownBank      = map[string]bool{"NACHA": true, "BACS": true, "SEPA_CT": true, "SEPA_DD": true, "NEFT": true, "RTGS": true, "SPEI": true, "EFT": true}
)

// Pack is the versioned metadata of one country pack (FR-48.1). Packs are
// metadata plus ER formats; they never carry code.
type Pack struct {
	Code                 string            `json:"code"`
	Version              string            `json:"version"`
	Name                 string            `json:"name"`
	Country              string            `json:"country"`
	Currency             string            `json:"currency"`
	Locale               string            `json:"locale"`
	TaxRegime            string            `json:"taxRegime"`
	FiscalYearStartMonth int               `json:"fiscalYearStartMonth"`
	HolidayRegion        string            `json:"holidayRegion"`
	BankFormats          []string          `json:"bankFormats"`
	TaxIDScheme          string            `json:"taxIdScheme"`
	RetentionMinYears    map[string]int    `json:"retentionMinYears"`
	DocumentLayouts      []string          `json:"documentLayouts"`
	StatutoryReports     []StatutoryReport `json:"statutoryReports"`
}

// StatutoryReport names a regulator output the pack ships (FR-48.2).
type StatutoryReport struct {
	Code           string   `json:"code"`
	Format         string   `json:"format"`
	Mandate        string   `json:"mandate"`
	RequiredFields []string `json:"requiredFields"`
}

// Error is a localization domain error with a stable LOC/ERP code.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func fail(code, format string, args ...any) error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// LoadPacks reads every <code>/pack.json under fsys, validates it and returns
// the packs keyed by code. The first invalid pack stops the load.
func LoadPacks(fsys fs.FS) (map[string]Pack, error) {
	matches, err := fs.Glob(fsys, "*/pack.json")
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	out := map[string]Pack{}
	for _, m := range matches {
		raw, err := fs.ReadFile(fsys, m)
		if err != nil {
			return nil, err
		}
		var p Pack
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("%s: %w", m, err)
		}
		if err := ValidatePack(p); err != nil {
			return nil, fmt.Errorf("%s: %w", m, err)
		}
		if _, dup := out[p.Code]; dup {
			return nil, fmt.Errorf("%s: duplicate pack %s", m, p.Code)
		}
		out[p.Code] = p
	}
	if len(out) != len(packCountry) {
		return nil, fmt.Errorf("expected %d country packs, found %d", len(packCountry), len(out))
	}
	return out, nil
}

// ValidatePack checks the pack metadata invariants.
func ValidatePack(p Pack) error {
	country, ok := packCountry[p.Code]
	switch {
	case !ok:
		return fail(CodeInvalid, "unknown pack code %q", p.Code)
	case p.Country != country:
		return fail(CodeInvalid, "pack %s must localize country %s", p.Code, country)
	case !versionPattern.MatchString(p.Version):
		return fail(CodeInvalid, "pack %s version %q must look like 2026.1", p.Code, p.Version)
	case len(p.Currency) != 3 || p.Name == "" || p.Locale == "" || p.TaxRegime == "":
		return fail(CodeInvalid, "pack %s needs name, locale, tax regime and a 3-letter currency", p.Code)
	case p.FiscalYearStartMonth < 1 || p.FiscalYearStartMonth > 12:
		return fail(CodeInvalid, "pack %s fiscal year start month must be 1..12", p.Code)
	case !validTaxIDScheme(p.TaxIDScheme):
		return fail(CodeInvalid, "pack %s tax id scheme %q is unknown", p.Code, p.TaxIDScheme)
	case len(p.BankFormats) == 0:
		return fail(CodeInvalid, "pack %s must declare at least one bank format", p.Code)
	}
	for _, b := range p.BankFormats {
		if !knownBank[b] {
			return fail(CodeInvalid, "pack %s bank format %q is unknown", p.Code, b)
		}
	}
	for class, years := range p.RetentionMinYears {
		if years < 0 {
			return fail(CodeInvalid, "pack %s retention for %s is negative", p.Code, class)
		}
	}
	seen := map[string]bool{}
	for _, r := range p.StatutoryReports {
		if r.Code == "" || seen[r.Code] || len(r.RequiredFields) == 0 {
			return fail(CodeInvalid, "pack %s statutory report %q is incomplete or duplicated", p.Code, r.Code)
		}
		seen[r.Code] = true
	}
	return nil
}

// CheckInstall decides whether a pack may be installed on a legal entity.
// primaryInstalled counts the entity's packs already marked primary.
func CheckInstall(p Pack, entityCountry string, primaryInstalled int, calendarOpenPeriodsOK bool) error {
	switch {
	case p.Country != entityCountry:
		return fail(CodeCountry, "pack %s localizes %s, the legal entity is in %s", p.Code, p.Country, entityCountry)
	case !calendarOpenPeriodsOK:
		return fail(CodeCalendar, "pack %s cannot be installed: fiscal calendar has open periods incompatible with the pack", p.Code)
	case primaryInstalled > 0:
		return fail(CodePrimary, "a legal entity has exactly one primary pack; %d is already installed", primaryInstalled)
	}
	return nil
}

// CheckRetention enforces that a retention setting never drops below the pack minimum (LOC-0502).
func CheckRetention(p Pack, recordClass string, years int) error {
	min, ok := p.RetentionMinYears[recordClass]
	if ok && years < min {
		return fail(CodeRetention, "retention for %s cannot be below the statutory minimum of %d years for %s", recordClass, min, p.Country)
	}
	return nil
}

// TaxIDSchemes lists the validator names a pack may reference.
func TaxIDSchemes() []string {
	return []string{"GB_VAT", "DE_USTID", "FR_VAT", "IN_GSTIN", "MX_RFC", "CA_BN", "US_EIN"}
}

func validTaxIDScheme(s string) bool {
	for _, k := range TaxIDSchemes() {
		if k == s {
			return true
		}
	}
	return false
}
