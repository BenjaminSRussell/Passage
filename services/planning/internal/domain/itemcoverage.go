package domain

import (
	"slices"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// Planning scopes of an item coverage (FR-22.18, MRP areas).
const (
	PlanScopeSite          = "site"
	PlanScopeWarehouse     = "warehouse"
	PlanScopeSubcontractor = "subcontractor"
)

// Planned order types an item coverage may default to (FR-22.3). Kanban is not
// produced by the engine and is not accepted.
var plannedOrderTypes = []string{OrderPurchase, OrderProduction, OrderTransfer}

// maxCoverageQty bounds min/max to what numeric(18,6) stores.
var maxCoverageQty = decimal.New(1, 12)

// ItemCoverageSpec is the setup of how one item is planned at one site,
// warehouse or subcontractor (data model ItemCoverage with the FR-22.16..FR-22.20
// fields). Optional references are empty strings when absent; a nil
// LeadTimeDays uses the item's lead time.
type ItemCoverageSpec struct {
	ItemID                  string
	SiteID                  string
	WarehouseID             string
	CoverageGroupID         string
	MinQty                  decimal.Decimal
	MaxQty                  decimal.Decimal
	SafetyMarginReceiptDays int
	LeadTimeDays            *int
	VendorID                string
	PlannedOrderType        string
	PlanningStrategy        string
	PlanningScope           string
	DecouplingPoint         bool
	BufferProfileID         string
	RequiredShelfLifeDays   int
	PriorityBased           bool
}

// DefaultItemCoverage is a site-scope coverage of item at site under the given
// coverage group with the defaults of 4.22.13: no lot limits, purchase orders
// and the mts-net strategy.
func DefaultItemCoverage(itemID, siteID, coverageGroupID string) ItemCoverageSpec {
	return ItemCoverageSpec{ItemID: itemID, SiteID: siteID, CoverageGroupID: coverageGroupID,
		MinQty: decimal.Zero, MaxQty: decimal.Zero, PlannedOrderType: OrderPurchase,
		PlanningStrategy: StrategyMTSNet, PlanningScope: PlanScopeSite}
}

// Validate returns one message per invalid field, keyed by the wire name of the
// field; nil means the spec is valid. The scope rules mirror the table checks
// (FR-22.18): a warehouse scope needs its warehouse and the other scopes carry
// none; a subcontractor scope needs the vendor and plans provision transfers.
func (c ItemCoverageSpec) Validate() map[string]string {
	errs := map[string]string{}
	if !ids.Valid(c.ItemID) {
		errs["itemId"] = "must be a UUID"
	}
	if !ids.Valid(c.SiteID) {
		errs["siteId"] = "must be a UUID"
	}
	if !ids.Valid(c.CoverageGroupID) {
		errs["coverageGroupId"] = "must be a UUID"
	}
	if c.VendorID != "" && !ids.Valid(c.VendorID) {
		errs["vendorId"] = "must be a UUID"
	}
	if c.BufferProfileID != "" && !ids.Valid(c.BufferProfileID) {
		errs["bufferProfileId"] = "must be a UUID"
	}
	if !slices.Contains(plannedOrderTypes, c.PlannedOrderType) {
		errs["plannedOrderType"] = "must be purchase, production or transfer"
	}
	if !ValidStrategy(c.PlanningStrategy) {
		errs["planningStrategy"] = "must be mts-net, mts-gross, mto, assemble-to-order-final, plan-without-final-assembly or subassembly"
	}
	switch c.PlanningScope {
	case PlanScopeWarehouse:
		if !ids.Valid(c.WarehouseID) {
			errs["warehouseId"] = "is required for warehouse scope and must be a UUID"
		}
	case PlanScopeSite, PlanScopeSubcontractor:
		if c.WarehouseID != "" {
			errs["warehouseId"] = "must be empty unless the scope is warehouse"
		}
		if c.PlanningScope == PlanScopeSubcontractor {
			if c.VendorID == "" {
				errs["vendorId"] = "is required for subcontractor scope"
			}
			if c.PlannedOrderType != OrderTransfer && errs["plannedOrderType"] == "" {
				errs["plannedOrderType"] = "must be transfer for subcontractor scope (provision orders)"
			}
		}
	default:
		errs["planningScope"] = "must be site, warehouse or subcontractor"
	}
	if msg := coverageQtyError(c.MinQty); msg != "" {
		errs["minQty"] = msg
	}
	if msg := coverageQtyError(c.MaxQty); msg != "" {
		errs["maxQty"] = msg
	} else if c.MaxQty.IsPositive() && c.MaxQty.LessThan(c.MinQty) {
		errs["maxQty"] = "must be 0 (no maximum) or at least minQty"
	}
	if c.SafetyMarginReceiptDays < 0 || c.SafetyMarginReceiptDays > 365 {
		errs["safetyMarginReceiptDays"] = "must be 0 to 365"
	}
	if c.LeadTimeDays != nil && (*c.LeadTimeDays < 0 || *c.LeadTimeDays > 3650) {
		errs["leadTimeDays"] = "must be 0 to 3650"
	}
	if c.RequiredShelfLifeDays < 0 || c.RequiredShelfLifeDays > 3650 {
		errs["requiredShelfLifeDays"] = "must be 0 to 3650"
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

func coverageQtyError(q decimal.Decimal) string {
	switch {
	case q.IsNegative():
		return "must not be negative"
	case q.Exponent() < -6:
		return "must have at most 6 decimal places"
	case q.GreaterThanOrEqual(maxCoverageQty):
		return "must be less than 1000000000000"
	}
	return ""
}
