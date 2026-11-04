package domain_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

func TestPriceListFor_KeepsApplicableScopesMostSpecificFirst(t *testing.T) {
	recs := []domain.Record{
		rec("all", domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindProduct, "P1", "USD", "0", "100"),
		rec("grp", domain.TypePrice, domain.CustomerKindGroup, "PG-A", domain.ProductKindProduct, "P1", "USD", "0", "95"),
		rec("cust", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "USD", "0", "90"),
		rec("other", domain.TypePrice, domain.CustomerKindCustomer, "C2", domain.ProductKindProduct, "P1", "USD", "0", "50"),
		rec("othergrp", domain.TypePrice, domain.CustomerKindGroup, "PG-B", domain.ProductKindProduct, "P1", "USD", "0", "60"),
		rec("p0", domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindProduct, "P0", "USD", "0", "10"),
		rec("brk", domain.TypePrice, domain.CustomerKindCustomer, "C1", domain.ProductKindProduct, "P1", "USD", "10", "85"),
		rec("disc", domain.TypeLineDisc, domain.CustomerKindAll, "", domain.ProductKindProduct, "P1", "USD", "0", "0"),
	}
	got := domain.PriceListFor(recs, "C1", "PG-A", map[domain.ConditionType]bool{domain.TypePrice: true})
	var ids, scopes []string
	for _, e := range got {
		ids, scopes = append(ids, e.Record.ID), append(scopes, e.Scope)
	}
	want := []string{"p0", "cust", "brk", "grp", "all"}
	if len(ids) != len(want) {
		t.Fatalf("price list = %v; want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("price list = %v; want %v", ids, want)
		}
	}
	if scopes[1] != domain.ScopeCustomer || scopes[3] != domain.ScopeGroup || scopes[4] != domain.ScopeAll {
		t.Fatalf("scopes = %v", scopes)
	}
}

func TestPriceListFor_NoGroupDropsGroupRecordsAndNoTypesKeepsAll(t *testing.T) {
	recs := []domain.Record{
		rec("grp", domain.TypePrice, domain.CustomerKindGroup, "PG-A", domain.ProductKindProduct, "P1", "USD", "0", "95"),
		rec("disc", domain.TypeLineDisc, domain.CustomerKindAll, "", domain.ProductKindProduct, "P1", "USD", "0", "0"),
	}
	got := domain.PriceListFor(recs, "C1", "", nil)
	if len(got) != 1 || got[0].Record.ID != "disc" {
		t.Fatalf("without a customer group only the all-customer record applies: %+v", got)
	}
}
