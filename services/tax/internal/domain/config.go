package domain

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	CodeConfigInUse = "TAX-0013"

	KindParty = "party"
	KindItem  = "item"
)

var (
	codePattern    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	countryPattern = regexp.MustCompile(`^[A-Z]{2}$`)

	jurisdictionKinds = []string{"country", "state", "county", "city", "district"}
)

// Jurisdiction is one node of the jurisdiction tree. Tax authorities nest: a country holds
// states, a state holds counties, a county holds cities.
type Jurisdiction struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Country    string `json:"country"`
	ParentCode string `json:"parent_code"`
}

// ConfigCode is a tax code as it is stored: the engine's Code plus a display name.
type ConfigCode struct {
	Code
	Name string `json:"name"`
}

// Group is a party tax group or an item tax group and the tax codes it selects.
type Group struct {
	Kind     string   `json:"kind"`
	Code     string   `json:"code"`
	Name     string   `json:"name"`
	TaxCodes []string `json:"tax_codes"`
}

func ValidCode(code string) bool { return codePattern.MatchString(code) }

func ValidCountry(c string) bool { return countryPattern.MatchString(c) }

// ValidateJurisdiction checks one jurisdiction against the tree it is saved into (existing, by
// code). The parent must exist, must not be the node itself or one of its descendants, and the
// tree stays at most 8 levels deep.
func ValidateJurisdiction(j Jurisdiction, existing map[string]Jurisdiction) error {
	switch {
	case !ValidCode(j.Code) || len(j.Code) > 32:
		return invalid("code", "code is required, at most 32 letters, digits, dots, dashes or underscores")
	case strings.TrimSpace(j.Name) == "" || len(j.Name) > 120:
		return invalid("name", "name is required and at most 120 characters")
	case !oneOf(j.Kind, jurisdictionKinds...):
		return invalid("kind", "kind must be one of %s", strings.Join(jurisdictionKinds, ", "))
	case !ValidCountry(j.Country):
		return invalid("country", "country must be a 2-letter ISO 3166 code in capitals")
	}
	if j.ParentCode == "" {
		return nil
	}
	if j.ParentCode == j.Code {
		return invalid("parent_code", "a jurisdiction cannot be its own parent")
	}
	depth := 0
	for at := j.ParentCode; at != ""; depth++ {
		p, ok := existing[at]
		switch {
		case !ok:
			return invalid("parent_code", "the parent jurisdiction does not exist")
		case at == j.Code || depth >= 8:
			return invalid("parent_code", "the parent would make the tree loop or nest deeper than 8 levels")
		}
		at = p.ParentCode
	}
	return nil
}

// JurisdictionPath returns the codes from the root down to code, for display.
func JurisdictionPath(code string, byCode map[string]Jurisdiction) []string {
	var path []string
	for at, n := code, 0; at != "" && n < 16; n++ {
		j, ok := byCode[at]
		if !ok {
			break
		}
		path = append([]string{at}, path...)
		at = j.ParentCode
	}
	return path
}

// ValidateConfigCode checks a stored tax code: names, mechanism, rounding and its rate history.
func ValidateConfigCode(c ConfigCode) error {
	switch {
	case !ValidCode(c.Code.Code):
		return invalid("code", "code is required, at most 64 letters, digits, dots, dashes or underscores")
	case strings.TrimSpace(c.Name) == "" || len(c.Name) > 120:
		return invalid("name", "name is required and at most 120 characters")
	case c.Jurisdiction == "":
		return invalid("jurisdiction", "jurisdiction is required")
	case !oneOf(c.Mechanism, "", MechStandard, MechReverseCharge, MechUseTax):
		return invalid("mechanism", "unknown mechanism %q", c.Mechanism)
	case !oneOf(c.CalcBasis, "", BasisLine, BasisDocument):
		return invalid("calc_basis", "unknown calc_basis %q", c.CalcBasis)
	case !oneOf(c.Rounding, "", RoundPerLine, RoundPerDocument):
		return invalid("rounding", "unknown rounding %q", c.Rounding)
	case c.CompoundSequence < 0:
		return invalid("compound_sequence", "compound_sequence must not be negative")
	case c.RecoverablePct != nil && (c.RecoverablePct.IsNegative() || c.RecoverablePct.GreaterThan(hundred)):
		return invalid("recoverable_pct", "recoverable_pct must be between 0 and 100")
	case len(c.Rates) > 200:
		return invalid("rates", "at most 200 rates per code")
	}
	for i, r := range c.Rates {
		field := fmt.Sprintf("rates[%d]", i)
		switch {
		case r.ValidFrom.IsZero():
			return invalid(field+".valid_from", "valid_from is required")
		case !r.ValidTo.IsZero() && r.ValidTo.Before(r.ValidFrom.Time):
			return invalid(field+".valid_to", "valid_to may not precede valid_from")
		case r.RatePct.IsNegative() || r.RatePct.GreaterThan(hundred):
			return invalid(field+".rate_pct", "rate_pct must be between 0 and 100")
		case r.FromAmount != nil && r.ToAmount != nil && r.ToAmount.LessThan(r.FromAmount.Decimal):
			return invalid(field+".to_amount", "to_amount may not be below from_amount")
		}
		for j := 0; j < i; j++ {
			if sameBracket(r, c.Rates[j]) && datesOverlap(r, c.Rates[j]) {
				return invalid(field+".valid_from", "rate %d overlaps rate %d for the same amount range", i+1, j+1)
			}
		}
	}
	return nil
}

func sameBracket(a, b Rate) bool {
	eq := func(x, y *Dec) bool {
		if x == nil || y == nil {
			return x == y
		}
		return x.Equal(y.Decimal)
	}
	return eq(a.FromAmount, b.FromAmount) && eq(a.ToAmount, b.ToAmount)
}

func datesOverlap(a, b Rate) bool {
	aEndsBeforeB := !a.ValidTo.IsZero() && a.ValidTo.Before(b.ValidFrom.Time)
	bEndsBeforeA := !b.ValidTo.IsZero() && b.ValidTo.Before(a.ValidFrom.Time)
	return !aEndsBeforeB && !bEndsBeforeA
}

// ValidateGroup checks a tax group; knownCodes are the stored tax codes it may select.
func ValidateGroup(g Group, knownCodes map[string]bool) error {
	switch {
	case g.Kind != KindParty && g.Kind != KindItem:
		return invalid("kind", "kind must be %q or %q", KindParty, KindItem)
	case !ValidCode(g.Code):
		return invalid("code", "code is required, at most 64 letters, digits, dots, dashes or underscores")
	case strings.TrimSpace(g.Name) == "" || len(g.Name) > 120:
		return invalid("name", "name is required and at most 120 characters")
	case len(g.TaxCodes) > 100:
		return invalid("tax_codes", "at most 100 tax codes per group")
	}
	seen := map[string]bool{}
	for _, c := range g.TaxCodes {
		switch {
		case seen[c]:
			return invalid("tax_codes", "tax code %q is listed twice", c)
		case !knownCodes[c]:
			return invalid("tax_codes", "tax code %q does not exist", c)
		}
		seen[c] = true
	}
	return nil
}

// AssembleConfig builds the engine's Config from the stored rows.
func AssembleConfig(codes []ConfigCode, groups []Group, defaultRounding string) Config {
	cfg := Config{DefaultRounding: defaultRounding, Groups: map[string][]string{}, ItemGroups: map[string][]string{}}
	for _, c := range codes {
		cfg.Codes = append(cfg.Codes, c.Code)
	}
	for _, g := range groups {
		target := cfg.Groups
		if g.Kind == KindItem {
			target = cfg.ItemGroups
		}
		target[g.Code] = append([]string{}, g.TaxCodes...)
	}
	return cfg
}
