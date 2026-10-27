// Package api is the HR HTTP surface: decode, validate, tenant check, then the
// store or a domain rule, answered with httpx.JSON or an RFC 9457 problem.
package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const (
	readyTimeout     = 500 * time.Millisecond
	idempotencyKey   = "Idempotency-Key"
	validationDetail = "request failed validation"
	dateLayout       = "2006-01-02"

	dutyWorkerMaintain = "HcmWorkerMaintain"
	dutyWorkerInquire  = "HcmWorkerInquire"
	dutyPIIRead        = domain.PIIReadDuty
	dutyActionMaintain = "HcmActionMaintain"
	dutyOrgMaintain    = "HcmOrgMaintain"
	dutyOrgInquire     = "HcmOrgInquire"
	maxSearchLength    = 100
	maxReasonLength    = 500
)

var (
	personnelNumber = regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{0,19}$`)
	jobCode         = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)
	workerTypes     = []string{"EMPLOYEE", "CONTRACTOR"}
	employmentTypes = []string{"FULL_TIME", "PART_TIME", "TEMP", "CONTRACTOR"}
	assignmentKinds = []string{"PRIMARY", "CONCURRENT", "GLOBAL_HOST"}
)

// API holds the store and the counters rendered at /metrics.
type API struct {
	store            *store.Postgres
	workersCreated   atomic.Int64
	employments      atomic.Int64
	assignments      atomic.Int64
	positionsCreated atomic.Int64
	piiReveals       atomic.Int64
	piiMaskedReads   atomic.Int64
	asOfQueries      atomic.Int64
	plansCreated     atomic.Int64
	accrualRuns      atomic.Int64
	requestsCreated  atomic.Int64
	decisions        atomic.Int64
	yearCloses       atomic.Int64
}

// New builds the API over a store.
func New(s *store.Postgres) *API { return &API{store: s} }

// Register mounts every HR route on r, each behind its duty.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyWorkerMaintain)).Post("/v1/hr/workers", a.createWorker)
	r.With(authz.RequireDuty(dutyWorkerInquire)).Get("/v1/hr/workers", a.listWorkers)
	r.With(authz.RequireDuty(dutyWorkerInquire)).Get("/v1/hr/workers/{id}/employments", workerRoute(a.listEmployments))
	r.With(authz.RequireDuty(dutyOrgInquire)).Get("/v1/hr/positions", a.listPositions)
	r.With(authz.RequireDuty(dutyOrgInquire)).Get("/v1/hr/positions/{id}", positionRoute(a.getPosition))
	r.With(authz.RequireDuty(dutyWorkerInquire)).Get("/v1/hr/workers/{id}", workerRoute(a.getWorker))
	r.With(authz.RequireDuty(dutyWorkerInquire)).Get("/v1/hr/workers/{id}/as-of", workerRoute(a.workerAsOf))
	r.With(authz.RequireDuty(dutyPIIRead)).Get("/v1/hr/workers/{id}/pii", workerRoute(a.revealPII))
	r.With(authz.RequireDuty(dutyWorkerMaintain)).Post("/v1/hr/workers/{id}/employments", workerRoute(a.createEmployment))
	r.With(authz.RequireDuty(dutyOrgMaintain)).Post("/v1/hr/positions", a.createPosition)
	r.With(authz.RequireDuty(dutyActionMaintain)).Post("/v1/hr/positions/{id}/assignments", positionRoute(a.createAssignment))
	r.With(authz.RequireDuty(dutyAbsenceAdmin)).Post("/v1/hr/absence/plans", a.createAbsencePlan)
	r.With(authz.RequireDuty(dutyEssAbsence)).Get("/v1/hr/absence/plans", a.listAbsencePlans)
	r.With(authz.RequireDuty(dutyAbsenceAdmin)).Get("/v1/hr/absence/plans/{id}", planRoute(a.getAbsencePlan))
	r.With(authz.RequireDuty(dutyAbsenceAdmin)).Post("/v1/hr/absence/plans/{id}/year-end", planRoute(a.closePlanYear("close absence plan year", a.store.CloseYear)))
	r.With(authz.RequireDuty(dutyAbsenceAdmin)).Post("/v1/hr/absence/plans/{id}/carry-over-expiry", planRoute(a.closePlanYear("expire absence carry-over", a.store.ExpireCarryOver)))
	r.With(authz.RequireDuty(dutyAbsenceAdmin)).Post("/v1/hr/absence/accrue", a.runAccrual)
	r.With(authz.RequireDuty(dutyEssAbsence)).Post("/v1/hr/absence/requests", a.createAbsenceRequest)
	r.With(authz.RequireDuty(dutyEssAbsence)).Get("/v1/hr/absence/requests", a.listAbsenceRequests)
	r.With(authz.RequireDuty(dutyMssApprove)).Post("/v1/hr/absence/requests/{id}/decide", absenceRequestRoute(a.decideAbsenceRequest))
	r.With(authz.RequireDuty(dutyEssAbsence)).Post("/v1/hr/absence/requests/{id}/cancel", absenceRequestRoute(a.cancelAbsenceRequest))
	r.With(authz.RequireDuty(dutyEssAbsence)).Get("/v1/hr/workers/{id}/absence/balances", workerRoute(a.absenceBalances))
}

// ready replaces the kernel's static /readyz: it pings the database within
// 500 ms and answers 503 when Postgres is unreachable.
func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		logFailure(r, "readiness", err)
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// Metrics renders the counters in Prometheus text format for the admin port.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		counter := func(name, help string, v int64) {
			fmt.Fprintf(w, "# HELP %[1]s %[2]s\n# TYPE %[1]s counter\n%[1]s %[3]d\n", name, help, v)
		}
		counter("hr_workers_created_total", "Workers created; idempotent replays are not counted.", a.workersCreated.Load())
		counter("hr_employments_started_total", "Employments started.", a.employments.Load())
		counter("hr_positions_created_total", "Positions created.", a.positionsCreated.Load())
		counter("hr_position_assignments_created_total", "Position assignments created.", a.assignments.Load())
		counter("hr_pii_reveals_total", "Unmasked PII reads that passed the reveal gate.", a.piiReveals.Load())
		counter("hr_pii_masked_reads_total", "Masked PII reads (audited).", a.piiMaskedReads.Load())
		counter("hr_as_of_queries_total", "Worker as-of queries answered.", a.asOfQueries.Load())
		counter("hr_absence_plans_created_total", "Absence plans created; idempotent replays are not counted.", a.plansCreated.Load())
		counter("hr_absence_accrual_runs_total", "Accrual runs executed; replays of a stored (plan, period) are not counted.", a.accrualRuns.Load())
		counter("hr_absence_requests_created_total", "Absence requests submitted; idempotent replays are not counted.", a.requestsCreated.Load())
		counter("hr_absence_decisions_total", "Absence decisions that changed a request's status.", a.decisions.Load())
		counter("hr_absence_year_closes_total", "Year-end and carry-over expiry calls that closed at least one worker.", a.yearCloses.Load())
	})
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	return true
}

// workerRoute checks the tenant and the id shape. A malformed id is a 404,
// the same answer an unknown id gets.
func workerRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeWorkerNotFound, "worker not found"))
			return
		}
		next(w, r, id)
	}
}

func positionRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodePositionNotFound, "position not found"))
			return
		}
		next(w, r, id)
	}
}

func validationFailed(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

// failDomain maps a domain rule violation to a 422 problem and anything else
// through httpx.Fail unchanged.
func failDomain(w http.ResponseWriter, r *http.Request, err error) {
	if de, ok := err.(*domain.Error); ok {
		status := http.StatusUnprocessableEntity
		if de.Code == "AUTH-0003" {
			status = http.StatusForbidden
		}
		httpx.Fail(w, r, httpx.NewProblem(status, de.Code, de.Detail))
		return
	}
	httpx.Fail(w, r, err)
}

func logFailure(r *http.Request, op string, err error) {
	slog.Error(op+" failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
}

// parseDate reads a YYYY-MM-DD date as the UTC day it names.
func parseDate(s string) (time.Time, bool) {
	t, err := time.Parse(dateLayout, s)
	return t, err == nil
}

// optionalDate reads an optional date. ok is false only when present and malformed.
func optionalDate(s *string) (*time.Time, bool) {
	if s == nil || *s == "" {
		return nil, true
	}
	t, ok := parseDate(*s)
	if !ok {
		return nil, false
	}
	return &t, true
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

type listResponse[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// pageParams reads limit (1 to store.MaxPageSize, default the maximum) and
// the opaque cursor, which is the base64url of the last key on the page.
func pageParams(r *http.Request) (int, string, bool) {
	q := r.URL.Query()
	limit := store.MaxPageSize
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxPageSize {
			return 0, "", false
		}
		limit = n
	}
	after := ""
	if raw := q.Get("cursor"); raw != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			return 0, "", false
		}
		after = string(decoded)
	}
	return limit, after, true
}

func cursor(after string) string {
	if after == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(after))
}

func badPage(w http.ResponseWriter, r *http.Request) {
	validationFailed(w, r, map[string]string{"limit": fmt.Sprintf("must be 1 to %d", store.MaxPageSize), "cursor": "must be a cursor from a previous page"})
}
