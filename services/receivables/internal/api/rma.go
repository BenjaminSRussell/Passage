package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

type rmaCreditRequest struct {
	RMAID         string                 `json:"rmaId"`
	RMANumber     string                 `json:"rmaNumber"`
	LegalEntityID string                 `json:"legalEntityId"`
	InvoiceNumber string                 `json:"invoiceNumber"`
	InvoiceID     string                 `json:"invoiceId"`
	PostingDate   string                 `json:"postingDate"`
	Gross         decimal.Decimal        `json:"gross"`
	Restocking    decimal.Decimal        `json:"restocking"`
	Net           decimal.Decimal        `json:"net"`
	TaxLines      []store.RMATaxLine     `json:"taxLines"`
	InvoiceLines  []store.RMAInvoiceLine `json:"invoiceLines"`
}

func (a *API) postRMACredit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req rmaCreditRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.RMAID) {
		fields["rmaId"] = "must be a UUID"
	}
	if n := len(req.RMANumber); n < 1 || n > 40 || strings.TrimSpace(req.RMANumber) != req.RMANumber {
		fields["rmaNumber"] = "must be 1 to 40 characters without surrounding spaces"
	}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if req.InvoiceNumber == "" {
		fields["invoiceNumber"] = "is required"
	}
	if len(req.TaxLines)+len(req.InvoiceLines) > 0 && !ids.Valid(req.InvoiceID) {
		fields["invoiceId"] = "must be the billing invoice's UUID when taxLines or invoiceLines are sent"
	}
	if _, err := store.ParseDay(req.PostingDate); err != nil {
		fields["postingDate"] = "must be YYYY-MM-DD"
	}
	if len(fields) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
		p.Errors = fields
		httpx.Fail(w, r, p)
		return
	}
	if err := store.ValidateRMAAmounts(req.Gross, req.Restocking, req.Net); err != nil {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeRMACreditInvalid, "the credit amounts are inconsistent")
		p.Errors = map[string]string{"amounts": err.Error()}
		httpx.Fail(w, r, p)
		return
	}
	if err := store.ValidateRMATax(req.TaxLines); err != nil {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeRMACreditInvalid, "the credit tax lines are inconsistent")
		p.Errors = map[string]string{"taxLines": err.Error()}
		httpx.Fail(w, r, p)
		return
	}
	if err := store.ValidateRMAInvoiceLines(req.InvoiceLines); err != nil {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeRMACreditInvalid, "the credit invoice lines are inconsistent")
		p.Errors = map[string]string{"invoiceLines": err.Error()}
		httpx.Fail(w, r, p)
		return
	}
	if !scopeAllows(w, r, req.LegalEntityID) {
		return
	}
	out, err := a.store.PostRMACredit(r.Context(), store.RMACredit{
		RMAID: req.RMAID, RMANumber: req.RMANumber, LegalEntityID: req.LegalEntityID, InvoiceNumber: req.InvoiceNumber, InvoiceID: req.InvoiceID,
		PostingDate: req.PostingDate, Gross: req.Gross, Restocking: req.Restocking, Net: req.Net, TaxLines: req.TaxLines, InvoiceLines: req.InvoiceLines,
	})
	if err != nil {
		a.fail("post rma credit", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

var rmaCreditStatuses = map[string]bool{"PENDING": true, "POSTED": true}

// listRMACredits is the operator's view of RMA credits: PENDING ones still waiting for the ledger and parked ones
// (parked=true) the ledger refused.
func (a *API) listRMACredits(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.RMACreditFilter{LegalEntityID: q.Get("legalEntityId"), Status: q.Get("status")}
	if f.LegalEntityID != "" && !ids.Valid(f.LegalEntityID) {
		badQuery(w, r, "legalEntityId", "must be a UUID")
		return
	}
	if f.Status != "" && !rmaCreditStatuses[f.Status] {
		badQuery(w, r, "status", "must be PENDING or POSTED")
		return
	}
	switch q.Get("parked") {
	case "", "false":
	case "true":
		f.ParkedOnly = true
	default:
		badQuery(w, r, "parked", "must be true or false")
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	f.Limit = limit
	if f.LegalEntityID != "" && !scopeAllows(w, r, f.LegalEntityID) {
		return
	}
	items, err := a.store.ListRMACredits(r.Context(), f)
	if err != nil {
		a.fail("list rma credits", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// legalEntityScoped refuses a caller without an explicit legal-entity claim: posting a journal signs a ledger token
// for the credit's legal entity, which an all-scope principal cannot get, so the attempt would only fail and push the
// credit's backoff out.
func legalEntityScoped(w http.ResponseWriter, r *http.Request) bool {
	p, _ := tenant.FromContext(r.Context())
	if len(tenant.LegalEntityScopeList(p)) == 0 {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
		p.Errors = map[string]string{"X-Passage-Legal-Entity": "posting an RMA credit needs a caller scoped to its legal entity"}
		httpx.Fail(w, r, p)
		return false
	}
	return true
}

type rmaRetryRequest struct {
	PostingDate string `json:"postingDate"`
}

// rmaCreditAction dispatches the custom methods on one RMA credit (chi does not match ':' after a parameter).
func (a *API) rmaCreditAction(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "ref"), ":")
	if !found || !ids.Valid(id) || action != "retry" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRMACreditNotFound, "RMA credit not found"))
		return
	}
	if !require(w, r, dutyPayment) || !legalEntityScoped(w, r) {
		return
	}
	var req rmaRetryRequest
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	if req.PostingDate != "" {
		if _, err := store.ParseDay(req.PostingDate); err != nil {
			badQuery(w, r, "postingDate", "must be YYYY-MM-DD")
			return
		}
	}
	out, err := a.store.RetryRMACredit(r.Context(), id, req.PostingDate)
	if err != nil {
		a.fail("retry rma credit", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// sweepRMACredits posts the caller's due PENDING RMA credits: the scheduler's entry point, and the same store call the
// in-process sweeper (RECEIVABLES_RMA_SWEEP_TENANT_IDS) runs.
func (a *API) sweepRMACredits(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || !legalEntityScoped(w, r) {
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	res, err := a.store.SweepRMACredits(r.Context(), time.Now().UTC(), limit)
	if err != nil {
		a.fail("sweep rma credits", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
