package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// parametersPath serves the pricing parameters of one legal entity (PRD 5.26.13). Reading
// takes pricing.view; changing takes pricing.admin (PricingAdmin, with the procedures).
const parametersPath = "/v1/pricing/parameters"

// parametersRequest is the PUT body. The PUT replaces every value, so each one is required:
// a missing journalFourEyes must not save as false.
type parametersRequest struct {
	LegalEntityID   string `json:"legalEntityId"`
	JournalFourEyes *bool  `json:"journalFourEyes"`
	// Version is the version read (0 when the legal entity has no stored parameters).
	Version int `json:"version"`
}

type parametersView struct {
	LegalEntityID   string     `json:"legalEntityId"`
	JournalFourEyes bool       `json:"journalFourEyes"`
	Version         int        `json:"version"`
	UpdatedBy       string     `json:"updatedBy,omitempty"`
	UpdatedAt       *time.Time `json:"updatedAt,omitempty"`
}

func parametersResponse(p store.Parameters) parametersView {
	return parametersView{LegalEntityID: p.LegalEntityID, JournalFourEyes: p.JournalFourEyes, Version: p.Version,
		UpdatedBy: p.UpdatedBy, UpdatedAt: p.UpdatedAt}
}

func (a *API) getParameters(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	if !legalEntityInScope(w, r, le) {
		return
	}
	p, err := a.store.GetParameters(r.Context(), le)
	if err != nil {
		slog.Error("get pricing parameters failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, parametersResponse(p))
}

func (a *API) setParameters(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req parametersRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if req.JournalFourEyes == nil {
		fields["journalFourEyes"] = "is required"
	}
	if req.Version < 0 {
		fields["version"] = "must be the version read, or 0 when none was stored"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !legalEntityInScope(w, r, req.LegalEntityID) {
		return
	}
	p, err := a.store.SetParameters(r.Context(), store.Parameters{LegalEntityID: req.LegalEntityID,
		JournalFourEyes: *req.JournalFourEyes}, req.Version, r.Header.Get(idempotencyKey))
	if err != nil {
		slog.Error("set pricing parameters failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, parametersResponse(p))
}

// legalEntityInScope refuses a legal entity outside the caller's scope (403 AUTH-0003). Row
// security would hide its stored parameters, so a read would show the defaults and a write
// would fail as a version conflict or a policy violation; the scope check says why instead.
func legalEntityInScope(w http.ResponseWriter, r *http.Request, legalEntityID string) bool {
	p, _ := tenant.FromContext(r.Context())
	if tenant.InLegalEntityScope(p, legalEntityID) {
		return true
	}
	httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity is outside the caller's scope"))
	return false
}

// registerParameterRoutes mounts the parameter endpoints.
func (a *API) registerParameterRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyView)).Get(parametersPath, a.getParameters)
	r.With(authz.RequireDuty(dutyAdmin)).Put(parametersPath, a.setParameters)
}
