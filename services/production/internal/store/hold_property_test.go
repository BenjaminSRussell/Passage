package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/issueclaim"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

func consumeOrderIn(t *testing.T, s *Postgres, ctx context.Context, warehouse string, items ...string) *stepper {
	t.Helper()
	var bom []domain.BOMComponent
	for i, item := range items {
		bom = append(bom, domain.BOMComponent{LineNo: fmt.Sprint((i + 1) * 10), ItemID: item, QtyPer: d("1"), UnitCost: d("10")})
	}
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
	return startedOrder(t, s, ctx, NewOrder{LegalEntityID: ids.New(), ProductID: ids.New(), SiteID: ids.New(), WarehouseID: warehouse,
		Currency: "USD", Quantity: d("100"), DeliveryDate: &date, OriginType: "Manual", Lines: lines, Operations: ops, IdempotencyKey: key, RequestHash: "h-" + key})
}

func consumeOrder(t *testing.T, s *Postgres, ctx context.Context, item string) *stepper {
	t.Helper()
	return consumeOrderIn(t, s, ctx, ids.New(), item)
}

func issue(lineNo, amount string) NewJournal {
	return NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: lineNo, Qty: d(amount), UnitCost: qty("10")}}}
}

func TestStore_ConsumeRefusesHeldQuantity(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(91))
	holds := &issueclaim.Fake{}
	s.WithHolds(holds)
	item := ids.New()
	st := consumeOrder(t, s, ctx, item)
	holds.Set(item, d("100"), d("80"))
	j := st.mustJournal(issue("10", "50"))
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

func TestStore_RepeatedConsumesCannotEatHeldStock(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(94))
	holds := &issueclaim.Fake{}
	s.WithHolds(holds)
	item := ids.New()
	st := consumeOrder(t, s, ctx, item)
	holds.Set(item, d("10"), d("8"))
	st.mustPost(st.mustJournal(issue("10", "2")).ID)
	j := st.mustJournal(issue("10", "2"))
	_, err := st.post(j.ID, nil)
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeQualityHold)
	if got := st.refresh().Lines[0].QtyConsumed; !got.Equal(d("2")) {
		t.Fatalf("consumed %s after a refused second consume, want 2", got)
	}
	holds.Set(item, d("10"), decimal.Zero)
	st.mustPost(j.ID)
}

func TestStore_ConsumeSumsLinesOfOneItemBeforeChecking(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(95))
	holds := &issueclaim.Fake{}
	s.WithHolds(holds)
	item := ids.New()
	st := consumeOrderIn(t, s, ctx, ids.New(), item, item)
	holds.Set(item, d("20"), d("10"))
	j := st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{
		{LineNo: "10", Qty: d("6"), UnitCost: qty("10")}, {LineNo: "20", Qty: d("6"), UnitCost: qty("10")}}})
	_, err := st.post(j.ID, nil)
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeQualityHold)
	if !holds.Claimed(item, issueclaim.Consume).IsZero() {
		t.Fatalf("refused journal left %s claimed", holds.Claimed(item, issueclaim.Consume))
	}
}

type brokenInventory struct{}

func (brokenInventory) Claim(context.Context, issueclaim.Claim) error {
	return errors.New("dial tcp inventory.internal:8080: connection refused")
}
func (brokenInventory) Release(context.Context, issueclaim.Claim) {}

func TestStore_InventoryOutageDuringConsumeIsAGeneric503(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(97))
	s.WithHolds(brokenInventory{})
	st := consumeOrder(t, s, ctx, ids.New())
	_, err := st.post(st.mustJournal(issue("10", "1")).ID, nil)
	expectProblem(t, err, http.StatusServiceUnavailable, "COM-0503")
	if strings.Contains(err.Error(), "inventory.internal") {
		t.Fatalf("outage detail leaks the inventory URL: %v", err)
	}
}

func TestStore_StartBackflushRefusesHeldQuantity(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(92))
	holds := &issueclaim.Fake{}
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
	if !holds.Claimed(item, issueclaim.Consume).IsZero() {
		t.Fatalf("refused start left %s claimed", holds.Claimed(item, issueclaim.Consume))
	}
	holds.Set(item, d("10"), decimal.Zero)
	st.must(domain.CmdStart, nil)
	if got := st.refresh(); got.Status != domain.Started || !got.Lines[0].QtyConsumed.Equal(d("10")) {
		t.Fatalf("start after usage decision: status %s consumed %s", got.Status, got.Lines[0].QtyConsumed)
	}
}

// TestHoldProperty_ConsumeNeverMovesHeldUntilUsageDecision is wave 33 AC2:
// random hold, release (usage decision) and consume sequences against one item
// never consume held quantity. Physical stays fixed, as it does in inventory.
func TestHoldProperty_ConsumeNeverMovesHeldUntilUsageDecision(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(93))
	holds := &issueclaim.Fake{}
	s.WithHolds(holds)
	rapid.Check(t, func(rt *rapid.T) {
		const physical = 100
		item := ids.New()
		st := consumeOrder(t, s, ctx, item)
		holds.Set(item, d("100"), decimal.Zero)
		consumed, held := 0, 0
		for i, steps := 0, rapid.IntRange(4, 10).Draw(rt, "steps"); i < steps; i++ {
			switch rapid.IntRange(0, 2).Draw(rt, "op") {
			case 0:
				held = rapid.IntRange(0, physical).Draw(rt, "held")
				holds.Set(item, d("100"), decimal.NewFromInt(int64(held)))
			case 1:
				held = 0
				holds.Set(item, d("100"), decimal.Zero)
			default:
				n := rapid.IntRange(1, 40).Draw(rt, "qty")
				if consumed+n > physical {
					continue
				}
				j, err := st.journal(issue("10", fmt.Sprint(n)))
				if err != nil {
					rt.Fatalf("draft: %v", err)
				}
				_, err = st.post(j.ID, nil)
				if held > 0 && n > physical-held-consumed {
					if _, code := problemStatus(err); code != CodeQualityHold {
						rt.Fatalf("held %d of %d, %d consumed, consume %d: %v, want %s", held, physical, consumed, n, err, CodeQualityHold)
					}
					if o := st.refresh(); !o.Lines[0].QtyConsumed.Equal(d(fmt.Sprint(consumed))) {
						rt.Fatalf("held consume booked %s, model %d", o.Lines[0].QtyConsumed, consumed)
					}
					continue
				}
				if err != nil {
					rt.Fatalf("consume of free %d (held %d, %d consumed): %v", n, held, consumed, err)
				}
				consumed += n
			}
		}
	})
}
