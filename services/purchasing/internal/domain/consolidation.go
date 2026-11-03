package domain

import (
	"fmt"
	"sort"
)

// Consolidation window bounds (3.17.13 consolidation_window_days: default 7, 1–60).
const (
	DefaultConsolidationWindowDays = 7
	MinConsolidationWindowDays     = 1
	MaxConsolidationWindowDays     = 60
)

// CodeConsolidationWindow rejects a window outside 1–60 days.
const CodeConsolidationWindow = "PRC-CONS-001"

// ConsolidationLine is an approved requisition line with the vendor it will be
// ordered from (the buyer's choice or the preferred vendor).
type ConsolidationLine struct {
	RequisitionLineID string
	LegalEntityID     string
	VendorID          string
	SiteID            string
	WarehouseID       string
	Currency          string
	RequestedDate     Day
}

// ConsolidationGroup is one future PO: lines sharing legal entity, vendor,
// currency, site, warehouse and delivery window.
type ConsolidationGroup struct {
	LegalEntityID string
	VendorID      string
	SiteID        string
	WarehouseID   string
	Currency      string
	WindowStart   Day
	Lines         []ConsolidationLine
}

func (l ConsolidationLine) sameStream(g ConsolidationGroup) bool {
	return l.LegalEntityID == g.LegalEntityID && l.VendorID == g.VendorID && l.Currency == g.Currency && l.SiteID == g.SiteID && l.WarehouseID == g.WarehouseID
}

// Consolidate groups lines into POs by legal entity × vendor × currency × site
// × warehouse × delivery window (FR-17.3; a PO belongs to one legal entity). Within each
// stream lines are ordered by requested date then id; a window opens at the
// earliest unassigned date and takes every line dated less than windowDays
// after it, so no PO spans more than windowDays. Groups come out ordered by
// legal entity, vendor, currency, site, warehouse and window start, so the same input
// always yields the same POs.
func Consolidate(lines []ConsolidationLine, windowDays int) ([]ConsolidationGroup, error) {
	if windowDays < MinConsolidationWindowDays || windowDays > MaxConsolidationWindowDays {
		return nil, fail(CodeConsolidationWindow, "consolidation window must be %d–%d days, not %d",
			MinConsolidationWindowDays, MaxConsolidationWindowDays, windowDays)
	}
	sorted := append([]ConsolidationLine(nil), lines...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		for _, k := range [][2]string{{a.LegalEntityID, b.LegalEntityID}, {a.VendorID, b.VendorID}, {a.Currency, b.Currency}, {a.SiteID, b.SiteID}, {a.WarehouseID, b.WarehouseID}} {
			if k[0] != k[1] {
				return k[0] < k[1]
			}
		}
		if !a.RequestedDate.Equal(b.RequestedDate.Time) {
			return a.RequestedDate.Before(b.RequestedDate.Time)
		}
		return a.RequisitionLineID < b.RequisitionLineID
	})
	var out []ConsolidationGroup
	for _, l := range sorted {
		n := len(out)
		if n == 0 || !l.sameStream(out[n-1]) || !l.RequestedDate.Before(out[n-1].WindowStart.AddDate(0, 0, windowDays)) {
			out = append(out, ConsolidationGroup{LegalEntityID: l.LegalEntityID, VendorID: l.VendorID, SiteID: l.SiteID, WarehouseID: l.WarehouseID,
				Currency: l.Currency, WindowStart: l.RequestedDate})
			n++
		}
		out[n-1].Lines = append(out[n-1].Lines, l)
	}
	return out, nil
}

// String is a debugging aid for group keys in test failures.
func (g ConsolidationGroup) String() string {
	return fmt.Sprintf("%s/%s/%s/%s/%s@%s(%d)", g.LegalEntityID, g.VendorID, g.SiteID, g.WarehouseID, g.Currency, g.WindowStart, len(g.Lines))
}
