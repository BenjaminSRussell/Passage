package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

// createScheduleReleaseRequest is the body of
// POST /v1/sales/agreements/{id}/schedule-releases (FR-27.20): the customer's
// release number, its cumulative received quantity and the requirement dates
// with cumulative required quantities.
type createScheduleReleaseRequest struct {
	ReleaseNo   string                 `json:"releaseNo"`
	ReleaseType string                 `json:"releaseType"`
	CumReceived string                 `json:"cumReceived"`
	Buckets     []releaseBucketRequest `json:"buckets"`
}

// resolveReleaseRequest is the body of the :resolve action on a HELD release.
type resolveReleaseRequest struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// recordShippedRequest is the body of POST /v1/sales/agreements/{id}:record-shipped.
type recordShippedRequest struct {
	CumShipped   string  `json:"cumShipped"`
	CumResetDate *string `json:"cumResetDate"`
}

func buildScheduleRelease(req createScheduleReleaseRequest) (store.NewScheduleRelease, map[string]string) {
	f := map[string]string{}
	if n := len(req.ReleaseNo); n == 0 || n > 40 {
		f["releaseNo"] = "must be 1 to 40 characters"
	}
	if req.ReleaseType != domain.ReleaseForecast && req.ReleaseType != domain.ReleaseJIT {
		f["releaseType"] = "must be FORECAST or JIT"
	}
	received := parseDecimal(f, "cumReceived", req.CumReceived)
	if err := domain.ValidateCumulative(received); err != nil && f["cumReceived"] == "" {
		f["cumReceived"] = err.Error()
	}
	buckets := parseBuckets(f, req.Buckets)
	return store.NewScheduleRelease{ReleaseNo: req.ReleaseNo, ReleaseType: req.ReleaseType, CumReceived: received, Buckets: buckets}, f
}

// createScheduleRelease stores a release against the agreement. A cumulative
// mismatch is not an error: the release is stored HELD (201) with code
// CUM_QTY_MISMATCH and changes no schedule line (BR-27.12, AC-27.13).
func (a *API) createScheduleRelease(w http.ResponseWriter, r *http.Request) {
	agreementRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		key := r.Header.Get(idempotencyKey)
		var req createScheduleReleaseRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		in, fields := buildScheduleRelease(req)
		if key == "" {
			fields[idempotencyKey] = "is required"
		}
		if len(fields) > 0 {
			validationFailed(w, r, fields)
			return
		}
		hash, err := domain.RequestHash(req)
		if err != nil {
			a.fail(w, r, "hash request", err)
			return
		}
		p, _ := tenant.FromContext(r.Context())
		in.AgreementID, in.IdempotencyKey, in.RequestHash, in.Actor = id, key, hash, p.UserID
		saved, created, err := a.store.CreateScheduleRelease(r.Context(), in, a.now().UTC().Truncate(time.Microsecond))
		if err != nil {
			a.fail(w, r, "create schedule release", err)
			return
		}
		status := http.StatusOK
		if created {
			a.created.Add(1)
			status = http.StatusCreated
		}
		setETag(w, saved.Version)
		httpx.JSON(w, status, saved)
	})(w, r)
}

// listScheduleReleases returns the agreement's releases, newest first.
func (a *API) listScheduleReleases(w http.ResponseWriter, r *http.Request) {
	agreementRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		limit, ok := pageLimit(r.URL.Query().Get("limit"))
		if !ok {
			validationFailed(w, r, map[string]string{"limit": fmt.Sprintf("must be 1 to %d", maxPageSize)})
			return
		}
		items, err := a.store.ListScheduleReleases(r.Context(), id, limit)
		if err != nil {
			a.fail(w, r, "list schedule releases", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	})(w, r)
}

func releaseRoute(next func(http.ResponseWriter, *http.Request, string, string)) http.HandlerFunc {
	return agreementRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		rid := chi.URLParam(r, "releaseId")
		if !ids.Valid(rid) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeReleaseNotFound, "schedule release not found"))
			return
		}
		next(w, r, id, rid)
	})
}

// getScheduleRelease returns one release with its schedule lines.
func (a *API) getScheduleRelease(w http.ResponseWriter, r *http.Request) {
	releaseRoute(func(w http.ResponseWriter, r *http.Request, id, rid string) {
		saved, err := a.store.GetScheduleRelease(r.Context(), id, rid)
		if err != nil {
			a.fail(w, r, "get schedule release", err)
			return
		}
		setETag(w, saved.Version)
		httpx.JSON(w, http.StatusOK, saved)
	})(w, r)
}

// resolveScheduleRelease accepts (override) or rejects a HELD release at its
// ETag.
func (a *API) resolveScheduleRelease(w http.ResponseWriter, r *http.Request) {
	releaseRoute(func(w http.ResponseWriter, r *http.Request, id, rid string) {
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		var req resolveReleaseRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		f := map[string]string{}
		if req.Decision != domain.DecisionAccept && req.Decision != domain.DecisionReject {
			f["decision"] = "must be ACCEPT or REJECT"
		}
		if n := len(req.Reason); n == 0 || n > 500 {
			f["reason"] = "must be 1 to 500 characters"
		}
		if len(f) > 0 {
			validationFailed(w, r, f)
			return
		}
		p, _ := tenant.FromContext(r.Context())
		saved, err := a.store.ResolveScheduleRelease(r.Context(), id, rid, version, req.Decision, req.Reason, p.UserID, a.now().UTC().Truncate(time.Microsecond))
		if err != nil {
			a.fail(w, r, "resolve schedule release", err)
			return
		}
		setETag(w, saved.Version)
		httpx.JSON(w, http.StatusOK, saved)
	})(w, r)
}

// recordShipped sets the agreement's cumulative shipped quantity and
// recalculates the open lines of the release in effect.
func (a *API) recordShipped(w http.ResponseWriter, r *http.Request) {
	agreementRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		var req recordShippedRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		f := map[string]string{}
		shipped := parseDecimal(f, "cumShipped", req.CumShipped)
		if err := domain.ValidateCumulative(shipped); err != nil && f["cumShipped"] == "" {
			f["cumShipped"] = err.Error()
		}
		if req.CumResetDate != nil {
			if _, err := time.Parse(dayLayout, *req.CumResetDate); err != nil {
				f["cumResetDate"] = "must be a date such as 2027-01-01"
			}
		}
		if len(f) > 0 {
			validationFailed(w, r, f)
			return
		}
		p, _ := tenant.FromContext(r.Context())
		saved, err := a.store.RecordShipped(r.Context(), id, version, shipped, req.CumResetDate, p.UserID, a.now().UTC().Truncate(time.Microsecond))
		if err != nil {
			a.fail(w, r, "record shipped", err)
			return
		}
		setETag(w, saved.Version)
		httpx.JSON(w, http.StatusOK, saved)
	})(w, r)
}
