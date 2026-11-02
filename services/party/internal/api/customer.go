package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

func (a *API) WithCustomers(customers store.CustomerStore) *API {
	a.customers = customers
	return a
}

func (a *API) registerCustomers(r chi.Router) {
	r.With(authz.RequireAnyDuty(dutyWrite, dutyImport)).Post("/v1/party/parties/{id}/customer", a.createCustomer)
}

func (a *API) createCustomer(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req domain.NewCustomer
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req = req.Clean()
	if errs := req.Validate(); len(errs) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeCustomerInvalid, "customer failed validation")
		p.Errors = errs
		httpx.Fail(w, r, p)
		return
	}
	caller, _ := tenant.FromContext(r.Context())
	customer, created, err := a.customers.CreateCustomer(r.Context(), chi.URLParam(r, "id"), store.NewCustomerRole{
		In: req, Key: r.Header.Get(idempotencyKeyHeader), Hash: domain.RequestHash(req), Import: caller.HasDuty(dutyImport),
	})
	if err != nil {
		failParty(w, r, err)
		return
	}
	if created {
		a.customersCreated.Add(1)
	}
	httpx.JSON(w, http.StatusCreated, customer)
}
