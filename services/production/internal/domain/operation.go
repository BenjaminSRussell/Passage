package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Stable error codes of the order quantity rules and the operation commands.
const (
	// CodeOrderQuantity refuses an order quantity below the item's minimum, above
	// its maximum or not a multiple of its multiple (FR-21.1).
	CodeOrderQuantity = "PRODUCTION_ORDER_QUANTITY"
	// CodeOperationState refuses `:finish` or `:reopen` on an operation of an
	// order that takes no feedback, or on a subcontracted operation (FR-21.6).
	CodeOperationState = "PRODUCTION_OPERATION_STATE"
)

// OrderQuantityRules are the item's released-product order settings (FR-21.1).
// A zero rule does not apply.
type OrderQuantityRules struct {
	Minimum  decimal.Decimal
	Maximum  decimal.Decimal
	Multiple decimal.Decimal
}

// CheckOrderQuantity applies the item's minimum, maximum and multiple to an
// order quantity (FR-21.1). The rules are not negative (the caller validates
// the request) and a maximum below the minimum leaves no valid quantity.
func CheckOrderQuantity(qty decimal.Decimal, r OrderQuantityRules) error {
	if r.Maximum.IsPositive() && r.Maximum.LessThan(r.Minimum) {
		return &Error{Code: CodeInvalidInput, Detail: fmt.Sprintf("maximum order quantity %s is below the minimum %s", r.Maximum, r.Minimum)}
	}
	if r.Minimum.IsPositive() && qty.LessThan(r.Minimum) {
		return &Error{Code: CodeOrderQuantity, Detail: fmt.Sprintf("quantity %s is below the minimum order quantity %s", qty, r.Minimum)}
	}
	if r.Maximum.IsPositive() && qty.GreaterThan(r.Maximum) {
		return &Error{Code: CodeOrderQuantity, Detail: fmt.Sprintf("quantity %s is above the maximum order quantity %s", qty, r.Maximum)}
	}
	if r.Multiple.IsPositive() && !qty.Mod(r.Multiple).IsZero() {
		return &Error{Code: CodeOrderQuantity, Detail: fmt.Sprintf("quantity %s is not a multiple of %s", qty, r.Multiple)}
	}
	return nil
}

// OperAction is a command on one operation of an order: `:finish` closes it to
// feedback without a route card, `:reopen` opens a finished one again (FR-21.6).
type OperAction string

const (
	OperFinish OperAction = "finish"
	OperReopen OperAction = "reopen"
)

// Valid reports whether a is one of the operation commands.
func (a OperAction) Valid() bool { return a == OperFinish || a == OperReopen }

// OperationTransition returns the operation status after action (FR-21.6) and
// whether it changes. Operations take feedback while the order is Started or
// ReportedAsFinished, so they are finished and reopened then. A subcontracted
// operation takes no route card and has no feedback status to change. Reopen
// returns the operation to started when it has feedback, otherwise to open, as
// a reversal of its route cards does. Repeating an action already in effect
// changes nothing.
func OperationTransition(order Status, op Operation, action OperAction) (OperStatus, bool, error) {
	if !action.Valid() {
		return op.Status, false, &Error{Code: CodeInvalidInput, Detail: fmt.Sprintf("unknown operation command %q", action)}
	}
	if order != Started && order != ReportedAsFinished {
		return op.Status, false, &Error{Code: CodeOperationState,
			Detail: fmt.Sprintf("operations are finished and reopened on a Started or ReportedAsFinished order, not %s", order)}
	}
	if op.Subcontract {
		return op.Status, false, &Error{Code: CodeOperationState,
			Detail: fmt.Sprintf("operation %d is subcontracted; its progress comes from the purchase order", op.OperNumber)}
	}
	current := op.Status
	if current == "" {
		current = OperOpen
	}
	switch {
	case action == OperFinish && current != OperFinished:
		return OperFinished, true, nil
	case action == OperReopen && current == OperFinished:
		if op.HoursReported.IsZero() && op.QtyGood.IsZero() && op.QtyError.IsZero() {
			return OperOpen, true, nil
		}
		return OperStarted, true, nil
	}
	return current, false, nil
}
