package domain

import (
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

// Batch attribute data types (FR-24.5, PRD 4.24.4 BatchAttribute.data_type).
const (
	AttrNumeric = "numeric"
	AttrText    = "text"
	AttrEnum    = "enum"
	AttrDate    = "date"
)

// AttributeDef is a batch attribute defined for an item (PRD 4.24.4
// BatchAttribute). Min and Max bound a numeric attribute (inclusive, nil is
// open); Target is informative; Options lists the values an enum allows.
type AttributeDef struct {
	Name    string
	Kind    string
	Target  *decimal.Decimal
	Min     *decimal.Decimal
	Max     *decimal.Decimal
	Options []string
}

// AttributeCheck is the outcome for one recorded value. Pass is false when a
// numeric value lies outside its limits; the value is still recorded and the
// failure becomes an EBR exception (BR-24.15), so it is not a refusal. Detail
// says why it failed.
type AttributeCheck struct {
	Name   string
	Pass   bool
	Detail string
}

// ValidateDefinitions checks the definitions themselves: names are unique and
// set, the kind is known, limits and target only on numeric attributes with
// min ≤ max, and an enum lists at least one option.
func ValidateDefinitions(defs []AttributeDef) error {
	seen := make(map[string]bool, len(defs))
	for _, d := range defs {
		if d.Name == "" {
			return errorf(CodeInvalid, "every attribute definition needs a name")
		}
		if seen[d.Name] {
			return errorf(CodeInvalid, "attribute %q is defined twice", d.Name)
		}
		seen[d.Name] = true
		switch d.Kind {
		case AttrNumeric:
			if d.Min != nil && d.Max != nil && d.Min.GreaterThan(*d.Max) {
				return errorf(CodeInvalid, "attribute %q has min %s above max %s", d.Name, d.Min, d.Max)
			}
		case AttrText, AttrEnum, AttrDate:
			if d.Min != nil || d.Max != nil || d.Target != nil {
				return errorf(CodeInvalid, "attribute %q is %s; min, max and target apply to numeric attributes only", d.Name, d.Kind)
			}
		default:
			return errorf(CodeInvalid, "attribute %q has unknown kind %q", d.Name, d.Kind)
		}
		if d.Kind == AttrEnum && len(d.Options) == 0 {
			return errorf(CodeInvalid, "enum attribute %q lists no options", d.Name)
		}
		if d.Kind != AttrEnum && len(d.Options) > 0 {
			return errorf(CodeInvalid, "attribute %q is %s; options apply to enum attributes only", d.Name, d.Kind)
		}
	}
	return nil
}

// CheckAttributes validates recorded values against their definitions (FR-24.5).
// Refused (COM-0001): invalid definitions, an attribute the item does not define,
// a value of the wrong type for its kind, and an enum value that is not an
// option. A numeric value outside min/max is accepted with Pass false. Results
// are sorted by name so the answer is stable.
func CheckAttributes(defs []AttributeDef, values map[string]Value) ([]AttributeCheck, error) {
	if err := ValidateDefinitions(defs); err != nil {
		return nil, err
	}
	byName := make(map[string]AttributeDef, len(defs))
	for _, d := range defs {
		byName[d.Name] = d
	}
	names := make([]string, 0, len(values))
	for n := range values {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]AttributeCheck, 0, len(names))
	for _, n := range names {
		d, ok := byName[n]
		if !ok {
			return nil, errorf(CodeInvalid, "attribute %q is not defined for this item", n)
		}
		c, err := checkValue(d, values[n])
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func checkValue(d AttributeDef, v Value) (AttributeCheck, error) {
	pass := AttributeCheck{Name: d.Name, Pass: true}
	switch d.Kind {
	case AttrNumeric:
		if v.Num == nil || v.Text != nil || v.Date != nil {
			return AttributeCheck{}, typeErr(d, "a number")
		}
		if d.Min != nil && v.Num.LessThan(*d.Min) {
			return AttributeCheck{Name: d.Name, Detail: fmt.Sprintf("%s %s is below the minimum %s", d.Name, v.Num, d.Min)}, nil
		}
		if d.Max != nil && v.Num.GreaterThan(*d.Max) {
			return AttributeCheck{Name: d.Name, Detail: fmt.Sprintf("%s %s is above the maximum %s", d.Name, v.Num, d.Max)}, nil
		}
		return pass, nil
	case AttrText:
		if v.Text == nil || v.Num != nil || v.Date != nil {
			return AttributeCheck{}, typeErr(d, "text")
		}
		return pass, nil
	case AttrEnum:
		if v.Text == nil || v.Num != nil || v.Date != nil {
			return AttributeCheck{}, typeErr(d, "one of its options")
		}
		for _, o := range d.Options {
			if o == *v.Text {
				return pass, nil
			}
		}
		return AttributeCheck{}, errorf(CodeInvalid, "attribute %q value %q is not one of its options", d.Name, *v.Text)
	default: // AttrDate; ValidateDefinitions refused any other kind
		if v.Date == nil || v.Num != nil || v.Text != nil {
			return AttributeCheck{}, typeErr(d, "a date")
		}
		return pass, nil
	}
}

func typeErr(d AttributeDef, want string) error {
	return errorf(CodeInvalid, "attribute %q must be %s", d.Name, want)
}
