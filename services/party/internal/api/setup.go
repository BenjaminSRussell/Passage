package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

// Setup records a customer role names (#500): read lists of payment terms and
// posting profiles, and customer groups, so a form picks them by name.

// WithSetup enables the payment term, posting profile and customer group routes.
func (a *API) WithSetup(s store.SetupStore) *API {
	a.setup = s
	return a
}

func (a *API) registerSetup(r chi.Router) {
	read := authz.RequireDuty(dutyRead)
	r.With(read).Get("/v1/party/payment-terms", a.listPaymentTerms)
	r.With(read).Get("/v1/party/payment-terms/{id}", a.getPaymentTerm)
	r.With(read).Get("/v1/party/posting-profiles", a.listPostingProfiles)
	r.With(read).Get("/v1/party/posting-profiles/{id}", a.getPostingProfile)
	r.With(authz.RequireAnyDuty(dutyWrite, dutyImport)).Post("/v1/party/customer-groups", a.createCustomerGroup)
	r.With(read).Get("/v1/party/customer-groups", a.listCustomerGroups)
	r.With(read).Get("/v1/party/customer-groups/{id}", a.getCustomerGroup)
}

func (a *API) listPaymentTerms(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	items, err := a.setup.ListPaymentTerms(r.Context())
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getPaymentTerm(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	t, err := a.setup.GetPaymentTerm(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, t)
}

func (a *API) listPostingProfiles(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if le != "" && !ids.Valid(le) {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
		p.Errors = map[string]string{"legalEntityId": "must be a UUID"}
		httpx.Fail(w, r, p)
		return
	}
	items, err := a.setup.ListPostingProfiles(r.Context(), le)
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getPostingProfile(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	p, err := a.setup.GetPostingProfile(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

func (a *API) createCustomerGroup(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req domain.NewCustomerGroup
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req = req.Clean()
	if errs := req.Validate(); len(errs) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeCustomerGroupInvalid, "customer group failed validation")
		p.Errors = errs
		httpx.Fail(w, r, p)
		return
	}
	group, _, err := a.setup.CreateCustomerGroup(r.Context(), store.NewCustomerGroupRequest{
		In: req, Key: r.Header.Get(idempotencyKeyHeader), Hash: domain.RequestHash(req),
	})
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, group)
}

func (a *API) listCustomerGroups(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	items, err := a.setup.ListCustomerGroups(r.Context())
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getCustomerGroup(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	g, err := a.setup.GetCustomerGroup(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, g)
}
