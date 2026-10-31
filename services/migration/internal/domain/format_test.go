package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

func withFormat(f string) func(*domain.FieldRule) {
	return func(r *domain.FieldRule) { r.Format = f }
}

func withFn(f string) func(*domain.FieldRule) {
	return func(r *domain.FieldRule) { r.Fn = f }
}

func TestApply_SourceFormats(t *testing.T) {
	cases := []struct {
		name     string
		typ      string
		format   string
		in       any
		want     any
		wantCode string
	}{
		{"dotted date", domain.TypeDate, domain.FmtDateDMYDot, "31.12.2024", "2024-12-31", ""},
		{"US slash date", domain.TypeDate, domain.FmtDateMDYSlash, "02/29/2024", "2024-02-29", ""},
		{"EU slash date", domain.TypeDate, domain.FmtDateDMYSlash, "29/02/2024", "2024-02-29", ""},
		{"compact date", domain.TypeDate, domain.FmtDateCompact, "20241231", "2024-12-31", ""},
		{"US date read as EU is impossible month", domain.TypeDate, domain.FmtDateDMYSlash, "02/29/2024", nil, domain.CodeMapCoerce},
		{"non leap day", domain.TypeDate, domain.FmtDateDMYDot, "29.02.2023", nil, domain.CodeMapCoerce},
		{"ISO text under dotted format", domain.TypeDate, domain.FmtDateDMYDot, "2024-12-31", nil, domain.CodeMapCoerce},
		{"dotted timestamp", domain.TypeTimestamp, domain.FmtTimeDMYDot, "31.12.2024 23:59:58", "2024-12-31T23:59:58Z", ""},
		{"timestamp without time", domain.TypeTimestamp, domain.FmtTimeDMYDot, "31.12.2024", nil, domain.CodeMapCoerce},
		{"decimal comma", domain.TypeDecimal, domain.FmtDecimalComma, "1.234.567,89", "1234567.89", ""},
		{"decimal comma no grouping", domain.TypeDecimal, domain.FmtDecimalComma, "12,5", "12.5", ""},
		{"decimal comma SAP trailing minus", domain.TypeDecimal, domain.FmtDecimalComma, "1.000,50-", "-1000.5", ""},
		{"decimal comma leading minus", domain.TypeDecimal, domain.FmtDecimalComma, "-3,25", "-3.25", ""},
		{"decimal comma bad grouping", domain.TypeDecimal, domain.FmtDecimalComma, "1.23,4", nil, domain.CodeMapCoerce},
		{"decimal comma rejects point text", domain.TypeDecimal, domain.FmtDecimalComma, "1234.56", nil, domain.CodeMapCoerce},
		{"decimal comma double minus", domain.TypeDecimal, domain.FmtDecimalComma, "-1,5-", nil, domain.CodeMapCoerce},
		{"decimal point grouped", domain.TypeDecimal, domain.FmtDecimalPoint, "1,234.50", "1234.5", ""},
		{"US slash timestamp", domain.TypeTimestamp, domain.FmtTimeMDYSlash, "12/31/2024 08:05:00", "2024-12-31T08:05:00Z", ""},
		{"single digit day rejected", domain.TypeDate, domain.FmtDateDMYDot, "1.12.2024", nil, domain.CodeMapCoerce},
		{"compact date from a JSON number", domain.TypeDate, domain.FmtDateCompact, json.Number("20241231"), "2024-12-31", ""},
		{"JSON number under decimal comma is not regrouped", domain.TypeDecimal, domain.FmtDecimalComma, json.Number("1.234"), "1.234", ""},
		{"float under decimal comma is its own value", domain.TypeDecimal, domain.FmtDecimalComma, 2.5, "2.5", ""},
		{"text that looks grouped under decimal comma", domain.TypeDecimal, domain.FmtDecimalComma, "1.234", "1234", ""},
		{"decimal point SAP trailing minus", domain.TypeDecimal, domain.FmtDecimalPoint, "2,000.25-", "-2000.25", ""},
		{"decimal point rejects comma decimal", domain.TypeDecimal, domain.FmtDecimalPoint, "1,5", nil, domain.CodeMapCoerce},
		{"decimal comma range still enforced", domain.TypeDecimal, domain.FmtDecimalComma, "0,1234567", nil, domain.CodeMapRange},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, rejects := rule(c.typ, withFormat(c.format)).Apply(map[string]any{"src": c.in})
			if c.wantCode != "" {
				if len(rejects) != 1 || rejects[0].Code != c.wantCode {
					t.Fatalf("rejects = %+v, want %s", rejects, c.wantCode)
				}
				return
			}
			if len(rejects) != 0 || out["dst"] != c.want {
				t.Fatalf("out = %v rejects = %+v, want %v", out, rejects, c.want)
			}
		})
	}
}

func TestApply_FormattedTimestampUsesSourceTimezone(t *testing.T) {
	pm, err := domain.ParseMapping([]byte(`{"entity":"e","version":1,"sourceTimezone":"Europe/Berlin","fields":[{"source":"src","target":"dst","type":"timestamp","format":"DD.MM.YYYY HH:mm:ss"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	out, rejects := pm.Apply(map[string]any{"src": "01.07.2024 12:00:00"})
	if len(rejects) != 0 || out["dst"] != "2024-07-01T10:00:00Z" {
		t.Fatalf("out = %v rejects = %+v", out, rejects)
	}
}

func TestParseMapping_FormatValidation(t *testing.T) {
	bad := []string{
		`{"source":"a","target":"t","type":"string","format":"DD.MM.YYYY"}`,
		`{"source":"a","target":"t","type":"date","format":"decimal_comma"}`,
		`{"source":"a","target":"t","type":"date","format":"YY-MM-DD"}`,
		`{"source":"a","target":"t","type":"date","fn":"sap_dats","format":"DD.MM.YYYY"}`,
		`{"source":"a","target":"t","type":"decimal","format":"DD.MM.YYYY"}`,
		`{"rule":"const","value":"2024-01-01","target":"t","type":"date","format":"DD.MM.YYYY"}`,
		`{"source":"a","target":"t","type":"date","default":"2024-01-01","format":"DD.MM.YYYY"}`,
	}
	for _, f := range bad {
		if _, err := domain.ParseMapping([]byte(`{"entity":"e","version":1,"fields":[` + f + `]}`)); err == nil {
			t.Errorf("accepted %s", f)
		}
	}
	good := `{"source":"a","target":"t","type":"date","format":"DD.MM.YYYY","default":"01.01.2024"}`
	if _, err := domain.ParseMapping([]byte(`{"entity":"e","version":1,"fields":[` + good + `]}`)); err != nil {
		t.Errorf("rejected default in the source format: %v", err)
	}
}

func TestApply_CodepageNormalize(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"smart punctuation", "Caf\u00e9 \u201cBar\u201d \u2013 O\u2019Neil", "Caf\u00e9 \"Bar\" - O'Neil"},
		{"nbsp and runs of spaces", "a\u00a0 \u00a0b\t\tc", "a b c"},
		{"control characters dropped", "ab\x00\x07c", "abc"},
		{"latin1 bytes repaired", "M\xfcller", "M\u00fcller"},
		{"windows-1252 quote and euro repaired, not dropped", "O\x92Neil \x80 5", "O'Neil \u20ac 5"},
		{"decomposed accent composed to NFC", "Mu\u0308ller", "M\u00fcller"},
		{"devanagari with joiner kept", "\u0915\u094d\u200d\u0937", "\u0915\u094d\u200d\u0937"},
		{"bom dropped", "\ufeffACME", "ACME"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, rejects := rule(domain.TypeString, withFn(domain.FnCodepageNormal)).Apply(map[string]any{"src": c.in})
			if len(rejects) != 0 || out["dst"] != c.want {
				t.Fatalf("out = %q rejects = %+v, want %q", out["dst"], rejects, c.want)
			}
		})
	}
	if _, err := domain.ParseMapping([]byte(`{"entity":"e","version":1,"fields":[{"source":"a","target":"t","type":"int","fn":"codepage_normalize"}]}`)); err == nil {
		t.Error("codepage_normalize accepted on an int")
	}
}

func TestApply_FormatAppliesToDefaultsAndTemplates(t *testing.T) {
	m, err := domain.ParseMapping([]byte(`{"entity":"e","version":1,"fields":[
		{"source":"a","target":"t","type":"decimal","format":"decimal_comma","default":"0,5"},
		{"source":"d","target":"d","type":"date","format":"DD.MM.YYYY"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	out, rejects := m.Apply(map[string]any{"a": "", "d": "01.02.2024"})
	if len(rejects) != 0 || out["t"] != "0.5" || out["d"] != "2024-02-01" {
		t.Fatalf("out = %v rejects = %+v", out, rejects)
	}
	iso := domain.DataTemplate{Name: "n", Entity: "e", Version: 1, Defaults: map[string]string{"d": "2024-02-01"}}
	if err := m.CheckTemplate(iso); err == nil {
		t.Error("template value in ISO accepted for a DD.MM.YYYY target")
	}
	dotted := domain.DataTemplate{Name: "n", Entity: "e", Version: 1, Defaults: map[string]string{"d": "01.02.2024"}}
	if err := m.CheckTemplate(dotted); err != nil {
		t.Errorf("template value in the source format rejected: %v", err)
	}
}
