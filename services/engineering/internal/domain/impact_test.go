package domain_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
)

func TestBuildImpact(t *testing.T) {
	oldC := version("c1", "C", "2025-01-01", "", line(10, "X", 1), line(20, "Y", 2))
	newC := version("c2", "C", "2026-07-01", "", line(10, "X", 3), line(20, "Z", 1))
	newC.Status = domain.StatusDraft
	sub := version("s", "SUB", "2025-01-01", "", line(10, "C", 1))
	sub.SiteID = "site-a"
	p1 := version("p1", "P1", "2025-01-01", "", line(10, "SUB", 1))
	p1.SiteID = "site-b"
	p2 := version("p2", "P2", "2025-01-01", "", line(10, "C", 2))
	gone := version("old", "P3", "2025-01-01", "2025-12-31", line(10, "C", 1))
	elsewhere := version("cz", "C", "2025-01-01", "", line(10, "X", 1))
	elsewhere.SiteID = "site-z"
	cat := catalogOf(oldC, elsewhere, sub, p1, p2, gone)
	eco := domain.Eco{Replaces: []domain.Replacement{{OldVersionID: "c1", NewVersionID: "c2"}}}

	got, err := domain.BuildImpact(cat, eco, []domain.BomVersion{oldC, newC}, date("2026-03-01"), nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range got.Products {
		ids = append(ids, p.ProductID)
	}
	if len(ids) != 4 || ids[0] != "C" || ids[1] != "P2" || ids[2] != "SUB" || ids[3] != "P1" {
		t.Fatalf("products = %v, want C, P2, SUB, P1 (P3's version ended)", ids)
	}
	if got.Products[0].Reason != domain.ReasonReplaced || got.Products[3].Level != 2 || got.Products[3].SiteIDs[0] != "site-b" {
		t.Fatalf("products = %+v", got.Products)
	}
	if c := got.Products[0]; len(c.VersionIDs) != 1 || c.VersionIDs[0] != "c1" || len(c.SiteIDs) != 0 {
		t.Fatalf("replaced C = %+v, want only c1 and no site: the site-z version is not replaced", c)
	}
	for _, p := range got.Products {
		if want := p.ProductID == "P1" || p.ProductID == "P2"; p.TopLevel != want {
			t.Fatalf("%s topLevel = %v, want %v", p.ProductID, p.TopLevel, want)
		}
	}
	if len(got.SiteIDs) != 2 || got.OpenProductionOrders != 0 || got.OpenPlannedOrders != 0 {
		t.Fatalf("sites %v orders %d/%d", got.SiteIDs, got.OpenProductionOrders, got.OpenPlannedOrders)
	}
	changes := got.Replacements[0].Changes
	if len(changes) != 3 || changes[0].ComponentProductID != "X" || changes[0].Change != domain.ChangeChanged ||
		changes[1].Change != domain.ChangeRemoved || changes[2].Change != domain.ChangeAdded {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestBuildImpactUnusedProductIsTopLevel(t *testing.T) {
	oldV := version("v1", "END", "2025-01-01", "", line(10, "X", 1))
	newV := version("v2", "END", "2026-07-01", "", line(10, "X", 1))
	newV.Status = domain.StatusDraft
	eco := domain.Eco{Replaces: []domain.Replacement{{OldVersionID: "v1", NewVersionID: "v2"}}}
	got, err := domain.BuildImpact(catalogOf(oldV), eco, []domain.BomVersion{oldV, newV}, date("2026-03-01"), countOrders{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Products) != 1 || !got.Products[0].TopLevel || len(got.Replacements[0].Changes) != 0 {
		t.Fatalf("impact = %+v, want END alone, top-level, no line changes", got)
	}
	if got.OpenProductionOrders != 3 || got.OpenPlannedOrders != 5 {
		t.Fatalf("orders = %d/%d, want the hook's counts", got.OpenProductionOrders, got.OpenPlannedOrders)
	}
}

type countOrders struct{}

func (countOrders) OpenOrders(string) (int, int) { return 3, 5 }

func TestBuildImpactCycle(t *testing.T) {
	a := version("a", "A", "2025-01-01", "", line(10, "B", 1))
	b := version("b", "B", "2025-01-01", "", line(10, "A", 1))
	cat := catalogOf(a, b)
	eco := domain.Eco{Replaces: []domain.Replacement{{OldVersionID: "a", NewVersionID: "a"}}}
	if _, err := domain.BuildImpact(cat, eco, []domain.BomVersion{a}, date("2026-03-01"), nil); !domain.HasCode(err, domain.CodeBomCycle) {
		t.Fatalf("err = %v, want cycle", err)
	}
}
