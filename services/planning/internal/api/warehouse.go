package api

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// maxWarehouseScopes bounds the warehouse scopes of one item in a run.
const maxWarehouseScopes = 50

// warehouseDTO is one warehouse planning scope of a run item (FR-22.18, AC-22.16):
// the warehouse's own on-hand, safety stock, demand and receipts, netted apart from
// the site, so warehouse stock never covers site demand. Coverage, order type,
// lead time and vendor are taken from the item's stored warehouse-scope item
// coverage when left out (inline values win). An orderType of transfer replenishes
// the warehouse from the site: each planned transfer creates demand for the item at
// the site on its order date, the delivery date less the lead time (BR-22.7);
// purchase and production replenish it directly.
type warehouseDTO struct {
	WarehouseID  string           `json:"warehouseId"`
	VendorID     string           `json:"vendorId,omitempty"`
	Coverage     coverageDTO      `json:"coverage"`
	OnHand       decimal.Decimal  `json:"onHand"`
	SafetyStock  decimal.Decimal  `json:"safetyStock"`
	OrderType    string           `json:"orderType"`
	LeadTimeDays *int             `json:"leadTimeDays"`
	Lot          lotDTO           `json:"lot"`
	Demand       []requirementDTO `json:"demand"`
	Receipts     []requirementDTO `json:"receipts"`
}

// asItem is the warehouse scope as a run item under id, so the item validation and
// the stored-coverage fill apply to it as they do to a site item.
func (w warehouseDTO) asItem(id string) itemDTO {
	return itemDTO{ItemID: id, VendorID: w.VendorID, Coverage: w.Coverage, OnHand: w.OnHand, SafetyStock: w.SafetyStock,
		OrderType: w.OrderType, LeadTimeDays: w.LeadTimeDays, Lot: w.Lot, Demand: w.Demand, Receipts: w.Receipts}
}

// validateWarehouses checks the warehouse scopes of one run item.
func validateWarehouses(fields map[string]string, prefix string, it itemDTO) {
	if len(it.Warehouses) > maxWarehouseScopes {
		fields[prefix+".warehouses"] = fmt.Sprintf("holds at most %d warehouse scopes", maxWarehouseScopes)
		return
	}
	seen := map[string]bool{}
	for j, w := range it.Warehouses {
		wp := fmt.Sprintf("%s.warehouses[%d]", prefix, j)
		switch {
		case !ids.Valid(w.WarehouseID):
			fields[wp+".warehouseId"] = "must be a UUID"
		case seen[w.WarehouseID]:
			fields[wp+".warehouseId"] = "is listed twice for the item"
		}
		seen[w.WarehouseID] = true
		validateItem(fields, wp, w.asItem(it.ItemID))
	}
}

// warehouseItems lists every warehouse scope of the run as an item DTO keyed by its
// engine id (domain.WarehouseKey), with the label field errors are reported under.
func warehouseItems(items []itemDTO) (dtos []itemDTO, labels []string) {
	for i, it := range items {
		for j, w := range it.Warehouses {
			dtos = append(dtos, w.asItem(domain.WarehouseKey(it.ItemID, w.WarehouseID)))
			labels = append(labels, fmt.Sprintf("items[%d].warehouses[%d]", i, j))
		}
	}
	return dtos, labels
}

// warehouseSpecs converts the run's warehouse scopes into engine scopes, filled
// from the stored warehouse-scope coverage (stored is keyed by domain.WarehouseKey).
// A scope that still has no coverage code or order type is a field error.
func (req runRequest) warehouseSpecs(stored map[string]store.RunCoverage, asOf time.Time) (map[string][]domain.WarehouseSpec, map[string]string, error) {
	dtos, labels := warehouseItems(req.Items)
	if len(dtos) == 0 {
		return nil, nil, nil
	}
	fields := withStoredCoverageAt(dtos, stored, func(i int) string { return labels[i] }, "warehouse")
	if len(fields) > 0 {
		return nil, fields, nil
	}
	sub := runRequest{Items: dtos}
	items, err := sub.toDomain(asOf)
	if err != nil {
		return nil, nil, err
	}
	specs := map[string][]domain.WarehouseSpec{}
	k := 0
	for i, it := range req.Items {
		for j, w := range it.Warehouses {
			// The vendor the stored coverage supplied is the scope's default vendor (FR-22.3).
			req.Items[i].Warehouses[j].VendorID = dtos[k].VendorID
			d := items[k]
			d.ID = ""
			specs[it.ItemID] = append(specs[it.ItemID], domain.WarehouseSpec{WarehouseID: w.WarehouseID, Item: d})
			k++
		}
	}
	return specs, nil, nil
}
