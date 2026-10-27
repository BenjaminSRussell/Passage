package domain_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

var oneInchMedia = []string{domain.Media2x1In, domain.Media3x1In}

// compactSamples is valid template data for every compact layout. The item label
// carries only its GTIN: with lot, expiry and serial its bar code is wider than 2 in.
func compactSamples(t *testing.T) map[string]map[string]string {
	t.Helper()
	item, err := domain.PrepareLabelData(domain.TemplateItem, map[string]string{"gtin": exampleGTIN})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]map[string]string{
		domain.TemplateLP:       lpData(),
		domain.TemplateLocation: {"location": "A-01-02"},
		domain.TemplateItem:     item,
	}
}

func renderCompact(t *testing.T, code, media string, data map[string]string) string {
	t.Helper()
	tpl, ok := domain.CompactLayout(code, media)
	if !ok {
		t.Fatalf("%s has no layout for %s", code, media)
	}
	zpl, err := domain.Render(tpl, data)
	if err != nil {
		t.Fatalf("render %s for %s: %v", code, media, err)
	}
	return zpl
}

// FR-32.7: the LP, location and item labels have a 1 in layout that fits 2 x 1
// and 3 x 1 in media at every supported dpi, bar code width and quiet zones included.
func TestCompactLayout_FitsOneInchMediaAtEveryDpi(t *testing.T) {
	for code, data := range compactSamples(t) {
		for _, media := range oneInchMedia {
			zpl := renderCompact(t, code, media, data)
			for _, dpi := range []int{203, 300, 600} {
				if _, err := domain.SizeLabel(zpl, media, dpi); err != nil {
					t.Errorf("%s on %s at %d dpi: %v", code, media, dpi, err)
				}
			}
		}
	}
}

// The compact layouts exist because the standard ones do not fit 1 in media.
func TestCompactLayout_StandardLayoutsDoNotFitOneInchMedia(t *testing.T) {
	for code, data := range compactSamples(t) {
		tpl, _ := domain.Template(code)
		zpl, err := domain.Render(tpl, data)
		if err != nil {
			t.Fatal(err)
		}
		for _, media := range oneInchMedia {
			var de *domain.Error
			if _, err := domain.SizeLabel(zpl, media, 203); !errors.As(err, &de) || de.Code != domain.CodeMediaMismatch {
				t.Errorf("standard %s on %s: want %s, got %v", code, media, domain.CodeMediaMismatch, err)
			}
		}
	}
}

func TestCompactLayout_OnlyForOneInchMediaAndKnownLabels(t *testing.T) {
	for _, media := range []string{domain.Media4x6In, domain.Media4x2In, domain.MediaA4} {
		if _, ok := domain.CompactLayout(domain.TemplateLP, media); ok {
			t.Errorf("LP_LABEL has a compact layout for %s", media)
		}
	}
	// The carton label is a 4 x 6 in label (§6.32.15); unknown codes have nothing.
	for _, code := range []string{domain.TemplateCarton, "NOPE"} {
		if _, ok := domain.CompactLayout(code, domain.Media2x1In); ok {
			t.Errorf("%s has a compact layout", code)
		}
	}
}

func TestCompactLayout_LPLabelMatchesGoldenZPL(t *testing.T) {
	got := renderCompact(t, domain.TemplateLP, domain.Media2x1In, lpData())
	want, err := os.ReadFile("../../testdata/golden/lp-label-2x1.zpl")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("ZPL differs from golden:\n got %q\nwant %q", got, want)
	}
}

// The compact bar code helpers validate like the standard ones.
func TestCompactLayout_HelpersStillValidate(t *testing.T) {
	data := lpData()
	data["sscc"] = "340123456789012341" // bad check digit
	tpl, _ := domain.CompactLayout(domain.TemplateLP, domain.Media2x1In)
	if _, err := domain.Render(tpl, data); err == nil {
		t.Fatal("compact SSCC bar code accepted a bad check digit")
	}
	item, _ := domain.CompactLayout(domain.TemplateItem, domain.Media3x1In)
	if _, err := domain.Render(item, map[string]string{"gtin": exampleGTIN, "gs1": "0109501101020918"}); err == nil {
		t.Fatal("compact GS1 bar code accepted a bad element string")
	}
	loc, _ := domain.CompactLayout(domain.TemplateLocation, domain.Media2x1In)
	if _, err := domain.Render(loc, map[string]string{"location": "A^FS"}); err == nil {
		t.Fatal("compact location bar code accepted a ZPL control character")
	}
}

// An item label with lot, expiry and serial has a bar code about 3.5 in wide at
// the 2 dot module, so 1 in devices refuse it (PRN-0007, naming the width) and
// the queue fails over; the standard layout prints it on 4 x 2 in media.
func TestCompactLayout_FullItemLabelNeedsFourInchMedia(t *testing.T) {
	data, err := domain.PrepareLabelData(domain.TemplateItem, itemData())
	if err != nil {
		t.Fatal(err)
	}
	for _, media := range oneInchMedia {
		var de *domain.Error
		_, err := domain.SizeLabel(renderCompact(t, domain.TemplateItem, media, data), media, 203)
		if !errors.As(err, &de) || de.Code != domain.CodeMediaMismatch || !strings.Contains(err.Error(), "dots wide") {
			t.Fatalf("full item label on %s: want a %s width refusal, got %v", media, domain.CodeMediaMismatch, err)
		}
	}
	tpl, _ := domain.Template(domain.TemplateItem)
	zpl, err := domain.Render(tpl, data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := domain.SizeLabel(zpl, domain.Media4x2In, 203); err != nil {
		t.Fatalf("full item label on 4 x 2: %v", err)
	}
}

func TestLabelForDevice(t *testing.T) {
	small := domain.Printer{DeviceType: domain.DeviceZPL, MediaSize: domain.Media2x1In}
	large := domain.Printer{DeviceType: domain.DeviceZPL, MediaSize: domain.Media4x6In}
	const standard = "^XA\nSTANDARD\n^XZ\n"
	compact := renderCompact(t, domain.TemplateLP, domain.Media2x1In, lpData())
	cases := []struct {
		name string
		d    domain.Printer
		tpl  string
		data map[string]string
		want string
	}{
		{"1 in device renders the compact layout", small, domain.TemplateLP, lpData(), compact},
		{"4 x 6 in device keeps the standard rendering", large, domain.TemplateLP, lpData(), standard},
		{"reprint (no data) reuses the stored payload", small, domain.TemplateLP, nil, standard},
		{"template without a compact layout", small, domain.TemplateCarton, lpData(), standard},
		{"PDF device", domain.Printer{DeviceType: domain.DevicePDFIPP, MediaSize: domain.MediaA4}, domain.TemplateLP, lpData(), standard},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := domain.LabelForDevice(c.d, c.tpl, standard, c.data)
			if err != nil || got != c.want {
				t.Fatalf("got %q, %v; want %q", got, err, c.want)
			}
		})
	}
}
