package domain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/shopspring/decimal"
)

// Lookup tables (outbound value lookups): tenant tables that translate a Passage value to the value the legacy ERP
// expects, by key. An enum table maps Passage codes ("kg", "pallet") to the legacy codes; a unit-of-measure table
// holds conversion factors keyed "FROM|TO"; a price list holds a price per item key. An outbound mapping reads one
// entry through its `lookups` and the mapping rules see it as the row field lookup.<name>. A unit conversion lookup
// with a quantityPath yields the event quantity times the factor (exact decimal arithmetic) instead of the factor.
//
// Tables are tenant data, like outbound mappings: one table serves every legal entity. A price list or code list that
// differs per legal entity or currency puts those event fields in its key (keyPaths ["legalentityid", "data.currency",
// "data.sku"] reads the entry "<legal entity id>|EUR|SKU-1").

const (
	LookupEnum  = "enum"
	LookupUoM   = "uom_conversion"
	LookupPrice = "price_list"

	CodeLookupInvalid = "MIG-LOOKUP-INVALID"

	// KeySeparator joins the parts of a composite key (a unit conversion's two units, an item and a currency).
	KeySeparator = "|"

	maxLookupEntries     = 5000
	maxLookupKeyLength   = 200
	maxLookupValueLength = 512
	maxOutboundLookups   = 8
	maxLookupKeyParts    = 3
	// maxFactorScale is the scale of a unit conversion factor: definitional factors need more than the 6 decimals of a
	// price (1 lb = 0.45359237 kg, 1 oz = 0.028349523125 kg). The mapping field that receives a converted quantity
	// rounds it to its own scale (half-even, MAP-PRECISION-LOSS when that changes the value).
	maxFactorScale = 12
)

var lookupTableNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.\-]{0,63}$`)

// ValidLookupKind reports whether kind is one of the table kinds.
func ValidLookupKind(kind string) bool {
	return kind == LookupEnum || kind == LookupUoM || kind == LookupPrice
}

// ValidLookupTableName checks the name an outbound mapping uses to refer to a table.
func ValidLookupTableName(name string) bool { return lookupTableNameRE.MatchString(name) }

// LookupDefinition is one version of a lookup table. Entries are keyed by text; a price or a factor is a decimal
// string (never a float), so it reaches a decimal mapping field exactly.
type LookupDefinition struct {
	Kind        string            `json:"kind"`
	Description string            `json:"description,omitempty"`
	Entries     map[string]string `json:"entries"`
}

// ParseLookupDefinition decodes and validates a table version.
func ParseLookupDefinition(raw []byte) (LookupDefinition, error) {
	var d LookupDefinition
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return d, &Error{CodeLookupInvalid, "definition JSON: " + err.Error()}
	}
	if d.Entries == nil {
		return d, &Error{CodeLookupInvalid, "entries is required (an object of key to value, {} for an empty table)"}
	}
	return d, d.check()
}

func (d LookupDefinition) check() error {
	bad := func(format string, a ...any) error { return &Error{CodeLookupInvalid, fmt.Sprintf(format, a...)} }
	if !ValidLookupKind(d.Kind) {
		return bad("kind must be %s, %s or %s", LookupEnum, LookupUoM, LookupPrice)
	}
	if len(d.Description) > 500 {
		return bad("description is at most 500 characters")
	}
	if len(d.Entries) > maxLookupEntries {
		return bad("at most %d entries", maxLookupEntries)
	}
	for k, v := range d.Entries {
		switch {
		case k == "" || len(k) > maxLookupKeyLength || strings.TrimSpace(k) != k:
			return bad("entry key %q must be 1 to %d characters with no leading or trailing space", k, maxLookupKeyLength)
		case v == "" || len(v) > maxLookupValueLength:
			return bad("entry %q needs a value of at most %d characters", k, maxLookupValueLength)
		}
		parts := strings.Split(k, KeySeparator)
		if len(parts) > maxLookupKeyParts || slicesContainsEmpty(parts) {
			return bad("entry key %q has an empty part or more than %d parts", k, maxLookupKeyParts)
		}
		switch d.Kind {
		case LookupUoM:
			if len(parts) != 2 || parts[0] == parts[1] {
				return bad("unit conversion key %q must be FROM%sTO with two different units", k, KeySeparator)
			}
			if f, err := lookupDecimal(v, maxFactorScale); err != nil || !f.IsPositive() {
				return bad("unit conversion %q needs a positive decimal factor with at most %d decimals", k, maxFactorScale)
			}
		case LookupPrice:
			if p, err := lookupDecimal(v, maxDecimalScale); err != nil || p.IsNegative() {
				return bad("price %q needs a decimal that is not negative", k)
			}
		}
	}
	return nil
}

func slicesContainsEmpty(parts []string) bool {
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			return true
		}
	}
	return false
}

// lookupDecimal parses a plain decimal (digits and a point, at most scale decimals, within the mapping DSL's magnitude).
func lookupDecimal(s string, scale int32) (decimal.Decimal, error) {
	d, err := decimal.NewFromString(s)
	if err != nil || strings.ContainsAny(s, "eE") {
		return d, fmt.Errorf("not a decimal")
	}
	if d.Exponent() < -scale || d.Abs().GreaterThanOrEqual(maxDecimalMagnitude) {
		return d, fmt.Errorf("out of range")
	}
	return d, nil
}

// OutboundLookup reads one entry of a lookup table for the event: the key is the event values at KeyPaths followed by
// the constant KeyConstants, joined by KeySeparator (a unit conversion to the legacy ERP's unit is keyPaths
// ["data.unit"] with keyConstants ["KG"]). A missing entry takes Default when there is one, is left out when Optional,
// and otherwise makes the render unresolved (422 MIG-XREF-UNRESOLVED), so a push never goes out with a guessed value.
// A table that does not exist or is archived is always unresolved: Default and Optional cover a missing entry, not a
// mapping that names the wrong table.
type OutboundLookup struct {
	Name         string   `json:"name"`
	Table        string   `json:"table"`
	KeyPaths     []string `json:"keyPaths"`
	KeyConstants []string `json:"keyConstants,omitempty"`
	// QuantityPath, for a unit conversion table, is the event quantity to convert: lookup.<name> is then the quantity
	// times the factor, and a conversion between equal units (KG|KG) is the quantity itself.
	QuantityPath string  `json:"quantityPath,omitempty"`
	Default      *string `json:"default,omitempty"`
	Optional     bool    `json:"optional,omitempty"`
}

func (l OutboundLookup) check() error {
	bad := func(format string, a ...any) error {
		return &Error{CodeOutboundInvalid, "lookup " + l.Name + ": " + fmt.Sprintf(format, a...)}
	}
	switch {
	case !xrefNameRE.MatchString(l.Name):
		return &Error{CodeOutboundInvalid, fmt.Sprintf("lookup name %q must start with a lower-case letter and use letters, digits and _", l.Name)}
	case !ValidLookupTableName(l.Table):
		return bad("table must start with a letter and use letters, digits, _ . -")
	case len(l.KeyPaths) < 1 || len(l.KeyPaths)+len(l.KeyConstants) > maxLookupKeyParts:
		return bad("keyPaths needs at least one dotted path into the event, and keyPaths and keyConstants at most %d parts together", maxLookupKeyParts)
	case l.QuantityPath != "" && !eventPathRE.MatchString(l.QuantityPath):
		return bad("quantityPath %q must be a dotted path into the event", l.QuantityPath)
	case l.Default != nil && (*l.Default == "" || len(*l.Default) > maxLookupValueLength):
		return bad("default must be 1 to %d characters", maxLookupValueLength)
	case l.Default != nil && l.Optional:
		return bad("default and optional cannot both be set: a default makes the lookup always yield a value")
	}
	for _, p := range l.KeyPaths {
		if !eventPathRE.MatchString(p) {
			return bad("keyPath %q must be a dotted path into the event", p)
		}
	}
	for _, c := range l.KeyConstants {
		if c == "" || strings.TrimSpace(c) != c || len(c) > maxLookupKeyLength || strings.Contains(c, KeySeparator) {
			return bad("keyConstant %q must be 1 to %d characters, without | or leading or trailing space", c, maxLookupKeyLength)
		}
	}
	return nil
}

// LookupAnswer is a table's answer for some keys: Found is false when no table has that name, Archived when the table
// is archived (archived tables do not resolve), otherwise Values holds the entries that exist. Kind and Version are
// the table's kind and the version the values were read from.
type LookupAnswer struct {
	Found, Archived bool
	Kind            string
	Version         int
	Values          map[string]string
}

// LookupUse records which version of a table a render read, so a pushed body can be traced to the entries behind it
// (the mapping reads a table's current version, not one pinned with the mapping).
type LookupUse struct {
	Name    string `json:"name"`
	Table   string `json:"table"`
	Version int    `json:"version"`
}

// LookupResolver reads lookup table entries by table name. The store implements it next to LegacyKeyResolver.
type LookupResolver interface {
	LookupValues(ctx context.Context, table string, keys []string) (LookupAnswer, error)
}

// lookupKey builds the composite key of event; why says what is missing when a part is absent or holds the separator
// (a key part never contains |, so "A|B" in one field cannot match the two-part entry A|B).
func (l OutboundLookup) lookupKey(event map[string]any) (key, why string) {
	parts := make([]string, 0, len(l.KeyPaths)+len(l.KeyConstants))
	for _, p := range l.KeyPaths {
		v, ok := idAt(event, p)
		if !ok {
			return "", "the event has no value at " + p
		}
		if strings.Contains(v, KeySeparator) {
			return "", "the value at " + p + " contains " + KeySeparator
		}
		parts = append(parts, v)
	}
	parts = append(parts, l.KeyConstants...)
	return strings.Join(parts, KeySeparator), ""
}

// convert multiplies the event quantity at QuantityPath by factor, exactly.
func (l OutboundLookup) convert(event map[string]any, factor string) (string, string) {
	q, ok := idAt(event, l.QuantityPath)
	if !ok {
		return "", "the event has no quantity at " + l.QuantityPath
	}
	qty, err := decimal.NewFromString(q)
	if err != nil || strings.ContainsAny(q, "eE") {
		return "", "the quantity at " + l.QuantityPath + " is not a decimal"
	}
	f, err := decimal.NewFromString(factor)
	if err != nil {
		return "", "the factor " + factor + " is not a decimal"
	}
	return qty.Mul(f).String(), ""
}

// resolveLookups fills values[name] for every lookup of the definition, returns the ones that found nothing, and the
// table versions it read.
func (d OutboundDefinition) resolveLookups(ctx context.Context, resolver any, event map[string]any) (map[string]any, []UnresolvedRef, []LookupUse, error) {
	values := map[string]any{}
	if len(d.Lookups) == 0 {
		return values, nil, nil, nil
	}
	lr, ok := resolver.(LookupResolver)
	if !ok {
		return nil, nil, nil, &Error{CodeOutboundInvalid, "this resolver cannot read lookup tables"}
	}
	var unresolved []UnresolvedRef
	var used []LookupUse
	for _, l := range d.Lookups {
		entity := "lookup:" + l.Table
		key, why := l.lookupKey(event)
		if why == "" {
			ans, err := lr.LookupValues(ctx, l.Table, []string{key})
			if err != nil {
				return nil, nil, nil, err
			}
			// A table that is missing, archived or of the wrong kind is a configuration problem: no default hides it.
			configWhy := ""
			switch {
			case !ans.Found:
				configWhy = "no lookup table named " + l.Table
			case ans.Archived:
				configWhy = "lookup table " + l.Table + " is archived"
			case l.QuantityPath != "" && ans.Kind != LookupUoM:
				configWhy = "lookup table " + l.Table + " is not a unit conversion table, so it cannot convert the quantity at " + l.QuantityPath
			}
			if configWhy != "" {
				unresolved = append(unresolved, UnresolvedRef{Name: l.Name, Entity: entity, ID: key, Reason: configWhy})
				continue
			}
			used = append(used, LookupUse{Name: l.Name, Table: l.Table, Version: ans.Version})
			v, found := ans.Values[key]
			if parts := strings.Split(key, KeySeparator); !found && ans.Kind == LookupUoM && len(parts) == 2 && parts[0] == parts[1] {
				v, found = "1", true // a unit converts to itself; tables cannot hold KG|KG
			}
			if found && l.QuantityPath != "" {
				v, why = l.convert(event, v)
				found = why == ""
			} else if !found {
				why = "the table has no entry for this key"
			}
			if found {
				values[l.Name] = v
				continue
			}
		}
		switch {
		case l.Default != nil:
			values[l.Name] = *l.Default
		case !l.Optional:
			unresolved = append(unresolved, UnresolvedRef{Name: l.Name, Entity: entity, ID: key, Reason: why})
		}
	}
	return values, unresolved, used, nil
}
