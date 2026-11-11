package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
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
	r.With(authz.RequireAnyDuty(dutyRead, dutyImportRead)).Get("/v1/party/customers/{id}", a.getCustomer)
	r.With(authz.RequireDuty(dutyWrite)).Put("/v1/party/customers/{id}/portal-access", a.setPortalAccess)
	r.With(authz.RequireDuty(dutyPortalAccountsRead)).Get("/v1/party/customer-portal/accounts", a.portalAccounts)
}

// dutyPortalAccountsRead is the customer portal's service-only duty to list the accounts its contact may act for.
const dutyPortalAccountsRead = "party.customer-portal.read"

// portalAccounts lists the signed-in contact's customer and its bill-to and ship-to accounts of the same customer group. The
// customer is the verified customer-portal token's; no query parameter names one.
func (a *API) portalAccounts(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	customer, ok := tenant.PortalCustomer(r.Context())
	if !ok || !ids.Valid(customer) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "PTY-CUS-001", "this account is not a customer account"))
		return
	}
	items, err := a.customers.PortalAccounts(r.Context(), customer)
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// getCustomer reads one customer role in the caller's legal entities.
func (a *API) getCustomer(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	customer, err := a.customers.GetCustomer(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	w.Header().Set("ETag", `"`+strconv.Itoa(customer.Version)+`"`)
	httpx.JSON(w, http.StatusOK, customer)
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

// setPortalAccess says whether contacts of a ship-to customer may also act for its bill-to account in the customer portal. Off by default.
func (a *API) setPortalAccess(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req struct {
		BillToAccess bool `json:"billToAccess"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := a.customers.SetPortalBillToAccess(r.Context(), chi.URLParam(r, "id"), req.BillToAccess); err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"billToAccess": req.BillToAccess})
}
