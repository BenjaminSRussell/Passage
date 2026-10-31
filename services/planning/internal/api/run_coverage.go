package api

import (
	"fmt"

	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// withStoredCoverage fills what a run item leaves out from the item's stored
// site-scope item coverage and its coverage group (FR-22.2, FR-22.5, data
// model ItemCoverage): a coverage without a code is replaced by the stored
// group's settings with the item coverage's min, max and receipt margin; an
// empty orderType takes the stored planned order type; an absent leadTimeDays
// takes the stored lead-time override when there is one. Inline values always
// win. It returns one message per item that still has no coverage code or
// order type, keyed like the request validation.
func withStoredCoverage(items []itemDTO, stored map[string]store.RunCoverage) map[string]string {
	fields := map[string]string{}
	for i := range items {
		it := &items[i]
		if c, ok := stored[it.ItemID]; ok {
			if it.Coverage.Code == "" {
				it.Coverage = storedCoverageDTO(c)
			}
			if it.OrderType == "" {
				it.OrderType = c.PlannedOrderType
			}
			if it.LeadTimeDays == nil && c.LeadTimeDays != nil {
				lead := *c.LeadTimeDays
				it.LeadTimeDays = &lead
			}
		}
		prefix := fmt.Sprintf("items[%d]", i)
		if it.Coverage.Code == "" {
			fields[prefix+".coverage.code"] = "is required when the item has no item coverage at the site"
		}
		if it.OrderType == "" {
			fields[prefix+".orderType"] = "is required when the item has no item coverage at the site"
		}
	}
	return fields
}

// storedCoverageDTO maps a stored coverage row to the run's coverage block.
func storedCoverageDTO(c store.RunCoverage) coverageDTO {
	return coverageDTO{
		Code:              c.CoverageCode,
		PeriodDays:        c.PeriodDays,
		MarginDays:        c.MarginDays,
		IssueMarginDays:   c.IssueMarginDays,
		ReorderMarginDays: c.ReorderMarginDays,
		PositiveDays:      c.PositiveDays,
		NegativeDays:      c.NegativeDays,
		ReorderMin:        c.MinQty,
		ReorderMax:        c.MaxQty,
		FreezeFenceDays:   c.FreezeFenceDays,
		ActionFenceDays:   c.ActionFenceDays,
		FuturesFenceDays:  c.FuturesFenceDays,
		CoverageFenceDays: c.CoverageFenceDays,
		FirmingFenceDays:  c.FirmingFenceDays,
		AutoFirm:          c.AutoFirm,
		SafetyStockPolicy: c.SafetyStockPolicy,
	}
}

// itemIDsOf lists the item ids of a run snapshot in request order.
func itemIDsOf(items []itemDTO) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ItemID)
	}
	return out
}

// deref reads an optional day count; absent is 0.
func deref(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}
