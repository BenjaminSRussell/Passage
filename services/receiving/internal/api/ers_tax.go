package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	serviceSheetSlipPrefix = "SES-"
	ErsSelfBillTopic       = "passage.receiving.ersselfbill"
	ErsSelfBillIssuedType  = "dev.passage.receiving.ersselfbill.issued.v1"
)

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

func (a *API) selfBillParties(ctx context.Context, in store.Invoice) (domain.SelfBillParties, error) {
	if a.ports.SelfBillParties == nil {
		return domain.SelfBillParties{}, unavailable("self-billing party data is not configured")
	}
	parties, err := a.ports.SelfBillParties.SelfBillParties(ctx, in.LegalEntityID, in.VendorID)
	if errors.Is(err, domain.ErrSelfBillPartyIncomplete) {
		slog.WarnContext(ctx, "self-billing party data incomplete", "err", err, "invoiceId", in.ID, "vendorId", in.VendorID, "correlationId", middleware.GetReqID(ctx))
		return domain.SelfBillParties{}, err
	}
	if err != nil {
		slog.ErrorContext(ctx, "self-billing party lookup failed", "err", err, "invoiceId", in.ID, "vendorId", in.VendorID, "correlationId", middleware.GetReqID(ctx))
		return domain.SelfBillParties{}, unavailable("self-billing party data unavailable")
	}
	return parties, nil
}

func (a *API) latestReceiptDate(ctx context.Context, in store.Invoice) (time.Time, error) {
	var latest time.Time
	seen := map[string]struct{}{}
	for _, l := range in.Lines {
		if _, done := seen[l.ReceiptID]; done {
			continue
		}
		seen[l.ReceiptID] = struct{}{}
		rec, err := a.store.GetReceipt(ctx, l.ReceiptID)
		if err != nil {
			return time.Time{}, err
		}
		if rec.Date.After(latest) {
			latest = rec.Date
		}
	}
	return latest, nil
}

func (a *API) applyERSTax(ctx context.Context, in *store.Invoice, parties domain.SelfBillParties) error {
	if a.ports.Tax == nil {
		return domain.ErrTaxNotDetermined
	}
	country := parties.Customer.Country
	if supplier := parties.Supplier.Country; supplier == "" || !strings.EqualFold(supplier, country) {
		return domain.ErrTaxNotDetermined
	}
	doc := domain.TaxDocument{Country: country, Currency: in.Currency, Date: in.InvoiceDate.Format(dateLayout)}
	for i, l := range in.Lines {
		doc.Lines = append(doc.Lines, domain.TaxDocumentLine{Ref: strconv.Itoa(i + 1), Amount: l.Amount})
	}
	res, err := a.ports.Tax.CalculateInput(ctx, in.LegalEntityID, doc)
	if errors.Is(err, domain.ErrTaxNotDetermined) {
		return err
	}
	if err != nil {
		slog.ErrorContext(ctx, "ers tax calculation failed", "err", err, "invoiceId", in.ID, "correlationId", middleware.GetReqID(ctx))
		return unavailable("tax calculation unavailable")
	}
	in.TaxAmount, in.TaxCode, in.TaxRatePct = res.Amount, res.Code, res.RatePct
	return nil
}

func (a *API) selfBillBuild(tenantID string, in store.Invoice, parties domain.SelfBillParties) store.SelfBillBuild {
	return func(number string) (store.SelfBill, events.Event, error) {
		net := invoiceAmount(in)
		doc := domain.SelfBill{
			Number: number, InvoiceID: in.ID, InvoiceNumber: in.InvoiceNumber, LegalEntityID: in.LegalEntityID, VendorID: in.VendorID,
			InvoiceDate: in.InvoiceDate.Format(dateLayout), SupplyDate: in.SupplyDate.Format(dateLayout), Currency: in.Currency, TaxCode: in.TaxCode, TaxRatePct: in.TaxRatePct,
			Parties: parties, Net: net, Tax: in.TaxAmount, Total: net.Add(in.TaxAmount),
		}
		for i, l := range in.Lines {
			doc.Lines = append(doc.Lines, domain.SelfBillLine{LineNo: i + 1, Qty: l.Qty, UnitPrice: l.UnitPrice, Amount: l.Amount})
		}
		evt, err := selfBillIssuedEvent(tenantID, in, doc)
		return store.SelfBill{
			Number: number, InvoiceID: in.ID, LegalEntityID: in.LegalEntityID, VendorID: in.VendorID, InvoiceNumber: in.InvoiceNumber,
			ContentType: "application/pdf", Body: doc.RenderPDF(), TaxCode: in.TaxCode, TaxAmount: in.TaxAmount, Total: doc.Total,
		}, evt, err
	}
}

func (a *API) ErsTaxConsumerHandlers() map[string]eventing.Handler {
	return map[string]eventing.Handler{ErsSelfBillIssuedType: eventing.HandlerFunc(a.recordSelfBillTax)}
}

func (a *API) recordSelfBillTax(ctx context.Context, _ pgx.Tx, evt events.Event) error {
	var data selfBillIssuedData
	if err := json.Unmarshal(evt.Data, &data); err != nil {
		return eventing.Permanent(fmt.Errorf("ersselfbill.issued data: %w", err))
	}
	tax, taxErr := decimal.NewFromString(data.Tax)
	if taxErr != nil || !ids.Valid(data.InvoiceID) || !ids.Valid(data.LegalEntityID) {
		return eventing.Permanent(errors.New("ersselfbill.issued needs invoice_id, legal_entity_id and a decimal tax"))
	}
	if !tax.IsPositive() {
		return nil
	}
	if a.ports.TaxRecorder == nil || data.TaxCode == "" {
		return eventing.Permanent(errors.New("input tax cannot be recorded: no tax recorder or no tax code"))
	}
	actor, err := tenant.ServicePrincipal(ErsConsumerName, evt.TenantID, data.LegalEntityID)
	if err != nil {
		return eventing.Permanent(err)
	}
	ctx, cancel := context.WithTimeout(tenant.With(ctx, actor), ersSettleTimeout)
	defer cancel()
	if err := a.ports.TaxRecorder.RecordInput(ctx, data.LegalEntityID, domain.TaxTxn{ID: data.InvoiceID, Code: data.TaxCode, Date: data.TaxDate, Amount: tax}); err != nil {
		return fmt.Errorf("record input tax: %w", err)
	}
	return nil
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
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.pdf"`, doc.Number))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(doc.Body)
}

type selfBillIssuedData struct {
	InvoiceID      string `json:"invoice_id"`
	DocumentNumber string `json:"document_number"`
	InvoiceNumber  string `json:"invoice_number"`
	TaxDate        string `json:"tax_date"`
	LegalEntityID  string `json:"legal_entity_id"`
	VendorID       string `json:"vendor_id"`
	Currency       string `json:"currency"`
	TaxCode        string `json:"tax_code,omitempty"`
	Tax            string `json:"tax"`
	Total          string `json:"total"`
}

func selfBillIssuedEvent(tenantID string, in store.Invoice, doc domain.SelfBill) (events.Event, error) {
	scale := money.MinorUnits(in.Currency)
	return newEvent("receiving", "ersselfbill", "issued", in.ID, tenantID, selfBillIssuedData{
		InvoiceID: in.ID, DocumentNumber: doc.Number, InvoiceNumber: in.InvoiceNumber, TaxDate: doc.InvoiceDate, LegalEntityID: in.LegalEntityID, VendorID: in.VendorID,
		Currency: in.Currency, TaxCode: in.TaxCode, Tax: doc.Tax.StringFixed(scale), Total: doc.Total.StringFixed(scale),
	})
}
