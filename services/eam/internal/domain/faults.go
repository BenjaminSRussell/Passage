package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrFaultCatalog is returned when a fault catalog definition is inconsistent.
var ErrFaultCatalog = errors.New("fault catalog is invalid")

var faultCodePattern = regexp.MustCompile(`^[A-Z0-9_]{1,20}$`)

// Catalog size limits, so one PUT stays a bounded transaction.
const (
	MaxFaultCodes        = 200
	MaxFaultCombinations = 1000
)

// ValidFaultCode reports whether s has the catalog code shape ^[A-Z0-9_]{1,20}$.
func ValidFaultCode(s string) bool { return faultCodePattern.MatchString(s) }

// FaultCode is one catalog entry (symptom, area, type, cause or remedy).
type FaultCode struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// FaultCombination is an allowed symptom/area/type triple for an asset type.
type FaultCombination struct {
	Symptom string `json:"symptom"`
	Area    string `json:"area"`
	Type    string `json:"type"`
}

// FaultCatalog is the designer's definition of one asset type's fault catalog (FR-44.16).
type FaultCatalog struct {
	Symptoms     []FaultCode        `json:"symptoms"`
	Areas        []FaultCode        `json:"areas"`
	Types        []FaultCode        `json:"types"`
	Causes       []FaultCode        `json:"causes"`
	Remedies     []FaultCode        `json:"remedies"`
	Combinations []FaultCombination `json:"allowedCombinations"`
}

// CheckCatalog validates codes (pattern, uniqueness, text), non-empty kinds, and that every allowed
// combination references existing symptom, area and type codes.
func CheckCatalog(c FaultCatalog) error {
	kinds := []struct {
		name  string
		codes []FaultCode
	}{{"symptoms", c.Symptoms}, {"areas", c.Areas}, {"types", c.Types}, {"causes", c.Causes}, {"remedies", c.Remedies}}
	sets := map[string]map[string]bool{}
	for _, k := range kinds {
		if len(k.codes) == 0 || len(k.codes) > MaxFaultCodes {
			return fmt.Errorf("%w: %s must have 1-%d codes", ErrFaultCatalog, k.name, MaxFaultCodes)
		}
		set := map[string]bool{}
		for _, fc := range k.codes {
			if !faultCodePattern.MatchString(fc.Code) {
				return fmt.Errorf("%w: code %q must match ^[A-Z0-9_]{1,20}$", ErrFaultCatalog, fc.Code)
			}
			if strings.TrimSpace(fc.Text) == "" || len(fc.Text) > 120 {
				return fmt.Errorf("%w: text for %q must be 1-120 characters", ErrFaultCatalog, fc.Code)
			}
			if set[fc.Code] {
				return fmt.Errorf("%w: duplicate %s code %q", ErrFaultCatalog, k.name, fc.Code)
			}
			set[fc.Code] = true
		}
		sets[k.name] = set
	}
	if len(c.Combinations) == 0 || len(c.Combinations) > MaxFaultCombinations {
		return fmt.Errorf("%w: a catalog needs 1-%d allowed combinations", ErrFaultCatalog, MaxFaultCombinations)
	}
	seen := map[FaultCombination]bool{}
	for _, cb := range c.Combinations {
		if !sets["symptoms"][cb.Symptom] || !sets["areas"][cb.Area] || !sets["types"][cb.Type] {
			return fmt.Errorf("%w: combination %s/%s/%s references an unknown code", ErrFaultCatalog, cb.Symptom, cb.Area, cb.Type)
		}
		if seen[cb] {
			return fmt.Errorf("%w: duplicate combination %s/%s/%s", ErrFaultCatalog, cb.Symptom, cb.Area, cb.Type)
		}
		seen[cb] = true
	}
	return nil
}

// CombinationAllowed reports whether c appears in the allowed list for an asset type (EAM-FLT-002).
func CombinationAllowed(allowed []FaultCombination, c FaultCombination) bool {
	for _, a := range allowed {
		if a == c {
			return true
		}
	}
	return false
}

// Fault is the five codes of one fault registration on a work order.
type Fault struct {
	Symptom string `json:"symptom"`
	Area    string `json:"area"`
	Type    string `json:"type"`
	Cause   string `json:"cause"`
	Remedy  string `json:"remedy"`
}

// ErrFaultNotAllowed is EAM-FLT-002: the codes are not allowed by the asset type's catalog.
var ErrFaultNotAllowed = errors.New("fault is not allowed by the catalog")

// CheckFault reports whether a registration fits the catalog (FR-44.16): the
// symptom/area/type triple is an allowed combination and the cause and remedy
// are catalog codes.
func CheckFault(c FaultCatalog, f Fault) error {
	if !CombinationAllowed(c.Combinations, FaultCombination{Symptom: f.Symptom, Area: f.Area, Type: f.Type}) {
		return fmt.Errorf("%w: combination %s/%s/%s is not allowed", ErrFaultNotAllowed, f.Symptom, f.Area, f.Type)
	}
	if !hasCode(c.Causes, f.Cause) {
		return fmt.Errorf("%w: cause %s is not in the catalog", ErrFaultNotAllowed, f.Cause)
	}
	if !hasCode(c.Remedies, f.Remedy) {
		return fmt.Errorf("%w: remedy %s is not in the catalog", ErrFaultNotAllowed, f.Remedy)
	}
	return nil
}

func hasCode(codes []FaultCode, code string) bool {
	for _, c := range codes {
		if c.Code == code {
			return true
		}
	}
	return false
}

// FaultRequired reports whether completing a work order of this type needs a registered fault (FR-44.16).
func FaultRequired(workType string) bool {
	return workType == "CORRECTIVE" || workType == "EMERGENCY"
}
