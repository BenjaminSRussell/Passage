package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/services/party/internal/domain"
	"github.com/BenjaminSRussell/depot/services/party/internal/store"
)

func (a *API) WithTerms(s store.TermStore) *API {
	a.terms = s
	return a
}

func (a *API) registerTerms(r chi.Router) {
	write := authz.RequireAnyDuty(dutyWrite, dutyImport)
	r.With(write).Post("/v1/party/payment-terms", a.createPaymentTerm)
	r.With(write).Post("/v1/party/payment-calendars", a.createPaymentCalendar)
	r.With(authz.RequireAnyDuty(dutyRead, dutyVendorLookup)).Post("/v1/party/payment-terms/{id}:due-dates", a.paymentTermDueDates)
}

func (a *API) createPaymentTerm(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req domain.NewPaymentTerm
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req = req.Clean()
	if errs := req.Validate(); len(errs) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeTermInvalid, "payment term failed validation")
		p.Errors = errs
		httpx.Fail(w, r, p)
		return
	}
	term, err := a.terms.CreatePaymentTerm(r.Context(), req)
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, term)
}

func (a *API) createPaymentCalendar(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req domain.NewPaymentCalendar
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req = req.Clean()
	if errs := req.Validate(); len(errs) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeCalendarInvalid, "payment calendar failed validation")
		p.Errors = errs
		httpx.Fail(w, r, p)
		return
	}
	calendar, err := a.terms.CreatePaymentCalendar(r.Context(), req)
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, calendar)
}

type dueDatesRequest struct {
	DocumentDate string `json:"documentDate"`
}

func (a *API) paymentTermDueDates(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req dueDatesRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	documentDate, err := time.Parse(time.DateOnly, req.DocumentDate)
	if err != nil {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeTermInvalid, "due dates request failed validation")
		p.Errors = map[string]string{"documentDate": "must be a YYYY-MM-DD date"}
		httpx.Fail(w, r, p)
		return
	}
	term, err := a.terms.TermForDates(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, term.DueDates(documentDate))
}
