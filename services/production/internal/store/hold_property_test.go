package store

import (
	"context"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/inventory"
)

func consumeOrder(t *testing.T, s *Postgres, ctx context.Context, item string) *stepper {
	t.Helper()
	bom := []domain.BOMComponent{{LineNo: "10", ItemID: item, QtyPer: d("1"), UnitCost: d("10")}}
	lines, err := domain.Explode(bom, d("100"))
	if err != nil {
		t.Fatal(err)
	}
	ops, err := domain.PlanOperations([]domain.Operation{{OperNumber: 10, ResourceID: ids.New(), RunHPer: d("0.1"), LaborRate: d("1"), OverheadRate: d("1")}}, d("100"))
	if err != nil {
		t.Fatal(err)
	}
	date := "2026-11-30"
	key := "hold-" + ids.New()
	return startedOrder(t, s, ctx, NewOrder{LegalEntityID: ids.New(), ProductID: ids.New(), SiteID: ids.New(), WarehouseID: ids.New(),
		Currency: "USD", Quantity: d("100"), DeliveryDate: &date, OriginType: "Manual", Lines: lines, Operations: ops, IdempotencyKey: key, RequestHash: "h-" + key})
}

func TestStore_ConsumeRefusesHeldQuantity(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(91))
	holds := &inventory.MapHold{}
	s.WithHolds(holds)
	item := ids.New()
	st := consumeOrder(t, s, ctx, item)
	holds.Set(item, d("100"), d("80"))
	j := st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d("50"), UnitCost: qty("10")}}})
	_, err := st.post(j.ID, nil)
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeQualityHold)
	if o := st.refresh(); !o.Lines[0].QtyConsumed.IsZero() {
		t.Fatalf("held consume booked %s", o.Lines[0].QtyConsumed)
	}
	holds.Set(item, d("100"), decimal.Zero)
	posted := st.mustPost(j.ID)
	if !posted.Posted || !st.refresh().Lines[0].QtyConsumed.Equal(d("50")) {
		t.Fatalf("consume after usage decision: %+v", posted)
	}
}

func TestStore_StartBackflushRefusesHeldQuantity(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(92))
	holds := &inventory.MapHold{}
	s.WithHolds(holds)
	item := ids.New()
	bom := []domain.BOMComponent{{LineNo: "10", ItemID: item, QtyPer: d("1"), UnitCost: d("10"), Flushing: domain.FlushStart}}
	lines, err := domain.Explode(bom, d("10"))
	if err != nil {
		t.Fatal(err)
	}
	ops, err := domain.PlanOperations([]domain.Operation{{OperNumber: 10, ResourceID: ids.New(), RunHPer: d("0.1"), LaborRate: d("1"), OverheadRate: d("1")}}, d("10"))
	if err != nil {
		t.Fatal(err)
	}
	date := "2026-11-30"
	holds.Set(item, d("10"), d("10"))
	le := ids.New()
	allProfile(t, s, ctx, le)
	o, _, err := s.CreateOrder(ctx, NewOrder{LegalEntityID: le, ProductID: ids.New(), SiteID: ids.New(), WarehouseID: ids.New(),
		Currency: "USD", Quantity: d("10"), DeliveryDate: &date, OriginType: "Manual", Lines: lines, Operations: ops, IdempotencyKey: "start-hold", RequestHash: "start-hold"})
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	for _, cmd := range []domain.Command{domain.CmdEstimate, domain.CmdSchedule, domain.CmdRelease} {
		st.must(cmd, nil)
	}
	_, err = st.step(domain.CmdStart, nil)
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeQualityHold)
	if got := st.refresh(); got.Status != domain.Released || !got.Lines[0].QtyConsumed.IsZero() {
		t.Fatalf("held start moved the order: %+v", got)
	}
	holds.Set(item, d("10"), decimal.Zero)
	st.must(domain.CmdStart, nil)
	if got := st.refresh(); got.Status != domain.Started || !got.Lines[0].QtyConsumed.Equal(d("10")) {
		t.Fatalf("start after usage decision: status %s consumed %s", got.Status, got.Lines[0].QtyConsumed)
	}
}

// TestHoldProperty_ConsumeNeverMovesHeldUntilUsageDecision is wave 33 AC2:
// random hold, release (usage decision) and consume sequences never move held
// quantity.
func TestHoldProperty_ConsumeNeverMovesHeldUntilUsageDecision(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(93))
	holds := &inventory.MapHold{}
	s.WithHolds(holds)
	rapid.Check(t, func(rt *rapid.T) {
		item := ids.New()
		st := consumeOrder(t, s, ctx, item)
		physical := 100
		consumed := 0
		steps := rapid.IntRange(6, 16).Draw(rt, "steps")
		held := 0
		for i := 0; i < steps; i++ {
			switch rapid.IntRange(0, 2).Draw(rt, "op") {
			case 0:
				held = rapid.IntRange(0, physical).Draw(rt, "held")
				holds.Set(item, d(itoa(physical)), d(itoa(held)))
			case 1:
				held = 0
				holds.Set(item, d(itoa(physical)), decimal.Zero)
			default:
				left := 100 - consumed
				if left == 0 {
					continue
				}
				n := rapid.IntRange(1, left).Draw(rt, "qty")
				j, err := st.journal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d(itoa(n)), UnitCost: qty("10")}}})
				if err != nil {
					rt.Fatalf("draft: %v", err)
				}
				_, err = st.post(j.ID, nil)
				free := physical - held
				if held > 0 && n > free {
					_, code := problemStatus(err)
					if code != CodeQualityHold {
						rt.Fatalf("held %d of %d, consume %d: %v, want %s", held, physical, n, err, CodeQualityHold)
					}
					if o := st.refresh(); !o.Lines[0].QtyConsumed.Equal(d(itoa(consumed))) {
						rt.Fatalf("held consume booked %s, model %d", o.Lines[0].QtyConsumed, consumed)
					}
					continue
				}
				if err != nil {
					rt.Fatalf("consume of free %d (held %d): %v", n, held, err)
				}
				consumed += n
				physical -= n
				if held > physical {
					held = physical
				}
				holds.Set(item, d(itoa(physical)), d(itoa(held)))
			}
		}
	})
}

func itoa(n int) string { return decimal.NewFromInt(int64(n)).String() }
