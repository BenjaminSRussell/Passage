package domain

import "github.com/shopspring/decimal"

// CanChange reports whether an order in status may be changed by PATCH. Orders
// that have shipped or closed are changed through returns and credit notes.
func CanChange(status string) bool {
	return status == OrderDraft || status == OrderOpen || status == OrderConfirmed
}

// CanCancel reports whether an order may be cancelled (BR-27.5).
func CanCancel(status string) bool { return CanChange(status) }

// Line statuses (BR-27.5).
const (
	LineOpen      = "OPEN"
	LineReserved  = "RESERVED"
	LineCancelled = "CANCELLED"
)

// LineChangeable reports whether a line in status may still be changed or
// cancelled. Lines move to CANCELLED only from OPEN or RESERVED (BR-27.5); a
// picked, delivered or invoiced line is corrected by a return, not a change.
func LineChangeable(status string) bool { return status == LineOpen || status == LineReserved }

// Repromises reports whether a change to a line of a confirmed order must
// re-run the promise: a new quantity or requested date does, a new price or
// discount does not (FR-27.9, AC-27.7).
func Repromises(before, after LineState) bool {
	return !before.Quantity.Equal(after.Quantity) || before.RequestedShipDate != after.RequestedShipDate
}

// LineState is the editable part of an order line.
type LineState struct {
	Quantity          decimal.Decimal
	UnitPrice         decimal.Decimal
	DiscPct           decimal.Decimal
	RequestedShipDate string
}

// FieldChange is one entry of a version's diff. LineNo is 0 for header fields.
type FieldChange struct {
	LineNo int    `json:"lineNo"`
	Field  string `json:"field"`
	Old    string `json:"old"`
	New    string `json:"new"`
}

// DiffLine lists the editable fields of one line that differ between before and
// after, in a fixed order so diffs are stable across runs.
func DiffLine(lineNo int, before, after LineState) []FieldChange {
	var out []FieldChange
	add := func(field, oldV, newV string) {
		if oldV != newV {
			out = append(out, FieldChange{LineNo: lineNo, Field: field, Old: oldV, New: newV})
		}
	}
	add("quantity", before.Quantity.String(), after.Quantity.String())
	add("unitPrice", before.UnitPrice.String(), after.UnitPrice.String())
	add("discPct", before.DiscPct.String(), after.DiscPct.String())
	add("requestedShipDate", before.RequestedShipDate, after.RequestedShipDate)
	return out
}

// DiffHeader returns a single change when a header field moved, else nil.
func DiffHeader(field, before, after string) []FieldChange {
	if before == after {
		return nil
	}
	return []FieldChange{{Field: field, Old: before, New: after}}
}
