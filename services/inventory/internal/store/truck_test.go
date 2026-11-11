package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

func truckDims() domain.Dimensions {
	return domain.Dimensions{domain.DimSite: "S1", domain.DimWarehouse: "truck-1"}
}

func serviceIssue(ctx context.Context, s *store.Postgres, product, source, qty string, neg domain.NegativePolicy) error {
	p := legalEntity(ctx)
	_, _, err := s.CreateTransaction(ctx, store.CreateTransactionInput{LegalEntityID: p, SourceType: "SERVICE_LINE", SourceID: source, Reference: "SWO-1", ProductID: product,
		Dims: truckDims(), Qty: dec("-" + qty), EnteredQty: dec("-" + qty), EnteredUnit: "EA", Status: domain.StatusDeducted, Currency: "USD", CostPrice: dec("2"),
		Date: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), IdempotencyKey: "truck:" + source, Negative: neg})
	return err
}

// BR-45.7, FR-45.9: a truck issue (origin SERVICE_LINE) takes stock from the truck's own
// warehouse and is refused when the truck is short unless the policy allows it; a truck below
// its minimum gets one draft transfer order up to its maximum, and no second one while that is
// on its way.
func TestTruckStockIssueAndReplenishment(t *testing.T) {
	s, _, _ := setup(t)
	le := ids.New()
	ctx := entityCtx(1, le)
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "50", "2", dims()), line(product, "4", "2", truckDims()))
	ref := store.TruckRef{LegalEntityID: le, SiteID: "S1", WarehouseID: "truck-1", SourceSiteID: "S1", SourceWarehouseID: "W1"}
	if err := s.SetTruckLevels(ctx, ref, []domain.TruckLevel{{ProductID: product, Unit: "EA", Min: dec("3"), Max: dec("10")}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTruckLevels(ctx, ref, []domain.TruckLevel{{ProductID: product, Unit: "EA", Min: dec("10"), Max: dec("5")}}); problemCode(err) != domain.CodeTruckLevel {
		t.Fatalf("a max below the min: %v", err)
	}
	levels, err := s.ListTruckLevels(ctx, le, "S1", "truck-1")
	if err != nil || len(levels) != 1 || !levels[0].OnHand.Equal(dec("4")) || !levels[0].Inbound.IsZero() {
		t.Fatalf("levels %+v %v", levels, err)
	}
	if orders, err := s.ReplenishTrucks(ctx, le, "", "", time.Now()); err != nil || len(orders) != 0 {
		t.Fatalf("a truck above its minimum got %v %v", orders, err)
	}

	src := ids.New()
	if err := serviceIssue(ctx, s, product, src, "2", domain.NegativePolicy{}); err != nil {
		t.Fatal(err)
	}
	if err := serviceIssue(ctx, s, product, src, "2", domain.NegativePolicy{}); err != nil { // the same origin again is a replay
		t.Fatal(err)
	}
	wantTruckQty(t, s, ctx, product, "2")

	if err := serviceIssue(ctx, s, product, ids.New(), "5", domain.NegativePolicy{}); problemCode(err) != domain.CodeNegativePhysical {
		t.Fatalf("an issue beyond the truck's stock: %v", err)
	}
	wantTruckQty(t, s, ctx, product, "2")

	day := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	orders, err := s.ReplenishTrucks(ctx, le, "S1", "truck-1", day)
	if err != nil || len(orders) != 1 {
		t.Fatalf("replenishment %v %v", orders, err)
	}
	o := orders[0]
	if o.Status != domain.TransferDraft || o.FromWarehouse != "W1" || o.ToWarehouse != "truck-1" || len(o.Lines) != 1 || !o.Lines[0].Qty.Equal(dec("8")) || o.Lines[0].ProductID != product {
		t.Fatalf("order %+v", o)
	}
	again, err := s.ReplenishTrucks(ctx, le, "", "", day)
	if err != nil || len(again) != 0 {
		t.Fatalf("a second run while 8 are on their way: %v %v", again, err)
	}
	levels, _ = s.ListTruckLevels(ctx, le, "", "")
	if !levels[0].Inbound.Equal(dec("8")) {
		t.Fatalf("inbound %s, want 8", levels[0].Inbound)
	}

	// With the policy on, the truck may go negative.
	if err := serviceIssue(ctx, s, product, ids.New(), "5", domain.NegativePolicy{PhysicalAllowed: true, FinancialAllowed: true}); err != nil {
		t.Fatalf("negative allowed: %v", err)
	}
	wantTruckQty(t, s, ctx, product, "-3")
	// The projection still equals a replay of the ledger, truck issues included.
	if rep, err := s.RebuildOnHand(ctx, false); err != nil || rep.DiffCount != 0 {
		t.Fatalf("rebuild %+v %v", rep, err)
	}
}

// wantTruckQty checks the physical quantity on the truck (a DEDUCTED issue is physical; its value is
// posted when the issue turns financial).
func wantTruckQty(t *testing.T, s *store.Postgres, ctx context.Context, product, qty string) {
	t.Helper()
	if q, _ := stockAt(t, s, ctx, product, "truck-1"); !q.Equal(dec(qty)) {
		t.Fatalf("truck holds %s, want %s", q, qty)
	}
}
