// Package residency is the one place that maps a tenant residency code (EU, US, UK, GLOBAL) to the regions that
// tenant's data may live in (issue 605, EU Data Boundary; wave 48 "data residency per tenant").
//
// The table is residency.json, embedded here and read by deploy/multi-region/tools/residency_report.py, so the
// provisioning check and the auditor's evidence report cannot disagree. Regions are written both as Azure region
// names (westeurope, swedencentral) and as the legacy ids of deploy/multi-region/values (eu-west, eu-central).
// A drift test in Go (the eu-data-boundary overlay of the cloud profile) and one in Python keep the file honest.
package residency

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Codes of the residency values.
const (
	EU     = "EU"
	UK     = "UK"
	US     = "US"
	Global = "GLOBAL"
)

// CodeRegionNotAllowed is the problem code of a refused placement. It is the PRD's OPS-1705 (9.50 error catalogue):
// the residency's region is not deployed, or the tenant would be placed outside its residency.
const CodeRegionNotAllowed = "OPS-1705"

//go:embed residency.json
var tableJSON []byte

// Entry is one residency code of the table.
type Entry struct {
	Description string   `json:"description"`
	Regions     []string `json:"regions"` // empty means any region
}

// Table is residency.json.
type Table struct {
	SchemaVersion int              `json:"schemaVersion"`
	Default       string           `json:"default"`
	Residencies   map[string]Entry `json:"residencies"`
}

var (
	once    sync.Once
	table   *Table
	tableEr error
)

// Load returns the parsed table.
func Load() (*Table, error) {
	once.Do(func() {
		table = &Table{}
		if tableEr = json.Unmarshal(tableJSON, table); tableEr == nil {
			for code, e := range table.Residencies {
				for i, r := range e.Regions {
					e.Regions[i] = NormalizeRegion(r)
				}
				table.Residencies[code] = e
			}
		}
	})
	return table, tableEr
}

func mustLoad() *Table {
	t, err := Load()
	if err != nil {
		panic("residency: embedded residency.json: " + err.Error())
	}
	return t
}

// NormalizeRegion trims and lower-cases a region name.
func NormalizeRegion(region string) string { return strings.ToLower(strings.TrimSpace(region)) }

// NormalizeCode trims and upper-cases a residency code; an empty code is the default (GLOBAL).
func NormalizeCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return mustLoad().Default
	}
	return code
}

// Codes lists the residency codes, sorted.
func Codes() []string {
	t := mustLoad()
	out := make([]string, 0, len(t.Residencies))
	for c := range t.Residencies {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Valid reports whether code names a residency (case-insensitive; empty is the default and valid).
func Valid(code string) bool {
	_, ok := mustLoad().Residencies[NormalizeCode(code)]
	return ok
}

// Regions returns the regions code allows, sorted, and whether code is known. A nil slice with true means any region.
func Regions(code string) ([]string, bool) {
	e, ok := mustLoad().Residencies[NormalizeCode(code)]
	if !ok {
		return nil, false
	}
	if len(e.Regions) == 0 {
		return nil, true
	}
	out := append([]string(nil), e.Regions...)
	sort.Strings(out)
	return out, true
}

// Allows reports whether a tenant with this residency may have data in region. An unknown residency allows nothing,
// and a blank region is never allowed.
func Allows(code, region string) bool {
	region = NormalizeRegion(region)
	if region == "" {
		return false
	}
	e, ok := mustLoad().Residencies[NormalizeCode(code)]
	if !ok {
		return false
	}
	if len(e.Regions) == 0 {
		return true
	}
	for _, r := range e.Regions {
		if r == region {
			return true
		}
	}
	return false
}

// ResidenciesOf lists the residency codes (other than the unrestricted one) whose region list holds region, sorted.
func ResidenciesOf(region string) []string {
	region = NormalizeRegion(region)
	var out []string
	for code, e := range mustLoad().Residencies {
		for _, r := range e.Regions {
			if r == region {
				out = append(out, code)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// Error is a refused placement. Code is the problem code (OPS-1705).
type Error struct {
	Code      string
	Residency string
	Region    string
	Detail    string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// Check is the provisioning check: it returns nil when a tenant with this residency may be placed in region and an
// *Error (OPS-1705) otherwise. Run it wherever a tenant is given a region, before anything is created there.
func Check(code, region string) error {
	code = NormalizeCode(code)
	if _, ok := mustLoad().Residencies[code]; !ok {
		return &Error{Code: CodeRegionNotAllowed, Residency: code, Region: region,
			Detail: fmt.Sprintf("residency %q is not one of %s", code, strings.Join(Codes(), ", "))}
	}
	if NormalizeRegion(region) == "" {
		return &Error{Code: CodeRegionNotAllowed, Residency: code, Region: region, Detail: "a tenant is placed in a named region"}
	}
	if !Allows(code, region) {
		return &Error{Code: CodeRegionNotAllowed, Residency: code, Region: region,
			Detail: fmt.Sprintf("region %s is outside residency %s; tenant not provisioned. Deploy an allowed region or change the residency", region, code)}
	}
	return nil
}
