package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// routesReleaseRule mounts the release and wave parameters per warehouse
// (section 5.28.13: release_horizon_days, release_requires_full_atp,
// wave_max_lines, wave_max_orders).
func (a *API) routesReleaseRule(r chi.Router) {
	r.With(authz.RequireDuty("fulfillment.warehouse.view")).Get("/v1/fulfillment/release-rules", a.listReleaseRules)
	r.With(authz.RequireDuty("fulfillment.warehouse.view")).Get("/v1/fulfillment/release-rules/{warehouseId}", a.getReleaseRule)
	r.With(authz.RequireDuty("fulfillment.warehouse.release")).Put("/v1/fulfillment/release-rules/{warehouseId}", a.saveReleaseRule)
}

type releaseRuleRequest struct {
	LegalEntityID  string `json:"legalEntityId"`
	HorizonDays    *int   `json:"horizonDays"`
	RequireFullATP *bool  `json:"requireFullAtp"`
	WaveMaxLines   *int   `json:"waveMaxLines"`
	WaveMaxOrders  *int   `json:"waveMaxOrders"`
}

func (a *API) listReleaseRules(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListReleaseRules(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.ReleaseRule]{Items: items})
}

func (a *API) getReleaseRule(w http.ResponseWriter, r *http.Request) {
	id, ok := warehouseParam(w, r)
	if !ok {
		return
	}
	rule, err := a.store.GetReleaseRule(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, rule.Version)
	httpx.JSON(w, http.StatusOK, rule)
}

func (a *API) saveReleaseRule(w http.ResponseWriter, r *http.Request) {
	id, ok := warehouseParam(w, r)
	if !ok {
		return
	}
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	if raw == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, CodePreconditionIf, "If-Match with the rule version is required (0 when none is saved)"))
		return
	}
	version, err := strconv.Atoi(raw)
	if err != nil || version < 0 {
		fail(w, r, "If-Match", "must be the rule version as an integer, 0 when none is saved")
		return
	}
	var req releaseRuleRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(req.LegalEntityID) {
		fail(w, r, "legalEntityId", "must be the UUID of the legal entity the warehouse belongs to")
		return
	}
	if req.HorizonDays == nil || *req.HorizonDays < 0 || *req.HorizonDays > 30 {
		fail(w, r, "horizonDays", "is required, 0 to 30 days")
		return
	}
	if req.RequireFullATP == nil {
		fail(w, r, "requireFullAtp", "is required")
		return
	}
	if req.WaveMaxLines == nil || *req.WaveMaxLines < 1 || *req.WaveMaxLines > 2000 {
		fail(w, r, "waveMaxLines", "is required, 1 to 2000")
		return
	}
	if req.WaveMaxOrders == nil || *req.WaveMaxOrders < 1 || *req.WaveMaxOrders > 500 {
		fail(w, r, "waveMaxOrders", "is required, 1 to 500")
		return
	}
	rule, err := a.store.SaveReleaseRule(r.Context(), id, req.LegalEntityID, version, store.ReleaseRuleInput{
		HorizonDays: *req.HorizonDays, RequireFullATP: *req.RequireFullATP, WaveMaxLines: *req.WaveMaxLines, WaveMaxOrders: *req.WaveMaxOrders}, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, rule.Version)
	httpx.JSON(w, http.StatusOK, rule)
}
