package api

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	eventSource  = "/passage/receiving/receiving"
	acctRounding = 4
)

type postedLineData struct {
	LineNo          int      `json:"line_no"`
	POLineID        string   `json:"po_line_id"`
	ProductID       string   `json:"product_id"`
	Unit            string   `json:"unit"`
	Qty             string   `json:"qty"`
	Batch           string   `json:"batch"`
	Serials         []string `json:"serials"`
	LocationID      string   `json:"location_id"`
	InventoryStatus string   `json:"inventory_status"`
	Amount          string   `json:"amount"`
	AmountAcct      string   `json:"amount_acct"`
}

type receiptPostedData struct {
	ReceiptID     string           `json:"receipt_id"`
	ReceiptNumber string           `json:"receipt_number"`
	POID          string           `json:"po_id"`
	LegalEntityID string           `json:"legal_entity_id"`
	VendorID      string           `json:"vendor_id"`
	PostingDate   string           `json:"posting_date"`
	Currency      string           `json:"currency"`
	LedgerEntryID string           `json:"ledger_entry_id"`
	Lines         []postedLineData `json:"lines"`
}

type correctedLineData struct {
	LineNo          int    `json:"line_no"`
	POLineID        string `json:"po_line_id"`
	ProductID       string `json:"product_id"`
	QtyDelta        string `json:"qty_delta"`
	AmountDelta     string `json:"amount_delta"`
	AmountAcctDelta string `json:"amount_acct_delta"`
}

type receiptCorrectedData struct {
	ReceiptID     string              `json:"receipt_id"`
	ReceiptNumber string              `json:"receipt_number"`
	Kind          string              `json:"kind"`
	CorrectionSeq int                 `json:"correction_seq"`
	LedgerEntryID string              `json:"ledger_entry_id"`
	Currency      string              `json:"currency"`
	Lines         []correctedLineData `json:"lines"`
}

type invoicePostedLineData struct {
	LineNo        int    `json:"line_no"`
	ReceiptID     string `json:"receipt_id"`
	ReceiptLineNo int    `json:"receipt_line_no"`
	Qty           string `json:"qty"`
	UnitPrice     string `json:"unit_price"`
	Amount        string `json:"amount"`
	GrirCleared   string `json:"grir_cleared_amount"`
}

type invoicePostedData struct {
	InvoiceID     string                  `json:"invoice_id"`
	OneTimePayee  string                  `json:"one_time_payee_id,omitempty"`
	LegalEntityID string                  `json:"legal_entity_id"`
	VendorID      string                  `json:"vendor_id"`
	InvoiceNumber string                  `json:"invoice_number"`
	Source        string                  `json:"source"`
	InvoiceDate   string                  `json:"invoice_date"`
	PostingDate   string                  `json:"posting_date"`
	Currency      string                  `json:"currency"`
	Total         string                  `json:"total"`
	LedgerEntryID string                  `json:"ledger_entry_id"`
	Lines         []invoicePostedLineData `json:"lines"`
}

type invoiceSampledData struct {
	InvoiceID     string `json:"invoice_id"`
	OneTimePayee  string `json:"one_time_payee_id,omitempty"`
	LegalEntityID string `json:"legal_entity_id"`
	VendorID      string `json:"vendor_id"`
	InvoiceNumber string `json:"invoice_number"`
	Source        string `json:"source"`
	Currency      string `json:"currency"`
	Total         string `json:"total"`
	SampleRatePct string `json:"sample_rate_pct"`
	Reason        string `json:"reason"`
}

type invoiceRejectedData struct {
	InvoiceID     string `json:"invoice_id"`
	LegalEntityID string `json:"legal_entity_id"`
	VendorID      string `json:"vendor_id"`
	InvoiceNumber string `json:"invoice_number"`
	Source        string `json:"source"`
	Currency      string `json:"currency"`
	Total         string `json:"total"`
	Reason        string `json:"reason"`
	RejectedBy    string `json:"rejected_by"`
}

type auditPolicyChangedData struct {
	LegalEntityID string `json:"legal_entity_id"`
	OldRatePct    string `json:"old_rate_pct"`
	NewRatePct    string `json:"new_rate_pct"`
	ChangedBy     string `json:"changed_by"`
}

type ersVendorChangedData struct {
	LegalEntityID string `json:"legal_entity_id"`
	VendorID      string `json:"vendor_id"`
	OldEnabled    bool   `json:"old_enabled"`
	NewEnabled    bool   `json:"new_enabled"`
	ChangedBy     string `json:"changed_by"`
}

type invoiceAdjustedLineData struct {
	LineNo        int    `json:"line_no"`
	InvoiceLineNo int    `json:"invoice_line_no"`
	Amount        string `json:"amount"`
}

type invoiceAdjustedData struct {
	AdjustmentID      string                    `json:"adjustment_id"`
	OriginalInvoiceID string                    `json:"original_invoice_id"`
	Kind              string                    `json:"kind"`
	LegalEntityID     string                    `json:"legal_entity_id"`
	VendorID          string                    `json:"vendor_id"`
	DocumentNumber    string                    `json:"document_number"`
	DocumentDate      string                    `json:"document_date"`
	PostingDate       string                    `json:"posting_date"`
	Currency          string                    `json:"currency"`
	Total             string                    `json:"total"`
	LedgerEntryID     string                    `json:"ledger_entry_id"`
	Lines             []invoiceAdjustedLineData `json:"lines"`
}

type settlementPostedData struct {
	SettlementID  string `json:"settlement_id"`
	Kind          string `json:"kind"`
	SourceRef     string `json:"source_ref"`
	LegalEntityID string `json:"legal_entity_id"`
	VendorID      string `json:"vendor_id"`
	PostingDate   string `json:"posting_date"`
	Currency      string `json:"currency"`
	Amount        string `json:"amount"`
	LedgerEntryID string `json:"ledger_entry_id"`
}

func newEvent(context, entity, verb, subject, tenantID string, data any) (events.Event, error) {
	return events.New(events.Type(context, entity, verb, 1), eventSource, subject, tenantID, data)
}

func acct(v, rate decimal.Decimal) decimal.Decimal { return v.Mul(rate).RoundBank(acctRounding) }

func receiptPostedEvent(tenantID string, r store.Receipt, amounts []store.LineAmount, ledgerEntryID string) (events.Event, error) {
	data := receiptPostedData{
		ReceiptID: r.ID, ReceiptNumber: r.Number, POID: r.PurchaseOrderID, LegalEntityID: r.LegalEntityID, VendorID: r.VendorID,
		PostingDate: r.Date.Format(dateLayout), Currency: r.Currency, LedgerEntryID: ledgerEntryID, Lines: make([]postedLineData, 0, len(r.Lines)),
	}
	byLine := map[int]store.LineAmount{}
	for _, a := range amounts {
		byLine[a.LineNo] = a
	}
	for _, l := range r.Lines {
		serials := l.Serials
		if serials == nil {
			serials = []string{}
		}
		a := byLine[l.LineNo]
		data.Lines = append(data.Lines, postedLineData{
			LineNo: l.LineNo, POLineID: l.POLineID, ProductID: l.ProductID, Unit: l.Unit, Qty: l.Qty.String(), Batch: l.Batch, Serials: serials,
			LocationID: l.LocationID, InventoryStatus: l.InventoryStatus, Amount: a.Amount.String(), AmountAcct: a.AmountAcct.String(),
		})
	}
	return newEvent("receiving", "receipt", "posted", r.ID, tenantID, data)
}

func receiptCorrectedEvent(tenantID, kind string, r store.Receipt, seq int, ledgerEntryID string, lines []correctedLineData) (events.Event, error) {
	return newEvent("receiving", "receipt", "corrected", r.ID, tenantID, receiptCorrectedData{
		ReceiptID: r.ID, ReceiptNumber: r.Number, Kind: kind, CorrectionSeq: seq, LedgerEntryID: ledgerEntryID, Currency: r.Currency, Lines: lines,
	})
}

func invoiceSampledEvent(tenantID string, in store.Invoice, total, ratePct decimal.Decimal) (events.Event, error) {
	return newEvent("payables", "invoice", "sampled", in.ID, tenantID, invoiceSampledData{
		InvoiceID: in.ID, OneTimePayee: in.OneTimePayeeID, LegalEntityID: in.LegalEntityID, VendorID: in.VendorID, InvoiceNumber: in.InvoiceNumber, Source: in.Source,
		Currency: in.Currency, Total: total.String(), SampleRatePct: ratePct.String(), Reason: in.AuditReason,
	})
}

func invoiceRejectedEvent(tenantID string, in store.Invoice, reason, by string) (events.Event, error) {
	return newEvent("payables", "invoice", "rejected", in.ID, tenantID, invoiceRejectedData{
		InvoiceID: in.ID, LegalEntityID: in.LegalEntityID, VendorID: in.VendorID, InvoiceNumber: in.InvoiceNumber, Source: in.Source,
		Currency: in.Currency, Total: in.Total.String(), Reason: reason, RejectedBy: by,
	})
}

func auditPolicyChangedEvent(tenantID, legalEntityID string, oldRate, newRate decimal.Decimal, by string) (events.Event, error) {
	return newEvent("receiving", "auditpolicy", "changed", legalEntityID, tenantID, auditPolicyChangedData{
		LegalEntityID: legalEntityID, OldRatePct: oldRate.String(), NewRatePct: newRate.String(), ChangedBy: by,
	})
}

func ersVendorChangedEvent(tenantID, legalEntityID, vendorID string, oldEnabled, newEnabled bool, by string) (events.Event, error) {
	return newEvent("receiving", "ersvendor", "changed", vendorID, tenantID, ersVendorChangedData{
		LegalEntityID: legalEntityID, VendorID: vendorID, OldEnabled: oldEnabled, NewEnabled: newEnabled, ChangedBy: by,
	})
}

func invoicePostedEvent(tenantID string, in store.Invoice, ledgerEntryID string, total decimal.Decimal, cleared []decimal.Decimal) (events.Event, error) {
	data := invoicePostedData{
		InvoiceID: in.ID, OneTimePayee: in.OneTimePayeeID, LegalEntityID: in.LegalEntityID, VendorID: in.VendorID, InvoiceNumber: in.InvoiceNumber, Source: in.Source,
		InvoiceDate: in.InvoiceDate.Format(dateLayout), PostingDate: in.PostingDate.Format(dateLayout), Currency: in.Currency,
		Total: total.String(), LedgerEntryID: ledgerEntryID, Lines: make([]invoicePostedLineData, 0, len(in.Lines)),
	}
	for i, l := range in.Lines {
		data.Lines = append(data.Lines, invoicePostedLineData{
			LineNo: i + 1, ReceiptID: l.ReceiptID, ReceiptLineNo: l.ReceiptLineNo, Qty: l.Qty.String(), UnitPrice: l.UnitPrice.String(), Amount: l.Amount.String(), GrirCleared: cleared[i].String(),
		})
	}
	return newEvent("payables", "invoice", "posted", in.ID, tenantID, data)
}

func invoiceAdjustedEvent(tenantID string, in store.InvoiceAdjustment, vendorID, currency, ledgerEntryID string, total decimal.Decimal) (events.Event, error) {
	data := invoiceAdjustedData{
		AdjustmentID: in.ID, OriginalInvoiceID: in.OriginalInvoiceID, Kind: in.Kind, LegalEntityID: in.LegalEntityID, VendorID: vendorID,
		DocumentNumber: in.DocumentNumber, DocumentDate: in.DocumentDate.Format(dateLayout), PostingDate: in.PostingDate.Format(dateLayout),
		Currency: currency, Total: total.String(), LedgerEntryID: ledgerEntryID, Lines: make([]invoiceAdjustedLineData, 0, len(in.Lines)),
	}
	for i, l := range in.Lines {
		data.Lines = append(data.Lines, invoiceAdjustedLineData{LineNo: i + 1, InvoiceLineNo: l.InvoiceLineNo, Amount: l.Amount.String()})
	}
	return newEvent("payables", "invoice", "adjusted", in.ID, tenantID, data)
}

func settlementPostedEvent(tenantID string, in store.Settlement, ledgerEntryID string) (events.Event, error) {
	return newEvent("payables", "settlement", "posted", in.ID, tenantID, settlementPostedData{
		SettlementID: in.ID, Kind: in.Kind, SourceRef: in.SourceRef, LegalEntityID: in.LegalEntityID, VendorID: in.VendorID,
		PostingDate: in.PostingDate.Format(dateLayout), Currency: in.Currency, Amount: in.Amount.String(), LedgerEntryID: ledgerEntryID,
	})
}
