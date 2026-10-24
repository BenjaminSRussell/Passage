package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// ValidateAttribute checks a single typed value (FR-8.6).
func ValidateAttribute(t AttributeType, value string) error {
	v := strings.TrimSpace(value)
	if v == "" {
		if t.Required {
			return errf(CodeAttrRequired, "required attributes missing: "+t.Code)
		}
		return nil
	}
	switch t.Kind {
	case "integer":
		n, err := strconv.Atoi(v)
		if err != nil {
			return errf(CodeAttrRange, t.Code+" must be an integer")
		}
		d := decimal.NewFromInt(int64(n))
		return inRange(t, d)
	case "decimal", "currency":
		d, err := decimal.NewFromString(v)
		if err != nil {
			return errf(CodeAttrRange, t.Code+" must be a decimal")
		}
		return inRange(t, d)
	case "boolean":
		if v != "true" && v != "false" {
			return errf(CodeAttrRange, t.Code+" must be true or false")
		}
	case "date":
		if _, err := time.Parse("2006-01-02", v); err != nil {
			return errf(CodeAttrRange, t.Code+" must be YYYY-MM-DD")
		}
	case "enum":
		for _, e := range t.Enum {
			if e == v {
				return nil
			}
		}
		return errf(CodeAttrRange, t.Code+" must be one of the enumerated values")
	case "text":
		if len(v) > 500 {
			return errf(CodeAttrRange, t.Code+" exceeds 500 characters")
		}
	}
	return nil
}

func inRange(t AttributeType, d decimal.Decimal) error {
	if t.Min != nil && d.LessThan(*t.Min) {
		return errf(CodeAttrRange, fmt.Sprintf("%s must be between %s and %s", t.Code, t.Min.String(), bound(t.Max)))
	}
	if t.Max != nil && d.GreaterThan(*t.Max) {
		return errf(CodeAttrRange, fmt.Sprintf("%s must be between %s and %s", t.Code, bound(t.Min), t.Max.String()))
	}
	return nil
}

func bound(d *decimal.Decimal) string {
	if d == nil {
		return "∞"
	}
	return d.String()
}

// MissingRequired returns required attribute codes that are empty. Enforced at active, not create (BR-8.9).
func MissingRequired(defs []AttributeType, values map[string]string) []string {
	var missing []string
	for _, d := range defs {
		if !d.Required {
			continue
		}
		if strings.TrimSpace(values[d.Code]) == "" {
			missing = append(missing, d.Code)
		}
	}
	return missing
}

// AssertActivationAttributes rejects an active transition with missing or invalid required attributes (AC-8.7).
func AssertActivationAttributes(from, to string, defs []AttributeType, values map[string]string) error {
	if to != StateActive || from == StateActive {
		return nil
	}
	if miss := MissingRequired(defs, values); len(miss) > 0 {
		return errf(CodeAttrRequired, "required attributes missing: "+strings.Join(miss, ", "))
	}
	for _, d := range defs {
		if err := ValidateAttribute(d, values[d.Code]); err != nil {
			return err
		}
	}
	return nil
}
