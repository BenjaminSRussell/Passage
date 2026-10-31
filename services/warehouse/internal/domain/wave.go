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
// a location whose stock, after the wave's picks and counting refills already
// on the way, is below MinQty is refilled to MaxQty from the ReserveZone
// (FR-31.5, min/max replenishment). UnitWeight and UnitVolume are per unit and
// give the refill lines their usage, so the refill is cut to the room the
// destination has left and confirmation enforces its capacity.
type ReplenishRule struct {
	ItemID      string
	PickZone    string
	ReserveZone string
	MinQty      decimal.Decimal
	MaxQty      decimal.Decimal
	UnitWeight  decimal.Decimal
	UnitVolume  decimal.Decimal
}

// WaveRelease is the work a wave creates: one SALES_PICK work per order, in
// order, and the REPLENISHMENT work the picks make necessary.
type WaveRelease struct {
	Picks         []Work `json:"picks"`
	Replenishment []Work `json:"replenishment"`
}

func validateRules(rules []ReplenishRule) error {
	seen := map[string]bool{}
	for _, r := range rules {
		switch {
		case r.MinQty.IsNegative() || !r.MaxQty.GreaterThan(r.MinQty):
			return fmt.Errorf("replenish rule for %s: max must exceed min and min must not be negative", r.ItemID)
		case r.PickZone == r.ReserveZone:
			return fmt.Errorf("replenish rule for %s: pick zone and reserve zone must differ", r.ItemID)
		case r.UnitWeight.IsNegative() || r.UnitVolume.IsNegative():
			return fmt.Errorf("replenish rule for %s: unit weight and volume must not be negative", r.ItemID)
		case seen[r.ItemID]:
			return fmt.Errorf("replenish rule for %s is given twice", r.ItemID)
		}
		seen[r.ItemID] = true
	}
	return nil
}

// ReleaseWave plans the picks of every order against locs, each order's
// reservations counting for the next, and the replenishment they need. A pick
// location is refilled when its stock, counting refills already open (Reserve)
// and less the pick demand no location could cover, is below the rule's
// minimum; the refill goes up to the maximum, limited by the destination's
// remaining weight and volume, from the reserve zone in pick sequence, never
// overdrawn. The picks are then planned against the real stock; an order that
// stock cannot cover but the stock plus the open and new refills can is planned
// against that and comes out WAITING on those refill works (DependsOn): it
// must not be assigned before they complete. Only pick-zone locations that held
// the item before the wave are refilled (slotting is not modeled), and the
// unmet demand of an item is charged to the first of them. locs is not
// modified. newID supplies the work ids.
func ReleaseWave(warehouseID string, newID func() string, orders []WaveOrder, rules []ReplenishRule, dirs []Directive, locs []Location) (WaveRelease, error) {
	if err := validateRules(rules); err != nil {
		return WaveRelease{}, err
	}
	if len(orders) == 0 {
		return WaveRelease{}, errors.New("a wave needs at least one order")
	}
	dry := CloneLocations(locs)
	addIncoming(dry)
	unmet := map[string]decimal.Decimal{}
	for _, o := range orders {
		w, _, err := Plan("", warehouseID, "SALES_PICK", 0, o.Lines, dirs, dry, Compliance{})
		if err != nil {
			return WaveRelease{}, fmt.Errorf("order %s: %w", o.ID, err)
		}
		Reserve(dry, w.Lines)
		for _, l := range w.Lines {
			if l.Direction == DirectionPick && l.Reason == CodeNoLocation {
				unmet[l.ItemID] = unmet[l.ItemID].Add(l.Qty)
			}
		}
	}

	var out WaveRelease
	for _, r := range rules {
		charged := false
		for i := range dry {
			if dry[i].Zone != r.PickZone || !locs[i].Stock[r.ItemID].IsPositive() || !dry[i].Status.Allows(DirectionPut) {
				continue
			}
			short := decimal.Zero
			if !charged {
				short, charged = unmet[r.ItemID], true
			}
			if w, ok := replenishment(newID, warehouseID, r, dry, i, short); ok {
				out.Replenishment = append(out.Replenishment, w)
			}
		}
	}

	now := CloneLocations(locs)
	soon := CloneLocations(locs)
	addIncoming(soon)
	index := make(map[string]int, len(soon))
	refills := map[string][]string{}
	for i := range soon {
		index[soon[i].ID] = i
		for item, from := range locs[i].incomingFrom {
			refills[soon[i].ID+"|"+item] = from
		}
	}
	for _, w := range out.Replenishment {
		for _, l := range w.Lines {
			if l.Direction == DirectionPut {
				at := &soon[index[l.ToLocationID]]
				at.Stock[l.ItemID] = at.Stock[l.ItemID].Add(l.Qty)
				refills[l.ToLocationID+"|"+l.ItemID] = append(refills[l.ToLocationID+"|"+l.ItemID], w.ID)
			}
		}
		Reserve(now, w.Lines)
		Reserve(soon, w.Lines)
	}
	for _, o := range orders {
		w, _, err := Plan(newID(), warehouseID, "SALES_PICK", 0, o.Lines, dirs, now, Compliance{})
		if err != nil {
			return WaveRelease{}, fmt.Errorf("order %s: %w", o.ID, err)
		}
		if w.Status == WorkBlockedNoLocation {
			if later, _, err := Plan(w.ID, warehouseID, "SALES_PICK", 0, o.Lines, dirs, soon, Compliance{}); err == nil && later.Status == WorkOpen {
				if deps := dependencies(later.Lines, refills); len(deps) > 0 {
					later.Status, later.DependsOn = WorkWaiting, deps
					w = later
				}
			}
		}
		Reserve(now, w.Lines)
		Reserve(soon, w.Lines)
		out.Picks = append(out.Picks, w)
	}
	return out, nil
}

func addIncoming(locs []Location) {
	for i := range locs {
		for item, qty := range locs[i].incoming {
			if locs[i].Stock == nil {
				locs[i].Stock = map[string]decimal.Decimal{}
			}
			locs[i].Stock[item] = locs[i].Stock[item].Add(qty)
		}
	}
}

func dependencies(lines []WorkLine, refills map[string][]string) []string {
	var deps []string
	for _, l := range lines {
		if l.Direction != DirectionPick {
			continue
		}
		for _, id := range refills[l.FromLocationID+"|"+l.ItemID] {
			if !slices.Contains(deps, id) {
				deps = append(deps, id)
			}
		}
	}
	return deps
}

func (r ReplenishRule) room(l Location) (decimal.Decimal, bool) {
	var limit decimal.Decimal
	limited := false
	for _, d := range []struct {
		max  *decimal.Decimal
		used decimal.Decimal
		unit decimal.Decimal
	}{{l.Capacity.MaxWeight, l.Used.Weight, r.UnitWeight}, {l.Capacity.MaxVolume, l.Used.Volume, r.UnitVolume}} {
		if d.max == nil || !d.unit.IsPositive() {
			continue
		}
		fits := d.max.Sub(d.used).Div(d.unit).Floor()
		if !limited || fits.LessThan(limit) {
			limit, limited = fits, true
		}
	}
	return limit, limited
}

func replenishment(newID func() string, warehouseID string, r ReplenishRule, dry []Location, target int, short decimal.Decimal) (Work, bool) {
	have := dry[target].Stock[r.ItemID].Sub(short)
	if !have.LessThan(r.MinQty) {
		return Work{}, false
	}
	need := r.MaxQty.Sub(have)
	if room, limited := r.room(dry[target]); limited {
		need = decimal.Min(need, room)
	}
	sources := make([]int, 0, len(dry))
	for i := range dry {
		if dry[i].Zone == r.ReserveZone && dry[i].Status.Allows(DirectionPick) && dry[i].Stock[r.ItemID].IsPositive() {
			sources = append(sources, i)
		}
	}
	slices.SortStableFunc(sources, func(a, b int) int { return dry[a].PickSequence - dry[b].PickSequence })
	w := Work{ID: newID(), WarehouseID: warehouseID, WorkType: "REPLENISHMENT", Status: WorkOpen, Priority: ReplenishPriority}
	for _, s := range sources {
		if !need.IsPositive() {
			break
		}
		take := decimal.Min(need, dry[s].Stock[r.ItemID])
		for _, l := range []struct {
			dir Direction
			at  string
		}{{DirectionPick, dry[s].ID}, {DirectionPut, dry[target].ID}} {
			line := WorkLine{LineNo: len(w.Lines) + 1, ItemID: r.ItemID, Direction: l.dir, Qty: take, ConfirmedQty: decimal.Zero,
				Weight: take.Mul(r.UnitWeight), Volume: take.Mul(r.UnitVolume), Status: LineOpen, WorkID: w.ID, Refill: true}
			if l.dir == DirectionPick {
				line.FromLocationID = l.at
			} else {
				line.ToLocationID = l.at
			}
			w.Lines = append(w.Lines, line)
		}
		dry[s].Stock[r.ItemID] = dry[s].Stock[r.ItemID].Sub(take)
		need = need.Sub(take)
	}
	return w, len(w.Lines) > 0
}
