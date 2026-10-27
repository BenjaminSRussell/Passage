package domain_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

func lpData() map[string]string {
	return map[string]string{"lp": "LP-000123", "item": "WIDGET", "lot": "L42", "expiry": "2027-06-30", "sscc": "340123456789012340"}
}

func TestRender_LPLabelMatchesGoldenZPL(t *testing.T) {
	tpl, ok := domain.Template(domain.TemplateLP)
	if !ok {
		t.Fatal("LP_LABEL template missing")
	}
	got, err := domain.Render(tpl, lpData())
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../testdata/golden/lp-label.zpl")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("ZPL differs from golden:\n%s", got)
	}
	// AC-32.5: a GS1-128 SSCC barcode is FNC1 then AI 00 then the 18 digits.
	if !strings.Contains(got, "^BCN,100,Y,N,N^FD>;>800"+lpData()["sscc"]+"^FS") {
		t.Fatalf("SSCC barcode does not carry AI 00:\n%s", got)
	}
}

func TestRender_RejectsBadData(t *testing.T) {
	tpl, _ := domain.Template(domain.TemplateLP)
	cases := []struct {
		name  string
		field string
		value string
	}{
		{"zpl control character", "item", "^XA"},
		{"tilde", "item", "~JC"},
		{"empty", "lot", ""},
		{"control rune", "lot", "L\n42"},
		{"sscc with bad check digit", "sscc", "340123456789012341"},
		{"sscc too short", "sscc", "34012345678901234"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := lpData()
			data[c.field] = c.value
			_, err := domain.Render(tpl, data)
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != domain.CodeInvalid {
				t.Fatalf("want %s, got %v", domain.CodeInvalid, err)
			}
		})
	}
}

func TestRender_MissingFieldIsRejected(t *testing.T) {
	data := lpData()
	delete(data, "lot")
	tpl, _ := domain.Template(domain.TemplateLP)
	if _, err := domain.Render(tpl, data); err == nil || !strings.Contains(err.Error(), "lot") {
		t.Fatalf("missing field not reported: %v", err)
	}
}

func TestMod10Valid_GS1Vectors(t *testing.T) {
	// GS1 GTIN-14 example (check digit 7); GTIN-14 00614141123452 (check digit 2);
	// the same payload with a wrong check digit; SSCC with check digit 0; and malformed input.
	cases := []struct {
		in   string
		want bool
	}{
		{"09501101020917", true},
		{"00614141123452", true},
		{"00614141123453", false},
		{"340123456789012340", true},
		{"340123456789012341", false},
		{"", false},
		{"1", false},
		{"12a4", false},
	}
	for _, c := range cases {
		if got := domain.Mod10Valid(c.in); got != c.want {
			t.Errorf("Mod10Valid(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestTemplate_UnknownCode(t *testing.T) {
	if _, ok := domain.Template("NOPE"); ok {
		t.Fatal("unknown template code resolved")
	}
}
