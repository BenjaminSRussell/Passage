package store_test

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/inventory"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func TestPostPackingSlip_RefusesHeldQuantity(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	holds := &inventory.MapHold{}
	s.WithHolds(holds)
	f := released(t, s, ctx, 10)
	packed := pickAndPack(t, s, ctx, f.shipment)
	product := f.shipment.Lines[0].ProductID
	holds.Set(product, decimal.NewFromInt(10), decimal.NewFromInt(8))
	in := store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(f.shipment, "12.50")}
	_, err := s.PostPackingSlip(ctx, f.shipment.ID, packed.Shipment.Version, in, idem("held"))
	if problemCode(err) != store.CodeQualityHold {
		t.Fatalf("held ship: %v, want %s", err, store.CodeQualityHold)
	}
	got, err := s.GetShipment(ctx, f.shipment.ID)
	if err != nil || got.Status != packed.Shipment.Status || !got.Lines[0].Shipped.IsZero() {
		t.Fatalf("held ship moved stock: %+v %v", got, err)
	}
	holds.Set(product, decimal.NewFromInt(10), decimal.Zero)
	slip, err := s.PostPackingSlip(ctx, f.shipment.ID, packed.Shipment.Version, in, idem("released"))
	if err != nil || slip.ID == "" {
		t.Fatalf("ship after usage decision: %+v %v", slip, err)
	}
}

// TestHoldProperty_ShipNeverMovesHeldUntilUsageDecision is wave 33 AC2: random
// hold, release (usage decision) and ship sequences never move held quantity.
func TestHoldProperty_ShipNeverMovesHeldUntilUsageDecision(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	holds := &inventory.MapHold{}
	s.WithHolds(holds)
	rapid.Check(t, func(rt *rapid.T) {
		qty := rapid.IntRange(1, 20).Draw(rt, "qty")
		held := rapid.IntRange(0, 20).Draw(rt, "held")
		physical := 20
		f := released(t, s, ctx, int64(qty))
		packed := pickAndPack(t, s, ctx, f.shipment)
		product := f.shipment.Lines[0].ProductID
		holds.Set(product, decimal.NewFromInt(int64(physical)), decimal.NewFromInt(int64(held)))
		in := store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(f.shipment, "1")}
		_, err := s.PostPackingSlip(ctx, f.shipment.ID, packed.Shipment.Version, in, idem("p-"+ids.New()))
		blocked := held > 0 && qty > physical-held
		if blocked {
			if problemCode(err) != store.CodeQualityHold {
				rt.Fatalf("held %d of %d, ship %d: %v, want %s", held, physical, qty, err, store.CodeQualityHold)
			}
			got, gerr := s.GetShipment(ctx, f.shipment.ID)
			if gerr != nil || !got.Lines[0].Shipped.IsZero() {
				rt.Fatalf("held ship moved quantity: %+v %v", got, gerr)
			}
			holds.Set(product, decimal.NewFromInt(int64(physical)), decimal.Zero)
			if _, err := s.PostPackingSlip(ctx, f.shipment.ID, packed.Shipment.Version, in, idem("r-"+ids.New())); err != nil {
				rt.Fatalf("ship after usage decision: %v", err)
			}
			return
		}
		if err != nil {
			rt.Fatalf("free ship of %d with held %d: %v", qty, held, err)
		}
	})
}
