package domain

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func readGolden(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/golden/" + name)
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	return string(raw)
}

// renderBuiltIn renders a built-in template with valid sample data.
func renderBuiltIn(t *testing.T, code string, data map[string]string) string {
	t.Helper()
	tpl, ok := Template(code)
	if !ok {
		t.Fatalf("template %s missing", code)
	}
	prepared, err := PrepareLabelData(code, data)
	if err != nil {
		t.Fatalf("prepare %s: %v", code, err)
	}
	out, err := Render(tpl, prepared)
	if err != nil {
		t.Fatalf("render %s: %v", code, err)
	}
	return out
}

func TestLabelDots_TableDriven(t *testing.T) {
	cases := []struct {
		name  string
		media string
		dpi   int
		w, h  int
		ok    bool
	}{
		{"4x6 at 203", Media4x6In, 203, 812, 1218, true},
		{"4x6 at 300", Media4x6In, 300, 1200, 1800, true},
		{"4x6 at 600", Media4x6In, 600, 2400, 3600, true},
		{"2x1 at 203", Media2x1In, 203, 406, 203, true},
		{"3x1 at 203", Media3x1In, 203, 609, 203, true},
		{"4x2 at 203", Media4x2In, 203, 812, 406, true},
		{"sheet media is not a label", MediaA4, 203, 0, 0, false},
		{"unsupported dpi", Media4x6In, 150, 0, 0, false},
		{"unknown media", "5X5_IN", 203, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, h, ok := LabelDots(c.media, c.dpi)
			if ok != c.ok || w != c.w || h != c.h {
				t.Fatalf("LabelDots(%s, %d) = %d, %d, %v; want %d, %d, %v", c.media, c.dpi, w, h, ok, c.w, c.h, c.ok)
			}
		})
	}
}

// The LP label sized for a 4 x 6 in device at 203 dpi keeps its layout and gains
// ^PW/^LL; at 300 dpi every dot value is scaled by 300/203 and ^BY sets the bar
// module so the label prints at the same physical size.
func TestSizeLabel_MatchesSizedGoldens(t *testing.T) {
	cases := []struct {
		dpi    int
		golden string
	}{
		{203, "lp-label-4x6-203.zpl"},
		{300, "lp-label-4x6-300.zpl"},
	}
	for _, c := range cases {
		got, err := SizeLabel(readGolden(t, "lp-label.zpl"), Media4x6In, c.dpi)
		if err != nil {
			t.Fatalf("SizeLabel at %d dpi: %v", c.dpi, err)
		}
		if want := readGolden(t, c.golden); got != want {
			t.Fatalf("label at %d dpi differs from %s\n got: %q\nwant: %q", c.dpi, c.golden, got, want)
		}
	}
}

// Which built-in labels fit which media (FR-32.13 media sizes): a field fits when
// its origin and its height (text, bars and the interpretation line) lie inside.
func TestSizeLabel_FitByTemplateAndMedia(t *testing.T) {
	lp := readGolden(t, "lp-label.zpl")
	location := renderBuiltIn(t, TemplateLocation, map[string]string{"location": "A-01-02"})
	carton := renderBuiltIn(t, TemplateCarton, map[string]string{"sscc": "340123456789012340", "shipToName": "ACME", "poNumber": "PO123", "shipToPostal": "10001"})
	cases := []struct {
		name  string
		zpl   string
		media string
		dpi   int
		fits  bool
	}{
		{"LP on 4x6", lp, Media4x6In, 203, true},
		{"LP on 4x6 at 600 dpi", lp, Media4x6In, 600, true},
		{"LP on 4x2: bars end at 350 of 406 dots", lp, Media4x2In, 203, true},
		{"LP on 4x2 at 300 dpi", lp, Media4x2In, 300, true},
		{"LP on 3x1: bar code below the label", lp, Media3x1In, 203, false},
		{"LP on 2x1 at 600 dpi is still 1 in long", lp, Media2x1In, 600, false},
		{"location on 4x2", location, Media4x2In, 203, true},
		{"location on 3x1: bars run past 1 in", location, Media3x1In, 203, false},
		{"carton on 4x6 (template sets its own ^PW/^LL)", carton, Media4x6In, 203, true},
		{"carton on 4x2", carton, Media4x2In, 203, true},
		{"carton on 2x1", carton, Media2x1In, 203, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := SizeLabel(c.zpl, c.media, c.dpi)
			if c.fits && err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if !c.fits {
				var de *Error
				if !errors.As(err, &de) || de.Code != CodeMediaMismatch {
					t.Fatalf("want %s, got %v", CodeMediaMismatch, err)
				}
			}
		})
	}
}

// A template that declares its own geometry (the carton label's ^PW812/^LL1218)
// prints with the device's geometry only, never two conflicting sets.
func TestSizeLabel_ReplacesTemplateGeometry(t *testing.T) {
	carton := renderBuiltIn(t, TemplateCarton, map[string]string{"sscc": "340123456789012340", "shipToName": "ACME", "poNumber": "PO123", "shipToPostal": "10001"})
	got, err := SizeLabel(carton, Media4x6In, 300)
	if err != nil {
		t.Fatalf("SizeLabel: %v", err)
	}
	if !strings.HasPrefix(got, "^XA\n^PW1200\n^LL1800\n^BY3\n") {
		t.Fatalf("carton label not sized for 4 x 6 in at 300 dpi: %q", got)
	}
	if strings.Count(got, "^PW") != 1 || strings.Count(got, "^LL") != 1 {
		t.Fatalf("template geometry left in the payload: %q", got)
	}
	if !strings.Contains(got, "^FO59,59^A0N,59,59^FDSHIP TO ACME^FS") {
		t.Fatalf("ship-to field not scaled to 300 dpi: %q", got)
	}
}

func TestSizeLabel_RejectsBadInput(t *testing.T) {
	zpl := readGolden(t, "lp-label.zpl")
	cases := []struct {
		name  string
		zpl   string
		media string
		dpi   int
		code  string
	}{
		{"sheet media", zpl, MediaA4, 203, CodeMediaMismatch},
		{"unsupported dpi", zpl, Media4x6In, 150, CodeMediaMismatch},
		{"not a ZPL document", "%PDF-1.4", Media4x6In, 203, CodeInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := SizeLabel(c.zpl, c.media, c.dpi)
			var de *Error
			if !errors.As(err, &de) || de.Code != c.code {
				t.Fatalf("want %s, got %v", c.code, err)
			}
		})
	}
}

func TestScaleDots_RoundsToNearestDot(t *testing.T) {
	cases := []struct{ n, dpi, want int }{
		{40, 203, 40}, {40, 300, 59}, {40, 600, 118}, {2, 300, 3}, {2, 600, 6}, {100, 300, 148}, {0, 600, 0},
	}
	for _, c := range cases {
		if got := scaleDots(c.n, c.dpi); got != c.want {
			t.Errorf("scaleDots(%d, %d) = %d, want %d", c.n, c.dpi, got, c.want)
		}
	}
}

func TestSizeForDevice_PassesThroughPDFAndNonZPLDevices(t *testing.T) {
	const payload = "%PDF-1.4 body"
	pdfPrinter := Printer{DeviceType: DevicePDFIPP, MediaSize: MediaA4, Dpi: 203}
	got, err := SizeForDevice(FormatPDF, pdfPrinter, payload)
	if err != nil || got != payload {
		t.Fatalf("PDF job must pass through unchanged: %q, %v", got, err)
	}
	zplOnPDFDevice, err := SizeForDevice(FormatZPL, pdfPrinter, payload)
	if err != nil || zplOnPDFDevice != payload {
		t.Fatalf("non-ZPL device must not size a ZPL payload: %q, %v", zplOnPDFDevice, err)
	}
	zplPrinter := Printer{DeviceType: DeviceZPL, MediaSize: Media4x6In, Dpi: 203}
	sized, err := SizeForDevice(FormatZPL, zplPrinter, readGolden(t, "lp-label.zpl"))
	if err != nil || !strings.HasPrefix(sized, "^XA\n^PW812\n^LL1218\n^CI28\n") {
		t.Fatalf("ZPL device must size the label: %q, %v", sized, err)
	}
}
