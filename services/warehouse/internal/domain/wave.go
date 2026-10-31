package domain

import (
	"errors"
	"fmt"
	"slices"

	"github.com/shopspring/decimal"
)

// ReplenishPriority is the priority of replenishment work: it runs before
// picking (DefaultPriority) so forward locations are filled first.
const ReplenishPriority = 40

// WaveOrder is one order released into a wave: its lines are picked as one work.
type WaveOrder struct {
	ID    string
	Lines []NewLine
}

// ReplenishRule keeps ItemID in the PickZone locations at or above MinQty:
// a location whose stock, after the wave's picks, is below MinQty is refilled
// to MaxQty from the ReserveZone (FR-31.5, min/max replenishment).
type ReplenishRule struct {
	ItemID      string
	PickZone    string
	ReserveZone string
	MinQty      decimal.Decimal
	MaxQty      decimal.Decimal
}

// WaveRelease is the work a wave creates: one SALES_PICK work per order, in
// order, and the REPLENISHMENT work the picks make necessary.
type WaveRelease struct {
	Picks         []Work
	Replenishment []Work
}

// ReleaseWave plans the picks of every order against locs, each order's
// reservations counting for the next, then plans the replenishment: a pick
// location left below the rule's minimum gets a work that moves stock from
// the reserve zone up to the maximum (a PICK line at the reserve location and a
// PUT line at the pick location per source). The reserve is taken in pick
// sequence and never overdrawn; a shortfall refills less. Only pick-zone
// locations that held the item before the wave are refilled (slotting is not
// modeled). locs is not modified.
// newID supplies the work ids.
func ReleaseWave(warehouseID string, newID func() string, orders []WaveOrder, rules []ReplenishRule, dirs []Directive, locs []Location) (WaveRelease, error) {
	for _, r := range rules {
		if r.MinQty.IsNegative() || !r.MaxQty.GreaterThan(r.MinQty) {
			return WaveRelease{}, fmt.Errorf("replenish rule for %s: max must exceed min and min must not be negative", r.ItemID)
		}
	}
	if len(orders) == 0 {
		return WaveRelease{}, errors.New("a wave needs at least one order")
	}
	pool := CloneLocations(locs)
	var out WaveRelease
	for _, o := range orders {
		w, _, err := Plan(newID(), warehouseID, "SALES_PICK", 0, o.Lines, dirs, pool, Compliance{})
		if err != nil {
			return WaveRelease{}, fmt.Errorf("order %s: %w", o.ID, err)
		}
		Reserve(pool, w.Lines)
		out.Picks = append(out.Picks, w)
	}
	for _, r := range rules {
		for i := range pool {
			if pool[i].Zone != r.PickZone || !locs[i].Stock[r.ItemID].IsPositive() || !pool[i].Status.Allows(DirectionPut) {
				continue
			}
			if w, ok := replenishment(newID, warehouseID, r, pool, i); ok {
				out.Replenishment = append(out.Replenishment, w)
			}
		}
	}
	return out, nil
}

func replenishment(newID func() string, warehouseID string, r ReplenishRule, pool []Location, target int) (Work, bool) {
	have := pool[target].Stock[r.ItemID]
	if !have.LessThan(r.MinQty) {
		return Work{}, false
	}
	need := r.MaxQty.Sub(have)
	sources := make([]int, 0, len(pool))
	for i := range pool {
		if pool[i].Zone == r.ReserveZone && pool[i].Status.Allows(DirectionPick) && pool[i].Stock[r.ItemID].IsPositive() {
			sources = append(sources, i)
		}
	}
	slices.SortStableFunc(sources, func(a, b int) int { return pool[a].PickSequence - pool[b].PickSequence })
	w := Work{ID: newID(), WarehouseID: warehouseID, WorkType: "REPLENISHMENT", Status: WorkOpen, Priority: ReplenishPriority}
	for _, s := range sources {
		if !need.IsPositive() {
			break
		}
		take := decimal.Min(need, pool[s].Stock[r.ItemID])
		for _, l := range []struct {
			dir Direction
			at  string
		}{{DirectionPick, pool[s].ID}, {DirectionPut, pool[target].ID}} {
			line := WorkLine{LineNo: len(w.Lines) + 1, ItemID: r.ItemID, Direction: l.dir, Qty: take, ConfirmedQty: decimal.Zero,
				Weight: decimal.Zero, Volume: decimal.Zero, Status: LineOpen}
			if l.dir == DirectionPick {
				line.FromLocationID = l.at
			} else {
				line.ToLocationID = l.at
			}
			w.Lines = append(w.Lines, line)
		}
		pool[s].Stock[r.ItemID] = pool[s].Stock[r.ItemID].Sub(take)
		need = need.Sub(take)
	}
	return w, len(w.Lines) > 0
}
