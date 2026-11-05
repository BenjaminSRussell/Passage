package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Export control codes (FR-48.20, BR-48.16, TRD-1xxx).
const (
	CodeEmbargo       = "TRD-1002" // comprehensive embargo, no release without officer and license reference
	CodeLicenseNeeded = "TRD-1003" // license required and none valid
	CodeExportInput   = "TRD-1007" // invalid destination, item or license data
)

// License check modes (9.48.13 trade.license_check).
const (
	LicenseCheckBlock = "BLOCK"
	LicenseCheckWarn  = "WARN"
	LicenseCheckOff   = "OFF"
)

// Regimes of export classification. HS codes are tariff codes and never need a license here.
const (
	RegimeUSEAR     = "US_EAR"
	RegimeEUDualUse = "EU_DUALUSE"
	RegimeHS        = "HS"
	wildcardCountry = "*"
	maxExportItems  = 5000
)

// comprehensiveEmbargo lists countries under a comprehensive embargo (pack table, BR-48.16).
var comprehensiveEmbargo = map[string]bool{"CU": true, "IR": true, "KP": true, "SY": true}

// embargoedRegions lists ISO 3166-2 regions under embargo: Crimea (with
// Sevastopol), Donetsk and Luhansk.
var embargoedRegions = map[string]bool{"UA-43": true, "UA-40": true, "UA-14": true, "UA-09": true}

// euMembers are the EU member states; EU dual-use licensing does not apply inside the EU.
var euMembers = map[string]bool{
	"AT": true, "BE": true, "BG": true, "HR": true, "CY": true, "CZ": true, "DK": true, "EE": true,
	"FI": true, "FR": true, "DE": true, "GR": true, "HU": true, "IE": true, "IT": true, "LV": true,
	"LT": true, "LU": true, "MT": true, "NL": true, "PL": true, "PT": true, "RO": true, "SK": true,
	"SI": true, "ES": true, "SE": true,
}

// LicenseRule is one row of the license decision table a pack ships (FR-3.13 rule set).
// Destinations "*" means every country except those in Except.
type LicenseRule struct {
	Regime       string   `json:"regime"`
	Code         string   `json:"code"`
	Destinations []string `json:"destinations"`
	Except       []string `json:"except,omitempty"`
}

// DefaultLicenseTable is the illustrative decision table shipped with the first pack
// set. The entries are ASSUMED pending export counsel review; the pack owns the real table.
var DefaultLicenseTable = []LicenseRule{
	{Regime: RegimeUSEAR, Code: "5A002", Destinations: []string{wildcardCountry}, Except: []string{"CA"}},
	{Regime: RegimeEUDualUse, Code: "1A001", Destinations: []string{wildcardCountry}, Except: sortedKeys(euMembers)},
	{Regime: RegimeEUDualUse, Code: "5A002", Destinations: []string{wildcardCountry}, Except: sortedKeys(euMembers)},
}

// Classification is the export classification of an item.
type Classification struct {
	Regime string `json:"regime"`
	Code   string `json:"code"`
}

// License is a tracked export license (9.48.4 export_license). Nil limits mean unlimited.
type License struct {
	LicenseID       string           `json:"licenseId"`
	Number          string           `json:"number"`
	Regime          string           `json:"regime"`
	Classifications []string         `json:"classifications"` // REGIME:CODE
	Destinations    []string         `json:"destinations"`
	QtyLimit        *decimal.Decimal `json:"qtyLimit,omitempty"`
	UsedQty         decimal.Decimal  `json:"usedQty"`
	ValueLimit      *decimal.Decimal `json:"valueLimit,omitempty"`
	UsedValue       decimal.Decimal  `json:"usedValue"`
	ExpiresAt       time.Time        `json:"expiresAt"`
}

// ExportItem is one line of a delivery to check.
type ExportItem struct {
	ItemID         string          `json:"itemId"`
	Classification Classification  `json:"classification"`
	Qty            decimal.Decimal `json:"qty"`
	Value          decimal.Decimal `json:"value"`
}

// ExportCheckInput is the input of EvaluateExport. Destination is ISO 3166 alpha-2;
// Region is ISO 3166-2 (for example UA-43); Mode is BLOCK, WARN or OFF.
// OfficerReleaseLicenseRef is set when a compliance.trade-screening.decide holder releases an
// embargoed delivery with a license reference (BR-48.16).
type ExportCheckInput struct {
	Destination              string        `json:"destination"`
	Region                   string        `json:"region,omitempty"`
	Items                    []ExportItem  `json:"items"`
	Licenses                 []License     `json:"licenses"`
	Table                    []LicenseRule `json:"-"` // the pack's table; nil uses DefaultLicenseTable
	Mode                     string        `json:"-"` // trade.license_check of the legal entity
	Now                      time.Time     `json:"-"` // server time; never the caller's
	OfficerReleaseLicenseRef string        `json:"officerReleaseLicenseRef,omitempty"`
}

// LineDecision is the outcome for one item.
type LineDecision struct {
	ItemID          string `json:"itemId"`
	LicenseRequired bool   `json:"licenseRequired"`
	LicenseID       string `json:"licenseId,omitempty"`
	Blocked         bool   `json:"blocked"`
	Warning         bool   `json:"warning"`
	Code            string `json:"code,omitempty"`
	Detail          string `json:"detail,omitempty"`
}

// ExportResult is the license determination for a delivery.
type ExportResult struct {
	Embargo bool           `json:"embargo"`
	Blocked bool           `json:"blocked"`
	Items   []LineDecision `json:"items"`
}

// IsEmbargoed reports whether a destination is under comprehensive embargo (country or region).
func IsEmbargoed(country, region string) bool {
	return comprehensiveEmbargo[strings.ToUpper(country)] || embargoedRegions[strings.ToUpper(region)]
}

// RequiresLicense reads the decision table for a classification and destination.
func RequiresLicense(table []LicenseRule, c Classification, country string) bool {
	country = strings.ToUpper(country)
	for _, r := range table {
		if r.Regime != c.Regime || r.Code != c.Code {
			continue
		}
		if contains(r.Except, country) {
			continue
		}
		if contains(r.Destinations, wildcardCountry) || contains(r.Destinations, country) {
			return true
		}
	}
	return false
}

// EvaluateExport decides, per item, whether the delivery may go: an embargo blocks
// everything unless an officer releases it with a license reference; otherwise a
// license is required by the table and must be valid, unexpired and within its
// remaining quantity and value.
//
// Lines of one delivery draw on the same license in turn: a license with 100
// units left covers a first line of 60 but not a second line of 60.
func EvaluateExport(in ExportCheckInput) (ExportResult, error) {
	in.Destination = strings.ToUpper(strings.TrimSpace(in.Destination))
	in.Region = strings.ToUpper(strings.TrimSpace(in.Region))
	if !isAlpha2(in.Destination) {
		return ExportResult{}, &Error{Code: CodeExportInput, Detail: "destination must be an ISO 3166 alpha-2 code"}
	}
	if in.Region != "" && !strings.HasPrefix(in.Region, in.Destination+"-") {
		return ExportResult{}, &Error{Code: CodeExportInput, Detail: "region must be an ISO 3166-2 code of the destination, for example UA-43"}
	}
	if len(in.Items) == 0 || len(in.Items) > maxExportItems {
		return ExportResult{}, &Error{Code: CodeExportInput, Detail: fmt.Sprintf("a delivery needs 1 to %d items", maxExportItems)}
	}
	mode := in.Mode
	if mode == "" {
		mode = LicenseCheckBlock
	}
	if mode != LicenseCheckBlock && mode != LicenseCheckWarn && mode != LicenseCheckOff {
		return ExportResult{}, &Error{Code: CodeExportInput, Detail: fmt.Sprintf("unknown license check mode %q", mode)}
	}
	table := in.Table
	if table == nil {
		table = DefaultLicenseTable
	}
	for _, l := range in.Licenses {
		if l.LicenseID == "" || l.UsedQty.IsNegative() || l.UsedValue.IsNegative() ||
			(l.QtyLimit != nil && l.QtyLimit.IsNegative()) || (l.ValueLimit != nil && l.ValueLimit.IsNegative()) {
			return ExportResult{}, &Error{Code: CodeExportInput, Detail: "every license needs an id and non-negative limits and usage"}
		}
	}
	res := ExportResult{Items: make([]LineDecision, 0, len(in.Items))}
	embargo := IsEmbargoed(in.Destination, in.Region)
	res.Embargo = embargo
	drawn := map[string]usage{}
	for _, it := range in.Items {
		if it.ItemID == "" || it.Qty.IsNegative() || it.Value.IsNegative() {
			return ExportResult{}, &Error{Code: CodeExportInput, Detail: "every item needs an id and a non-negative quantity and value"}
		}
		if !validRegime(it.Classification.Regime) || strings.TrimSpace(it.Classification.Code) == "" {
			return ExportResult{}, &Error{Code: CodeExportInput, Detail: fmt.Sprintf("item %s needs a classification regime (US_EAR, EU_DUALUSE or HS) and a code", it.ItemID)}
		}
		d := decideItem(it, in, table, mode, embargo, drawn)
		if d.Blocked {
			res.Blocked = true
		}
		res.Items = append(res.Items, d)
	}
	return res, nil
}

// usage is what earlier lines of the delivery drew on a license.
type usage struct{ qty, value decimal.Decimal }

func decideItem(it ExportItem, in ExportCheckInput, table []LicenseRule, mode string, embargo bool, drawn map[string]usage) LineDecision {
	d := LineDecision{ItemID: it.ItemID}
	if embargo {
		d.LicenseRequired = true
		if in.OfficerReleaseLicenseRef != "" {
			d.LicenseID = in.OfficerReleaseLicenseRef
			return d
		}
		d.Blocked = true
		d.Code = CodeEmbargo
		d.Detail = fmt.Sprintf("destination %s is under embargo; delivery blocked", in.Destination)
		return d
	}
	if mode == LicenseCheckOff || !RequiresLicense(table, it.Classification, in.Destination) {
		return d
	}
	d.LicenseRequired = true
	if id, ok := matchLicense(in.Licenses, it, in.Destination, in.Now, drawn); ok {
		u := drawn[id]
		drawn[id] = usage{qty: u.qty.Add(it.Qty), value: u.value.Add(it.Value)}
		d.LicenseID = id
		return d
	}
	d.Code = CodeLicenseNeeded
	d.Detail = fmt.Sprintf("export license required for %s (%s:%s) to %s; none valid", it.ItemID, it.Classification.Regime, it.Classification.Code, in.Destination)
	if mode == LicenseCheckWarn {
		d.Warning = true
		return d
	}
	d.Blocked = true
	return d
}

// matchLicense returns the first valid license for the item in license id order,
// counting what earlier lines of the delivery already drew on it.
func matchLicense(ls []License, it ExportItem, dest string, now time.Time, drawn map[string]usage) (string, bool) {
	sorted := append([]License(nil), ls...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].LicenseID < sorted[j].LicenseID })
	key := it.Classification.Regime + ":" + it.Classification.Code
	for _, l := range sorted {
		if !l.ExpiresAt.After(now) || l.Regime != it.Classification.Regime {
			continue
		}
		if !contains(l.Classifications, key) || !containsFold(l.Destinations, dest) {
			continue
		}
		u := drawn[l.LicenseID]
		if !withinLimit(l.QtyLimit, l.UsedQty.Add(u.qty), it.Qty) || !withinLimit(l.ValueLimit, l.UsedValue.Add(u.value), it.Value) {
			continue
		}
		return l.LicenseID, true
	}
	return "", false
}

// withinLimit reports whether used + requested stays within the limit; nil means unlimited.
func withinLimit(limit *decimal.Decimal, used, requested decimal.Decimal) bool {
	return limit == nil || used.Add(requested).LessThanOrEqual(*limit)
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func containsFold(xs []string, v string) bool {
	for _, x := range xs {
		if strings.EqualFold(strings.TrimSpace(x), v) {
			return true
		}
	}
	return false
}

func isAlpha2(s string) bool {
	return len(s) == 2 && s[0] >= 'A' && s[0] <= 'Z' && s[1] >= 'A' && s[1] <= 'Z'
}

func validRegime(r string) bool { return r == RegimeUSEAR || r == RegimeEUDualUse || r == RegimeHS }

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
