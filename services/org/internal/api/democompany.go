package api

import (
	"log/slog"
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

// DemoCompanyResetResult says what a demo company reset removed and what the load after it created.
type DemoCompanyResetResult struct {
	Package string `json:"package"`
	Removed int    `json:"removed"`
	Created int    `json:"created"`
	Present int    `json:"present"`
}

func (a *API) registerDemoCompany(r chi.Router) {
	r.With(authz.RequireDuty(dutyDemoCompany)).Post(demoCompanyPath, a.createDemoCompany)
	r.With(authz.RequireDuty(dutyDemoCompany)).Post(demoCompanyPath+":reset", a.resetDemoCompany)
}

// demoAdmin is the caller of a demo company route: a tenant administrator without a legal-entity scope, because
// the package spans three legal entities.
func demoAdmin(w http.ResponseWriter, r *http.Request, action string) (tenant.Principal, bool) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !p.HasDuty(tenant.DutyTenantAdmin) || p.LegalEntityID != "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "ORG-0403", action+" the demo company needs the tenant.administer duty and a request without a legal-entity scope"))
		return tenant.Principal{}, false
	}
	return p, true
}

// resetDemoCompany removes the records the demo company created in the caller's tenant and loads it again, so a
// trial or sales demo can start over. Records are recognised by the idempotency key namespace only the demo
// loader writes; anything else is never deleted, and if something else hangs off a demo record the reset is
// refused with 409 ORG-DEMO-IN-USE before anything changes. The removal is recorded by a
// dev.passage.org.democompany.removed.v1 event naming the caller.
func (a *API) resetDemoCompany(w http.ResponseWriter, r *http.Request) {
	p, ok := demoAdmin(w, r, "resetting")
	if !ok {
		return
	}
	actor := p.UserID
	if actor == "" {
		actor = "unknown"
	}
	result, err := seed.Reset(r.Context(), a.store, p.TenantID, actor, seed.DemoManufacturer)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	slog.InfoContext(r.Context(), "demo company reset", "tenant", p.TenantID, "actor", actor, "removed", result.Removed, "created", result.Created)
	httpx.JSON(w, http.StatusOK, DemoCompanyResetResult{Package: seed.DemoManufacturer, Removed: result.Removed, Created: result.Created, Present: result.Present})
}

// createDemoCompany loads the demo manufacturer (three legal entities with their sites, warehouses, operating
// units and teams) into the caller's tenant. It runs the package the service seeds at start, so it is safe to
// repeat: rows that exist are left alone and missing ones are added. It never deletes, so it tops the demo up
// but does not wipe it; resetDemoCompany does that.
func (a *API) createDemoCompany(w http.ResponseWriter, r *http.Request) {
	p, ok := demoAdmin(w, r, "loading")
	if !ok {
		return
	}
	result, err := seed.Run(r.Context(), a.store, p.TenantID, seed.DemoManufacturer)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, DemoCompanyResult{Package: seed.DemoManufacturer, Created: result.Created, Present: result.Present})
}
