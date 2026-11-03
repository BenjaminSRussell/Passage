package store

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/inventory"
)

func pickReady(t *testing.T, st *Store, ctx context.Context, wh, item, qty string) domain.Work {
	t.Helper()
	mustLocations(t, st, ctx, wh, loc("P-01", "PICK", 1, domain.Capacity{AllowMixedItems: true}))
	mustDirective(t, st, ctx, directive(wh, "PURCHASE_PUTAWAY", domain.DirectionPut, domain.StrategyConsolidate, "PICK"))
	mustDirective(t, st, ctx, directive(wh, "SALES_PICK", domain.DirectionPick, domain.StrategyFIFO, "PICK"))
	receive(t, st, ctx, wh, item, qty, qty)
	w := plan(t, st, ctx, wh, "SALES_PICK", domain.NewLine{ItemID: item, Direction: domain.DirectionPick, Qty: dq(qty), Weight: dq(qty)})
	if got := assign(t, st, ctx, wh, "picker", time.Now()); got == nil || got.ID != w.ID {
		t.Fatalf("assign pick = %+v", got)
	}
	return w
}

func TestStore_PickRefusesHeldQuantity(t *testing.T) {
	st, ctx, wh := setup(t)
	holds := &inventory.MapHold{}
	st.WithHolds(holds)
	item := "BOLT"
	w := pickReady(t, st, ctx, wh, item, "10")
	holds.Set(item, dq("10"), dq("8"))
	_, _, err := st.ConfirmLine(ctx, key("held"), ConfirmInput{WorkID: w.ID, LineNo: 1, Qty: dq("10"), User: "picker", Now: time.Now()})
	assertCode(t, err, domain.CodeHoldBlocked)
	if q := stockAt(t, st, ctx, wh, "P-01", item); !q.Equal(dq("10")) {
		t.Fatalf("held pick moved stock: %s", q)
	}
	holds.Set(item, dq("10"), decimal.Zero)
	done, _, err := st.ConfirmLine(ctx, key("released"), ConfirmInput{WorkID: w.ID, LineNo: 1, Qty: dq("10"), User: "picker", Now: time.Now()})
	if err != nil || done.Status != domain.WorkCompleted {
		t.Fatalf("pick after usage decision: %+v %v", done, err)
	}
	if q := stockAt(t, st, ctx, wh, "P-01", item); !q.IsZero() {
		t.Fatalf("after release P-01 holds %s", q)
	}
}

func TestStore_ShortPickRefusesHeldQuantity(t *testing.T) {
	st, ctx, wh := setup(t)
	holds := &inventory.MapHold{}
	st.WithHolds(holds)
	w := pickReady(t, st, ctx, wh, "BOLT", "10")
	holds.Set("BOLT", dq("10"), dq("8"))
	_, _, err := st.ExceptWork(ctx, key("short-held"), ExceptionInput{WorkID: w.ID, Action: ActionShortPick, LineNo: 1, Qty: dq("5"), Reason: "QUALITY_HOLD", User: "picker", Now: time.Now()})
	assertCode(t, err, domain.CodeHoldBlocked)
	if q := stockAt(t, st, ctx, wh, "P-01", "BOLT"); !q.Equal(dq("10")) {
		t.Fatalf("held short pick moved stock: %s", q)
	}
}

// TestHoldProperty_PickNeverMovesHeldUntilUsageDecision is wave 33 AC2: random
// hold, release (usage decision) and pick sequences never move held quantity.
func TestHoldProperty_PickNeverMovesHeldUntilUsageDecision(t *testing.T) {
	st, ctx, _ := setup(t)
	holds := &inventory.MapHold{}
	st.WithHolds(holds)
	rapid.Check(t, func(rt *rapid.T) {
		wh := ids.New()
		item := "BOLT-" + ids.New()
		onHand := rapid.IntRange(5, 30).Draw(rt, "onHand")
		mustLocations(t, st, ctx, wh, loc("P-01", "PICK", 1, domain.Capacity{AllowMixedItems: true}))
		mustDirective(t, st, ctx, directive(wh, "PURCHASE_PUTAWAY", domain.DirectionPut, domain.StrategyConsolidate, "PICK"))
		mustDirective(t, st, ctx, directive(wh, "SALES_PICK", domain.DirectionPick, domain.StrategyFIFO, "PICK"))
		receive(t, st, ctx, wh, item, itoa(onHand), itoa(onHand))
		held := 0
		holds.Set(item, dq(itoa(onHand)), decimal.Zero)
		steps := rapid.IntRange(8, 25).Draw(rt, "steps")
		for i := 0; i < steps; i++ {
			switch rapid.IntRange(0, 2).Draw(rt, "op") {
			case 0:
				held = rapid.IntRange(0, onHand).Draw(rt, "held")
				holds.Set(item, dq(itoa(onHand)), dq(itoa(held)))
			case 1:
				held = 0
				holds.Set(item, dq(itoa(onHand)), decimal.Zero)
			default:
				if onHand == 0 {
					continue
				}
				qty := rapid.IntRange(1, onHand).Draw(rt, "qty")
				w := plan(t, st, ctx, wh, "SALES_PICK", domain.NewLine{ItemID: item, Direction: domain.DirectionPick, Qty: dq(itoa(qty)), Weight: dq(itoa(qty))})
				if w.Status != domain.WorkOpen {
					rt.Fatalf("plan pick of %d from %d: status %s", qty, onHand, w.Status)
				}
				if got := assign(t, st, ctx, wh, "picker", time.Now()); got == nil {
					rt.Fatalf("assign pick")
				}
				_, _, err := st.ConfirmLine(ctx, key("p-"+ids.New()), ConfirmInput{WorkID: w.ID, LineNo: 1, Qty: dq(itoa(qty)), User: "picker", Now: time.Now()})
				free := onHand - held
				if held > 0 && qty > free {
					assertCode(t, err, domain.CodeHoldBlocked)
					if q := stockAt(t, st, ctx, wh, "P-01", item); !q.Equal(dq(itoa(onHand))) {
						rt.Fatalf("held pick moved stock: have %s want %d", q, onHand)
					}
					if _, _, cerr := st.ExceptWork(ctx, key("c-"+ids.New()), ExceptionInput{WorkID: w.ID, Action: ActionCancel, Reason: "QUALITY_HOLD", User: "picker", Now: time.Now()}); cerr != nil {
						rt.Fatalf("cancel blocked pick: %v", cerr)
					}
					continue
				}
				if err != nil {
					rt.Fatalf("pick of free %d (held %d of %d): %v", qty, held, onHand, err)
				}
				onHand -= qty
				if q := stockAt(t, st, ctx, wh, "P-01", item); !q.Equal(dq(itoa(onHand))) {
					rt.Fatalf("stock %s want %d", q, onHand)
				}
				if held > onHand {
					held = onHand
					holds.Set(item, dq(itoa(onHand)), dq(itoa(held)))
				}
			}
		}
	})
}

func itoa(n int) string { return strconv.Itoa(n) }
