package api

import (
	"errors"
	"fmt"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// withStoredCoverage fills what a run item leaves out from the item's stored
// site-scope item coverage and its coverage group (FR-22.2, FR-22.5, data
// model ItemCoverage): a coverage without a code is replaced by the stored
// group's settings with the item coverage's min, max and receipt margin; an
// empty orderType takes the stored planned order type; an absent leadTimeDays
// takes the stored lead-time override when there is one; an absent vendorId takes the stored default vendor. Inline values always
// win. An item with neither a coverage code nor a stored coverage takes def,
// the default coverage group REQ (FR-22.2, 4.22.13), when def is not nil. It
// returns one message per item that still has no coverage code or order type,
// keyed like the request validation.
func withStoredCoverage(items []itemDTO, stored map[string]store.RunCoverage, def *store.RunCoverage) map[string]string {
	return withStoredCoverageAt(items, stored, def, func(i int) string { return fmt.Sprintf("items[%d]", i) }, "site")
}

// defaultCoverageCode is the code of the default coverage group (PRD 4.22.13).
const defaultCoverageCode = "REQ"

// needsDefaultCoverage reports whether any run item would be left without a
// coverage code: it sends none and has no stored site-scope coverage.
func needsDefaultCoverage(items []itemDTO, stored map[string]store.RunCoverage) bool {
	for _, it := range items {
		if _, ok := stored[it.ItemID]; !ok && it.Coverage.Code == "" {
			return true
		}
	}
	return false
}

// defaultCoverageOf maps the stored default group to the coverage an item without
// one takes. The group's settings apply without the min, max and margins of an item coverage.
func defaultCoverageOf(g store.CoverageGroup) store.RunCoverage {
	s := g.Spec
	return store.RunCoverage{GroupCode: s.Code, CoverageCode: s.CoverageCode, CoverageFenceDays: s.CoverageTimeFenceDays,
		NegativeDays: s.NegativeDays, PositiveDays: s.PositiveDays, PeriodDays: s.PeriodDays, FreezeFenceDays: s.FreezeFenceDays,
		ActionFenceDays: s.ActionFenceDays, FuturesFenceDays: s.FuturesFenceDays, FirmingFenceDays: s.FirmingFenceDays,
		AutoFirm: s.AutoFirm, SafetyStockPolicy: s.SafetyStockPolicy}
}

// builtinDefaultCoverage is the group REQ a tenant that never stored one gets:
// requirement coverage with the FR-22.2 defaults.
func builtinDefaultCoverage() store.RunCoverage {
	return defaultCoverageOf(store.CoverageGroup{Spec: domain.DefaultCoverageGroup(defaultCoverageCode, domain.CoverageRequirement)})
}

// withStoredCoverageAt is withStoredCoverage for any list of scopes: label names item i in
// the field errors and scope ("site" or "warehouse") the planning scope of the stored coverage.
func withStoredCoverageAt(items []itemDTO, stored map[string]store.RunCoverage, def *store.RunCoverage, label func(int) string, scope string) map[string]string {
	fields := map[string]string{}
	for i := range items {
		it := &items[i]
		if c, ok := stored[it.ItemID]; ok {
			if it.Coverage.Code == "" {
				strategy := it.Coverage.Strategy
				it.Coverage = storedCoverageDTO(c)
				it.Coverage.Strategy = strategy
			}
			if it.OrderType == "" {
				it.OrderType = c.PlannedOrderType
			}
			if it.VendorID == "" {
				it.VendorID = c.VendorID
			}
			if it.LeadTimeDays == nil && c.LeadTimeDays != nil {
				lead := *c.LeadTimeDays
				it.LeadTimeDays = &lead
			}
		}
		if _, ok := stored[it.ItemID]; !ok && it.Coverage.Code == "" && def != nil {
			strategy := it.Coverage.Strategy
			it.Coverage = storedCoverageDTO(*def)
			it.Coverage.Strategy = strategy
		}
		prefix := label(i)
		if it.Coverage.Code == "" {
			fields[prefix+".coverage.code"] = "is required when the item has no " + scope + "-scope item coverage"
		}
		if it.OrderType == "" {
			fields[prefix+".orderType"] = "is required when the item has no " + scope + "-scope item coverage"
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

// defaultVendors lists the default vendor of each run item that has one, after
// withStoredCoverage has filled it from the stored item coverage.
func defaultVendors(items []itemDTO) map[string]string {
	out := map[string]string{}
	for _, it := range items {
		if it.VendorID != "" {
			out[it.ItemID] = it.VendorID
		}
		for _, w := range it.Warehouses {
			if w.VendorID != "" {
				out[domain.WarehouseKey(it.ItemID, w.WarehouseID)] = w.VendorID
			}
		}
	}
	return out
}

// isCoverageNotFound reports whether err is the store's unknown coverage group problem.
func isCoverageNotFound(err error) bool {
	var p *httpx.Problem
	return errors.As(err, &p) && p.Code == store.CodeCoverageNotFound
}
