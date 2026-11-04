package domain

import (
	"fmt"
	"regexp"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

// Process message category master data (FR-25.17, O05C/O06C/O07C equivalent).
const (
	maxCategoryCodeLen = 32
	maxCategoryNameLen = 120
	maxUnitLen         = 16
)

var categoryCodePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// DefaultCategoryCodes lists the built-in categories, in the order the monitor offers them.
var DefaultCategoryCodes = []string{"phase-start", "phase-end", "consumption", "yield", "process-data", "quality-result", "deviation", "order-status", "custom"}

// ValidCategoryCode reports whether code has the shape of a category code: lowercase letters,
// digits and hyphens, 1 to 32 characters, starting with a letter or digit.
func ValidCategoryCode(code string) bool {
	return len(code) >= 1 && len(code) <= maxCategoryCodeLen && categoryCodePattern.MatchString(code)
}

// CharacteristicDef is a characteristic a category defines: its type, unit and limits. A value
// of that name takes the definition's unit and limits, and a required one must be present.
type CharacteristicDef struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Unit     *string `json:"unit,omitempty"`
	Min      *string `json:"min,omitempty"`
	Max      *string `json:"max,omitempty"`
	Required bool    `json:"required"`
}

// CategoryInput is what a create or update of a category writes.
type CategoryInput struct {
	Code             string
	Name             string
	Destinations     []string
	RejectOutOfLimit bool
	Characteristics  []CharacteristicDef
	Active           bool
}

// FieldErrors checks the shape of a category. creating also checks the code, which never changes
// afterwards. Keys are JSON field names.
func (c CategoryInput) FieldErrors(creating bool) map[string]string {
	f := map[string]string{}
	if creating && !ValidCategoryCode(c.Code) {
		f["code"] = fmt.Sprintf("must be 1 to %d lowercase letters, digits or hyphens", maxCategoryCodeLen)
	}
	if n := utf8.RuneCountInString(c.Name); n < 1 || n > maxCategoryNameLen {
		f["name"] = fmt.Sprintf("must be 1 to %d characters", maxCategoryNameLen)
	}
	if len(c.Destinations) < 1 {
		f["destinations"] = "must name at least one destination"
	}
	seenDest := map[string]bool{}
	for _, d := range c.Destinations {
		switch {
		case !KnownDestination(d):
			f["destinations"] = "must be ebr, journal, quality, kafka or webhook"
		case seenDest[d]:
			f["destinations"] = "must not repeat a destination"
		}
		seenDest[d] = true
	}
	if len(c.Characteristics) > maxCharacters {
		f["characteristics"] = fmt.Sprintf("must define at most %d characteristics", maxCharacters)
	}
	seen := map[string]bool{}
	for i, d := range c.Characteristics {
		key := fmt.Sprintf("characteristics[%d]", i)
		if n := utf8.RuneCountInString(d.Name); n < 1 || n > maxNameLen {
			f[key+".name"] = fmt.Sprintf("must be 1 to %d characters", maxNameLen)
		} else if seen[d.Name] {
			f[key+".name"] = "must be unique within the category"
		}
		seen[d.Name] = true
		if d.Unit != nil && utf8.RuneCountInString(*d.Unit) > maxUnitLen {
			f[key+".unit"] = fmt.Sprintf("must be at most %d characters", maxUnitLen)
		}
		switch d.Type {
		case "numeric":
			lo, loErr := defBound(d.Min)
			hi, hiErr := defBound(d.Max)
			switch {
			case loErr != nil:
				f[key+".min"] = "must be a decimal number"
			case hiErr != nil:
				f[key+".max"] = "must be a decimal number"
			case lo != nil && hi != nil && lo.GreaterThan(*hi):
				f[key+".min"] = "must not be above the maximum"
			}
		case "text", "boolean":
			if d.Min != nil || d.Max != nil {
				f[key+".min"] = "limits apply to numeric characteristics only"
			}
		default:
			f[key+".type"] = "must be numeric, text or boolean"
		}
	}
	return f
}

func defBound(s *string) (*decimal.Decimal, error) {
	if s == nil {
		return nil, nil
	}
	d, err := decimal.NewFromString(*s)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// applyDefinitions lets the category's characteristic definitions govern the message values: a
// value whose name is defined must have the defined type (SF-0115) and takes the definition's unit
// and limits in place of its own; a required definition without a value is SF-0115. Values with no
// definition pass through unchanged.
func applyDefinitions(cat Category, in []Characteristic) ([]Characteristic, error) {
	if len(cat.Characteristics) == 0 {
		return in, nil
	}
	defs := make(map[string]CharacteristicDef, len(cat.Characteristics))
	for _, d := range cat.Characteristics {
		defs[d.Name] = d
	}
	out := make([]Characteristic, 0, len(in))
	present := map[string]bool{}
	for _, c := range in {
		present[c.Name] = true
		if d, ok := defs[c.Name]; ok {
			if c.Type != d.Type {
				return nil, invalidValue(c.Name, fmt.Sprintf("must be %s in category %s", d.Type, cat.Code))
			}
			c.Unit, c.Min, c.Max = d.Unit, d.Min, d.Max
		}
		out = append(out, c)
	}
	for _, d := range cat.Characteristics {
		if d.Required && !present[d.Name] {
			return nil, &Error{Code: CodeInvalidValue, Detail: fmt.Sprintf("category %s requires a value for %s", cat.Code, d.Name)}
		}
	}
	return out, nil
}
