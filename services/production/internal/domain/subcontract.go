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
