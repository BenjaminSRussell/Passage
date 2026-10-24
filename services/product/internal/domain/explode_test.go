package domain_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/product/internal/domain"
)

func master3x10() (domain.ExplosionRequest, []domain.Dimension) {
	dims := make([]domain.Dimension, 3)
	names := []string{"size", "color", "style"}
	for i, name := range names {
		vals := make([]string, 10)
		for j := 0; j < 10; j++ {
			vals[j] = fmt.Sprintf("%s%02d", name[:1], j)
		}
		dims[i] = domain.Dimension{Code: name, Name: name, Values: vals}
	}
	return domain.ExplosionRequest{MasterNumber: "SHIRT", Dimensions: dims, MaxVariants: 10000}, dims
}

func TestExplodeThreeByTenCreatesThousandAndLooksUp(t *testing.T) {
	req, _ := master3x10()
	start := time.Now()
	got, err := domain.Explode(nil, req)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Created) != 1000 {
		t.Fatalf("created %d, want 1000", len(got.Created))
	}
	if elapsed > 5*time.Second {
		t.Fatalf("explosion took %s, want < 5s", elapsed)
	}
	for _, v := range got.Created {
		found, ok := domain.Lookup(got.Created, v.DimensionValues)
		if !ok || found.VariantHash != v.VariantHash {
			t.Fatalf("lookup missed %+v", v.DimensionValues)
		}
	}
	second, err := domain.Explode(got.Created, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Created) != 0 || len(second.Skipped) != 1000 {
		t.Fatalf("second run created=%d skipped=%d, want 0/1000", len(second.Created), len(second.Skipped))
	}
}

func TestExplodeRejectsUnknownValueAndCap(t *testing.T) {
	req, _ := master3x10()
	req.Selected = map[string][]string{"size": {"nope"}}
	if _, err := domain.Explode(nil, req); err == nil {
		t.Fatal("unknown dimension value must be rejected")
	}
	req, _ = master3x10()
	req.MaxVariants = 10
	if _, err := domain.Explode(nil, req); err == nil {
		t.Fatal("over-cap explosion must be rejected")
	}
}

func TestVariantNumberCollapsesEmptyAndHashesLong(t *testing.T) {
	short := domain.VariantNumber("A", "", []string{"size"}, map[string]string{"size": "M"})
	if short != "A-M" {
		t.Fatalf("short number %s", short)
	}
	long := domain.VariantNumber("VERYLONGMASTERNUMBER", "{master}-{size}-{color}-{style}-{config}", []string{"size", "color", "style", "config"},
		map[string]string{"size": "EXTRALARGE", "color": "BRIGHTRED", "style": "SLIMFIT", "config": "PREMIUM"})
	if len(long) > domain.MaxProductNumber {
		t.Fatalf("long number %s exceeds 40", long)
	}
}
