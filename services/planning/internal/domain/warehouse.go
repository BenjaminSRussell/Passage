package domain

import (
	"fmt"
	"slices"
	"strings"

	"github.com/shopspring/decimal"
)

// WarehouseSep joins an item id and a warehouse id into the key of a warehouse
// planning scope inside one engine run (FR-22.18). Ids are UUIDs, so the
// separator never occurs in one.
const WarehouseSep = "@"

// WarehouseKey is the engine id of an item planned in the scope of a warehouse.
func WarehouseKey(itemID, warehouseID string) string {
	return itemID + WarehouseSep + warehouseID
}

// SplitWarehouseKey returns the item id and the warehouse id of an engine id;
// the warehouse id is empty for an item of the site scope.
func SplitWarehouseKey(key string) (itemID, warehouseID string) {
	itemID, warehouseID, _ = strings.Cut(key, WarehouseSep)
	return itemID, warehouseID
}

// WarehouseSpec is one warehouse planning scope of an item at the site: its own
// on-hand, safety stock, demand and receipts, netted apart from the site (AC-22.16).
// Item holds the coverage of the scope with ID left empty. A scope whose
// OrderType is transfer is replenished from the site (BR-22.7); one with
// purchase or production is replenished directly.
type WarehouseSpec struct {
	WarehouseID string
	Item        Item
}

// ExpandWarehouses returns the snapshot with every warehouse scope as an item of
// its own, keyed by WarehouseKey. A transfer scope has the site item as its
// component with quantity 1, so each planned transfer creates demand for the site
// item at its order date, the delivery date less the transfer lead time (BR-22.7).
// A production scope explodes the site item's BOM, so its components are demanded
// in their site scope; a purchase scope has no components.
// The site item's own demand and on-hand are untouched, so warehouse stock is
// never netted against site demand. Warehouses of an item that is not in the
// snapshot, a duplicate scope, or an empty warehouse id are ErrInvalidItem.
func ExpandWarehouses(items []Item, specs map[string][]WarehouseSpec) ([]Item, error) {
	if len(specs) == 0 {
		return items, nil
	}
	known := make(map[string]bool, len(items))
	for _, it := range items {
		known[it.ID] = true
	}
	out := make([]Item, len(items), len(items)+len(specs))
	copy(out, items)
	for _, base := range items {
		seen := map[string]bool{}
		for _, w := range specs[base.ID] {
			if w.WarehouseID == "" || seen[w.WarehouseID] {
				return nil, fmt.Errorf("%w: %q warehouse scope %q is empty or repeated", ErrInvalidItem, base.ID, w.WarehouseID)
			}
			seen[w.WarehouseID] = true
			it := w.Item
			it.ID = WarehouseKey(base.ID, w.WarehouseID)
			switch it.OrderType {
			case OrderTransfer:
				it.Components = []BOMLine{{ItemID: base.ID, Qty: decimal.NewFromInt(1)}}
			case OrderProduction:
				// Production in the warehouse explodes the item's own BOM; the
				// components are planned in their site scope.
				it.Components = slices.Clone(base.Components)
			default:
				it.Components = nil
			}
			out = append(out, it)
		}
	}
	for id := range specs {
		if !known[id] {
			return nil, fmt.Errorf("%w: warehouse scope of unknown item %q", ErrInvalidItem, id)
		}
	}
	return out, nil
}

// SplitWarehouses turns the engine ids of warehouse scopes in a result back into
// the item id and its WarehouseID, on orders, pegs, messages and net rows. The
// hash is kept: it was taken over the engine ids, so it still tells scopes apart.
func SplitWarehouses(res Result) Result {
	for i := range res.Orders {
		res.Orders[i].ItemID, res.Orders[i].WarehouseID = SplitWarehouseKey(res.Orders[i].ItemID)
	}
	for i := range res.Pegging {
		res.Pegging[i].ItemID, res.Pegging[i].WarehouseID = SplitWarehouseKey(res.Pegging[i].ItemID)
	}
	for i := range res.Messages {
		res.Messages[i].ItemID, res.Messages[i].WarehouseID = SplitWarehouseKey(res.Messages[i].ItemID)
	}
	for i := range res.Net {
		res.Net[i].ItemID, res.Net[i].WarehouseID = SplitWarehouseKey(res.Net[i].ItemID)
	}
	return res
}
