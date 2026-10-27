package domain_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

// The GS1 general-specification example uses GTIN 09501101020917, which passes
// the mod-10 check. (The PRD example 09501234567893 fails it, so it is not used.)
const exampleGTIN = "09501101020917"

func itemData() map[string]string {
	return map[string]string{"gtin": exampleGTIN, "lot": "123ABC", "expiry": "261231", "serial": "SN0007"}
}

func cartonData() map[string]string {
	return map[string]string{"sscc": "340123456789012340", "shipToName": "ACME DC", "poNumber": "PO-7781", "shipToPostal": "60601"}
}

func TestPrepareLabelData_ItemComposesElementString(t *testing.T) {
	out, err := domain.PrepareLabelData(domain.TemplateItem, itemData())
	if err != nil {
		t.Fatal(err)
	}
	// AI 10 is variable-length and followed by AI 17, so FNC1 (>8) separates them.
	want := "0109501101020917" + "10123ABC" + ">8" + "17261231" + "21SN0007"
	if out["gs1"] != want {
		t.Fatalf("gs1 = %q, want %q", out["gs1"], want)
	}
}

func TestPrepareLabelData_CartonSeparatesVariableElements(t *testing.T) {
	out, err := domain.PrepareLabelData(domain.TemplateCarton, cartonData())
	if err != nil {
		t.Fatal(err)
	}
	want := "00340123456789012340" + "400PO-7781" + ">8" + "42060601"
	if out["gs1"] != want {
		t.Fatalf("gs1 = %q, want %q", out["gs1"], want)
	}
}

func TestPrepareLabelData_ReplacesCallerSuppliedGS1(t *testing.T) {
	data := itemData()
	data["gs1"] = "01>8^FD"
	out, err := domain.PrepareLabelData(domain.TemplateItem, data)
	if err != nil {
		t.Fatal(err)
	}
	if out["gs1"] == "01>8^FD" {
		t.Fatal("caller-supplied gs1 field was not replaced")
	}
}

func TestPrepareLabelData_RejectsBadInput(t *testing.T) {
	cases := []struct {
		name     string
		template string
		field    string
		value    string
	}{
		{"gtin check digit", domain.TemplateItem, "gtin", "09501101020918"},
		{"gtin not numeric", domain.TemplateItem, "gtin", "0950110102091A"},
		{"gtin missing", domain.TemplateItem, "gtin", ""},
		{"expiry month 13", domain.TemplateItem, "expiry", "261301"},
		{"expiry day past month end", domain.TemplateItem, "expiry", "250231"},
		{"lot too long", domain.TemplateItem, "lot", "123456789012345678901"},
		{"lot outside set 82", domain.TemplateItem, "lot", "AB^C"},
		{"lot contains FNC1 escape", domain.TemplateItem, "lot", "AB>8C"},
		{"lot contains a space", domain.TemplateItem, "lot", "AB C"},
		{"sscc check digit", domain.TemplateCarton, "sscc", "340123456789012341"},
		{"carton missing ship-to", domain.TemplateCarton, "shipToName", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := itemData()
			if c.template == domain.TemplateCarton {
				data = cartonData()
			}
			data[c.field] = c.value
			_, err := domain.PrepareLabelData(c.template, data)
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != domain.CodeInvalid {
				t.Fatalf("want %s, got %v", domain.CodeInvalid, err)
			}
		})
	}
}

func TestRender_ItemLabelMatchesGoldenZPL(t *testing.T) {
	data, err := domain.PrepareLabelData(domain.TemplateItem, itemData())
	if err != nil {
		t.Fatal(err)
	}
	tpl, ok := domain.Template(domain.TemplateItem)
	if !ok {
		t.Fatal("ITEM_LABEL template missing")
	}
	got, err := domain.Render(tpl, data)
	if err != nil {
		t.Fatal(err)
	}
	// Start code C (>;) and FNC1, digit pairs in subset C, a switch to subset B (>6) for
	// "3ABC" and "SN", and back to C (>5) for the expiry and serial digits.
	want := "^XA\n^CI28\n^FO40,40^A0N,40,40^FDGTIN " + exampleGTIN + "^FS\n^FO40,100^BCN,100,Y,N,N^FD>;>8" +
		"0109501101020917" + "1012" + ">6" + "3ABC" + ">8" + ">5" + "1726123121" + ">6" + "SN" + ">5" + "0007" + "^FS\n^XZ\n"
	if got != want {
		t.Fatalf("ZPL differs:\n got %q\nwant %q", got, want)
	}
}

func TestRender_CartonLabelCarriesSSCCAndGS1(t *testing.T) {
	data, err := domain.PrepareLabelData(domain.TemplateCarton, cartonData())
	if err != nil {
		t.Fatal(err)
	}
	tpl, _ := domain.Template(domain.TemplateCarton)
	got, err := domain.Render(tpl, data)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"SHIP TO ACME DC", "SSCC 340123456789012340", "^PW812", "^BCN,100,Y,N,N^FD>;>8" + "0034012345678901234040" + ">6" + "0PO-7781" + ">8" + ">5" + "42060601"} {
		if !strings.Contains(got, part) {
			t.Fatalf("carton ZPL missing %q:\n%s", part, got)
		}
	}
}

func TestParseGS1_Vectors(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
		want    map[string]string
	}{
		{"fixed and variable", "0109501101020917" + "10123ABC" + ">8" + "17261231" + "21SN0007", false,
			map[string]string{"01": exampleGTIN, "10": "123ABC", "17": "261231", "21": "SN0007"}},
		{"SSCC then PO and postal", "00340123456789012340400PO-7781>842060601", false,
			map[string]string{"00": "340123456789012340", "400": "PO-7781", "420": "60601"}},
		{"expiry day 00 means month end", "1725020010ABC", false,
			map[string]string{"17": "250200", "10": "ABC"}},
		{"GTIN check digit wrong", "0109501101020918", true, nil},
		{"unknown AI 99", "99ABC", true, nil},
		{"truncated GTIN", "010950110102", true, nil},
		{"invalid month", "17261301", true, nil},
		{"empty element after FNC1", "10ABC>8", true, nil},
		{"duplicate AI", "10ABC>810DEF", true, nil},
		{"single character", "1", true, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := domain.ParseGS1(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for k, v := range c.want {
				if got[k] != v {
					t.Fatalf("AI %s = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestRender_NumericItemLabelStartsInSubsetC(t *testing.T) {
	data, err := domain.PrepareLabelData(domain.TemplateItem, map[string]string{"gtin": exampleGTIN, "expiry": "261231"})
	if err != nil {
		t.Fatal(err)
	}
	tpl, _ := domain.Template(domain.TemplateItem)
	got, err := domain.Render(tpl, data)
	if err != nil {
		t.Fatal(err)
	}
	if want := "^BCN,100,Y,N,N^FD>;>8" + "0109501101020917" + "17261231" + "^FS"; !strings.Contains(got, want) {
		t.Fatalf("numeric GS1 label lacks %q:\n%s", want, got)
	}
}

func TestRender_RejectsAnElementStringThatDoesNotParse(t *testing.T) {
	tpl, _ := domain.Template(domain.TemplateItem)
	// Render without PrepareLabelData: the gs1 helper still refuses a bad element string.
	_, err := domain.Render(tpl, map[string]string{"gtin": exampleGTIN, "gs1": "0109501101020918"})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeInvalid {
		t.Fatalf("want %s, got %v", domain.CodeInvalid, err)
	}
}

func TestParseGS1_UnknownAIReportsPosition(t *testing.T) {
	_, err := domain.ParseGS1("0109501101020917" + "99XYZ")
	var de *domain.Error
	if !errors.As(err, &de) {
		t.Fatalf("want domain error, got %v", err)
	}
	if want := "position 16"; !strings.Contains(de.Detail, want) {
		t.Fatalf("detail %q lacks %q", de.Detail, want)
	}
}

func TestParseGS1_PositionCountsFNC1(t *testing.T) {
	// "10ABC" (5) + ">8" (2) puts the unknown AI at position 7.
	_, err := domain.ParseGS1("10ABC>899X")
	var de *domain.Error
	if !errors.As(err, &de) || !strings.Contains(de.Detail, "position 7") {
		t.Fatalf("want position 7, got %v", err)
	}
}

func TestPrepareLabelData_RoundTripProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		lot := rapid.StringMatching(`[A-Z0-9]{1,20}`).Draw(t, "lot")
		serial := rapid.StringMatching(`[A-Z0-9]{1,20}`).Draw(t, "serial")
		yy := rapid.IntRange(0, 99).Draw(t, "yy")
		mm := rapid.IntRange(1, 12).Draw(t, "mm")
		dd := rapid.IntRange(0, 28).Draw(t, "dd")
		expiry := fmt.Sprintf("%02d%02d%02d", yy, mm, dd)

		data := map[string]string{"gtin": exampleGTIN, "lot": lot, "expiry": expiry, "serial": serial}
		out, err := domain.PrepareLabelData(domain.TemplateItem, data)
		if err != nil {
			t.Fatalf("valid input rejected: %v", err)
		}
		got, err := domain.ParseGS1(out["gs1"])
		if err != nil {
			t.Fatalf("composed string does not parse: %v", err)
		}
		if got["01"] != exampleGTIN || got["10"] != lot || got["17"] != expiry || got["21"] != serial {
			t.Fatalf("round trip lost data: %v", got)
		}
	})
}
