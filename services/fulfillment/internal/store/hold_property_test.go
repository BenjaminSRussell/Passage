package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/issueclaim"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func qn(n int64) decimal.Decimal { return decimal.NewFromInt(n) }

func postIn(sh store.Shipment) store.PostInput {
	return store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(sh, "1")}
}

func packedIn(t *testing.T, s *store.Postgres, ctx context.Context, warehouse, legalEntity string, products []string, qtys ...int64) (store.PackResult, store.PostInput) {
	t.Helper()
	f := releasedOf(t, s, ctx, warehouse, legalEntity, products, qtys...)
	return pickAndPack(t, s, ctx, f.shipment), postIn(f.shipment)
}

func TestPostPackingSlip_RefusesHeldQuantity(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	holds := &issueclaim.Fake{}
	s.WithHolds(holds)
	f := released(t, s, ctx, 10)
	packed := pickAndPack(t, s, ctx, f.shipment)
	product := f.shipment.Lines[0].ProductID
	holds.Set(product, qn(10), qn(8))
	in := postIn(f.shipment)
	_, err := s.PostPackingSlip(ctx, f.shipment.ID, packed.Shipment.Version, in, idem("held"))
	if problemCode(err) != store.CodeQualityHold {
		t.Fatalf("held ship: %v, want %s", err, store.CodeQualityHold)
	}
	got, err := s.GetShipment(ctx, f.shipment.ID)
	if err != nil || got.Status != packed.Shipment.Status || !got.Lines[0].Shipped.IsZero() {
		t.Fatalf("held ship moved stock: %+v %v", got, err)
	}
	holds.Set(product, qn(10), decimal.Zero)
	slip, err := s.PostPackingSlip(ctx, f.shipment.ID, packed.Shipment.Version, in, idem("released"))
	if err != nil || slip.ID == "" {
		t.Fatalf("ship after usage decision: %+v %v", slip, err)
	}
}

func TestPostPackingSlip_RepeatedShipmentsCannotEatHeldStock(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	holds := &issueclaim.Fake{}
	s.WithHolds(holds)
	warehouse, entity, product := ids.New(), ids.New(), ids.New()
	keepOpen(t, s, ctx, warehouse, entity)
	holds.Set(product, qn(10), qn(8))
	a, inA := packedIn(t, s, ctx, warehouse, entity, []string{product}, 2)
	b, inB := packedIn(t, s, ctx, warehouse, entity, []string{product}, 2)
	if _, err := s.PostPackingSlip(ctx, a.Shipment.ID, a.Shipment.Version, inA, idem("a")); err != nil {
		t.Fatalf("shipping the free 2: %v", err)
	}
	_, err := s.PostPackingSlip(ctx, b.Shipment.ID, b.Shipment.Version, inB, idem("b"))
	if problemCode(err) != store.CodeQualityHold {
		t.Fatalf("second shipment of the same free 2 against 8 held of 10: %v, want %s", err, store.CodeQualityHold)
	}
	if got, gerr := s.GetShipment(ctx, b.Shipment.ID); gerr != nil || !got.Lines[0].Shipped.IsZero() {
		t.Fatalf("refused shipment moved quantity: %+v %v", got, gerr)
	}
	if !holds.Claimed(product, issueclaim.Ship).Equal(qn(2)) {
		t.Fatalf("claimed %s, want only the shipped 2", holds.Claimed(product, issueclaim.Ship))
	}
	holds.Set(product, qn(10), decimal.Zero)
	if _, err := s.PostPackingSlip(ctx, b.Shipment.ID, b.Shipment.Version, inB, idem("b2")); err != nil {
		t.Fatalf("after the usage decision: %v", err)
	}
}

func TestPostPackingSlip_SumsLinesOfOneProductBeforeChecking(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	holds := &issueclaim.Fake{}
	s.WithHolds(holds)
	product, warehouse, entity := ids.New(), ids.New(), ids.New()
	keepOpen(t, s, ctx, warehouse, entity)
	holds.Set(product, qn(20), qn(10))
	packed, in := packedIn(t, s, ctx, warehouse, entity, []string{product, product}, 6, 6)
	_, err := s.PostPackingSlip(ctx, packed.Shipment.ID, packed.Shipment.Version, in, idem("two-lines"))
	if problemCode(err) != store.CodeQualityHold {
		t.Fatalf("6+6 against 10 free: %v, want %s", err, store.CodeQualityHold)
	}
	if !holds.Claimed(product, issueclaim.Ship).IsZero() {
		t.Fatalf("refused post left %s claimed", holds.Claimed(product, issueclaim.Ship))
	}
}

// racingClaimer holds the first claim until a competing post of the same
// shipment version has taken its own claim, so both posts pass every check and
// only the version bump decides which one commits.
type racingClaimer struct {
	*issueclaim.Fake
	mu      sync.Mutex
	calls   int
	compete func()
	rival   chan struct{}
}

func (r *racingClaimer) Claim(ctx context.Context, c issueclaim.Claim) error {
	err := r.Fake.Claim(ctx, c)
	r.mu.Lock()
	r.calls++
	first := r.calls == 1
	r.mu.Unlock()
	if !first {
		close(r.rival)
		return err
	}
	go r.compete()
	select {
	case <-r.rival:
	case <-time.After(10 * time.Second):
		return errors.New("the competing post never claimed")
	}
	return err
}

// A double submit of one packing slip at the same version: one post commits,
// the other fails, and the failed one's withdrawal must not take
// the committed shipment's quantity out of the SHIP tally.
func TestPostPackingSlip_FailedDoubleSubmitKeepsTheCommittedClaim(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	warehouse, entity, product := ids.New(), ids.New(), ids.New()
	keepOpen(t, s, ctx, warehouse, entity)
	packed, in := packedIn(t, s, ctx, warehouse, entity, []string{product}, 2)
	racer := &racingClaimer{Fake: &issueclaim.Fake{}, rival: make(chan struct{})}
	racer.Set(product, qn(10), qn(6))
	s.WithHolds(racer)
	errs := make(chan error, 2)
	racer.compete = func() {
		_, err := s.PostPackingSlip(ctx, packed.Shipment.ID, packed.Shipment.Version, in, idem("second"))
		errs <- err
	}
	_, err := s.PostPackingSlip(ctx, packed.Shipment.ID, packed.Shipment.Version, in, idem("first"))
	errs <- err
	var ok, failed int
	for range 2 {
		// The loser fails on the shipped-within-packed check or the version.
		if err := <-errs; err == nil {
			ok++
		} else if problemCode(err) == store.CodeQualityHold {
			t.Fatalf("free quantity refused: %v", err)
		} else {
			failed++
		}
	}
	if ok != 1 || failed != 1 {
		t.Fatalf("%d posts committed and %d failed, want 1 and 1", ok, failed)
	}
	if got := racer.Claimed(product, issueclaim.Ship); !got.Equal(qn(2)) {
		t.Fatalf("SHIP tally %s after the double submit, want the committed 2", got)
	}
}

type brokenInventory struct{}

func (brokenInventory) Claim(context.Context, issueclaim.Claim) error {
	return errors.New("dial tcp inventory.internal:8080: connection refused")
}
func (brokenInventory) Release(context.Context, issueclaim.Claim) {}

func TestPostPackingSlip_InventoryOutageIsAGeneric503(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	s.WithHolds(brokenInventory{})
	f := released(t, s, ctx, 1)
	packed := pickAndPack(t, s, ctx, f.shipment)
	_, err := s.PostPackingSlip(ctx, f.shipment.ID, packed.Shipment.Version, postIn(f.shipment), idem("outage"))
	if problemCode(err) != "COM-0503" || strings.Contains(err.Error(), "inventory.internal") {
		t.Fatalf("outage: %v, want COM-0503 without the URL", err)
	}
}

func TestHoldProperty_ShipNeverMovesHeldUntilUsageDecision(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	holds := &issueclaim.Fake{}
	s.WithHolds(holds)
	rapid.Check(t, func(rt *rapid.T) {
		const physical = 20
		warehouse, entity, product := ids.New(), ids.New(), ids.New()
		keepOpen(t, s, ctx, warehouse, entity)
		holds.Set(product, qn(physical), decimal.Zero)
		held, shipped := 0, 0
		for i, steps := 0, rapid.IntRange(3, 6).Draw(rt, "steps"); i < steps; i++ {
			switch rapid.IntRange(0, 2).Draw(rt, "op") {
			case 0:
				held = rapid.IntRange(0, physical).Draw(rt, "held")
				holds.Set(product, qn(physical), qn(int64(held)))
			case 1:
				held = 0
				holds.Set(product, qn(physical), decimal.Zero)
			default:
				qty := rapid.IntRange(1, 8).Draw(rt, "qty")
				packed, in := packedIn(t, s, ctx, warehouse, entity, []string{product}, int64(qty))
				_, err := s.PostPackingSlip(ctx, packed.Shipment.ID, packed.Shipment.Version, in, idem("p-"+ids.New()))
				if held > 0 && qty > physical-held-shipped {
					if problemCode(err) != store.CodeQualityHold {
						rt.Fatalf("held %d of %d, %d already shipped, ship %d: %v, want %s", held, physical, shipped, qty, err, store.CodeQualityHold)
					}
					if got, gerr := s.GetShipment(ctx, packed.Shipment.ID); gerr != nil || !got.Lines[0].Shipped.IsZero() {
						rt.Fatalf("held ship moved quantity: %+v %v", got, gerr)
					}
					continue
				}
				if err != nil {
					rt.Fatalf("free ship of %d (held %d, %d shipped): %v", qty, held, shipped, err)
				}
				shipped += qty
			}
		}
	})
}
