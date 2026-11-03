// Package reimbursement holds the wire contract between expense and payables for
// paying an approved expense report: expense publishes ReportApproved, payables turns
// it into a vendor open item, and payables publishes OpenItemSettled when a payment
// run settles that item.
package reimbursement

import "github.com/BenjaminSRussell/passage/platform/events"

var (
	ReportApprovedType  = events.Type("expense", "report", "approved", 1)
	OpenItemSettledType = events.Type("payables", "openitem", "settled", 1)
)

const (
	StatusSettled = "SETTLED"
	InvoicePrefix = "EXP-"
)

type ReportApproved struct {
	ReportID      string `json:"reportId"`
	LegalEntityID string `json:"legalEntityId"`
	WorkerID      string `json:"workerId"`
	Currency      string `json:"currency"`
	Amount        string `json:"amount"`
	ApprovedOn    string `json:"approvedOn"`
}

type OpenItemSettled struct {
	OpenItemID      string `json:"open_item_id"`
	LegalEntityID   string `json:"legal_entity_id"`
	VendorID        string `json:"vendor_id"`
	SourceInvoiceID string `json:"source_invoice_id"`
	Currency        string `json:"currency"`
	OpenAmount      string `json:"open_amount"`
	Status          string `json:"status"`
	SettledAmount   string `json:"settled_amount"`
	SettlementID    string `json:"settlement_id"`
}
