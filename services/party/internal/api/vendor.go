package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

const (
	dutyBankWrite  = "party.vendor_bank.write"
	dutyBankReveal = "party.vendor_bank.reveal"
)

func (a *API) WithVendors(vendors store.VendorStore) *API {
	a.vendors = vendors
	return a
}

func (a *API) registerVendors(r chi.Router) {
	r.With(authz.RequireAnyDuty(dutyWrite, dutyImport)).Post("/v1/party/parties/{id}/vendor", a.createVendor)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/party/parties/{id}/vendors", a.listPartyVendors)
	r.With(authz.RequireAnyDuty(dutyRead, dutyImportRead)).Get("/v1/party/vendors/{id}", a.getVendor)
	r.With(authz.RequireAnyDuty(dutyWrite, dutyImport)).Post("/v1/party/vendor-groups", a.createVendorGroup)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/party/vendor-groups", a.listVendorGroups)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/party/vendor-groups/{id}", a.getVendorGroup)
	r.With(authz.RequireDuty(dutyBankWrite)).Post("/v1/party/vendors/{id}/bank-accounts", a.addBankAccount)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/party/vendors/{id}/bank-accounts", a.listBankAccounts(false))
	r.With(auditReveal, authz.RequireDuty(dutyBankReveal)).Get("/v1/party/vendors/{id}/bank-accounts:reveal", a.listBankAccounts(true))
}

func auditReveal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		caller, _ := tenant.FromContext(r.Context())
		outcome, level := "revealed", slog.LevelInfo
		if status := ww.Status(); status < 200 || status > 299 {
			outcome, level = "refused", slog.LevelWarn
		}
		slog.Log(r.Context(), level, "vendor bank reveal "+outcome, "vendorId", chi.URLParam(r, "id"), "userId", caller.UserID, "service", caller.Service, "tenantId", caller.TenantID, "status", ww.Status(), "correlationId", middleware.GetReqID(r.Context()))
	})
}

func (a *API) createVendor(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req domain.NewVendor
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req = req.Clean()
	if errs := req.Validate(); len(errs) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeVendorInvalid, "vendor failed validation")
		p.Errors = errs
		httpx.Fail(w, r, p)
		return
	}
	vendor, created, err := a.vendors.CreateVendorRole(r.Context(), chi.URLParam(r, "id"), store.NewVendorRole{
		In: req, Key: r.Header.Get(idempotencyKeyHeader), Hash: domain.RequestHash(req),
	})
	if err != nil {
		failParty(w, r, err)
		return
	}
	if created {
		a.vendorsCreated.Add(1)
	}
	httpx.JSON(w, http.StatusCreated, vendor)
}

func (a *API) listPartyVendors(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	vendors, err := a.vendors.ListPartyVendors(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"vendors": vendors})
}

func (a *API) getVendor(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	vendor, err := a.vendors.GetVendor(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, vendor)
}

func (a *API) addBankAccount(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req domain.NewBankAccount
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req = req.Clean()
	if errs := req.Validate(); len(errs) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeBankInvalid, "bank account failed validation")
		p.Errors = errs
		httpx.Fail(w, r, p)
		return
	}
	account, err := a.vendors.AddBankAccount(r.Context(), chi.URLParam(r, "id"), req)
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, account)
}

func (a *API) listBankAccounts(reveal bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		vendorID := chi.URLParam(r, "id")
		accounts, err := a.vendors.ListBankAccounts(r.Context(), vendorID, reveal)
		if err != nil {
			failParty(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"bankAccounts": accounts})
	}
}

func (a *API) createVendorGroup(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req domain.NewVendorGroup
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req = req.Clean()
	if errs := req.Validate(); len(errs) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeVendorGroupInvalid, "vendor group failed validation")
		p.Errors = errs
		httpx.Fail(w, r, p)
		return
	}
	group, _, err := a.vendors.CreateVendorGroup(r.Context(), store.NewVendorGroupRequest{
		In: req, Key: r.Header.Get(idempotencyKeyHeader), Hash: domain.RequestHash(req),
	})
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, group)
}

func (a *API) listVendorGroups(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	groups, err := a.vendors.ListVendorGroups(r.Context())
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"vendorGroups": groups})
}

func (a *API) getVendorGroup(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	group, err := a.vendors.GetVendorGroup(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, group)
}
