package store_test

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
)

const (
	// The legal entity of testkit.Ctx, so the registry rows are in the caller's scope.
	regLeA = "00000000-0000-0000-0000-0000000000e1"
	regLeB = "00000000-0000-0000-0000-0000000000e1"
)

// The registry, the matrix, the prices and the settings work as the application role, and each tenant sees only its own.
func TestRegistry_AsTheAppRoleAndPerTenant(t *testing.T) {
	s, _, ctx := appSetup(t)
	other := testkit.Ctx(testkit.TenantID(2))
	for _, e := range []domain.EntityRecord{
		{Entity: "A", LegalEntityID: regLeA, LedgerID: "L-A", Currency: "USD", Country: "US", FXDifferenceAccount: "7100"},
		{Entity: "B", LegalEntityID: regLeB, LedgerID: "L-B", Currency: "EUR", Country: "DE"}} {
		if err := s.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	// A second put replaces the entry.
	if err := s.PutEntity(ctx, domain.EntityRecord{Entity: "A", LegalEntityID: regLeA, LedgerID: "L-A2", Currency: "USD", Country: "US"}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListEntities(ctx)
	if err != nil || len(list) != 2 || list[0].LedgerID != "L-A2" || list[0].FXDifferenceAccount != "" {
		t.Fatalf("entities = %+v, %v", list, err)
	}
	if got, _ := s.ListEntities(other); len(got) != 0 {
		t.Fatalf("another tenant sees %d entities", len(got))
	}

	m := domain.Matrix{{EntityA: "A", EntityB: "B", DueFrom: "1500", DueTo: "2500"}, {EntityA: "B", EntityB: "A", DueFrom: "1500", DueTo: "2500"}}
	if err := s.ReplaceMatrix(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceMatrix(ctx, domain.Matrix{{EntityA: "A", EntityB: "Z", DueFrom: "1", DueTo: "2"}}); err == nil {
		t.Fatal("a matrix with an unregistered entity must be refused")
	}
	if got, err := s.GetMatrix(ctx); err != nil || len(got) != 2 {
		t.Fatalf("a refused replacement must leave the matrix alone: %+v, %v", got, err)
	}
	if err := s.ReplaceMatrix(ctx, m[:1]); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetMatrix(ctx); len(got) != 1 {
		t.Fatalf("matrix = %+v", got)
	}

	day := func(d string) time.Time { v, _ := time.Parse(time.DateOnly, d); return v }
	p := domain.TransferPrice{Lender: "A", Borrower: "B", Category: "Hour", Role: "Engineer", Method: domain.TPCostPlus, MarkupPct: decimal.NewFromInt(10), Currency: "USD", EffectiveFrom: day("2026-01-01")}
	if _, err := s.AddTransferPrice(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTransferPrice(ctx, p); err == nil {
		t.Fatal("a second price effective on the same date must be refused")
	}
	later := p
	later.MarkupPct, later.EffectiveFrom = decimal.NewFromInt(12), day("2026-07-01")
	if _, err := s.AddTransferPrice(ctx, later); err != nil {
		t.Fatal(err)
	}
	prices, err := s.PricesFor(ctx, "A", "B")
	if err != nil || len(prices) != 2 {
		t.Fatalf("prices = %+v, %v", prices, err)
	}
	got, err := domain.ResolveTransferPrice(prices, "A", "B", "Hour", "Engineer", day("2026-03-01"))
	if err != nil || !got.MarkupPct.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("price on 2026-03-01 = %+v, %v", got, err)
	}
	if got, _ := s.PricesFor(other, "A", "B"); len(got) != 0 {
		t.Fatal("another tenant sees the prices")
	}

	if st, err := s.GetSettings(ctx); err != nil || st.ChargeSettlementMode != domain.SettlementJournal || !st.FXTolerancePct.Equal(domain.DefaultFXTolerancePct) {
		t.Fatalf("defaults = %+v, %v", st, err)
	}
	want := domain.Settings{ChargeSettlementMode: domain.SettlementInvoice, CrossBorderTax: true, ReconCurrency: "EUR", FXTolerancePct: decimal.NewFromInt(2)}
	if err := s.PutSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	if st, err := s.GetSettings(ctx); err != nil || st.ChargeSettlementMode != "Invoice" || !st.CrossBorderTax || st.ReconCurrency != "EUR" || !st.FXTolerancePct.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("settings = %+v, %v", st, err)
	}
	if st, _ := s.GetSettings(other); st.CrossBorderTax {
		t.Fatal("another tenant sees the settings")
	}
}
