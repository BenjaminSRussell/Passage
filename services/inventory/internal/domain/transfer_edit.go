package domain

import (
	"slices"
	"time"

	"github.com/shopspring/decimal"
)

// LineEdit changes the quantity or tolerances of one line of a CREATED order.
// A nil field is left as it is.
type LineEdit struct {
	LineID   string
	Qty      *decimal.Decimal
	UnderPct *decimal.Decimal
	OverPct  *decimal.Decimal
}

// TransferEdit is one atomic change of a CREATED transfer order (FR-16.15):
// header fields, lines added, lines changed and lines removed. A nil header
// field is left as it is; ClearPlannedReceipt removes the planned receipt date.
type TransferEdit struct {
	ShipDate            *time.Time
	PlannedReceipt      *time.Time
	ClearPlannedReceipt bool
	ToSite              *string
	Add                 []TransferLine
	Update              []LineEdit
	Remove              []string
}

// Empty reports whether the edit changes nothing.
func (e TransferEdit) Empty() bool {
	return e.ShipDate == nil && e.PlannedReceipt == nil && !e.ClearPlannedReceipt && e.ToSite == nil &&
		len(e.Add) == 0 && len(e.Update) == 0 && len(e.Remove) == 0
}

// CanEdit allows an edit only while the order is CREATED, before anything has
// shipped (INV-TRF-006). Warehouses, transit and the legal entity never change:
// cancel the order and create another.
func CanEdit(o TransferOrder) error {
	if o.Status != TransferCreated {
		return fail(CodeTransferState, "transfer order in status %s cannot be edited", o.Status)
	}
	for _, l := range o.Lines {
		if l.ShippedQty.IsPositive() {
			return fail(CodeTransferState, "transfer order %s has shipped quantity and cannot be edited", o.Number)
		}
	}
	return nil
}

// ApplyTransferEdit applies e to a CREATED order and returns the edited order.
// Added lines come back with an empty ID and the next line numbers after the
// highest line on the order before the edit, so a line removed in the same
// edit never gives its number to an added one (a number removed by an earlier
// edit can come back); removed lines are gone.
// The result is validated like a new order, so an order never ends without
// lines or with a line that would not have been accepted at creation.
func ApplyTransferEdit(o TransferOrder, e TransferEdit, maxScale int32) (TransferOrder, error) {
	if err := CanEdit(o); err != nil {
		return o, err
	}
	if e.Empty() {
		return o, fail(CodeInvalid, "the edit changes nothing")
	}
	byID := map[string]int{}
	for i, l := range o.Lines {
		byID[l.ID] = i
	}
	next := o
	next.Lines = slices.Clone(o.Lines)
	touched := map[string]bool{}
	for _, id := range e.Remove {
		if _, ok := byID[id]; !ok {
			return o, fail(CodeTransferNotFound, "line %s is not on transfer order %s", id, o.Number)
		}
		if touched[id] {
			return o, fail(CodeInvalid, "line %s is named twice", id)
		}
		touched[id] = true
	}
	for _, u := range e.Update {
		i, ok := byID[u.LineID]
		if !ok {
			return o, fail(CodeTransferNotFound, "line %s is not on transfer order %s", u.LineID, o.Number)
		}
		if touched[u.LineID] {
			return o, fail(CodeInvalid, "line %s is named twice", u.LineID)
		}
		touched[u.LineID] = true
		if u.Qty != nil {
			next.Lines[i].Qty = *u.Qty
		}
		if u.UnderPct != nil {
			next.Lines[i].UnderPct = *u.UnderPct
		}
		if u.OverPct != nil {
			next.Lines[i].OverPct = *u.OverPct
		}
	}
	lineNo := 0
	for _, l := range o.Lines {
		lineNo = max(lineNo, l.LineNo)
	}
	next.Lines = slices.DeleteFunc(next.Lines, func(l TransferLine) bool { return slices.Contains(e.Remove, l.ID) })
	for _, l := range e.Add {
		lineNo++
		l.ID, l.LineNo = "", lineNo
		next.Lines = append(next.Lines, l)
	}
	if e.ShipDate != nil {
		next.ShipDate = *e.ShipDate
	}
	switch {
	case e.ClearPlannedReceipt:
		next.PlannedReceipt = time.Time{}
	case e.PlannedReceipt != nil:
		next.PlannedReceipt = *e.PlannedReceipt
	}
	if e.ToSite != nil {
		next.ToSite = *e.ToSite
	}
	if err := ValidateNewTransfer(next, maxScale); err != nil {
		return o, err
	}
	return next, nil
}
