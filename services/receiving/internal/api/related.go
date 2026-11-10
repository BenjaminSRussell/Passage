package api

import (
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	dutyDocumentFlow = "receiving.document-flow.read"
	relatedLimit     = 200
)

type relatedReceipt struct {
	ID            string `json:"id"`
	ReceiptNumber string `json:"receiptNumber"`
	State         string `json:"state"`
	ReceiptDate   string `json:"receiptDate"`
	LedgerEntryID string `json:"ledgerEntryId,omitempty"`
}

type relatedInvoice struct {
	ID            string `json:"id"`
	InvoiceNumber string `json:"invoiceNumber"`
	Status        string `json:"status"`
	InvoiceDate   string `json:"invoiceDate"`
	Total         string `json:"total"`
	Currency      string `json:"currency"`
	LedgerEntryID string `json:"ledgerEntryId,omitempty"`
}

// relatedDocuments is the answer of GET /v1/receiving/related-documents: the receipts and
// vendor invoices that belong to the same purchase order as the document asked about.
type relatedDocuments struct {
	PurchaseOrderID string           `json:"purchaseOrderId,omitempty"`
	Receipts        []relatedReceipt `json:"receipts"`
	Invoices        []relatedInvoice `json:"invoices"`
	// Truncated is true when a list holds only the newest 200 documents.
	Truncated bool `json:"truncated"`
}

// relatedDocuments serves GET /v1/receiving/related-documents (issue #502, the document flow
// panel). Give exactly one of purchaseOrderId, receiptId or invoiceId. A receipt or an invoice
// is traced to its purchase order (an invoice through the receipts on its lines); the answer then
// lists every receipt and invoice of that order. A receipt that names no order stands alone with
// the invoices that settle it.
func (a *API) relatedDocuments(w http.ResponseWriter, r *http.Request) {
	q := query{values: r.URL.Query(), fields: map[string]string{}}
	poID, receiptID, invoiceID := q.uuid("purchaseOrderId"), q.uuid("receiptId"), q.uuid("invoiceId")
	given := 0
	for _, v := range []string{poID, receiptID, invoiceID} {
		if v != "" {
			given++
		}
	}
	if given != 1 && len(q.fields) == 0 {
		q.fields["purchaseOrderId"] = "give exactly one of purchaseOrderId, receiptId or invoiceId"
	}
	if len(q.fields) > 0 {
		httpx.Fail(w, r, validationProblem(q.fields))
		return
	}
	ctx := r.Context()
	if invoiceID != "" {
		inv, err := a.store.GetInvoice(ctx, invoiceID)
		if err != nil {
			writeError(w, r, err)
			return
		}
		for _, l := range inv.Lines {
			rec, err := a.store.GetReceipt(ctx, l.ReceiptID)
			if err != nil {
				writeError(w, r, err)
				return
			}
			if rec.PurchaseOrderID != "" {
				poID = rec.PurchaseOrderID
				break
			}
			receiptID = rec.ID
		}
		if poID == "" && receiptID == "" {
			// An invoice with no receipt lines has nothing upstream; show itself.
			httpx.JSON(w, http.StatusOK, relatedDocuments{Receipts: []relatedReceipt{}, Invoices: []relatedInvoice{toRelatedInvoice(inv)}})
			return
		}
	}
	if receiptID != "" && poID == "" {
		rec, err := a.store.GetReceipt(ctx, receiptID)
		if err != nil {
			writeError(w, r, err)
			return
		}
		poID = rec.PurchaseOrderID
	}
	out := relatedDocuments{PurchaseOrderID: poID, Receipts: []relatedReceipt{}, Invoices: []relatedInvoice{}}
	receipts, err := a.relatedReceipts(r, poID, receiptID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	invoices, err := a.store.ListInvoices(ctx, store.InvoiceFilter{PurchaseOrderID: poID, ReceiptID: receiptIDWhenNoOrder(poID, receiptID), Limit: relatedLimit + 1})
	if err != nil {
		writeError(w, r, err)
		return
	}
	if len(receipts) > relatedLimit {
		receipts, out.Truncated = receipts[:relatedLimit], true
	}
	if len(invoices) > relatedLimit {
		invoices, out.Truncated = invoices[:relatedLimit], true
	}
	for _, rec := range receipts {
		out.Receipts = append(out.Receipts, relatedReceipt{ID: rec.ID, ReceiptNumber: rec.Number, State: string(rec.State), ReceiptDate: toReceiptBody(rec).ReceiptDate, LedgerEntryID: toReceiptBody(rec).LedgerEntryID})
	}
	for _, inv := range invoices {
		out.Invoices = append(out.Invoices, toRelatedInvoice(inv))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func receiptIDWhenNoOrder(poID, receiptID string) string {
	if poID == "" {
		return receiptID
	}
	return ""
}

func (a *API) relatedReceipts(r *http.Request, poID, receiptID string) ([]store.Receipt, error) {
	if poID != "" {
		return a.store.ListReceipts(r.Context(), store.ReceiptFilter{PurchaseOrderID: poID, Limit: relatedLimit + 1})
	}
	rec, err := a.store.GetReceipt(r.Context(), receiptID)
	if err != nil {
		return nil, err
	}
	return []store.Receipt{rec}, nil
}

func toRelatedInvoice(inv store.Invoice) relatedInvoice {
	b := toInvoiceBody(inv)
	return relatedInvoice{ID: b.ID, InvoiceNumber: b.InvoiceNumber, Status: b.Status, InvoiceDate: b.InvoiceDate, Total: b.Total, Currency: b.Currency, LedgerEntryID: b.LedgerEntryID}
}
