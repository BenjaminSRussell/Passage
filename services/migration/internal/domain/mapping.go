package domain

import (
	"bytes"
	"encoding/json"
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
	TypeString  = "string"
	TypeInt     = "int"
	TypeDecimal = "decimal"
	TypeDate    = "date"
	TypeBool    = "bool"
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
	Source   string            `json:"source"`
	Target   string            `json:"target"`
	Type     string            `json:"type"`
	Enum     map[string]string `json:"enum,omitempty"`
	Default  *string           `json:"default,omitempty"`
	Required bool              `json:"required,omitempty"`
}

type Mapping struct {
	Entity  string      `json:"entity"`
	Version int         `json:"version"`
	Fields  []FieldRule `json:"fields"`
}

type Reject struct{ Field, Code, Detail string }

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
	targets := map[string]bool{}
	for _, f := range m.Fields {
		if f.Source == "" || f.Target == "" {
			return Mapping{}, &Error{CodeMapInvalid, "every field needs source and target"}
		}
		if targets[f.Target] {
			return Mapping{}, &Error{CodeMapInvalid, "duplicate target " + f.Target}
		}
		targets[f.Target] = true
		switch f.Type {
		case TypeString, TypeInt, TypeDecimal, TypeDate, TypeBool:
		default:
			return Mapping{}, &Error{CodeMapInvalid, "unknown type for target " + f.Target}
		}
		if f.Default != nil {
			if _, rej := coerce(f, *f.Default); rej != nil {
				return Mapping{}, &Error{CodeMapInvalid, "default for target " + f.Target + " does not satisfy its type"}
			}
		}
	}
	return m, nil
}

func (m Mapping) Apply(row map[string]any) (map[string]any, []Reject) {
	out := make(map[string]any, len(m.Fields))
	var rejects []Reject
	for _, f := range m.Fields {
		text, present, scalar := sourceText(row[f.Source])
		if !scalar {
			rejects = append(rejects, Reject{f.Target, CodeMapCoerce, f.Source + " is not a scalar"})
			continue
		}
		if !present {
			if f.Default != nil {
				text, present = *f.Default, true
			} else if f.Required {
				rejects = append(rejects, Reject{f.Target, CodeMapRequired, f.Source + " is required"})
				continue
			} else {
				continue
			}
		} else if len(f.Enum) > 0 {
			mapped, ok := f.Enum[text]
			if !ok {
				rejects = append(rejects, Reject{f.Target, CodeMapEnum, f.Source + " has no enum mapping"})
				continue
			}
			text = mapped
		}
		v, rej := coerce(f, text)
		if rej != nil {
			rejects = append(rejects, *rej)
			continue
		}
		out[f.Target] = v
	}
	if len(rejects) > 0 {
		return nil, rejects
	}
	return out, nil
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

func coerce(f FieldRule, text string) (any, *Reject) {
	bad := func(code, why string) (any, *Reject) {
		return nil, &Reject{f.Target, code, f.Source + " " + why}
	}
	switch f.Type {
	case TypeString:
		return text, nil
	case TypeInt:
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return bad(CodeMapCoerce, "is not an integer")
		}
		return n, nil
	case TypeDecimal:
		d, err := decimal.NewFromString(text)
		if err != nil {
			return bad(CodeMapCoerce, "is not a decimal")
		}
		if !d.Round(maxDecimalScale).Equal(d) || d.Abs().GreaterThanOrEqual(maxDecimalMagnitude) {
			return bad(CodeMapRange, "exceeds scale 6 or magnitude 1e14")
		}
		return d.String(), nil
	case TypeDate:
		t, err := time.Parse(dateLayout, text)
		if err != nil {
			t, err = time.Parse(time.RFC3339, text)
		}
		if err != nil {
			return bad(CodeMapCoerce, "is not an ISO date")
		}
		return t.Format(dateLayout), nil
	case TypeBool:
		switch strings.ToLower(text) {
		case "true", "1", "y", "yes":
			return true, nil
		case "false", "0", "n", "no":
			return false, nil
		}
		return bad(CodeMapCoerce, "is not a boolean")
	}
	return bad(CodeMapInvalid, "has an unknown type")
}
