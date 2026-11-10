package gs1

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

type vector struct {
	ID         string            `json:"id"`
	Origin     string            `json:"origin"`
	Data       string            `json:"data"`
	OK         bool              `json:"ok"`
	Fields     map[string]string `json:"fields"`
	Order      []string          `json:"order"`
	FirstError *string           `json:"firstError"`
}

type suite struct {
	ReferenceDate string                            `json:"referenceDate"`
	Prefixes      []string                          `json:"symbologyPrefixes"`
	Counts        struct{ GS1, Passage, Total int } `json:"counts"`
	Vectors       []vector                          `json:"vectors"`
}

func loadSuite(t *testing.T) suite {
	t.Helper()
	b, err := os.ReadFile("../../../../apps/web/src/modules/mobile-flows/domain/testdata/gs1-ai-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var s suite
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAC321VectorsMatchWithEverySymbologyPrefix(t *testing.T) {
	s := loadSuite(t)
	if len(s.Vectors) != s.Counts.Total || s.Counts.Passage != 60 {
		t.Fatalf("fixture holds %d vectors, counts say %+v", len(s.Vectors), s.Counts)
	}
	now, err := time.Parse("2006-01-02", s.ReferenceDate)
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range s.Prefixes {
		var bad []string
		for _, v := range s.Vectors {
			got := Parse(prefix+v.Data, Options{Now: now})
			var ok bool
			if v.OK {
				ok = got.OK && reflect.DeepEqual(got.Fields, v.Fields) && reflect.DeepEqual(got.Order, v.Order)
			} else {
				ok = !got.OK && v.FirstError != nil && got.Errors[0].Code == *v.FirstError
			}
			if !ok {
				bad = append(bad, v.ID+" "+v.Data)
			}
		}
		if len(bad) > 0 {
			t.Errorf("prefix %q: %d mismatches, first: %v", prefix, len(bad), bad[:min(5, len(bad))])
		}
	}
}

func TestParseFullAITable(t *testing.T) {
	now := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name, in string
		want     map[string]string
	}{
		{"gtin batch expiry serial", "01095011015300031726013110BATCH1\x1d21SER9", map[string]string{"01": "09501101530003", "17": "2026-01-31", "10": "BATCH1", "21": "SER9"}},
		{"sscc", "00106141411234567897", map[string]string{"00": "106141411234567897"}},
		{"count", "3712", map[string]string{"37": "12"}},
		{"net weight implied decimal", "3102012345", map[string]string{"3102": "123.45"}},
		{"expiry day zero is month end", "17260200", map[string]string{"17": "2026-02-28"}},
		{"production date", "11260115", map[string]string{"11": "2026-01-15"}},
	}
	for _, c := range cases {
		got := Parse(c.in, Options{Now: now})
		if !got.OK || !reflect.DeepEqual(got.Fields, c.want) {
			t.Errorf("%s: ok=%v fields=%v errors=%v", c.name, got.OK, got.Fields, got.Errors)
		}
	}
}

func TestParseRejects(t *testing.T) {
	now := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	cases := []struct{ name, in, code string }{
		{"bad gtin check digit", "0109501101530004", CodeCheckDigit},
		{"bad sscc check digit", "00106141411234567890", CodeCheckDigit},
		{"month 13", "17261301", CodeBadDate},
		{"feb 30", "17260230", CodeBadDate},
		{"unknown AI", "99", CodeMalformed},
		{"empty", "]C1", CodeMalformed},
		{"charset", "10AB\x1fC", CodeMalformed},
		{"short fixed", "01095011015300", CodeMalformed},
		{"conflicting repeat", "10A\x1d10B", CodeMalformed},
	}
	for _, c := range cases {
		got := Parse(c.in, Options{Now: now})
		if got.OK || len(got.Errors) == 0 || got.Errors[0].Code != c.code {
			t.Errorf("%s: ok=%v errors=%v want %s", c.name, got.OK, got.Errors, c.code)
		}
	}
}

func TestParseFNC1Substitute(t *testing.T) {
	got := Parse("10LOT1|21SER", Options{FNC1Substitute: "|"})
	if !got.OK || got.Fields["10"] != "LOT1" || got.Fields["21"] != "SER" {
		t.Fatalf("substitute not honoured: %+v", got)
	}
}

func TestCenturyWindow(t *testing.T) {
	cases := []struct{ yy, ref, want int }{{26, 2026, 2026}, {75, 2026, 2075}, {77, 2026, 1977}, {76, 2026, 2076}, {0, 2026, 2000}}
	for _, c := range cases {
		if got := ExpandYear(c.yy, c.ref); got != c.want {
			t.Errorf("ExpandYear(%d,%d)=%d want %d", c.yy, c.ref, got, c.want)
		}
	}
}

func TestClassifyAndCheck(t *testing.T) {
	gtin := Classify("]C10109501101530003", ClassifyOptions{})
	if gtin.Kind != "gs1" || Check(ClassGTIN, gtin) != nil {
		t.Fatalf("gtin scan: %+v", gtin)
	}
	if v := Check(ClassLP, gtin); v == nil || v.Code != CodeNoMatch || v.Message != "Expected LP, scanned GTIN." {
		t.Fatalf("mismatch verdict: %+v", v)
	}
	loc := Classify("A-01-02", ClassifyOptions{})
	if loc.Kind != "plain" || Check(ClassLocation, loc) != nil {
		t.Fatalf("location scan: %+v", loc)
	}
	if v := Check(ClassLocation, Classify("]C10109501101530004", ClassifyOptions{})); v == nil || v.Code != CodeCheckDigit {
		t.Fatalf("parse failure must outrank mismatch: %+v", v)
	}
	if c := Classify("0109501101530003", ClassifyOptions{RequireSymbologyID: true, ExpectGS1: true}); c.Kind != "plain" {
		t.Fatalf("require symbology id must keep an unprefixed scan plain: %+v", c)
	}
	if c := Classify("0109501101530003", ClassifyOptions{ExpectGS1: true}); c.Kind != "gs1" {
		t.Fatalf("expected gs1: %+v", c)
	}
	if c := Classify("]E04006381333931", ClassifyOptions{}); c.Kind != "plain" || c.Value != "4006381333931" {
		t.Fatalf("non-GS1 symbology must strip its prefix: %+v", c)
	}
	if Check(ClassGTIN, Classify("4006381333931", ClassifyOptions{})) != nil {
		t.Fatal("plain EAN-13 must satisfy a GTIN step")
	}
}
