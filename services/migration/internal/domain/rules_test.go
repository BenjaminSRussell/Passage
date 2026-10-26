package domain_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

func fieldDoc(tz, field string) string {
	return fmt.Sprintf(`{"entity":"e","version":1,"sourceTimezone":%q,"fields":[%s]}`, tz, field)
}

func parseMapping(t testing.TB, doc string) domain.Mapping {
	t.Helper()
	m, err := domain.ParseMapping([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func parseCode(doc string) string {
	_, err := domain.ParseMapping([]byte(doc))
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	if err != nil {
		return "unclassified"
	}
	return ""
}

func TestParse_RejectsUnknownAndMalformedRules(t *testing.T) {
	cases := []struct {
		name, field, wantCode string
	}{
		{"unknown rule", `{"rule":"split","source":"a","target":"t","type":"string"}`, domain.CodeMapUnknownFn},
		{"unknown fn", `{"source":"a","target":"t","type":"string","fn":"lookup_x"}`, domain.CodeMapUnknownFn},
		{"one-source concat", `{"rule":"concat","sources":["a"],"target":"t","type":"string"}`, domain.CodeMapInvalid},
		{"const with source", `{"rule":"const","source":"a","value":"1","target":"t","type":"int"}`, domain.CodeMapInvalid},
		{"const value fails type", `{"rule":"const","value":"x","target":"t","type":"int"}`, domain.CodeMapInvalid},
		{"fn with wrong type", `{"source":"a","target":"t","type":"string","fn":"ax_noyes"}`, domain.CodeMapInvalid},
		{"sap_dats timestamp without timeSource", `{"source":"a","target":"t","type":"timestamp","fn":"sap_dats"}`, domain.CodeMapInvalid},
		{"lead zero width out of range", `{"source":"a","target":"t","type":"string","fn":"sap_lead_zero","width":41}`, domain.CodeMapInvalid},
		{"scale on string", `{"source":"a","target":"t","type":"string","scale":2}`, domain.CodeMapInvalid},
		{"scale above 6", `{"source":"a","target":"t","type":"decimal","scale":7}`, domain.CodeMapInvalid},
		{"enum default fails type", `{"source":"a","target":"t","type":"int","enum":{"A":"1"},"enumDefault":"x"}`, domain.CodeMapInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCode(fieldDoc("", tc.field)); got != tc.wantCode {
				t.Fatalf("got %q, want %q", got, tc.wantCode)
			}
		})
	}
	if got := parseCode(fieldDoc("Mars/Olympus", `{"source":"a","target":"t","type":"string"}`)); got != domain.CodeMapInvalid {
		t.Fatalf("bad timezone: got %q", got)
	}
}

func TestApply_RuleKindsAndFunctions(t *testing.T) {
	const (
		axDatetime = `{"source":"src","target":"dst","type":"timestamp","fn":"ax_datetime"}`
		sapDats    = `{"source":"src","target":"dst","type":"date","fn":"sap_dats"}`
		sapTims    = `{"source":"src","target":"dst","type":"timestamp","fn":"sap_dats","timeSource":"TIMS"}`
		leadZero   = `{"source":"src","target":"dst","type":"string","fn":"sap_lead_zero","width":10}`
		concat     = `{"rule":"concat","sources":["DataAreaId","AccountNum"],"separator":"|","target":"dst","type":"string"}`
	)
	cases := []struct {
		name     string
		tz       string
		field    string
		row      map[string]any
		want     any
		wantCode string
		omitted  bool
	}{
		{"concat joins trimmed parts", "", concat, map[string]any{"DataAreaId": " USMF ", "AccountNum": "US-00123"}, "USMF|US-00123", "", false},
		{"concat skips blank part", "", concat, map[string]any{"DataAreaId": " ", "AccountNum": "US-00123"}, "US-00123", "", false},
		{"concat all blank required", "", `{"rule":"concat","sources":["a","b"],"separator":"|","target":"dst","type":"string","required":true}`, map[string]any{"a": "", "b": nil}, nil, domain.CodeMapRequired, false},
		{"concat all blank omitted", "", `{"rule":"concat","sources":["a","b"],"separator":"|","target":"dst","type":"string"}`, map[string]any{"a": "", "b": nil}, nil, "", true},
		{"const needs no source", "", `{"rule":"const","value":"1","target":"dst","type":"int"}`, map[string]any{}, int64(1), "", false},
		{"ax_datetime in New York", "America/New_York", axDatetime, map[string]any{"src": "2026-03-08 01:30:00"}, "2026-03-08T06:30:00Z", "", false},
		{"ax_datetime T separator", "America/New_York", axDatetime, map[string]any{"src": "2026-03-08T01:30:00"}, "2026-03-08T06:30:00Z", "", false},
		{"ax_datetime null marker absent", "", axDatetime, map[string]any{"src": "1900-01-01 00:00:00"}, nil, "", true},
		{"ax_datetime null marker takes default", "", `{"source":"src","target":"dst","type":"timestamp","fn":"ax_datetime","default":"2000-01-01 00:00:00"}`, map[string]any{"src": "1900-01-01"}, "2000-01-01T00:00:00Z", "", false},
		{"ax_noyes Yes", "", `{"source":"src","target":"dst","type":"bool","fn":"ax_noyes"}`, map[string]any{"src": "Yes"}, true, "", false},
		{"ax_noyes 1", "", `{"source":"src","target":"dst","type":"bool","fn":"ax_noyes"}`, map[string]any{"src": "1"}, true, "", false},
		{"ax_noyes no", "", `{"source":"src","target":"dst","type":"bool","fn":"ax_noyes"}`, map[string]any{"src": "no"}, false, "", false},
		{"ax_noyes 2 rejects", "", `{"source":"src","target":"dst","type":"bool","fn":"ax_noyes"}`, map[string]any{"src": "2"}, nil, domain.CodeMapCoerce, false},
		{"sap_dats date", "", sapDats, map[string]any{"src": "20261025"}, "2026-10-25", "", false},
		{"sap_dats zero marker absent", "", sapDats, map[string]any{"src": "00000000"}, nil, "", true},
		{"sap_dats open-ended marker absent", "", sapDats, map[string]any{"src": "99991231"}, nil, "", true},
		{"sap_dats month 13 rejects", "", sapDats, map[string]any{"src": "20261325"}, nil, domain.CodeMapCoerce, false},
		{"sap_dats with TIMS in Berlin after DST change", "Europe/Berlin", sapTims, map[string]any{"src": "20261025", "TIMS": "143000"}, "2026-10-25T13:30:00Z", "", false},
		{"sap_lead_zero pads", "", leadZero, map[string]any{"src": "100123"}, "0000100123", "", false},
		{"sap_lead_zero keeps alpha", "", leadZero, map[string]any{"src": "AB-1"}, "AB-1", "", false},
		{"sap_lead_zero too long rejects", "", leadZero, map[string]any{"src": "12345678901"}, nil, domain.CodeMapCoerce, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := parseMapping(t, fieldDoc(tc.tz, tc.field))
			out, rejects := m.Apply(tc.row)
			if tc.wantCode != "" {
				if len(rejects) != 1 || rejects[0].Code != tc.wantCode {
					t.Fatalf("got rejects %+v, want %s", rejects, tc.wantCode)
				}
				for _, v := range tc.row {
					if s, ok := v.(string); ok && len(s) >= 4 && strings.Contains(rejects[0].Detail, s) {
						t.Fatalf("reject detail echoes a source value: %q", rejects[0].Detail)
					}
				}
				return
			}
			if len(rejects) > 0 {
				t.Fatalf("unexpected rejects %+v", rejects)
			}
			got, present := out["dst"]
			if tc.omitted {
				if present {
					t.Fatalf("expected omitted, got %v", got)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("got %v (%T), want %v (%T)", got, got, tc.want, tc.want)
			}
		})
	}
}

func TestSapLeadZero_PropertyPadsToWidth(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		width := rapid.IntRange(1, 40).Draw(t, "width")
		digits := rapid.StringMatching(`[0-9]{1,40}`).Draw(t, "digits")
		field := fmt.Sprintf(`{"source":"src","target":"dst","type":"string","fn":"sap_lead_zero","width":%d}`, width)
		m, err := domain.ParseMapping([]byte(fieldDoc("", field)))
		if err != nil {
			t.Fatal(err)
		}
		out, rejects := m.Apply(map[string]any{"src": digits})
		if len(digits) > width {
			if len(rejects) != 1 || rejects[0].Code != domain.CodeMapCoerce {
				t.Fatalf("width %d digits %s: rejects %+v", width, digits, rejects)
			}
			return
		}
		got, _ := out["dst"].(string)
		if len(got) != width || !strings.HasSuffix(got, digits) {
			t.Fatalf("width %d digits %s: got %q", width, digits, got)
		}
	})
}

func TestDecimalScale_PropertyHalfEvenWithinHalfUnit(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		scale := rapid.IntRange(0, 6).Draw(t, "scale")
		micro := rapid.Int64Range(0, 1e12).Draw(t, "micro")
		text := fmt.Sprintf("%d.%06d", micro/1e6, micro%1e6)
		field := fmt.Sprintf(`{"source":"src","target":"dst","type":"decimal","scale":%d}`, scale)
		m, err := domain.ParseMapping([]byte(fieldDoc("", field)))
		if err != nil {
			t.Fatal(err)
		}
		out, _, rejects := m.ApplyWithWarnings(map[string]any{"src": text})
		if len(rejects) > 0 {
			t.Fatalf("scale %d value %s rejected: %+v", scale, text, rejects)
		}
		got := decimal.RequireFromString(out["dst"].(string))
		delta := got.Sub(decimal.RequireFromString(text)).Abs()
		if delta.GreaterThan(decimal.New(5, int32(-scale-1))) {
			t.Fatalf("scale %d value %s became %s, moved by %s", scale, text, got, delta)
		}
	})
}

func TestDecimalScale_HalfEvenWarningsAndMagnitude(t *testing.T) {
	cases := []struct {
		name, field, src string
		want             string
		wantWarning      bool
		wantCode         string
		detailHas        string
	}{
		{"half-even rounds down with delta", `{"source":"src","target":"dst","type":"decimal","scale":2}`, "1.005", "1.00", true, "", "0.005"},
		{"half-even rounds to even", `{"source":"src","target":"dst","type":"decimal","scale":1}`, "1.25", "1.2", true, "", "0.05"},
		{"exact value no warning", `{"source":"src","target":"dst","type":"decimal","scale":2}`, "1.50", "1.50", false, "", ""},
		{"no scale rejects scale 7", `{"source":"src","target":"dst","type":"decimal"}`, "0.1234567", "", false, domain.CodeMapRange, ""},
		{"magnitude checked after rounding", `{"source":"src","target":"dst","type":"decimal","scale":0}`, "99999999999999.6", "", false, domain.CodeMapRange, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := parseMapping(t, fieldDoc("", tc.field))
			out, warnings, rejects := m.ApplyWithWarnings(map[string]any{"src": tc.src})
			if tc.wantCode != "" {
				if len(rejects) != 1 || rejects[0].Code != tc.wantCode {
					t.Fatalf("got rejects %+v", rejects)
				}
				return
			}
			if len(rejects) > 0 || out["dst"] != tc.want {
				t.Fatalf("got %v rejects %+v", out["dst"], rejects)
			}
			if !tc.wantWarning {
				if len(warnings) != 0 {
					t.Fatalf("unexpected warnings %+v", warnings)
				}
				return
			}
			if len(warnings) != 1 || warnings[0].Code != domain.CodePrecisionLoss || warnings[0].Field != "dst" || !strings.Contains(warnings[0].Detail, tc.detailHas) {
				t.Fatalf("got warnings %+v", warnings)
			}
		})
	}
}

func TestEnumDefault_MapsAndWarns(t *testing.T) {
	field := `{"source":"st","target":"status","type":"string","enum":{"A":"active"},"enumDefault":"unknown"}`
	m := parseMapping(t, fieldDoc("", field))
	out, warnings, rejects := m.ApplyWithWarnings(map[string]any{"st": "Blocked"})
	if len(rejects) != 0 || out["status"] != "unknown" {
		t.Fatalf("got %v rejects %+v", out, rejects)
	}
	if len(warnings) != 1 || warnings[0].Code != domain.CodeMapEnumDefault || !strings.Contains(warnings[0].Detail, "unknown") {
		t.Fatalf("got warnings %+v", warnings)
	}
	plain := parseMapping(t, fieldDoc("", `{"source":"st","target":"status","type":"string","enum":{"A":"active"}}`))
	_, rejects = plain.Apply(map[string]any{"st": "Blocked"})
	if len(rejects) != 1 || rejects[0].Code != domain.CodeMapEnum {
		t.Fatalf("without enumDefault got %+v", rejects)
	}
}

func TestRunTests_ReportsFailingCases(t *testing.T) {
	doc := `{"entity":"e","version":1,"fields":[{"source":"GRP","target":"seg","type":"string","enum":{"10":"retail"}}],
	"tests":[
		{"name":"maps","input":{"GRP":"10"},"expect":{"seg":"retail"}},
		{"name":"wrong output","input":{"GRP":"10"},"expect":{"seg":"wholesale"}},
		{"name":"expects reject but maps","input":{"GRP":"10"},"expectRejects":["MIG-MAP-ENUM"]},
		{"name":"expected reject","input":{"GRP":"99"},"expectRejects":["MIG-MAP-ENUM"]}
	]}`
	failed := parseMapping(t, doc).RunTests()
	names := make([]string, 0, len(failed))
	for _, f := range failed {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "wrong output,expects reject but maps" {
		t.Fatalf("failed cases %v", names)
	}
}

func TestParse_RejectsBadTestCases(t *testing.T) {
	field := `{"source":"a","target":"t","type":"string"}`
	dup := fmt.Sprintf(`{"entity":"e","version":1,"fields":[%s],"tests":[{"name":"x","input":{},"expect":{}},{"name":"x","input":{},"expect":{}}]}`, field)
	both := fmt.Sprintf(`{"entity":"e","version":1,"fields":[%s],"tests":[{"name":"y","input":{},"expect":{},"expectRejects":[]}]}`, field)
	neither := fmt.Sprintf(`{"entity":"e","version":1,"fields":[%s],"tests":[{"name":"z","input":{}}]}`, field)
	for name, doc := range map[string]string{"duplicate names": dup, "both expects": both, "neither expects": neither} {
		if got := parseCode(doc); got != domain.CodeMapInvalid {
			t.Fatalf("%s: got %q", name, got)
		}
	}
}
