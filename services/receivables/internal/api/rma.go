package api

import (
	"net/http"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

type rmaCreditRequest struct {
	RMAID         string          `json:"rmaId"`
	RMANumber     string          `json:"rmaNumber"`
	LegalEntityID string          `json:"legalEntityId"`
	InvoiceNumber string          `json:"invoiceNumber"`
	PostingDate   string          `json:"postingDate"`
	Gross         decimal.Decimal `json:"gross"`
	Restocking    decimal.Decimal `json:"restocking"`
	Net           decimal.Decimal `json:"net"`
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
	if !scopeAllows(w, r, req.LegalEntityID) {
		return
	}
	out, err := a.store.PostRMACredit(r.Context(), store.RMACredit{
		RMAID: req.RMAID, RMANumber: req.RMANumber, LegalEntityID: req.LegalEntityID, InvoiceNumber: req.InvoiceNumber,
		PostingDate: req.PostingDate, Gross: req.Gross, Restocking: req.Restocking, Net: req.Net,
	})
	if err != nil {
		a.fail("post rma credit", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}
