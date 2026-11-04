package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/issueclaim"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
)

func pickSetup(t *testing.T, st *Store, ctx context.Context, wh, item, qty string) {
	t.Helper()
	mustLocations(t, st, ctx, wh, loc("P-01", "PICK", 1, domain.Capacity{AllowMixedItems: true}))
	mustDirective(t, st, ctx, directive(wh, "PURCHASE_PUTAWAY", domain.DirectionPut, domain.StrategyConsolidate, "PICK"))
	mustDirective(t, st, ctx, directive(wh, "SALES_PICK", domain.DirectionPick, domain.StrategyFIFO, "PICK"))
	receive(t, st, ctx, wh, item, qty, qty)
}

func pickReady(t *testing.T, st *Store, ctx context.Context, wh, item, qty string) domain.Work {
	t.Helper()
	pickSetup(t, st, ctx, wh, item, qty)
	return nextPick(t, st, ctx, wh, item, qty)
}

func nextPick(t *testing.T, st *Store, ctx context.Context, wh, item, qty string) domain.Work {
	t.Helper()
	w := plan(t, st, ctx, wh, "SALES_PICK", domain.NewLine{ItemID: item, Direction: domain.DirectionPick, Qty: dq(qty), Weight: dq(qty)})
	if got := assign(t, st, ctx, wh, "picker", time.Now()); got == nil || got.ID != w.ID {
		t.Fatalf("assign pick = %+v", got)
	}
	return w
}

func pickConfirm(st *Store, ctx context.Context, w domain.Work, qty string) error {
	_, _, err := st.ConfirmLine(ctx, key("c-"+ids.New()), ConfirmInput{WorkID: w.ID, LineNo: 1, Qty: dq(qty), User: "picker", Now: time.Now()})
	return err
}

func TestStore_PickRefusesHeldQuantity(t *testing.T) {
	st, ctx, wh := setup(t)
	holds := &issueclaim.Fake{}
	st.WithHolds(holds)
	item := ids.New()
	w := pickReady(t, st, ctx, wh, item, "10")
	holds.Set(item, dq("10"), dq("8"))
	assertCode(t, pickConfirm(st, ctx, w, "10"), domain.CodeHoldBlocked)
	if q := stockAt(t, st, ctx, wh, "P-01", item); !q.Equal(dq("10")) {
		t.Fatalf("held pick moved stock: %s", q)
	}
	holds.Set(item, dq("10"), decimal.Zero)
	if err := pickConfirm(st, ctx, w, "10"); err != nil {
		t.Fatalf("pick after usage decision: %v", err)
	}
	if q := stockAt(t, st, ctx, wh, "P-01", item); !q.IsZero() {
		t.Fatalf("after release P-01 holds %s", q)
	}
}

func TestStore_ShortPickRefusesHeldQuantity(t *testing.T) {
	st, ctx, wh := setup(t)
	holds := &issueclaim.Fake{}
	st.WithHolds(holds)
	item := ids.New()
	w := pickReady(t, st, ctx, wh, item, "10")
	holds.Set(item, dq("10"), dq("8"))
	_, _, err := st.ExceptWork(ctx, key("short-held"), ExceptionInput{WorkID: w.ID, Action: ActionShortPick, LineNo: 1, Qty: dq("5"), Reason: "QUALITY_HOLD", User: "picker", Now: time.Now()})
	assertCode(t, err, domain.CodeHoldBlocked)
	if q := stockAt(t, st, ctx, wh, "P-01", item); !q.Equal(dq("10")) {
		t.Fatalf("held short pick moved stock: %s", q)
	}
	if !holds.Claimed(item, issueclaim.Pick).IsZero() {
		t.Fatalf("refused short pick left %s claimed", holds.Claimed(item, issueclaim.Pick))
	}
}

func TestStore_RepeatedPicksCannotEatHeldStock(t *testing.T) {
	st, ctx, wh := setup(t)
	holds := &issueclaim.Fake{}
	st.WithHolds(holds)
	item := ids.New()
	pickSetup(t, st, ctx, wh, item, "10")
	holds.Set(item, dq("10"), dq("8"))
	if err := pickConfirm(st, ctx, nextPick(t, st, ctx, wh, item, "2"), "2"); err != nil {
		t.Fatalf("picking the free 2: %v", err)
	}
	second := nextPick(t, st, ctx, wh, item, "2")
	assertCode(t, pickConfirm(st, ctx, second, "2"), domain.CodeHoldBlocked)
	if q := stockAt(t, st, ctx, wh, "P-01", item); !q.Equal(dq("8")) {
		t.Fatalf("stock %s after a refused second pick, want 8", q)
	}
}

func TestStore_PickChecksTheWarehouseNotTheLocation(t *testing.T) {
	st, ctx, wh := setup(t)
	holds := &issueclaim.Fake{}
	st.WithHolds(holds)
	item := ids.New()
	pickSetup(t, st, ctx, wh, item, "10")
	holds.Set(item, dq("20"), dq("8"))
	if err := pickConfirm(st, ctx, nextPick(t, st, ctx, wh, item, "3"), "3"); err != nil {
		t.Fatalf("3 from P-01 while the 8 held sit elsewhere in the warehouse: %v", err)
	}
}

func TestStore_PickOfAnItemThatIsNotAProductFailsClosed(t *testing.T) {
	st, ctx, wh := setup(t)
	st.WithHolds(&issueclaim.Fake{})
	w := pickReady(t, st, ctx, wh, "BOLT", "10")
	err := pickConfirm(st, ctx, w, "10")
	assertCode(t, err, domain.CodeHoldBlocked)
	if !strings.Contains(err.Error(), "not an inventory product id") {
		t.Fatalf("the refusal must say why, not report a hold: %v", err)
	}
	if q := stockAt(t, st, ctx, wh, "P-01", "BOLT"); !q.Equal(dq("10")) {
		t.Fatalf("unchecked item moved stock: %s", q)
	}
}

type brokenInventory struct{}

func (brokenInventory) Claim(context.Context, issueclaim.Claim) error {
	return errors.New("dial tcp inventory.internal:8080: connection refused")
}
func (brokenInventory) Release(context.Context, issueclaim.Claim) {}

func TestStore_InventoryOutageIsAGeneric503(t *testing.T) {
	st, ctx, wh := setup(t)
	st.WithHolds(brokenInventory{})
	item := ids.New()
	w := pickReady(t, st, ctx, wh, item, "10")
	err := pickConfirm(st, ctx, w, "10")
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Code != "COM-0503" || p.Status != 503 || strings.Contains(p.Detail, "inventory.internal") {
		t.Fatalf("outage: %v, want COM-0503 without the URL", err)
	}
}

func TestStore_PickWithoutALegalEntityIsNotAQualityHold(t *testing.T) {
	st, ctx, wh := setup(t)
	st.WithHolds(&issueclaim.Fake{})
	item := ids.New()
	w := pickReady(t, st, ctx, wh, item, "10")
	noEntity := tenant.With(ctx, tenant.Principal{TenantID: "00000000-0000-0000-0000-000000000001", UserID: "user-test", Duties: map[string]struct{}{}})
	err := pickConfirm(st, noEntity, w, "10")
	var p *httpx.Problem
	var de *domain.Error
	if errors.As(err, &de) || !errors.As(err, &p) || p.Status != 403 {
		t.Fatalf("no legal entity: %v, want a 403 that is not QUALITY_HOLD", err)
	}
}

// TestHoldProperty_PickNeverMovesHeldUntilUsageDecision is wave 33 AC2: random
// hold, release (usage decision) and pick sequences never pick held quantity.
// Inventory's physical stays fixed while picks are not journals there.
func TestHoldProperty_PickNeverMovesHeldUntilUsageDecision(t *testing.T) {
	st, ctx, _ := setup(t)
	holds := &issueclaim.Fake{}
	st.WithHolds(holds)
	rapid.Check(t, func(rt *rapid.T) {
		wh, item := ids.New(), ids.New()
		const physical = 30
		pickSetup(t, st, ctx, wh, item, strconv.Itoa(physical))
		held, picked := 0, 0
		holds.Set(item, dq(strconv.Itoa(physical)), decimal.Zero)
		for i, steps := 0, rapid.IntRange(6, 14).Draw(rt, "steps"); i < steps; i++ {
			switch rapid.IntRange(0, 2).Draw(rt, "op") {
			case 0:
				held = rapid.IntRange(0, physical).Draw(rt, "held")
				holds.Set(item, dq(strconv.Itoa(physical)), dq(strconv.Itoa(held)))
			case 1:
				held = 0
				holds.Set(item, dq(strconv.Itoa(physical)), decimal.Zero)
			default:
				qty := rapid.IntRange(1, 10).Draw(rt, "qty")
				if picked+qty > physical {
					continue
				}
				w := nextPick(t, st, ctx, wh, item, strconv.Itoa(qty))
				err := pickConfirm(st, ctx, w, strconv.Itoa(qty))
				if held > 0 && qty > physical-held-picked {
					assertCode(t, err, domain.CodeHoldBlocked)
					if q := stockAt(t, st, ctx, wh, "P-01", item); !q.Equal(dq(strconv.Itoa(physical - picked))) {
						rt.Fatalf("held pick moved stock: have %s want %d", q, physical-picked)
					}
					if _, _, cerr := st.ExceptWork(ctx, key("c-"+ids.New()), ExceptionInput{WorkID: w.ID, Action: ActionCancel, Reason: "QUALITY_HOLD", User: "picker", Now: time.Now()}); cerr != nil {
						rt.Fatalf("cancel blocked pick: %v", cerr)
					}
					continue
				}
				if err != nil {
					rt.Fatalf("pick of free %d (held %d, %d picked): %v", qty, held, picked, err)
				}
				picked += qty
			}
		}
	})
}
