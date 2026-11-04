package domain_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

func TestResolve_TotalDiscountBasisGross(t *testing.T) {
	// 10.00 a unit, 10% line discount, 10% total discount, two units.
	price := rec("p", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "USD", "0", "10.00")
	lineDisc := rec("l", domain.TypeLineDisc, domain.CustomerKindCustomer, "C1", domain.ProductKindAll, "", "USD", "0", "0")
	lineDisc.DiscPct1 = d("10")
	total := rec("t", domain.TypeTotalDisc, domain.CustomerKindCustomer, "C1", domain.ProductKindAll, "", "USD", "0", "0")
	total.DiscPct1 = d("10")
	recs := []domain.Record{price, lineDisc, total}
	run := func(basis string) domain.Result {
		return domain.Resolve(domain.Input{Header: header("USD"), Lines: []domain.Line{line("2")}, Records: recs, TotalDiscountBasis: basis})
	}
	for basis, want := range map[string]string{"": "1.80", domain.TotalBasisAfterLineDisc: "1.80", domain.TotalBasisGross: "2"} {
		res := run(basis)
		if !res.Subtotal.Equal(d("18")) || !res.TotalDiscount.Equal(d(want)) || !res.OrderNet.Equal(d("18").Sub(d(want))) {
			t.Fatalf("basis %q: subtotal %s discount %s net %s; want 18, %s", basis, res.Subtotal, res.TotalDiscount, res.OrderNet, want)
		}
	}
}

func TestResolve_GrossTotalDiscountNeverExceedsTheNet(t *testing.T) {
	price := rec("p", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "USD", "0", "10.00")
	lineDisc := rec("l", domain.TypeLineDisc, domain.CustomerKindCustomer, "C1", domain.ProductKindAll, "", "USD", "0", "0")
	lineDisc.DiscPct1 = d("90")
	total := rec("t", domain.TypeTotalDisc, domain.CustomerKindCustomer, "C1", domain.ProductKindAll, "", "USD", "0", "0")
	total.DiscPct1 = d("50")
	res := domain.Resolve(domain.Input{Header: header("USD"), Lines: []domain.Line{line("1")}, Records: []domain.Record{price, lineDisc, total},
		TotalDiscountBasis: domain.TotalBasisGross})
	// Net after the line discount is 1.00; 50% of the gross 10.00 would be 5.00.
	if !res.TotalDiscount.Equal(d("1")) || !res.OrderNet.IsZero() {
		t.Fatalf("discount %s net %s; want 1 and 0", res.TotalDiscount, res.OrderNet)
	}
}

func TestParseBands(t *testing.T) {
	bands, faults := domain.ParseBands(domain.DefaultBandSpecs())
	if faults != nil || len(bands) != 3 || !bands[2].Unbounded || !bands[0].UpTo.Equal(d("10")) {
		t.Fatalf("default = %v, %v", bands, faults)
	}
	if got := domain.SmartRound(d("7.7805"), bands); !got.Equal(d("7.99")) {
		t.Fatalf("7.7805 = %s", got)
	}
	bad := map[string][]domain.BandSpec{
		"smartRoundingBands":           nil,
		"smartRoundingBands[0].upTo":   {{UpTo: "x", Ending: "0.5"}, {Ending: "0"}},
		"smartRoundingBands[1].upTo":   {{UpTo: "10", Ending: "0.5"}, {UpTo: "10", Ending: "0.5"}, {Ending: "0"}},
		"smartRoundingBands[0].ending": {{UpTo: "10", Ending: "1"}, {Ending: "0"}},
		"smartRoundingBands[1].ending": {{UpTo: "10", Ending: "0.5"}, {Ending: "0.12345"}},
	}
	for field, specs := range bad {
		if _, faults := domain.ParseBands(specs); faults[field] == "" {
			t.Errorf("%s: faults = %v", field, faults)
		}
	}
	if _, faults := domain.ParseBands([]domain.BandSpec{{UpTo: "10", Ending: "0.5"}}); faults["smartRoundingBands[0].upTo"] == "" {
		t.Errorf("a last band with upTo must be refused, got %v", faults)
	}
}

func TestBandSpecs_Canonical(t *testing.T) {
	bands, faults := domain.ParseBands([]domain.BandSpec{{UpTo: "10.00", Ending: "0.990"}, {UpTo: "100", Ending: "0.95"}, {Ending: "0.0"}})
	if faults != nil {
		t.Fatal(faults)
	}
	got, want := domain.BandSpecs(bands), domain.DefaultBandSpecs()
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("band %d = %+v; want %+v", i, got[i], want[i])
		}
	}
}
