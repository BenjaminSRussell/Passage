package api

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

type parametersPatchRequest struct {
	DefaultMode       *string `json:"defaultMode"`
	DefaultDirection  *string `json:"defaultDirection"`
	DefaultFinite     *bool   `json:"defaultFinite"`
	DragSnapMinutes   *int    `json:"dragSnapMinutes"`
	PropagationOrders *int    `json:"propagationOrders"`
	PropagationLevels *int    `json:"propagationLevels"`
}

// getParameters returns the tenant's scheduling parameters with the version
// to send as If-Match (PRD 4.23.13).
func (a *API) getParameters(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	p, err := a.store.GetParameters(r.Context())
	if err != nil {
		slog.Error("get parameters failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(p.Version))
	httpx.JSON(w, http.StatusOK, p)
}

// updateParameters changes the defaults runs and moves take and the drag snap
// of the Gantt. The duty is Maintain scheduling setup (PRD 4.23.17 lists
// parameters under it).
func (a *API) updateParameters(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req parametersPatchRequest
	body, ok := decodeBody(w, r, &req)
	if !ok {
		return
	}
	idem, ok := requireIdem(w, r, body)
	if !ok {
		return
	}
	version, ok := ifMatchOf(w, r, "scheduling parameters")
	if !ok {
		return
	}
	idem.Fingerprint = fingerprint("parameters|"+strconv.Itoa(version), body)
	if req.DefaultMode == nil && req.DefaultDirection == nil && req.DefaultFinite == nil && req.DragSnapMinutes == nil && req.PropagationOrders == nil && req.PropagationLevels == nil {
		invalid(w, r, map[string]string{"body": "send at least one of defaultMode, defaultDirection, defaultFinite, dragSnapMinutes, propagationOrders or propagationLevels"})
		return
	}
	// Validate the sent fields against valid defaults, so only they can fail.
	probe := domain.DefaultParameters()
	if req.DefaultMode != nil {
		probe.DefaultMode = *req.DefaultMode
	}
	if req.DefaultDirection != nil {
		probe.DefaultDirection = *req.DefaultDirection
	}
	if req.DragSnapMinutes != nil {
		probe.DragSnapMinutes = *req.DragSnapMinutes
	}
	if req.PropagationOrders != nil {
		probe.PropagationOrders = *req.PropagationOrders
	}
	if req.PropagationLevels != nil {
		probe.PropagationLevels = *req.PropagationLevels
	}
	if fields := probe.Validate(); len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	p, replayed, err := a.store.UpdateParameters(r.Context(), version, store.ParametersPatch{DefaultMode: req.DefaultMode,
		DefaultDirection: req.DefaultDirection, DefaultFinite: req.DefaultFinite, DragSnapMinutes: req.DragSnapMinutes,
		PropagationOrders: req.PropagationOrders, PropagationLevels: req.PropagationLevels}, idem)
	if err != nil {
		slog.Error("update parameters failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("ETag", etag(p.Version))
	httpx.JSON(w, http.StatusOK, p)
}

// runDefaults is what a run takes from the parameters when the request leaves
// the choice out.
type runDefaults struct {
	Mode     string
	Backward bool
	Finite   bool
}

// resolveRunDefaults reads the parameters only when the request omitted
// something they decide.
func (a *API) resolveRunDefaults(r *http.Request, mode, direction string, finite *bool) (runDefaults, error) {
	out := runDefaults{Mode: mode, Backward: direction == "backward", Finite: finite == nil || *finite}
	if mode != "" && direction != "" && finite != nil {
		return out, nil
	}
	p, err := a.store.GetParameters(r.Context())
	if err != nil {
		return runDefaults{}, err
	}
	if mode == "" {
		out.Mode = p.DefaultMode
	}
	if direction == "" {
		out.Backward = p.DefaultDirection == "backward"
	}
	if finite == nil {
		out.Finite = p.DefaultFinite
	}
	return out, nil
}
