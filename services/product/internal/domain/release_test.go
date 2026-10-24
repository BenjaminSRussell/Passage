package domain_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/product/internal/domain"
	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestResolveSiteDefaultCascade(t *testing.T) {
	defaults := domain.ReleaseDefaults{
		LegalEntityID:      "USMF",
		ItemGroupID:        "FG",
		ItemModelGroupID:   "STD",
		DefaultSiteID:      "S1",
		DefaultWarehouseID: "W1",
		SiteDefaults: map[string]domain.SiteDefault{
			"S2": {SiteID: "S2", WarehouseID: "W3", MinQty: decimal.NewFromInt(2)},
		},
	}
	s1, err := domain.ResolveSiteDefault(defaults, "S1")
	if err != nil || s1.WarehouseID != "W1" {
		t.Fatalf("S1 = %+v %v", s1, err)
	}
	s2, err := domain.ResolveSiteDefault(defaults, "S2")
	if err != nil || s2.WarehouseID != "W3" {
		t.Fatalf("S2 = %+v %v", s2, err)
	}
}

func TestReleasedProductDefaultsCascadeProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		sites := []string{"S1", "S2", "S3", "S4", "S5"}
		overrides := map[string]domain.SiteDefault{}
		nOver := rapid.IntRange(0, 3).Draw(t, "overrides")
		used := map[string]bool{}
		for i := 0; i < nOver; i++ {
			site := rapid.SampledFrom(sites).Draw(t, "site")
			if site == "S1" || used[site] {
				continue
			}
			used[site] = true
			overrides[site] = domain.SiteDefault{SiteID: site, WarehouseID: "W-" + site}
		}
		defaults := domain.ReleaseDefaults{
			LegalEntityID:      "USMF",
			DefaultSiteID:      "S1",
			DefaultWarehouseID: "W1",
			SiteDefaults:       overrides,
		}
		for i := 0; i < 200; i++ {
			site := rapid.SampledFrom(sites).Draw(t, "lookup")
			got, err := domain.ResolveSiteDefault(defaults, site)
			if err != nil {
				t.Fatalf("resolve %s: %v", site, err)
			}
			if over, ok := overrides[site]; ok {
				if got.WarehouseID != over.WarehouseID {
					t.Fatalf("site %s: got %s want override %s", site, got.WarehouseID, over.WarehouseID)
				}
				continue
			}
			if got.WarehouseID != "W1" {
				t.Fatalf("site %s: got %s want entity default W1", site, got.WarehouseID)
			}
		}
	})
}

func TestApplyReleaseProfileRejectsDraft(t *testing.T) {
	_, err := domain.ApplyReleaseProfile(domain.Product{ProductNumber: "A", LifecycleState: domain.StateDraft}, domain.ReleaseDefaults{
		LegalEntityID: "USMF", ItemGroupID: "FG", ItemModelGroupID: "STD", DefaultSiteID: "S1", DefaultWarehouseID: "W1",
	}, "")
	if err == nil {
		t.Fatal("draft release must fail")
	}
}

func TestDimensionGroupLockedAfterFinancialTxn(t *testing.T) {
	if err := domain.AssertDimensionGroupUnlocked(true); err == nil {
		t.Fatal("expected PRD-DIMGRP-LOCKED")
	}
}
