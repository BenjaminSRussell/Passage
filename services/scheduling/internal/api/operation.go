package api

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

type operationPatchRequest struct {
	Minutes      *int       `json:"minutes"`
	DueAt        *time.Time `json:"dueAt"`
	ReadyAt      *time.Time `json:"readyAt"`
	Priority     *int       `json:"priority"`
	SetupMinutes *int       `json:"setupMinutes"`
	ResourceIDs  []string   `json:"resourceIds"`
}

func operationID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeOperationNotFound, "operation not found"))
		return "", false
	}
	return id, true
}

// getOperation answers one operation with its version for If-Match.
func (a *API) getOperation(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := operationID(w, r)
	if !ok {
		return
	}
	op, err := a.store.GetOperation(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(op.Version))
	httpx.JSON(w, http.StatusOK, op)
}

// updateOperation changes an operation's duration, dates, priority, setup
// minutes or candidate resources (FR-23.4). A change that makes its placement
// stale takes its non-firmed jobs off the schedule.
func (a *API) updateOperation(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := operationID(w, r)
	if !ok {
		return
	}
	var req operationPatchRequest
	body, ok := decodeBody(w, r, &req)
	if !ok {
		return
	}
	idem, ok := requireIdem(w, r, body)
	if !ok {
		return
	}
	version, ok := ifMatchOf(w, r, "operation")
	if !ok {
		return
	}
	idem.Fingerprint = fingerprint(id+"|"+strconv.Itoa(version), body)
	fields := map[string]string{}
	patch := store.OperationPatch{DueAt: req.DueAt, ReadyAt: req.ReadyAt}
	if req.Minutes == nil && req.DueAt == nil && req.ReadyAt == nil && req.Priority == nil && req.SetupMinutes == nil && req.ResourceIDs == nil {
		fields["body"] = "send at least one of minutes, dueAt, readyAt, priority, setupMinutes or resourceIds"
	}
	if req.Minutes != nil {
		if *req.Minutes < 1 || *req.Minutes > 100000 {
			fields["minutes"] = "must be 1 to 100000"
		}
		patch.Minutes = req.Minutes
	}
	if req.Priority != nil {
		if *req.Priority < 0 || *req.Priority > 1000 {
			fields["priority"] = "must be 0 to 1000"
		}
		patch.Priority = req.Priority
	}
	if req.SetupMinutes != nil {
		if *req.SetupMinutes < 0 || *req.SetupMinutes > maxWeekMinutes {
			fields["setupMinutes"] = "must be 0 to 10080"
		}
		patch.SetupMinutes = req.SetupMinutes
	}
	if req.ResourceIDs != nil {
		if len(req.ResourceIDs) < 1 || len(req.ResourceIDs) > 50 {
			fields["resourceIds"] = "must list 1 to 50 resources in priority order"
		}
		for i, rid := range req.ResourceIDs {
			req.ResourceIDs[i] = strings.ToLower(rid)
			if !ids.Valid(req.ResourceIDs[i]) {
				fields["resourceIds"] = "every entry must be a UUID"
			}
		}
		patch.ResourceIDs = req.ResourceIDs
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	op, replayed, err := a.store.UpdateOperation(r.Context(), id, version, patch, idem)
	if err != nil {
		slog.Error("update operation failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("ETag", etag(op.Version))
	httpx.JSON(w, http.StatusOK, op)
}

// deleteOperation removes an operation and its non-firmed jobs.
func (a *API) deleteOperation(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := operationID(w, r)
	if !ok {
		return
	}
	idem, ok := requireIdem(w, r, nil)
	if !ok {
		return
	}
	version, ok := ifMatchOf(w, r, "operation")
	if !ok {
		return
	}
	idem.Fingerprint = fingerprint(id+"|"+strconv.Itoa(version), nil)
	out, replayed, err := a.store.DeleteOperation(r.Context(), id, version, idem)
	if err != nil {
		slog.Error("delete operation failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, http.StatusOK, out)
}

// unscheduleOrder takes an order off the schedule; a firmed operation stays.
func (a *API) unscheduleOrder(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	orderID := strings.ToLower(chi.URLParam(r, "orderId"))
	if !ids.Valid(orderID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeOrderHasNoOperations, "no operations are known for that order"))
		return
	}
	idem, ok := requireIdem(w, r, nil)
	if !ok {
		return
	}
	idem.Fingerprint = fingerprint(orderID, nil)
	out, replayed, err := a.store.UnscheduleOrder(r.Context(), orderID, idem)
	if err != nil {
		slog.Error("unschedule order failed", "err", err, "orderId", orderID, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, http.StatusOK, out)
}
