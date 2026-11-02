package api

import (
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

// DutyJobReopen reopens a finished job. FR-25.3 says a supervisor reopens it; the PRD duty
// table has no duty for that, and *Approve time registrations* is about pay.
const DutyJobReopen = "shopfloor.job.reopen"

type reopenRequest struct {
	Reason string `json:"reason"`
}

// reopenJob puts a finished job back to started with a reason. It needs the job version in
// If-Match and an Idempotency-Key, and an authenticated user, who is recorded.
func (a *API) reopenJob(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "reopening a job")
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodeJobNotFound, "job")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req reopenRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	reason, problem := cleanReason(req.Reason)
	if problem != "" {
		validationFailed(w, r, map[string]string{"reason": problem})
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	job, err := a.store.ReopenJob(r.Context(), id, version, reason, user, idem)
	if err != nil {
		a.fail(w, r, "reopen job", err)
		return
	}
	a.jobsReopened.Add(1)
	w.Header().Set("ETag", etag(job.Version))
	httpx.JSON(w, http.StatusOK, job)
}

// listJobReopenings is the audit list of a job's reopenings, newest first.
func (a *API) listJobReopenings(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeJobNotFound, "job")
	if !ok {
		return
	}
	limit, ok := limitParam(w, r, 100, 500)
	if !ok {
		return
	}
	items, err := a.store.ListJobReopenings(r.Context(), id, limit)
	if err != nil {
		a.fail(w, r, "list job reopenings", err)
		return
	}
	if items == nil {
		items = []store.JobReopening{}
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.JobReopening]{Items: items})
}
