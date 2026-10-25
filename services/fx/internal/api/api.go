package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fx/internal/store"
)

const (
	dutyRead  = "fx.read"
	dutyWrite = "fx.write"
)

type API struct {
	store store.Store
}

func New(s store.Store) *API { return &API{store: s} }

func (a *API) Register(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/fx/rate-types", a.listRateTypes)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/fx/rate-types/{code}", a.getRateType)
	r.With(authz.RequireDuty(dutyWrite)).Put("/v1/fx/rate-types/{code}", a.putRateType)
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	if p.LegalEntityID != "" && p.LegalEntityID != "*" && !ids.Valid(p.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "legal entity must be a single UUID"))
		return false
	}
	return true
}
