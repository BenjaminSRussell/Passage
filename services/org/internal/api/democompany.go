package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/org/internal/seed"
)

const (
	dutyDemoCompany = "org.demo-company.manage"
	demoCompanyPath = "/v1/org/demo-company"
)

// DemoCompanyResult says what the one-click demo company did to the tenant's organization model.
type DemoCompanyResult struct {
	Package string `json:"package"`
	Created int    `json:"created"`
	Present int    `json:"present"`
}

func (a *API) registerDemoCompany(r chi.Router) {
	r.With(authz.RequireDuty(dutyDemoCompany)).Post(demoCompanyPath, a.createDemoCompany)
}

// createDemoCompany loads the demo manufacturer (three legal entities with their sites, warehouses, operating
// units and teams) into the caller's tenant. It runs the package the service seeds at start, so it is safe to
// repeat: rows that exist are left alone and missing ones are added. It never deletes, so it tops the demo up
// but does not wipe it. Only a tenant administrator without a legal-entity scope may call it, because the
// package spans three legal entities.
func (a *API) createDemoCompany(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !p.HasDuty(tenant.DutyTenantAdmin) || p.LegalEntityID != "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "ORG-0403", "loading the demo company needs the tenant.administer duty and a request without a legal-entity scope"))
		return
	}
	result, err := seed.Run(r.Context(), a.store, p.TenantID, seed.DemoManufacturer)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, DemoCompanyResult{Package: seed.DemoManufacturer, Created: result.Created, Present: result.Present})
}
