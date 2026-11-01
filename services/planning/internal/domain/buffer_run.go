package domain

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// RunBuffer is the stored site-scope DDMRP buffer of a priority-based item, as
// a plan run reads it (FR-22.16, BR-22.11): the decoupled lead time, the order
// quantity the last recalculation computed (top of green minus the net-flow
// position, with MOQ and order multiple) and the zone the net-flow position
// is in, with its planning priority (net-flow position / top of green).
type RunBuffer struct {
	ItemID     string
	DLTDays    decimal.Decimal
	PlannedQty decimal.Decimal
	Priority   decimal.Decimal
	Zone       string
}

// bufferRefPrefix starts the demand reference of a buffer replenishment; the
// zone follows, so the red-zone message needs no second lookup.
const bufferRefPrefix = "ddmrp:"

// ApplyBuffers turns the buffered items of a snapshot into buffer-managed
// items (priority-based planning, FR-22.16, BR-22.11): an item with a stored
// buffer is replenished by the buffer's planned quantity, not netted against
// its own demand. Its independent demand, receipts, on-hand and safety stock
// are dropped (the net-flow position the quantity came from already counts
// them), its min/max is switched off, and one demand of the planned quantity,
// referenced "ddmrp:<zone>:<item>", is due after the decoupled lead time (at
// least the item's lead time, so the order is not released in the past). The
// item is removed from the components of every other item: a decoupling point
// absorbs the dependent demand above it. The item's own components still
// explode from its replenishment order. A buffer whose planned quantity is
// zero (green zone) leaves the item with no demand. Items without a buffer,
// and buffers of items outside the snapshot, are untouched. The input is not
// modified.
func ApplyBuffers(items []Item, buffers []RunBuffer) []Item {
	if len(buffers) == 0 {
		return items
	}
	byItem := make(map[string]RunBuffer, len(buffers))
	for _, b := range buffers {
		byItem[b.ItemID] = b
	}
	out := slices.Clone(items)
	managed := map[string]bool{}
	for i := range out {
		b, ok := byItem[out[i].ID]
		if !ok {
			continue
		}
		managed[out[i].ID] = true
		it := &out[i]
		it.Coverage, it.PeriodDays = CoverageRequirement, 0
		it.OnHand, it.SafetyStock = decimal.Zero, decimal.Zero
		it.ReorderMin, it.ReorderMax = decimal.Zero, decimal.Zero
		it.Receipts, it.Demand = nil, nil
		if b.PlannedQty.IsPositive() {
			due := max(int(b.DLTDays.Ceil().IntPart()), it.leadTotal())
			it.Demand = []Requirement{{Ref: bufferRefPrefix + b.Zone + ":" + b.ItemID, RefType: RefBuffer, Date: due, Qty: b.PlannedQty, Priority: PrioritySafety}}
		}
	}
	for i := range out {
		var keep []BOMLine
		for _, c := range out[i].Components {
			if !managed[c.ItemID] {
				keep = append(keep, c)
			}
		}
		if len(keep) != len(out[i].Components) {
			out[i].Components = keep
		}
	}
	return out
}

// AddBufferResults finishes a run over buffered items (FR-22.16, BR-22.11):
// every planned order of an item with a buffer carries the buffer's planning
// priority and zone, and every planned order that replenishes a buffer whose
// net-flow position is in the red zone raises the PLAN-0010 red-zone message:
// the order is the supply, the buffer demand the reference, and the date the
// delivery the order is planned for. It runs on the final orders (after the
// scope filter and any quota split), so a split order gets one message per
// part. Messages are re-sorted, and the plan hash is extended with the stamped
// orders and the added messages, so the hash of earlier steps (a quota
// allocation) stays part of it. A result with no buffered order is returned as
// it is.
func AddBufferResults(res Result, buffers []RunBuffer) Result {
	if len(buffers) == 0 {
		return res
	}
	byItem := make(map[string]RunBuffer, len(buffers))
	for _, b := range buffers {
		byItem[b.ItemID] = b
	}
	h := sha256.New()
	h.Write([]byte(res.Hash))
	changed := false
	orders := slices.Clone(res.Orders)
	for i := range orders {
		b, ok := byItem[orders[i].ItemID]
		if !ok {
			continue
		}
		prio := b.Priority
		orders[i].PlanningPriority, orders[i].BufferZone = &prio, b.Zone
		fmt.Fprintf(h, "B|%s|%s|%s\n", orders[i].Number, prio, b.Zone)
		changed = true
	}
	var added []Message
	for _, p := range res.Pegging {
		if p.DemandRefType == RefBuffer && strings.HasPrefix(p.DemandRef, bufferRefPrefix+ZoneRed+":") {
			m := Message{Action: ActionRedZone, ItemID: p.ItemID, SupplyRef: p.SupplyRef, DemandRef: p.DemandRef, SuggestedDate: p.DemandDate, Qty: p.Qty}
			added = append(added, m)
			fmt.Fprintf(h, "M|%s|%s|%s|%s|%d|%d|%s\n", m.Action, m.ItemID, m.SupplyRef, m.DemandRef, m.SuggestedDate, m.DelayDays, m.Qty)
		}
	}
	if !changed && len(added) == 0 {
		return res
	}
	res.Orders = orders
	if len(added) > 0 {
		msgs := append(slices.Clone(res.Messages), added...)
		sort.SliceStable(msgs, func(a, b int) bool { return msgLess(msgs[a], msgs[b]) })
		res.Messages = dedupeMessages(msgs)
	}
	res.Hash = fmt.Sprintf("%x", h.Sum(nil))
	return res
}
