package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

const settingsPath = "/v1/integration/migration-settings"

type putSettingsRequest struct {
	// Version is the version the caller read: 0 when the tenant has never saved settings.
	Version *int `json:"version"`
	// RecurringMinIntervalMin is the shortest gap, in minutes, a project or binding cron may leave between runs.
	RecurringMinIntervalMin *int `json:"recurringMinIntervalMin"`
}

func (a *API) registerSettings(r chi.Router) {
	r.With(authz.RequireDuty(dutyRun)).Get(settingsPath, a.getSettings)
	r.With(authz.RequireDuty(dutyProjectWrite)).Put(settingsPath, a.putSettings)
}

func (a *API) getSettings(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	st, err := a.store.GetTenantSettings(r.Context())
	if err != nil {
		a.logFailure(r, "get tenant settings failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, st)
}

func (a *API) putSettings(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req putSettingsRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	bad := map[string]string{}
	if req.Version == nil || *req.Version < 0 {
		bad["version"] = "is required (0 for a tenant that has never saved settings)"
	}
	if req.RecurringMinIntervalMin == nil {
		bad["recurringMinIntervalMin"] = "is required"
	} else if msg := domain.RecurringMinIntervalProblem(*req.RecurringMinIntervalMin); msg != "" {
		bad["recurringMinIntervalMin"] = msg
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	st, err := a.store.PutTenantSettings(r.Context(), *req.Version, *req.RecurringMinIntervalMin)
	if err != nil {
		a.logFailure(r, "put tenant settings failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, st)
}

// scheduleProblem says why expr cannot schedule runs for this tenant ("" when it can): the five-field and 15-minute
// rules of every tenant, then the tenant's own `mig.recurring.min_interval_min`.
func (a *API) scheduleProblem(r *http.Request, expr string) (string, error) {
	if msg := domain.ScheduleCronProblem(expr); msg != "" || expr == "" {
		return msg, nil
	}
	st, err := a.store.GetTenantSettings(r.Context())
	if err != nil {
		return "", err
	}
	return domain.ScheduleCronProblemMin(expr, st.RecurringMinIntervalMin), nil
}
