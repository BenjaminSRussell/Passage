package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// PackedDemand is one planned or firmed packed-item order to fill from a bulk
// batch (FR-24.17): its packed units and due date.
type PackedDemand struct {
	OrderID string
	ItemID  string
	Units   decimal.Decimal
	Due     time.Time
}

// BulkPack is a bulk-to-packed relation: bulk UoM quantity per packed unit, the
// formula multiple and the formula maximum (BR-24.12). Max zero means no maximum.
type BulkPack struct {
	PackQtyPerUnit decimal.Decimal
	Multiple       decimal.Decimal
	Max            decimal.Decimal
}

// Peg ties a packing order to a bulk order for BulkQty of the bulk item.
type Peg struct {
	OrderID string
	ItemID  string
	BulkQty decimal.Decimal
}

// BulkOrder is one bulk batch order of a consolidation and the packing orders
// pegged to it (FR-24.17). The packing orders reserve its batch (same-batch).
type BulkOrder struct {
	Qty  decimal.Decimal
	Pegs []Peg
}

// Consolidate implements BR-24.12. The bulk quantity is Σ units × pack quantity
// rounded up to the formula multiple. If it exceeds the formula maximum it is
// split into ⌈qty / max⌉ bulk orders of at most max each (max must itself be a
// multiple, so every split order is too). Packing orders are pegged to the bulk
// orders by due date: the earliest due fill the first bulk order, and an order
// that does not fit is split across the next one.
func Consolidate(demands []PackedDemand, rel BulkPack) ([]BulkOrder, error) {
	if !rel.PackQtyPerUnit.IsPositive() || !rel.Multiple.IsPositive() {
		return nil, errorf(CodeInvalid, "pack quantity and formula multiple must be positive")
	}
	if rel.Max.IsNegative() {
		return nil, errorf(CodeInvalid, "formula maximum must not be negative")
	}
	if rel.Max.IsPositive() && rel.Max.Mod(rel.Multiple).IsPositive() {
		return nil, errorf(CodeInvalid, "formula maximum %s is not a multiple of %s", rel.Max, rel.Multiple)
	}
	if len(demands) == 0 {
		return nil, errorf(CodeInvalid, "no packed demand to consolidate")
	}
	seen := map[string]bool{}
	total := decimal.Zero
	for _, d := range demands {
		if seen[d.OrderID] {
			return nil, errorf(CodeInvalid, "packing order %s listed twice", d.OrderID)
		}
		seen[d.OrderID] = true
		if !d.Units.IsPositive() {
			return nil, errorf(CodeInvalid, "packing order %s has no packed units", d.OrderID)
		}
		total = total.Add(d.Units.Mul(rel.PackQtyPerUnit))
	}
	qty := total.DivRound(rel.Multiple, 24).Ceil().Mul(rel.Multiple)

	sizes := []decimal.Decimal{qty}
	if rel.Max.IsPositive() && qty.GreaterThan(rel.Max) {
		sizes = sizes[:0]
		for rest := qty; rest.IsPositive(); {
			size := decimal.Min(rel.Max, rest)
			sizes = append(sizes, size)
			rest = rest.Sub(size)
		}
	}

	byDue := make([]PackedDemand, len(demands))
	copy(byDue, demands)
	sort.SliceStable(byDue, func(i, j int) bool { return byDue[i].Due.Before(byDue[j].Due) })

	out := make([]BulkOrder, len(sizes))
	b, room := 0, sizes[0]
	for i, s := range sizes {
		out[i].Qty = s
	}
	for _, d := range byDue {
		need := d.Units.Mul(rel.PackQtyPerUnit)
		for need.IsPositive() {
			if !room.IsPositive() {
				b++
				room = sizes[b]
			}
			take := decimal.Min(need, room)
			out[b].Pegs = append(out[b].Pegs, Peg{OrderID: d.OrderID, ItemID: d.ItemID, BulkQty: take})
			need = need.Sub(take)
			room = room.Sub(take)
		}
	}
	return out, nil
}
