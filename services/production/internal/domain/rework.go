package domain

import (
	"strings"

	"github.com/shopspring/decimal"
)

// OriginRework is the origin type of a rework order (FR-21.20).
const OriginRework = "Rework"

// OriginSalesLine is the origin type of the order built for a configured sales order line.
const OriginSalesLine = "SalesOrderLine"

// CodeReworkReference is raised when a rework order's reference does not name
// an order of the same product with enough reported output.
const CodeReworkReference = "PRODUCTION_REWORK_REFERENCE_INVALID"

// VarRework is the variance type of a rework order's cost (FR-21.20, added to
// the four of BR-21.6).
const VarRework VarianceType = "rework"

// MaxTrackingID bounds a serial or batch number and a rework reference.
const MaxTrackingID = 100

// ReworkRefType names what a rework order reports against.
type ReworkRefType string

const (
	ReworkRefOrder  ReworkRefType = "order"
	ReworkRefBatch  ReworkRefType = "batch"
	ReworkRefSerial ReworkRefType = "serial"
)

// Valid reports whether t is order, batch or serial.
func (t ReworkRefType) Valid() bool {
	return t == ReworkRefOrder || t == ReworkRefBatch || t == ReworkRefSerial
}

// ReworkRef is the original order (its id), batch or serial (its number) a
// rework order reports against.
type ReworkRef struct {
	Type ReworkRefType `json:"type"`
	ID   string        `json:"id"`
}

// ValidTrackingID reports whether s is usable as a serial, batch or reference
// number: 1 to MaxTrackingID characters without surrounding spaces.
func ValidTrackingID(s string) bool {
	return s != "" && strings.TrimSpace(s) == s && len(s) <= MaxTrackingID
}

// CheckReworkSource checks a rework order against the order it references
// (FR-21.20): the same product, and no more units than that order reported as
// finished.
func CheckReworkSource(productID string, qty decimal.Decimal, originalProductID string, originalRAF decimal.Decimal) error {
	switch {
	case originalProductID != productID:
		return &Error{Code: CodeReworkReference, Detail: "the referenced order produced another product"}
	case qty.GreaterThan(originalRAF):
		return &Error{Code: CodeReworkReference, Detail: "the rework quantity " + qty.String() +
			" exceeds the quantity the referenced order reported as finished (" + originalRAF.String() + ")"}
	}
	return nil
}

// ReworkReceiptBase is the value a rework order's reports as finished receive
// in total: the planned material cost of the order's own line for the product,
// which carries the reworked units in at their existing cost. Replacement parts,
// labor and overhead stay in WIP as rework cost. Without such a line the units
// never left inventory, so the receipts carry no value.
func ReworkReceiptBase(planned []CostRow, productID string) decimal.Decimal {
	sum := decimal.Zero
	for _, c := range planned {
		if c.Group == GroupMaterial && c.Key == productID {
			sum = sum.Add(c.Amount)
		}
	}
	return sum
}

// WIPGroup maps a WIP posting type to the cost group it carries. The finished
// receipt offsets material, because it takes back the reworked units' cost.
func WIPGroup(postingType string) (CostGroup, bool) {
	switch postingType {
	case PostWIPMaterial, PostWIPReceipt:
		return GroupMaterial, true
	case PostWIPLabor:
		return GroupLabor, true
	case PostWIPOverhead:
		return GroupOverhead, true
	}
	return "", false
}

// ReworkVariances settles a rework order at End (FR-21.20, AC-21.15). Rework is
// not capitalized: the whole WIP balance posts as variances of type rework, one
// per cost group (material issued less the value received, labor, overhead).
// byGroup is the WIP balance by group, so the variances sum to the balance.
func ReworkVariances(byGroup map[CostGroup]decimal.Decimal) []Variance {
	out := []Variance{}
	for _, g := range []CostGroup{GroupMaterial, GroupLabor, GroupOverhead, GroupSubcontract} {
		if amt := byGroup[g]; !amt.IsZero() {
			out = append(out, Variance{Type: VarRework, Group: g, Amount: amt})
		}
	}
	return out
}
