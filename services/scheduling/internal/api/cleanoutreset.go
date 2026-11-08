package api

import (
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

type cleanoutResetRequest struct {
	BatchesSince *int   `json:"batchesSince"`
	MinutesSince *int   `json:"minutesSince"`
	Reason       string `json:"reason"`
}

func resourcePathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeResourceNotFound, "resource not found"))
		return "", false
	}
	return id, true
}

// listCleanoutCounters answers the batches and run minutes since the last
// cleanout of every resource (FR-23.15).
func (a *API) listCleanoutCounters(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	items, err := a.store.ListCleanoutCounters(r.Context())
	if err != nil {
		slog.Error("list cleanout counters failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// getCleanoutCounter answers one resource's counter and its latest resets.
func (a *API) getCleanoutCounter(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := resourcePathID(w, r)
	if !ok {
		return
	}
	d, err := a.store.GetCleanoutCounter(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

// resetCleanoutCounter tells the scheduler a resource was cleaned out outside
// the plan: its counter returns to zero. The caller sends the counts it saw so
// a counter that moved meanwhile is refused (SCHED-0148).
func (a *API) resetCleanoutCounter(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := resourcePathID(w, r)
	if !ok {
		return
	}
	var req cleanoutResetRequest
	body, ok := decodeBody(w, r, &req)
	if !ok {
		return
	}
	idem, ok := requireIdem(w, r, body)
	if !ok {
		return
	}
	idem.Fingerprint = fingerprint(id, body)
	fields := map[string]string{}
	if req.BatchesSince == nil || *req.BatchesSince < 0 {
		fields["batchesSince"] = "required: the batches since the last cleanout you saw (0 or more)"
	}
	if req.MinutesSince == nil || *req.MinutesSince < 0 {
		fields["minutesSince"] = "required: the run minutes since the last cleanout you saw (0 or more)"
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" || utf8.RuneCountInString(req.Reason) > 200 {
		fields["reason"] = "required, at most 200 characters"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	res, isNew, err := a.store.ResetCleanoutCounter(r.Context(), store.CleanoutResetInput{ResourceID: id,
		SeenBatches: *req.BatchesSince, SeenMinutes: *req.MinutesSince, Reason: req.Reason}, idem)
	if err != nil {
		slog.Error("reset cleanout counter failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	created(w, res, isNew)
}
