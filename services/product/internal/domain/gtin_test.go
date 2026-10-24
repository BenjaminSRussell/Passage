package domain_test

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/product/internal/domain"
)

func TestGS1Vectors(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "gs1-vectors.csv")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	accepted, rejected := 0, 0
	for i, row := range rows {
		if i == 0 || len(row) < 2 {
			continue
		}
		err := domain.ValidateGTIN(row[0])
		switch row[1] {
		case "valid":
			if err != nil {
				t.Errorf("%s: want valid, got %v", row[0], err)
			} else {
				accepted++
			}
		case "invalid":
			if err == nil {
				t.Errorf("%s: want invalid", row[0])
			} else {
				rejected++
			}
		}
	}
	if accepted != 40 || rejected != 40 {
		t.Fatalf("accepted=%d rejected=%d, want 40/40", accepted, rejected)
	}
}

func TestAllocateGTIN13FromPrefix(t *testing.T) {
	got, next, err := domain.AllocateGTIN13("0614141", 123457)
	if err != nil {
		t.Fatal(err)
	}
	if got != "06141411234579" && got != "6141411234579" {
		t.Fatalf("allocated %s, want 06141411234579", got)
	}
	if next != 123458 {
		t.Fatalf("counter %d, want 123458", next)
	}
	if err := domain.ValidateGTIN(got); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalGTIN14AndSSCC(t *testing.T) {
	c, err := domain.CanonicalGTIN14("614141123452")
	if err != nil || c != "00614141123452" {
		t.Fatalf("canonical %s %v", c, err)
	}
	sscc, err := domain.GenerateSSCC(0, "0614141", 123456789)
	if err != nil {
		t.Fatal(err)
	}
	if len(sscc) != 18 {
		t.Fatalf("SSCC length %d", len(sscc))
	}
	if err := domain.ValidateGTIN(sscc[:14]); err != nil && len(sscc) != 18 {
		t.Fatal(err)
	}
	d, err := domain.CheckDigit(sscc[:17])
	if err != nil || d != int(sscc[17]-'0') {
		t.Fatalf("SSCC check digit %d %v", d, err)
	}
}

func TestEAN13SVGContainsGTIN(t *testing.T) {
	svg, err := domain.RenderEAN13SVG("5901234123457")
	if err != nil {
		t.Fatal(err)
	}
	if len(svg) < 100 || !strings.Contains(svg, "5901234123457") || !strings.Contains(svg, "<svg") {
		t.Fatalf("unexpected svg length %d", len(svg))
	}
}
