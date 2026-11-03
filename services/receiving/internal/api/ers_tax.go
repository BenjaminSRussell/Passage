package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const serviceSheetSlipPrefix = "SES-"

func ersInvoiceNumber(rec store.Receipt) string {
	if id, ok := cutSheetID(rec.PackingSlip); ok {
		return "ERS-" + id
	}
	return "ERS-" + rec.Number
}

func cutSheetID(packingSlip string) (string, bool) {
	if len(packingSlip) <= len(serviceSheetSlipPrefix) || packingSlip[:len(serviceSheetSlipPrefix)] != serviceSheetSlipPrefix {
		return "", false
	}
	id := packingSlip[len(serviceSheetSlipPrefix):]
	if !ids.Valid(id) {
		return "", false
	}
	return id, true
}

func (a *API) applyERSTax(ctx context.Context, in *store.Invoice) error {
	if a.ports.Tax == nil || in.TaxAmount.IsPositive() {
		return nil
	}
	doc := domain.TaxDocument{Currency: in.Currency, Date: in.InvoiceDate.Format(dateLayout)}
	for i, l := range in.Lines {
		doc.Lines = append(doc.Lines, domain.TaxDocumentLine{Ref: strconv.Itoa(i + 1), Amount: l.Amount})
	}
	res, err := a.ports.Tax.CalculateInput(ctx, in.LegalEntityID, doc)
	if err != nil {
		slog.ErrorContext(ctx, "ers tax calculation failed", "err", err, "invoiceId", in.ID, "correlationId", middleware.GetReqID(ctx))
		return unavailable("tax calculation unavailable")
	}
	in.TaxAmount, in.TaxCode = res.Amount, res.Code
	return nil
}

func (a *API) issueSelfBill(ctx context.Context, tenantID string, in store.Invoice) error {
	net := invoiceAmount(in)
	doc := domain.SelfBill{
		InvoiceID: in.ID, InvoiceNumber: in.InvoiceNumber, LegalEntityID: in.LegalEntityID, VendorID: in.VendorID,
		InvoiceDate: in.InvoiceDate.Format(dateLayout), Currency: in.Currency, TaxCode: in.TaxCode,
		Net: net, Tax: in.TaxAmount, Total: in.Total,
	}
	if doc.Total.IsZero() {
		doc.Total = net.Add(in.TaxAmount)
	}
	for i, l := range in.Lines {
		doc.Lines = append(doc.Lines, domain.SelfBillLine{LineNo: i + 1, Qty: l.Qty, UnitPrice: l.UnitPrice, Amount: l.Amount})
	}
	evt, err := selfBillIssuedEvent(tenantID, in, doc)
	if err != nil {
		return err
	}
	return a.store.PutSelfBill(ctx, store.SelfBill{
		InvoiceID: in.ID, LegalEntityID: in.LegalEntityID, VendorID: in.VendorID, InvoiceNumber: in.InvoiceNumber,
		ContentType: "application/pdf", Body: doc.RenderPDF(), TaxCode: in.TaxCode, TaxAmount: in.TaxAmount, Total: doc.Total,
	}, evt)
}

func (a *API) recordERSTax(ctx context.Context, in store.Invoice) {
	if a.ports.TaxRecorder == nil || !in.TaxAmount.IsPositive() || in.TaxCode == "" || !ids.Valid(in.ID) {
		return
	}
	if err := a.ports.TaxRecorder.RecordInput(ctx, in.LegalEntityID, domain.TaxTxn{
		ID: in.ID, Code: in.TaxCode, Date: in.InvoiceDate.Format(dateLayout), Amount: in.TaxAmount,
	}); err != nil {
		slog.WarnContext(ctx, "ers tax transaction was not recorded", "err", err, "invoiceId", in.ID, "correlationId", middleware.GetReqID(ctx))
	}
}

func (a *API) getSelfBill(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, validationProblem(map[string]string{"id": "must be a UUID"}))
		return
	}
	doc, err := a.store.GetSelfBill(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := inScope(p, doc.LegalEntityID); err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", doc.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.pdf"`, doc.InvoiceNumber))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(doc.Body)
}

type selfBillIssuedData struct {
	InvoiceID     string `json:"invoice_id"`
	InvoiceNumber string `json:"invoice_number"`
	LegalEntityID string `json:"legal_entity_id"`
	VendorID      string `json:"vendor_id"`
	Currency      string `json:"currency"`
	TaxCode       string `json:"tax_code,omitempty"`
	Tax           string `json:"tax"`
	Total         string `json:"total"`
}

func selfBillIssuedEvent(tenantID string, in store.Invoice, doc domain.SelfBill) (events.Event, error) {
	scale := money.MinorUnits(in.Currency)
	return newEvent("receiving", "ersselfbill", "issued", in.ID, tenantID, selfBillIssuedData{
		InvoiceID: in.ID, InvoiceNumber: in.InvoiceNumber, LegalEntityID: in.LegalEntityID, VendorID: in.VendorID,
		Currency: in.Currency, TaxCode: in.TaxCode, Tax: doc.Tax.StringFixed(scale), Total: doc.Total.StringFixed(scale),
	})
}
