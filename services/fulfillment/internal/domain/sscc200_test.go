package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sscc200 is testdata/sscc-200.json, written by the independent reference
// testdata/gen_sscc200.py (section 5.28.9 fixture "sscc-200").
type sscc200 struct {
	SSCC []struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		Input     string `json:"input"`
		Valid     bool   `json:"valid"`
		Error     string `json:"error"`
		Extension *int   `json:"extension"`
		Prefix    string `json:"prefix"`
		Serial    uint64 `json:"serial"`
	} `json:"sscc"`
	GS1 []struct {
		ID    string `json:"id"`
		Form  string `json:"form"`
		Rule  string `json:"rule"`
		Input string `json:"input"`
		Valid bool   `json:"valid"`
		Error string `json:"error"`
		Want  *struct {
			SSCC   string `json:"sscc"`
			GTIN   string `json:"gtin"`
			Batch  string `json:"batch"`
			Serial string `json:"serial"`
			Expiry string `json:"expiry"`
			Count  int    `json:"count"`
		} `json:"want"`
	} `json:"gs1"`
}

func loadSSCC200(t *testing.T) sscc200 {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/sscc-200.json")
	if err != nil {
		t.Fatal(err)
	}
	var f sscc200
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// labelBarcode returns the GS1-128 element string a ZPL label's Code 128 field
// encodes: the ^BC field data must start in code set C (">;") with FNC1 (">8")
// as the GS1-128 indicator and hold only digit pairs after it, so a printer
// encodes it in set C without a code-set switch.
var bcField = regexp.MustCompile(`\^BC[^\^]*\^FD([^\^]*)\^FS`)

func labelBarcode(t *testing.T, zpl string) string {
	t.Helper()
	m := bcField.FindStringSubmatch(zpl)
	if m == nil {
		t.Fatalf("no Code 128 field in label:\n%s", zpl)
	}
	data, ok := strings.CutPrefix(m[1], ">;>8")
	if !ok {
		t.Fatalf("barcode data %q does not start with code set C and FNC1", m[1])
	}
	if len(data)%2 != 0 || !allDigits(data) {
		t.Fatalf("barcode data %q after FNC1 is not digit pairs for code set C", data)
	}
	return data
}

// TestSSCC200_CheckDigitAndLabel is the SSCC half of the "SSCC / GS1-128" test
// plan line (200/200): every fixture string gets the verdict of the reference
// check digit (format or check-digit error), every valid one is rebuilt from
// its extension, prefix and serial where the fixture gives them, scans back
// from (00) in both forms and prints a label whose GS1-128 barcode decodes to
// the same SSCC; an invalid one is refused by the parser and the label.
func TestSSCC200_CheckDigitAndLabel(t *testing.T) {
	f := loadSSCC200(t)
	if len(f.SSCC) != 200 {
		t.Fatalf("fixture has %d SSCC cases, want 200", len(f.SSCC))
	}
	agree, valid, undetectable := 0, 0, 0
	for _, c := range f.SSCC {
		err := ValidSSCC(c.Input)
		switch {
		case c.Valid && err != nil:
			t.Errorf("%s %s %q: ValidSSCC = %v, reference says valid", c.ID, c.Kind, c.Input, err)
			continue
		case !c.Valid && c.Error == "format" && !errors.Is(err, ErrSSCCFormat):
			t.Errorf("%s %s %q: ValidSSCC = %v, want ErrSSCCFormat", c.ID, c.Kind, c.Input, err)
			continue
		case !c.Valid && c.Error == "check" && !errors.Is(err, ErrCheckDigit):
			t.Errorf("%s %s %q: ValidSSCC = %v, want ErrCheckDigit", c.ID, c.Kind, c.Input, err)
			continue
		}
		label, lerr := ContainerLabelZPL(c.Input, "SH-0000001", "PALLET")
		_, perr := ParseGS1("(00)" + c.Input)
		if !c.Valid {
			if lerr == nil || perr == nil {
				t.Errorf("%s %q: invalid SSCC accepted (label err %v, scan err %v)", c.ID, c.Input, lerr, perr)
				continue
			}
			agree++
			continue
		}
		valid++
		if c.Kind == "adjacent-transposition" {
			undetectable++
		}
		if c.Extension != nil {
			got, err := SSCC(*c.Extension, c.Prefix, c.Serial)
			if err != nil || got != c.Input {
				t.Errorf("%s: SSCC(%d, %s, %d) = %s, %v; want %s", c.ID, *c.Extension, c.Prefix, c.Serial, got, err, c.Input)
				continue
			}
		}
		if lerr != nil {
			t.Errorf("%s: label refused a valid SSCC: %v", c.ID, lerr)
			continue
		}
		element := labelBarcode(t, label)
		if element != "00"+c.Input {
			t.Errorf("%s: barcode element string %s, want 00%s", c.ID, element, c.Input)
			continue
		}
		for _, in := range []string{element, "(00)" + c.Input} {
			scan, err := ParseGS1(in)
			if err != nil || scan != (Scan{SSCC: c.Input}) {
				t.Errorf("%s: scan of %q = %+v, %v; want SSCC %s", c.ID, in, scan, err, c.Input)
			}
		}
		agree++
	}
	t.Logf("%d/200 SSCC cases agree with the reference (%d valid, %d invalid; %d adjacent transpositions keep a valid check digit, the mod-10 blind spot)",
		agree, valid, 200-valid, undetectable)
	if agree != 200 {
		t.Fatalf("%d/200 SSCC cases agree with the reference", agree)
	}
}

// TestGS1ElementStrings_Fixture is the GS1-128 half: every combination of the
// seven application identifiers (00, 01, 10, 17, 21, 30, 37; not 30 with 37,
// which fill the same field) round-trips in the bracketed and the raw FNC1 form,
// and each invalid element string is refused with the class of its GS1 rule
// (value, length/structure, unknown identifier).
func TestGS1ElementStrings_Fixture(t *testing.T) {
	f := loadSSCC200(t)
	classes := map[string]error{"value": ErrGS1Value, "parse": ErrGS1Parse, "unknown": ErrUnknownAI}
	roundTrips := 0
	for _, c := range f.GS1 {
		got, err := ParseGS1(c.Input)
		if !c.Valid {
			if want := classes[c.Error]; !errors.Is(err, want) {
				t.Errorf("%s %s %q: err = %v, want %v", c.ID, c.Rule, c.Input, err, want)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s %s %q: %v", c.ID, c.Form, c.Input, err)
			continue
		}
		w := c.Want
		want := Scan{SSCC: w.SSCC, GTIN: w.GTIN, Batch: w.Batch, Serial: w.Serial, Expiry: w.Expiry, Count: w.Count}
		if got != want {
			t.Errorf("%s %s %q: got %+v, want %+v", c.ID, c.Form, c.Input, got, want)
			continue
		}
		roundTrips++
	}
	if roundTrips < 190 {
		t.Fatalf("only %d element strings round-tripped; the fixture holds 95 combinations in two forms", roundTrips)
	}
}

// TestSSCC200_FixtureMatchesReference: the committed fixture is exactly what the
// independent reference writes, so no expected value is edited by hand or taken
// from the engine.
func TestSSCC200_FixtureMatchesReference(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	out := filepath.Join(t.TempDir(), "sscc-200.json")
	if msg, err := exec.Command(py, "-I", "../../testdata/gen_sscc200.py", out).CombinedOutput(); err != nil {
		t.Fatalf("reference generator failed: %v\n%s", err, msg)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../testdata/sscc-200.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("testdata/sscc-200.json differs from the reference output; regenerate it with python3 -I testdata/gen_sscc200.py testdata/sscc-200.json")
	}
}
