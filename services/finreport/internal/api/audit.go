package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

type auditDTO struct {
	ID         string         `json:"id"`
	OccurredAt time.Time      `json:"occurredAt"`
	LedgerID   string         `json:"ledgerId,omitempty"`
	Period     string         `json:"period,omitempty"`
	Kind       string         `json:"kind"`
	ActorID    string         `json:"actorId"`
	ObjectRef  string         `json:"objectRef"`
	Details    map[string]any `json:"details"`
	TraceID    string         `json:"traceId,omitempty"`
	TraceURL   string         `json:"traceUrl,omitempty"`
}

// auditFilter reads the audit trail filter from the query string. Malformed
// values, and a filter with neither a period nor a from/to window, are 400
// COM-0002 before any query runs.
func auditFilter(q url.Values) (domain.AuditFilter, error) {
	f := domain.AuditFilter{LedgerID: q.Get("ledger"), Period: q.Get("period"), Kind: q.Get("kind"), ActorID: q.Get("actor")}
	for _, b := range []struct {
		name string
		dst  *time.Time
	}{{"from", &f.From}, {"to", &f.To}} {
		if v := q.Get(b.name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return f, fmt.Errorf("%w: %s must be an RFC 3339 time such as 2026-03-01T00:00:00Z", domain.ErrAuditFilter, b.name)
			}
			*b.dst = t
		}
	}
	return f, f.Validate()
}

func badAuditQuery(w http.ResponseWriter, r *http.Request, err error) {
	httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", err.Error()))
}

// auditTrail is the audit trail report (FR-15.10): the entries for a period
// or a date window, optionally for one ledger, kind or actor, oldest first and
// keyset paged.
func (a *API) auditTrail(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	q := r.URL.Query()
	f, err := auditFilter(q)
	if err != nil {
		badAuditQuery(w, r, err)
		return
	}
	limit := domain.AuditDefaultPage
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > domain.AuditMaxPage {
			badAuditQuery(w, r, fmt.Errorf("limit must be 1 to %d", domain.AuditMaxPage))
			return
		}
		limit = n
	}
	var after *domain.AuditCursor
	if v := q.Get("cursor"); v != "" {
		c, err := domain.DecodeAuditCursor(v)
		if err != nil {
			badAuditQuery(w, r, errors.New("cursor is not one this endpoint returned"))
			return
		}
		after = &c
	}
	page, err := a.store.AuditTrail(r.Context(), f, after, limit)
	if err != nil {
		failAudit(w, r, err)
		return
	}
	items := make([]auditDTO, 0, len(page.Items))
	for _, e := range page.Items {
		details := e.Details
		if details == nil {
			details = map[string]any{}
		}
		items = append(items, auditDTO{ID: e.ID, OccurredAt: e.OccurredAt, LedgerID: e.LedgerID, Period: e.Period, Kind: e.Kind,
			ActorID: e.ActorID, ObjectRef: e.ObjectRef, Details: details, TraceID: e.TraceID, TraceURL: a.traceLink(e.TraceID)})
	}
	var next any
	if page.Next != nil {
		next = page.Next.Encode()
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "nextCursor": next})
}

// exportAuditTrail returns every entry matching the filter as CSV (FR-15.10).
// The SHA-256 of the file is in X-Passage-Export-Sha256 and the export is
// recorded in the trail under X-Passage-Export-Id, so the file can be verified
// later. A filter matching too many entries is refused (REPORT-0030), never
// cut short.
func (a *API) exportAuditTrail(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	f, err := auditFilter(r.URL.Query())
	if err != nil {
		badAuditQuery(w, r, err)
		return
	}
	exp, err := a.store.ExportAuditTrail(r.Context(), f)
	if err != nil {
		failAudit(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="audit-trail-%s.csv"`, exp.ID))
	w.Header().Set("X-Passage-Export-Id", exp.ID)
	w.Header().Set("X-Passage-Export-Sha256", exp.SHA256)
	w.Header().Set("X-Passage-Export-Entries", strconv.Itoa(exp.Entries))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(exp.Body)
}

// failAudit maps a filter refused by the store to 400 and everything else to fail.
func failAudit(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrAuditFilter) {
		badAuditQuery(w, r, err)
		return
	}
	fail(w, r, err)
}
