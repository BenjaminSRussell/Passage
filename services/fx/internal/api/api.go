package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fx/internal/store"
)

const (
	dutyRead       = "View exchange rates"
	dutyWrite      = "Maintain exchange rates"
	dutyAdmin      = "Administer exchange rates"
	dutyRateLookup = "fx.rate.lookup"
	readyTimeout   = 500 * time.Millisecond
)

type API struct {
	store     store.Store
	importers map[string]Importer
}

func New(s store.Store) *API { return &API{store: s} }

func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/fx/rate-types", a.listRateTypes)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/fx/rate-types/{code}", a.getRateType)
	r.With(authz.RequireDuty(dutyWrite)).Post("/v1/fx/rate-types", a.createRateType)
	r.With(authz.RequireDuty(dutyWrite)).Put("/v1/fx/rate-types/{code}", a.putRateType)
	r.With(authz.RequireDuty(dutyWrite)).Post("/v1/fx/rates", a.publishRates)
	r.With(authz.RequireAnyDuty(dutyRead, dutyRateLookup)).Get("/v1/fx/rates", a.lookupRate)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/fx/rates/history", a.rateHistory)
	r.With(authz.RequireDuty(dutyRead)).Post("/v1/fx/convert", a.convert)
	r.With(authz.RequireDuty(dutyAdmin)).Post("/v1/fx/providers/{p}:import", a.importNow)
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
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
