package api

import (
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

type rmaCreditRequest struct {
	RMAID         string          `json:"rmaId"`
	RMANumber     string          `json:"rmaNumber"`
	CustomerID    string          `json:"customerId"`
	LegalEntityID string          `json:"legalEntityId"`
	Currency      string          `json:"currency"`
	PostingDate   string          `json:"postingDate"`
	LedgerID      string          `json:"ledgerId"`
	Gross         decimal.Decimal `json:"gross"`
	Restocking    decimal.Decimal `json:"restocking"`
	Net           decimal.Decimal `json:"net"`
}

func (a *API) postRMACredit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key := r.Header.Get(idempotencyKey)
	if key == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "Idempotency-Key header is required"))
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
	if req.RMANumber == "" {
		fields["rmaNumber"] = "is required"
	}
	if !ids.Valid(req.CustomerID) {
		fields["customerId"] = "must be a UUID"
	}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if req.LedgerID != "" && !ids.Valid(req.LedgerID) {
		fields["ledgerId"] = "must be a UUID"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be a 3-letter ISO code"
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
	if !scopeAllows(w, r, req.LegalEntityID) {
		return
	}
	out, err := a.store.PostRMACredit(r.Context(), store.RMACredit{
		RMAID: req.RMAID, RMANumber: req.RMANumber, CustomerID: req.CustomerID, LegalEntityID: req.LegalEntityID,
		Currency: req.Currency, PostingDate: req.PostingDate, IdempotencyKey: key, LedgerID: req.LedgerID,
		Gross: req.Gross, Restocking: req.Restocking, Net: req.Net,
	})
	if err != nil {
		a.fail("post rma credit", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}
