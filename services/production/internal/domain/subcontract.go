package domain

import "github.com/shopspring/decimal"

// SubcontractRequest is the purchase request raised for one subcontract
// operation of a released order (FR-21.10): the vendor resource, the quantity
// to process (the operation's process quantity, BR-21.8), the vendor unit price
// and the planned amount, which is the estimate's subcontract cost row.
type SubcontractRequest struct {
	OperNumber int
	ResourceID string
	Qty        decimal.Decimal
	UnitPrice  decimal.Decimal
	Amount     decimal.Decimal
}

// SubcontractAmount is the planned subcontract cost of an operation: the vendor
// unit price × the process quantity at cost scale, as Estimate plans it.
func SubcontractAmount(unitPrice, processQty decimal.Decimal) decimal.Decimal {
	return money(unitPrice.Mul(processQty))
}

// SubcontractRequests lists the purchase requests for the subcontract operations
// among ops, in route order. Operations on in-house resources raise none.
func SubcontractRequests(ops []Operation) []SubcontractRequest {
	out := []SubcontractRequest{}
	for _, op := range ops {
		if !op.Subcontract {
			continue
		}
		out = append(out, SubcontractRequest{
			OperNumber: op.OperNumber,
			ResourceID: op.ResourceID,
			Qty:        op.ProcessQty,
			UnitPrice:  op.SubcontractPrice,
			Amount:     SubcontractAmount(op.SubcontractPrice, op.ProcessQty),
		})
	}
	return out
}

// SubcontractAction is what a change to an order does to an operation's vendor
// purchase request (FR-21.10): `change` updates the PO line, `cancel` withdraws it.
type SubcontractAction string

const (
	SubcontractChange SubcontractAction = "change"
	SubcontractCancel SubcontractAction = "cancel"
)

// Reasons a purchase request is withdrawn.
const (
	CancelOrderCancelled = "order-cancelled"
	CancelOpRemoved      = "operation-removed"
	CancelSubcontractOff = "subcontract-off"
	CancelVendorChanged  = "vendor-changed"
)

// RequestedOp is a subcontract operation as purchasing was last told about it.
type RequestedOp struct {
	OperNumber int
	ResourceID string
	Qty        decimal.Decimal
	UnitPrice  decimal.Decimal
}

// SubcontractChangePlan is one follow-up request owed to purchasing for an
// operation that was requested before: the previous figures, the current ones
// (zero on a cancel) and the reason a cancel carries.
type SubcontractChangePlan struct {
	Action     SubcontractAction
	Reason     string
	OperNumber int
	ResourceID string
	Previous   RequestedOp
	Qty        decimal.Decimal
	UnitPrice  decimal.Decimal
	Amount     decimal.Decimal
	// KeepRow is false when the operation row is gone; otherwise the row's
	// request marker is cleared on a cancel so a fresh request can follow.
	KeepRow bool
}

// PlanSubcontractChanges compares what was requested (before, in route order)
// with the order's operations now and lists the change and cancel requests
// purchasing is owed. A cancelled order withdraws every request. Otherwise an
// operation that is gone, no longer subcontracted or moved to another vendor
// resource is withdrawn (a vendor change is then requested afresh by the
// caller's claim), and one whose process quantity or unit price differs, or
// every kept one when the order's delivery date (the PO due date) moved
// (dueChanged), is changed. An unchanged operation owes nothing.
func PlanSubcontractChanges(before []RequestedOp, now []Operation, orderCancelled, dueChanged bool) []SubcontractChangePlan {
	current := map[int]Operation{}
	for _, op := range now {
		current[op.OperNumber] = op
	}
	out := []SubcontractChangePlan{}
	for _, was := range before {
		op, ok := current[was.OperNumber]
		plan := SubcontractChangePlan{OperNumber: was.OperNumber, ResourceID: was.ResourceID, Previous: was, KeepRow: ok}
		switch {
		case orderCancelled:
			plan.Action, plan.Reason = SubcontractCancel, CancelOrderCancelled
		case !ok:
			plan.Action, plan.Reason = SubcontractCancel, CancelOpRemoved
		case !op.Subcontract:
			plan.Action, plan.Reason = SubcontractCancel, CancelSubcontractOff
		case op.ResourceID != was.ResourceID:
			plan.Action, plan.Reason = SubcontractCancel, CancelVendorChanged
		case dueChanged || !op.ProcessQty.Equal(was.Qty) || !op.SubcontractPrice.Equal(was.UnitPrice):
			plan.Action = SubcontractChange
			plan.Qty, plan.UnitPrice = op.ProcessQty, op.SubcontractPrice
			plan.Amount = SubcontractAmount(op.SubcontractPrice, op.ProcessQty)
			plan.ResourceID = op.ResourceID
		default:
			continue
		}
		out = append(out, plan)
	}
	return out
}
