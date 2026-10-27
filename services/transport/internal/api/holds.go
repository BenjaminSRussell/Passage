package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

var holdStatuses = []string{domain.HoldOpen, domain.HoldReleased}

type releaseHoldRequest struct {
	Reason string `json:"reason"`
}

// listScreeningHolds is the Screening holds inquiry (§6.34.14), newest first,
// filtered by status and load.
func (a *API) listScreeningHolds(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.HoldFilter{Status: q.Get("status"), LoadID: q.Get("loadId")}
	fields := map[string]string{}
	if f.Status != "" && f.Status != domain.HoldOpen && f.Status != domain.HoldReleased {
		fields["status"] = "must be one of " + strings.Join(holdStatuses, ", ")
	}
	checkOptionalID(fields, "loadId", f.LoadID)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	holds, err := a.store.ListScreeningHolds(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list screening holds failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": holds})
}

func (a *API) getScreeningHold(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		httpx.Fail(w, r, store.NotFound("screening hold"))
		return
	}
	hold, err := a.store.GetScreeningHold(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get screening hold failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, hold)
}

// releaseScreeningHold releases a POTENTIAL_MATCH or SCREENING_PENDING hold
// (BR-34.17). The reason is required and the releasing user is the caller, so
// the release log shows user and reason (AC-34.16); tendering then proceeds.
func (a *API) releaseScreeningHold(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		httpx.Fail(w, r, store.NotFound("screening hold"))
		return
	}
	var req releaseHoldRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := domain.ValidateHoldRelease(req.Reason); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if p, _ := tenant.FromContext(r.Context()); strings.TrimSpace(p.UserID) == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "a release needs an identified user"))
		return
	}
	hold, err := a.store.ReleaseScreeningHold(r.Context(), id, strings.TrimSpace(req.Reason), a.now())
	if err != nil {
		a.fail(w, r, "release screening hold failed", err)
		return
	}
	a.holdsReleased.Add(1)
	httpx.JSON(w, http.StatusOK, hold)
}
