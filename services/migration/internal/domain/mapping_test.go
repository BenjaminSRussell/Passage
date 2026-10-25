package domain_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

func rule(typ string, mods ...func(*domain.FieldRule)) domain.Mapping {
	r := domain.FieldRule{Source: "src", Target: "dst", Type: typ}
	for _, m := range mods {
		m(&r)
	}
	return domain.Mapping{Entity: "e", Version: 1, Fields: []domain.FieldRule{r}}
}

func required(r *domain.FieldRule) { r.Required = true }

func withDefault(s string) func(*domain.FieldRule) {
	return func(r *domain.FieldRule) { r.Default = &s }
}

func withEnum(r *domain.FieldRule) { r.Enum = map[string]string{"A": "active", "I": "inactive"} }

func TestApply_Rules(t *testing.T) {
	cases := []struct {
		name     string
		mapping  domain.Mapping
		row      map[string]any
		want     any
		wantCode string
		omitted  bool
	}{
		{"string trimmed", rule(domain.TypeString), map[string]any{"src": "  abc "}, "abc", "", false},
		{"int from string", rule(domain.TypeInt), map[string]any{"src": "42"}, int64(42), "", false},
		{"int from whole float", rule(domain.TypeInt), map[string]any{"src": 3.0}, int64(3), "", false},
		{"int from fractional float", rule(domain.TypeInt), map[string]any{"src": 3.5}, nil, domain.CodeMapCoerce, false},
		{"int from text", rule(domain.TypeInt), map[string]any{"src": "abc"}, nil, domain.CodeMapCoerce, false},
		{"decimal exact", rule(domain.TypeDecimal), map[string]any{"src": "1234.560000"}, "1234.56", "", false},
		{"decimal scale 7 rejected", rule(domain.TypeDecimal), map[string]any{"src": "0.1234567"}, nil, domain.CodeMapRange, false},
		{"decimal 1e14 rejected", rule(domain.TypeDecimal), map[string]any{"src": "100000000000000"}, nil, domain.CodeMapRange, false},
		{"decimal negative 1e14 rejected", rule(domain.TypeDecimal), map[string]any{"src": "-1e14"}, nil, domain.CodeMapRange, false},
		{"decimal just under limit", rule(domain.TypeDecimal), map[string]any{"src": "99999999999999.999999"}, "99999999999999.999999", "", false},
		{"decimal huge positive exponent rejected", rule(domain.TypeDecimal), map[string]any{"src": "1e9999999"}, nil, domain.CodeMapRange, false},
		{"decimal huge negative exponent rejected", rule(domain.TypeDecimal), map[string]any{"src": "1e-9999999"}, nil, domain.CodeMapRange, false},
		{"decimal NaN", rule(domain.TypeDecimal), map[string]any{"src": "NaN"}, nil, domain.CodeMapCoerce, false},
		{"decimal object", rule(domain.TypeDecimal), map[string]any{"src": map[string]any{"a": 1}}, nil, domain.CodeMapCoerce, false},
		{"date iso", rule(domain.TypeDate), map[string]any{"src": "2024-02-29"}, "2024-02-29", "", false},
		{"date rfc3339", rule(domain.TypeDate), map[string]any{"src": "2024-02-29T10:00:00Z"}, "2024-02-29", "", false},
		{"date future accepted", rule(domain.TypeDate), map[string]any{"src": "2999-12-31"}, "2999-12-31", "", false},
		{"date garbage", rule(domain.TypeDate), map[string]any{"src": "29/02/2024"}, nil, domain.CodeMapCoerce, false},
		{"bool Y", rule(domain.TypeBool), map[string]any{"src": "Y"}, true, "", false},
		{"bool 0", rule(domain.TypeBool), map[string]any{"src": 0}, false, "", false},
		{"bool maybe", rule(domain.TypeBool), map[string]any{"src": "maybe"}, nil, domain.CodeMapCoerce, false},
		{"enum mapped", rule(domain.TypeString, withEnum), map[string]any{"src": "A"}, "active", "", false},
		{"enum unmapped", rule(domain.TypeString, withEnum), map[string]any{"src": "Z"}, nil, domain.CodeMapEnum, false},
		{"default fills absent", rule(domain.TypeString, withDefault("n/a")), map[string]any{}, "n/a", "", false},
		{"default fills nil", rule(domain.TypeString, withDefault("n/a")), map[string]any{"src": nil}, "n/a", "", false},
		{"default fills blank", rule(domain.TypeInt, withDefault("7")), map[string]any{"src": "   "}, int64(7), "", false},
		{"default not used when present", rule(domain.TypeString, withDefault("n/a")), map[string]any{"src": "x"}, "x", "", false},
		{"required absent", rule(domain.TypeString, required), map[string]any{}, nil, domain.CodeMapRequired, false},
		{"required blank", rule(domain.TypeString, required), map[string]any{"src": ""}, nil, domain.CodeMapRequired, false},
		{"optional absent omitted", rule(domain.TypeString), map[string]any{}, nil, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, rejects := c.mapping.Apply(c.row)
			if c.wantCode != "" {
				if len(rejects) != 1 || rejects[0].Code != c.wantCode || rejects[0].Field != "dst" {
					t.Fatalf("rejects = %+v, want one %s", rejects, c.wantCode)
				}
				if out != nil {
					t.Fatalf("out = %v, want nil on reject", out)
				}
				return
			}
			if len(rejects) != 0 {
				t.Fatalf("unexpected rejects %+v", rejects)
			}
			got, ok := out["dst"]
			if c.omitted {
				if ok {
					t.Fatalf("dst present: %v", got)
				}
				return
			}
			if got != c.want {
				t.Fatalf("dst = %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestApply_RejectDetailNeverEchoesValue(t *testing.T) {
	const secret = "SENSITIVE-9921"
	_, rejects := rule(domain.TypeInt).Apply(map[string]any{"src": secret})
	if len(rejects) != 1 {
		t.Fatalf("rejects = %+v", rejects)
	}
	if got := rejects[0].Detail + rejects[0].Code + rejects[0].Field; strings.Contains(got, secret) {
		t.Fatalf("reject leaks value: %s", got)
	}
}

func TestApply_ReportsEveryFailingField(t *testing.T) {
	m := customerMapping(t)
	_, rejects := m.Apply(map[string]any{"CustGroup": "99", "CreditMax": "x", "Blocked": "maybe", "CreatedDate": "nope"})
	codes := map[string]string{}
	for _, r := range rejects {
		codes[r.Field] = r.Code
	}
	want := map[string]string{
		"accountNumber": domain.CodeMapRequired, "name": domain.CodeMapRequired, "segment": domain.CodeMapEnum,
		"creditLimit": domain.CodeMapCoerce, "blocked": domain.CodeMapCoerce, "createdOn": domain.CodeMapCoerce,
	}
	for f, c := range want {
		if codes[f] != c {
			t.Errorf("field %s code = %q, want %q (all: %v)", f, codes[f], c, codes)
		}
	}
}

func TestParseMapping_Invalid(t *testing.T) {
	cases := map[string]string{
		"unknown field":   `{"entity":"e","version":1,"fields":[{"source":"a","target":"b","type":"string","bogus":1}]}`,
		"wrong typed":     `{"entity":"e","version":"1","fields":[]}`,
		"unknown type":    `{"entity":"e","version":1,"fields":[{"source":"a","target":"b","type":"uuid"}]}`,
		"no fields":       `{"entity":"e","version":1,"fields":[]}`,
		"duplicate":       `{"entity":"e","version":1,"fields":[{"source":"a","target":"b","type":"string"},{"source":"c","target":"b","type":"string"}]}`,
		"bad default":     `{"entity":"e","version":1,"fields":[{"source":"a","target":"b","type":"int","default":"x"}]}`,
		"missing target":  `{"entity":"e","version":1,"fields":[{"source":"a","type":"string"}]}`,
		"not json":        `entity: e`,
		"version missing": `{"entity":"e","fields":[{"source":"a","target":"b","type":"string"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := domain.ParseMapping([]byte(body))
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != domain.CodeMapInvalid {
				t.Fatalf("err = %v, want %s", err, domain.CodeMapInvalid)
			}
		})
	}
}

func TestApply_PropertyDecimalNeverRoundedAndNeverPanics(t *testing.T) {
	m := rule(domain.TypeDecimal)
	rapid.Check(t, func(t *rapid.T) {
		whole := rapid.Int64Range(0, 99999999999999).Draw(t, "whole")
		digits := rapid.IntRange(1, 12).Draw(t, "digits")
		frac := rapid.StringMatching(`[0-9]{1,12}`).Draw(t, "frac")
		frac = frac[:min(digits, len(frac))]
		text := strconv.FormatInt(whole, 10) + "." + frac
		out, rejects := m.Apply(map[string]any{"src": text})
		trimmed := frac
		for len(trimmed) > 0 && trimmed[len(trimmed)-1] == '0' {
			trimmed = trimmed[:len(trimmed)-1]
		}
		if len(trimmed) > 6 {
			if len(rejects) != 1 || rejects[0].Code != domain.CodeMapRange {
				t.Fatalf("%s: scale > 6 must be a range reject, got %+v", text, rejects)
			}
			return
		}
		if len(rejects) != 0 {
			t.Fatalf("%s: unexpected rejects %+v", text, rejects)
		}
		if _, ok := out["dst"].(string); !ok {
			t.Fatalf("%s: decimal output must be a string, got %T", text, out["dst"])
		}
	})
}
