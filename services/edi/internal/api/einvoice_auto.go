package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

// Automatic e-invoicing (#35, 35-2): a customer registered under a mandate gets its e-invoice built when its billing invoice posts
// (the invoice.posted consumer), and handed to the access point as well when the registration says autoSubmit.

type mandateCustomerBody struct {
	LegalEntityID string `json:"legalEntityId"`
	AutoSubmit    bool   `json:"autoSubmit"`
}

func (a *API) mandateKey(w http.ResponseWriter, r *http.Request) (customer, mandate string, ok bool) {
	customer, mandate = chi.URLParam(r, "customerId"), strings.ToUpper(chi.URLParam(r, "mandate"))
	if _, known := a.mandates()[mandate]; !validID(customer) || !known {
		httpx.Fail(w, r, notFound("e-invoice mandate"))
		return "", "", false
	}
	return customer, mandate, true
}

func inScope(r *http.Request, legalEntityID string) bool {
	caller, _ := tenant.FromContext(r.Context())
	return validID(legalEntityID) && tenant.InLegalEntityScope(caller, legalEntityID)
}

func (a *API) putEInvoiceMandate(w http.ResponseWriter, r *http.Request) {
	customer, mandate, ok := a.mandateKey(w, r)
	if !ok {
		return
	}
	var body mandateCustomerBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !inScope(r, body.LegalEntityID) {
		httpx.Fail(w, r, validation(map[string]string{"legalEntityId": "must be a legal entity of the caller"}))
		return
	}
	m := store.MandateCustomer{LegalEntityID: body.LegalEntityID, CustomerID: customer, Mandate: mandate, AutoSubmit: body.AutoSubmit}
	if err := a.store.PutMandateCustomer(r.Context(), m); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, m)
}

func (a *API) deleteEInvoiceMandate(w http.ResponseWriter, r *http.Request) {
	customer, mandate, ok := a.mandateKey(w, r)
	if !ok {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if !inScope(r, le) {
		httpx.Fail(w, r, validation(map[string]string{"legalEntityId": "must be a legal entity of the caller"}))
		return
	}
	if err := a.store.DeleteMandateCustomer(r.Context(), le, customer, mandate); errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("e-invoice mandate"))
		return
	} else if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listEInvoiceMandates(w http.ResponseWriter, r *http.Request) {
	le, customer := r.URL.Query().Get("legalEntityId"), r.URL.Query().Get("customerId")
	fields := map[string]string{}
	if le != "" && !inScope(r, le) {
		fields["legalEntityId"] = "must be a legal entity of the caller"
	}
	if customer != "" && !validID(customer) {
		fields["customerId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	items, err := a.store.ListMandateCustomers(r.Context(), le, customer)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// AutoEInvoice builds (and, where registered, submits) the e-invoices of a posted invoice for the mandates its customer is
// registered under. A customer with no registration costs one lookup and no read of billing. Problems that no retry changes (a
// missing party profile, an invoice that is not final, another legal entity's invoice) are returned as skips by SkipEInvoice;
// the rest (a source outage, an access point that did not answer) are returned for the consumer to retry.
func (a *API) AutoEInvoice(ctx context.Context, legalEntityID, customerID, invoiceID string) error {
	regs, err := a.store.ListMandateCustomers(ctx, legalEntityID, customerID)
	if err != nil || len(regs) == 0 {
		return err
	}
	var first error
	for _, reg := range regs {
		mandate, known := a.mandates()[reg.Mandate]
		if !known {
			continue
		}
		e, _, err := a.BuildEInvoice(ctx, mandate, legalEntityID, invoiceID)
		if err == nil && reg.AutoSubmit && e.Status == "VALID" && a.AccessPoint != nil {
			_, err = a.SubmitEInvoice(ctx, e.ID)
		}
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

// ValidateStoredEInvoice runs the Peppol rule subset on the stored document of an e-invoice: POST /einvoice/invoices/{id}:validate.
func (a *API) validateEInvoice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("e-invoice"))
		return
	}
	e, err := a.store.GetEInvoice(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, eInvoiceProblem(err))
		return
	}
	if e.Mandate != domain.MandatePeppol || e.XML == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeEInvoiceState, "the Peppol rules apply to a built Peppol e-invoice"))
		return
	}
	issues := domain.CheckPeppolRules(e.XML)
	httpx.JSON(w, http.StatusOK, map[string]any{"valid": len(issues) == 0, "issues": append([]domain.Issue{}, issues...), "rules": "peppol-bis-3.0-go-subset"})
}

// getFacturXPDF serves the Factur-X PDF (the invoice page with the CII XML embedded) of a FACTURX e-invoice.
func (a *API) getFacturXPDF(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("e-invoice"))
		return
	}
	e, err := a.store.GetEInvoice(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, eInvoiceProblem(err))
		return
	}
	if e.Mandate != domain.MandateFacturX || e.XML == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeEInvoiceState, "the Factur-X PDF applies to a built FACTURX e-invoice"))
		return
	}
	pdf, err := domain.FacturXPDF(e.XML, e.CreatedAt)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.NewReplacer(`"`, "", "/", "-", "\\", "-").Replace(e.Number)+`.pdf"`)
	_, _ = w.Write(pdf)
}
