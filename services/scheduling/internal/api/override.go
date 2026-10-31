package api

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

const maxOverrideSpan = 31 * 24 * time.Hour

type overrideRequest struct {
	Kind   string    `json:"kind"`
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Reason string    `json:"reason"`
}

// createCapacityOverride closes (downtime) or opens (extra) a span of a
// resource's calendar and reports the booked jobs a downtime overlaps
// (FR-23.1, AC-23.7).
func (a *API) createCapacityOverride(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeResourceNotFound, "resource not found"))
		return
	}
	var req overrideRequest
	body, ok := decodeBody(w, r, &req)
	if !ok {
		return
	}
	idem, ok := requireIdem(w, r, body)
	if !ok {
		return
	}
	// The resource is in the path: the same key and body sent to another
	// resource is a different request, not a replay.
	idem.Fingerprint = fingerprint(id, body)
	fields := map[string]string{}
	if req.Kind != domain.OverrideDowntime && req.Kind != domain.OverrideExtra {
		fields["kind"] = "must be downtime or extra"
	}
	switch {
	case req.Start.IsZero() || req.End.IsZero():
		fields["start"] = "start and end are required RFC 3339 times"
	case !req.End.After(req.Start):
		fields["end"] = "must be after start"
	case req.End.Sub(req.Start) > maxOverrideSpan:
		fields["end"] = "must be at most 31 days after start"
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if len(req.Reason) > 200 {
		fields["reason"] = "must be at most 200 characters"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	res, isNew, err := a.store.CreateCapacityOverride(r.Context(), store.CapacityOverride{ResourceID: id, Kind: req.Kind,
		StartAt: req.Start.UTC().Truncate(time.Minute), EndAt: req.End.UTC().Truncate(time.Minute), Reason: req.Reason}, idem)
	if err != nil {
		slog.Error("create capacity override failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	created(w, res, isNew)
}

func (a *API) listCapacityOverrides(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeResourceNotFound, "resource not found"))
		return
	}
	var from, to time.Time
	var err error
	q := r.URL.Query()
	if v := q.Get("from"); v != "" {
		if from, err = time.Parse(time.RFC3339, v); err != nil {
			invalid(w, r, map[string]string{"from": "must be an RFC 3339 time"})
			return
		}
	}
	if v := q.Get("to"); v != "" {
		if to, err = time.Parse(time.RFC3339, v); err != nil {
			invalid(w, r, map[string]string{"to": "must be an RFC 3339 time"})
			return
		}
	}
	items, err := a.store.ListCapacityOverrides(r.Context(), id, from.UTC(), to.UTC())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) deleteCapacityOverride(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, oid := strings.ToLower(chi.URLParam(r, "id")), strings.ToLower(chi.URLParam(r, "overrideId"))
	if !ids.Valid(id) || !ids.Valid(oid) {
		invalid(w, r, map[string]string{"id": "resource and override ids must be UUIDs"})
		return
	}
	if err := a.store.DeleteCapacityOverride(r.Context(), id, oid); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
