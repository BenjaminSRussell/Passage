package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	CodeMapInvalid  = "MIG-MAP-INVALID"
	CodeMapRequired = "MIG-MAP-REQUIRED"
	CodeMapCoerce   = "MIG-MAP-COERCE"
	CodeMapEnum     = "MIG-MAP-ENUM"
	CodeMapRange    = "MIG-MAP-RANGE"
)

const (
	TypeString    = "string"
	TypeInt       = "int"
	TypeDecimal   = "decimal"
	TypeDate      = "date"
	TypeBool      = "bool"
	TypeTimestamp = "timestamp"
)

const (
	maxDecimalScale      = 6
	maxDecimalExponent   = 14
	maxDecimalScaleGuard = 64
	dateLayout           = "2006-01-02"
)

var maxDecimalMagnitude = decimal.New(1, 14)

type Error struct{ Code, Detail string }

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

type FieldRule struct {
	Source      string            `json:"source"`
	Target      string            `json:"target"`
	Type        string            `json:"type"`
	Rule        string            `json:"rule,omitempty"`
	Value       *string           `json:"value,omitempty"`
	Sources     []string          `json:"sources,omitempty"`
	Separator   string            `json:"separator,omitempty"`
	Fn          string            `json:"fn,omitempty"`
	TimeSource  string            `json:"timeSource,omitempty"`
	Width       int               `json:"width,omitempty"`
	Enum        map[string]string `json:"enum,omitempty"`
	EnumDefault *string           `json:"enumDefault,omitempty"`
	Scale       *int              `json:"scale,omitempty"`
	Default     *string           `json:"default,omitempty"`
	Required    bool              `json:"required,omitempty"`
}

type Mapping struct {
	Entity         string        `json:"entity"`
	Version        int           `json:"version"`
	SourceTimezone string        `json:"sourceTimezone,omitempty"`
	Fields         []FieldRule   `json:"fields"`
	RejectWhen     []RejectWhen  `json:"rejectWhen,omitempty"`
	Tests          []MappingTest `json:"tests,omitempty"`
	loc            *time.Location
}

type Reject struct {
	Field  string `json:"field"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func ParseMapping(data []byte) (Mapping, error) {
	var m Mapping
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Mapping{}, &Error{CodeMapInvalid, "mapping JSON: " + err.Error()}
	}
	if m.Entity == "" || m.Version < 1 || len(m.Fields) == 0 {
		return Mapping{}, &Error{CodeMapInvalid, "entity, version >= 1 and at least one field are required"}
	}
	loc, err := time.LoadLocation(m.SourceTimezone)
	if err != nil {
		return Mapping{}, &Error{CodeMapInvalid, "sourceTimezone is not an IANA zone"}
	}
	m.loc = loc
	targets := map[string]bool{}
	for _, f := range m.Fields {
		if e := f.check(); e != nil {
			return Mapping{}, e
		}
		if targets[f.Target] {
			return Mapping{}, &Error{CodeMapInvalid, "duplicate target " + f.Target}
		}
		targets[f.Target] = true
		for _, v := range []*string{f.Default, f.Value, f.EnumDefault} {
			if v == nil {
				continue
			}
			if _, _, rej := m.coerce(f, *v, nil); rej != nil {
				return Mapping{}, &Error{CodeMapInvalid, "value for target " + f.Target + " does not satisfy its type"}
			}
		}
	}
	if e := m.checkRejectWhen(targets); e != nil {
		return Mapping{}, e
	}
	if e := m.checkTests(); e != nil {
		return Mapping{}, e
	}
	return m, nil
}

func (m Mapping) Apply(row map[string]any) (map[string]any, []Reject) {
	out, _, rejects := m.ApplyWithWarnings(row)
	return out, rejects
}

func (m Mapping) ApplyWithWarnings(row map[string]any) (map[string]any, []Warning, []Reject) {
	out := make(map[string]any, len(m.Fields))
	var warnings []Warning
	var rejects []Reject
	for _, f := range m.Fields {
		text, present, rej := f.text(row)
		if rej != nil {
			rejects = append(rejects, *rej)
			continue
		}
		if present && isNullMarker(f.Fn, text) {
			present = false
		}
		if !present {
			if f.Default != nil {
				text, present = *f.Default, true
			} else if f.Required {
				rejects = append(rejects, Reject{f.Target, CodeMapRequired, f.origin() + " is required"})
				continue
			} else {
				continue
			}
		} else if len(f.Enum) > 0 {
			mapped, ok := f.Enum[text]
			if !ok && f.EnumDefault != nil {
				warnings = append(warnings, Warning{f.Target, CodeMapEnumDefault, fmt.Sprintf("%s has no enum mapping; default %s used", f.origin(), *f.EnumDefault)})
				mapped, ok = *f.EnumDefault, true
			}
			if !ok {
				rejects = append(rejects, Reject{f.Target, CodeMapEnum, f.origin() + " has no enum mapping"})
				continue
			}
			text = mapped
		}
		v, w, rej := m.coerce(f, text, row)
		if rej != nil {
			rejects = append(rejects, *rej)
			continue
		}
		if w != nil {
			warnings = append(warnings, *w)
		}
		out[f.Target] = v
	}
	if len(rejects) == 0 {
		rejects = m.rejectConditions(out)
	}
	if len(rejects) > 0 {
		return nil, nil, rejects
	}
	return out, warnings, nil
}

func (m Mapping) location() *time.Location {
	if m.loc == nil {
		return time.UTC
	}
	return m.loc
}

func (m Mapping) coerce(f FieldRule, text string, row map[string]any) (any, *Warning, *Reject) {
	if f.Fn != "" {
		return m.coerceFn(f, text, row)
	}
	switch f.Type {
	case TypeString:
		return text, nil, nil
	case TypeInt:
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, nil, badValue(f, CodeMapCoerce, "is not an integer")
		}
		return n, nil, nil
	case TypeDecimal:
		return coerceDecimal(f, text)
	case TypeDate:
		t, err := time.Parse(dateLayout, text)
		if err != nil {
			t, err = time.Parse(time.RFC3339, text)
		}
		if err != nil {
			return nil, nil, badValue(f, CodeMapCoerce, "is not an ISO date")
		}
		return t.Format(dateLayout), nil, nil
	case TypeTimestamp:
		t, err := time.Parse(time.RFC3339, text)
		if err != nil {
			return nil, nil, badValue(f, CodeMapCoerce, "is not an RFC 3339 timestamp")
		}
		return t.UTC().Format(time.RFC3339), nil, nil
	case TypeBool:
		switch strings.ToLower(text) {
		case "true", "1", "y", "yes":
			return true, nil, nil
		case "false", "0", "n", "no":
			return false, nil, nil
		}
		return nil, nil, badValue(f, CodeMapCoerce, "is not a boolean")
	}
	return nil, nil, badValue(f, CodeMapInvalid, "has an unknown type")
}

func coerceDecimal(f FieldRule, text string) (any, *Warning, *Reject) {
	d, err := decimal.NewFromString(text)
	if err != nil {
		return nil, nil, badValue(f, CodeMapCoerce, "is not a decimal")
	}
	rangeWhy := "exceeds scale 6 or magnitude 1e14"
	if exp := d.Exponent(); exp > maxDecimalExponent || exp < -maxDecimalScaleGuard {
		return nil, nil, badValue(f, CodeMapRange, rangeWhy)
	}
	places := int32(maxDecimalScale)
	if f.Scale != nil {
		places = int32(*f.Scale)
	}
	r := d.RoundBank(places)
	if (f.Scale == nil && !r.Equal(d)) || r.Abs().GreaterThanOrEqual(maxDecimalMagnitude) {
		return nil, nil, badValue(f, CodeMapRange, rangeWhy)
	}
	if f.Scale == nil {
		return d.String(), nil, nil
	}
	var w *Warning
	if !r.Equal(d) {
		w = &Warning{f.Target, CodePrecisionLoss, fmt.Sprintf("%s rounded to scale %d, delta %s", f.origin(), places, d.Sub(r).Abs())}
	}
	return r.StringFixed(places), w, nil
}

func sourceText(v any) (text string, present, scalar bool) {
	var s string
	switch x := v.(type) {
	case nil:
		return "", false, true
	case string:
		s = x
	case int:
		s = strconv.Itoa(x)
	case int64:
		s = strconv.FormatInt(x, 10)
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		s = x.String()
	case bool:
		s = strconv.FormatBool(x)
	case time.Time:
		s = x.Format(dateLayout)
	default:
		return "", false, false
	}
	s = strings.TrimSpace(s)
	return s, s != "", true
}
