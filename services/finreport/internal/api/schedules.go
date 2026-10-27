package api

import (
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

// scheduleDTO is a schedule as the API returns it: the stored schedule and the
// next time it fires in its time zone (PRD §2.15.6 Schedules screen).
type scheduleDTO struct {
	domain.Schedule
	NextRunAt *time.Time `json:"nextRunAt,omitempty"`
}

// now is the clock used for nextRunAt; tests replace it.
var now = time.Now

func toScheduleDTO(sc domain.Schedule) scheduleDTO {
	out := scheduleDTO{Schedule: sc}
	if sc.Enabled {
		if next, err := sc.NextRun(now()); err == nil {
			out.NextRunAt = &next
		}
	}
	return out
}

func (a *API) listSchedules(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	items, err := a.store.ListSchedules(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	out := make([]scheduleDTO, 0, len(items))
	for _, sc := range items {
		out = append(out, toScheduleDTO(sc))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) getSchedule(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	sc, rev, err := a.store.GetSchedule(r.Context(), chi.URLParam(r, "code"))
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(rev))
	httpx.JSON(w, http.StatusOK, toScheduleDTO(sc))
}

// putSchedule creates or replaces a schedule (FR-15.8). The body is validated
// with REPORT-0018, the definition must have a published version
// (REPORT-0001), and replacing an existing schedule needs If-Match. Repeating
// a PUT with the current revision gives the same schedule (PUT idempotent).
func (a *API) putSchedule(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	code := chi.URLParam(r, "code")
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", "unreadable body"))
		return
	}
	// The body may be a schedule as GET returned it: nextRunAt and updatedBy
	// are read-only and ignored.
	var in scheduleDTO
	if err := decodeStrict(raw, &in); err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", err.Error()))
		return
	}
	sc := in.Schedule
	if sc.Code == "" {
		sc.Code = code
	}
	if sc.Code != code {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeScheduleInvalid, "code: body code does not match the path"))
		return
	}
	sc = sc.Normalize()
	if issues := sc.Validate(); len(issues) > 0 {
		httpx.Fail(w, r, issueProblem(issues))
		return
	}
	ifMatch, err := parseIfMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	sc.UpdatedBy = p.UserID
	saved, rev, err := a.store.SaveSchedule(r.Context(), sc, ifMatch)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(rev))
	httpx.JSON(w, http.StatusOK, toScheduleDTO(saved))
}
