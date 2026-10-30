package domain_test

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

func fgDay(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func fgRule(id string, kind domain.CustomerKind, key, freeKind, calc string) domain.FreeGoodsDef {
	return domain.FreeGoodsDef{ID: id, LegalEntityID: "le", CustomerKind: kind, CustomerKey: key, ProductID: "P1", Unit: "EA",
		Kind: freeKind, Calc: calc, Every: d("10"), Free: d("1"), ValidFrom: fgDay("2026-01-01"), ValidTo: fgDay("2026-12-31"), Version: 1}
}

func fgHeader() domain.Header {
	return domain.Header{CustomerID: "C1", CustomerGroupID: "G1", Currency: "USD", PricingDate: fgDay("2026-06-01")}
}

func fgLines(qty string) []domain.Line {
	return []domain.Line{{No: 1, ProductID: "P1", Unit: "EA", Quantity: d(qty)}}
}

func TestFreeGoodsCalcRules(t *testing.T) {
	r := domain.FreeGoodsRule{Kind: domain.FreeGoodsInclusive, Every: d("10"), Free: d("1"), Calc: domain.CalcProportional}
	if got := domain.FreeGoods(d("25"), r); !got.Free.Equal(d("2.5")) || !got.Priced.Equal(d("22.5")) {
		t.Errorf("proportional = %+v; want 2.5 free", got)
	}
	r.Calc = domain.CalcUnitReference
	if got := domain.FreeGoods(d("25"), r); !got.Free.Equal(d("2")) {
		t.Errorf("unit reference = %+v; want 2 free", got)
	}
	r.Calc = domain.CalcWholeUnits
	r.Free = d("3")
	if got := domain.FreeGoods(d("25"), r); !got.Free.Equal(d("6")) {
		t.Errorf("whole units 3 per 10 = %+v; want 6 free", got)
	}
	r.Calc = domain.CalcUnitReference
	if got := domain.FreeGoods(d("25"), r); !got.Free.Equal(d("7")) {
		t.Errorf("unit reference 3 per 10 = %+v; want 7 free (floor of 7.5)", got)
	}
}

func TestProposeFreeGoods_MostSpecificRuleWins(t *testing.T) {
	rules := []domain.FreeGoodsDef{
		fgRule("all", domain.CustomerKindAll, "", domain.FreeGoodsInclusive, domain.CalcWholeUnits),
		fgRule("grp", domain.CustomerKindGroup, "G1", domain.FreeGoodsInclusive, domain.CalcWholeUnits),
		fgRule("cus", domain.CustomerKindCustomer, "C1", domain.FreeGoodsExclusive, domain.CalcWholeUnits),
		fgRule("other", domain.CustomerKindCustomer, "C9", domain.FreeGoodsExclusive, domain.CalcWholeUnits),
	}
	got := domain.ProposeFreeGoods(fgHeader(), fgLines("25"), rules, nil)
	if len(got) != 1 || got[0].RuleID != "cus" || !got[0].PricedQuantity.Equal(d("25")) || !got[0].FreeQuantity.Equal(d("2")) {
		t.Fatalf("proposal = %+v; want customer rule, 25 priced and 2 extra", got)
	}
	got = domain.ProposeFreeGoods(fgHeader(), fgLines("25"), rules[:2], nil)
	if got[0].RuleID != "grp" || !got[0].PricedQuantity.Equal(d("23")) {
		t.Fatalf("proposal = %+v; want group rule, 23 priced (AC-26.13)", got)
	}
}

func TestProposeFreeGoods_GatesAndWarning(t *testing.T) {
	rule := fgRule("r", domain.CustomerKindAll, "", domain.FreeGoodsInclusive, domain.CalcWholeUnits)
	rule.MinQty = d("20")
	if got := domain.ProposeFreeGoods(fgHeader(), fgLines("15"), []domain.FreeGoodsDef{rule}, nil); len(got) != 0 {
		t.Errorf("below minimum quantity: %+v", got)
	}
	h := fgHeader()
	h.PricingDate = fgDay("2027-01-01")
	if got := domain.ProposeFreeGoods(h, fgLines("25"), []domain.FreeGoodsDef{rule}, nil); len(got) != 0 {
		t.Errorf("outside validity: %+v", got)
	}
	wd := rule
	wd.Withdrawn = true
	if got := domain.ProposeFreeGoods(fgHeader(), fgLines("25"), []domain.FreeGoodsDef{wd}, nil); len(got) != 0 {
		t.Errorf("withdrawn: %+v", got)
	}
	got := domain.ProposeFreeGoods(fgHeader(), fgLines("25"), []domain.FreeGoodsDef{rule}, map[int]decimal.Decimal{1: decimal.Zero})
	if len(got) != 1 || got[0].Warning != domain.CodeFreeGoodsWithFullDiscount {
		t.Errorf("zero-priced line: %+v; want PRC-0014 warning", got)
	}
}

func TestValidateFreeGoodsAndOverlap(t *testing.T) {
	ok := fgRule("a", domain.CustomerKindAll, "", domain.FreeGoodsInclusive, domain.CalcWholeUnits)
	if f := domain.ValidateFreeGoods(ok); len(f) != 0 {
		t.Fatalf("valid rule: %v", f)
	}
	bad := ok
	bad.CustomerKind, bad.CustomerKey, bad.Kind, bad.Calc, bad.Every = "x", "", "Z", "Q", decimal.Zero
	bad.MinQty = d("-1")
	f := domain.ValidateFreeGoods(bad)
	for _, k := range []string{"customerKind", "kind", "calcRule", "everyQuantity", "minQuantity"} {
		if f[k] == "" {
			t.Errorf("no error for %s: %v", k, f)
		}
	}
	other := ok
	other.ID = "b"
	other.ValidFrom, other.ValidTo = fgDay("2026-12-31"), fgDay("2027-06-30")
	if _, clash := domain.FreeGoodsOverlap(other, []domain.FreeGoodsDef{ok}); !clash {
		t.Error("touching last day should overlap")
	}
	other.ValidFrom = fgDay("2027-01-01")
	if _, clash := domain.FreeGoodsOverlap(other, []domain.FreeGoodsDef{ok}); clash {
		t.Error("next day should not overlap")
	}
}

func fgPrice(id, from, amount string) domain.Record {
	return domain.Record{ID: id, Status: domain.StatusActive, Type: domain.TypePrice, CustomerKind: domain.CustomerKindAll,
		ProductKind: domain.ProductKindProduct, ProductKey: "P1", Currency: "USD", Unit: "EA", FromQuantity: d(from),
		Amount: d(amount), ValidFrom: fgDay("2026-01-01"), ValidTo: fgDay("2026-12-31")}
}

// AC-26.13 on the priced document: an inclusive rule charges 23 of the 25 ordered units.
func TestResolveWithFreeGoods_InclusiveChargesThePricedQuantity(t *testing.T) {
	in := domain.Input{Header: fgHeader(), Lines: fgLines("25"), Records: []domain.Record{fgPrice("R", "0", "9.00")}}
	inclusive := fgRule("inc", domain.CustomerKindAll, "", domain.FreeGoodsInclusive, domain.CalcWholeUnits)
	res, props := domain.ResolveWithFreeGoods(in, []domain.FreeGoodsDef{inclusive})
	if len(props) != 1 || !props[0].PricedQuantity.Equal(d("23")) || !props[0].FreeQuantity.Equal(d("2")) || !props[0].OrderedQuantity.Equal(d("25")) {
		t.Fatalf("proposals = %+v; want 25 ordered, 23 priced, 2 free", props)
	}
	if l := res.Lines[0]; !l.Quantity.Equal(d("23")) || !l.LineNet.Equal(d("207")) || !res.OrderNet.Equal(d("207")) {
		t.Fatalf("line = %+v order net %s; want 23 units at 9.00 = 207.00", l, res.OrderNet)
	}
	if in.Lines[0].Quantity.String() != "25" {
		t.Fatalf("caller's lines changed: %s", in.Lines[0].Quantity)
	}

	exclusive := fgRule("exc", domain.CustomerKindAll, "", domain.FreeGoodsExclusive, domain.CalcWholeUnits)
	res, props = domain.ResolveWithFreeGoods(in, []domain.FreeGoodsDef{exclusive})
	if len(props) != 1 || !res.Lines[0].Quantity.Equal(d("25")) || !res.OrderNet.Equal(d("225")) {
		t.Fatalf("exclusive: line %+v proposals %+v; want 25 charged and 2 extra", res.Lines[0], props)
	}
}

// The break comes from the ordered quantity, so free goods never cost the customer a
// better tier: 25 ordered at the 25+ price of 8.00 charges 23 x 8.00, not 23 x 9.00.
func TestResolveWithFreeGoods_BreaksCountTheOrderedQuantity(t *testing.T) {
	inclusive := fgRule("inc", domain.CustomerKindAll, "", domain.FreeGoodsInclusive, domain.CalcWholeUnits)
	tiers := []domain.Record{fgPrice("R0", "0", "9.00"), fgPrice("R25", "25", "8.00")}
	res, props := domain.ResolveWithFreeGoods(domain.Input{Header: fgHeader(), Lines: fgLines("25"), Records: tiers}, []domain.FreeGoodsDef{inclusive})
	if len(props) != 1 || !res.Lines[0].UnitPrice.Equal(d("8")) || !res.Lines[0].LineNet.Equal(d("184")) || !res.Subtotal.Equal(d("184")) {
		t.Fatalf("line = %+v subtotal %s; want the 25+ tier on 23 charged units = 184.00", res.Lines[0], res.Subtotal)
	}
	if res, props := domain.ResolveWithFreeGoods(domain.Input{Header: fgHeader(), Lines: fgLines("25")}, []domain.FreeGoodsDef{inclusive}); res.Lines[0].Error == "" || len(props) != 0 {
		t.Fatalf("unpriced line: %+v proposals %+v; want no proposal", res.Lines[0], props)
	}
}
