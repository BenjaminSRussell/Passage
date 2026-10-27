package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Stable error codes of as-built genealogy (FR-21.19, BR-21.14).
const (
	CodeAsBuiltState           = "PRODUCTION_AS_BUILT_ORDER_STATE"
	CodeAsBuiltNotOnBOM        = "PRODUCTION_AS_BUILT_COMPONENT_NOT_ON_BOM"
	CodeAsBuiltExceedsConsumed = "PRODUCTION_AS_BUILT_EXCEEDS_CONSUMED"
	CodeAsBuiltFinishedCount   = "PRODUCTION_AS_BUILT_TOO_MANY_FINISHED"
	// CodeSerialDuplicate is the PRD's code (BR-21.10, PROD-0009) for a serial already in use.
	CodeSerialDuplicate = "SERIAL_DUPLICATE"
)

// Tracking is how a component is identified: by serial (quantity 1) or by batch.
type Tracking string

const (
	TrackSerial Tracking = "serial"
	TrackBatch  Tracking = "batch"
)

// Valid reports whether t is serial or batch.
func (t Tracking) Valid() bool { return t == TrackSerial || t == TrackBatch }

// AsBuilt is one assignment: a component serial or batch consumed into a
// finished serial or batch of an order (FR-21.19).
type AsBuilt struct {
	FinishedSerial  string
	ComponentItemID string
	Tracking        Tracking
	ComponentSerial string
	Qty             decimal.Decimal
}

// AsBuiltFacts is what an assignment is checked against: the order's status,
// quantity and BOM lines, the quantity of the component already assigned on
// the order, the number of distinct finished serials or batches already used,
// and whether the assignment's finished serial is one of them.
type AsBuiltFacts struct {
	Status            Status
	OrderQty          decimal.Decimal
	Lines             []OrderLine
	ComponentAssigned decimal.Decimal
	FinishedCount     int
	FinishedKnown     bool
}

// CheckAsBuilt applies the rules of FR-21.19 and BR-21.14 to one assignment:
// the order has consumed material (Started or ReportedAsFinished), the
// component is on the order's BOM, the assigned quantity of the component
// stays within what the order consumed of it, and the distinct finished
// serials or batches stay within the order quantity (rounded up). The
// uniqueness of a component serial across the tenant is the store's check.
func CheckAsBuilt(a AsBuilt, f AsBuiltFacts) error {
	if f.Status != Started && f.Status != ReportedAsFinished {
		return &Error{Code: CodeAsBuiltState, Detail: fmt.Sprintf("as-built records are kept while the order is Started or ReportedAsFinished, not %s", f.Status)}
	}
	consumed, onBOM := decimal.Zero, false
	for _, l := range f.Lines {
		if l.ItemID == a.ComponentItemID {
			onBOM = true
			consumed = consumed.Add(l.QtyConsumed)
		}
	}
	if !onBOM {
		return &Error{Code: CodeAsBuiltNotOnBOM, Detail: fmt.Sprintf("component item %s is not on the order's BOM", a.ComponentItemID)}
	}
	if after := f.ComponentAssigned.Add(a.Qty); after.GreaterThan(consumed) {
		return &Error{Code: CodeAsBuiltExceedsConsumed, Detail: fmt.Sprintf("assigning %s would bring component %s to %s, more than the %s the order consumed",
			a.Qty, a.ComponentItemID, after, consumed)}
	}
	if !f.FinishedKnown && decimal.NewFromInt(int64(f.FinishedCount+1)).GreaterThan(f.OrderQty.Ceil()) {
		return &Error{Code: CodeAsBuiltFinishedCount, Detail: fmt.Sprintf("the order already has %d finished serials or batches for a quantity of %s",
			f.FinishedCount, f.OrderQty)}
	}
	return nil
}
