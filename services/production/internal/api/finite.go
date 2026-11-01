package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

// actionApplyFiniteDates is the `:apply-finite-dates` command (FR-23.10): the windows
// finite-capacity scheduling found replace the order's infinite-capacity dates.
const actionApplyFiniteDates = "apply-finite-dates"

type finiteWindow struct {
	OperNumber int       `json:"operNumber"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
}

type finiteRequest struct {
	RunID      string         `json:"runId"`
	Operations []finiteWindow `json:"operations"`
}

// applyFiniteDates takes scheduling's operation windows (production.order.write). A person
// (a principal without a service) must send If-Match with the order version, so windows typed
// against a stale order cannot silently overwrite newer dates; a service applying a run may
// omit it, and If-Match is checked when sent. No Idempotency-Key is needed: applying the dates
// the order already has is a no-op (200, Idempotent-Replayed, no new version, no event) whatever
// the If-Match, so a retry or a redelivery cannot do harm.
func (a *API) applyFiniteDates(w http.ResponseWriter, r *http.Request, id string) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var ifMatch int
	if raw := r.Header.Get("If-Match"); raw != "" || p.Service == "" {
		v, err := parseIfMatch(raw)
		if err != nil {
			a.refuse(w, r, err)
			return
		}
		ifMatch = v
	}
	var req finiteRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if len(req.RunID) > 64 {
		fields["runId"] = "at most 64 characters"
	}
	if len(req.Operations) == 0 || len(req.Operations) > domain.MaxFiniteWindows {
		fields["operations"] = fmt.Sprintf("send between 1 and %d operation windows", domain.MaxFiniteWindows)
	}
	in := store.FiniteDates{OrderID: id, RunID: req.RunID, IfMatch: ifMatch, Actor: p.UserID, TraceID: middleware.GetReqID(r.Context())}
	seen := map[int]bool{}
	for i, op := range req.Operations {
		at := fmt.Sprintf("operations[%d]", i)
		switch {
		case op.OperNumber < 1:
			fields[at+".operNumber"] = "must be a positive whole number"
		case seen[op.OperNumber]:
			fields[at+".operNumber"] = "appears twice"
		}
		seen[op.OperNumber] = true
		switch {
		case op.Start.IsZero() || op.End.IsZero():
			fields[at] = "start and end are required"
		case op.End.Before(op.Start):
			fields[at+".end"] = "must not be before start"
		}
		in.Windows = append(in.Windows, domain.OperationWindow{OperNumber: op.OperNumber, Start: op.Start, End: op.End})
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	order, changed, err := a.store.ApplyFiniteDates(r.Context(), in)
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(order.Version))
	if !changed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, http.StatusOK, order)
}
