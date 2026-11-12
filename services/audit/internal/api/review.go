package api

import (
	"encoding/base64"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/audit/internal/domain"
	"github.com/BenjaminSRussell/passage/services/audit/internal/store"
)

// dutyReadLogReview lists break-glass reads and reviews them (#606, HIPAA emergency access).
const dutyReadLogReview = "audit.read-log.review"

type reviewRequest struct {
	Outcome string `json:"outcome"`
	Note    string `json:"note"`
}

// listBreakGlass is GET /v1/audit/reads:break-glass: reads recorded with the BREAK-GLASS reason code, oldest first,
// with their review. status is pending (the default), reviewed or all.
func (a *API) listBreakGlass(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	status := q.Get("status")
	if status == "" {
		status = store.ReviewPending
	}
	if status != store.ReviewPending && status != store.ReviewReviewed && status != store.ReviewAll {
		badQuery(w, r, "status", "must be pending, reviewed or all")
		return
	}
	limit := defaultReadLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxReadLimit {
			badQuery(w, r, "limit", "must be a whole number from 1 to "+strconv.Itoa(maxReadLimit))
			return
		}
		limit = n
	}
	var after int64
	if v := q.Get("cursor"); v != "" {
		raw, err := base64.RawURLEncoding.DecodeString(v)
		seq, perr := strconv.ParseInt(string(raw), 10, 64)
		if err != nil || perr != nil || seq < 1 {
			badQuery(w, r, "cursor", "is not a cursor this service issued")
			return
		}
		after = seq
	}
	rows, err := a.store.ListBreakGlass(r.Context(), status, limit+1, after)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := map[string]any{}
	if len(rows) > limit {
		rows = rows[:limit]
		out["nextCursor"] = base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(rows[limit-1].Seq, 10)))
	}
	out["items"] = rows
	httpx.JSON(w, http.StatusOK, out)
}

// reviewRead is POST /v1/audit/reads/{id}:review: a second person's review of one break-glass read, made once.
func (a *API) reviewRead(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req reviewRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if problems := domain.ValidateReview(req.Outcome, req.Note); len(problems) > 0 {
		pr := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
		pr.Errors = problems
		httpx.Fail(w, r, pr)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	rec, err := a.store.ReviewRead(r.Context(), chi.URLParam(r, "id"), p.UserID, req.Outcome, req.Note)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.reviews.Add(1)
	httpx.JSON(w, http.StatusOK, rec)
}
