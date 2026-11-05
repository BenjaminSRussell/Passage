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
	"github.com/BenjaminSRussell/passage/services/hr/internal/numbering"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const (
	readyTimeout     = 500 * time.Millisecond
	idempotencyKey   = "Idempotency-Key"
	validationDetail = "request failed validation"
	dateLayout       = "2006-01-02"

	dutyWorkerMaintain = "hr.worker.maintain"
	dutyWorkerInquire  = "hr.worker.inquire"
	dutyPIIRead        = domain.PIIReadDuty
	dutyActionMaintain = "hr.action.maintain"
	dutyOrgMaintain    = "hr.org.maintain"
	dutyOrgInquire     = "hr.org.inquire"
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
	store                *store.Postgres
	sequence             Sequencer
	workersCreated       atomic.Int64
	employments          atomic.Int64
	assignments          atomic.Int64
	positionsCreated     atomic.Int64
	piiReveals           atomic.Int64
	piiMaskedReads       atomic.Int64
	asOfQueries          atomic.Int64
	plansCreated         atomic.Int64
	accrualRuns          atomic.Int64
	requestsCreated      atomic.Int64
	decisions            atomic.Int64
	yearCloses           atomic.Int64
	benefitPlans         atomic.Int64
	simulations          atomic.Int64
	enrollments          atomic.Int64
	enrollmentsEnded     atomic.Int64
	skillsCreated        atomic.Int64
	skillRatings         atomic.Int64
	certsIssued          atomic.Int64
	skillSearches        atomic.Int64
	coursesCreated       atomic.Int64
	sessionsCreated      atomic.Int64
	registrations        atomic.Int64
	cancellations        atomic.Int64
	passes               atomic.Int64
	personnel            personnelCounters
	purgeEnabled         bool
	holds                store.CandidateHolds
	incidentsReported    atomic.Int64
	incidentTransitions  atomic.Int64
	oshaExtracts         atomic.Int64
	loansIssued          atomic.Int64
	loansClosed          atomic.Int64
	checklistsStarted    atomic.Int64
	checklistsCompleted  atomic.Int64
	tasksFinished        atomic.Int64
	questionnaireAnswers atomic.Int64
	goalsCreated         atomic.Int64
	reviewsOpened        atomic.Int64
	reviewsSelfSubmitted atomic.Int64
	reviewsSignedOff     atomic.Int64
}

// Sequencer allocates the next formatted number of a numbering sequence.
type Sequencer interface {
	Next(ctx context.Context, code, idempotencyKey string) (string, error)
}

// New builds the API over a store.
func New(s *store.Postgres) *API {
	return &API{store: s, sequence: numbering.Unavailable{}, purgeEnabled: true}
}

func (a *API) WithLegalHolds(h store.CandidateHolds) *API {
	a.holds = h
	return a
}

func (a *API) WithSequencer(s Sequencer) *API {
	a.sequence = s
	return a
}

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
	r.With(authz.RequireDuty(dutySetupAdmin)).Post("/v1/hr/benefits/plans", a.createBenefitPlan)
	r.With(authz.RequireDuty(dutyEssBenefits)).Get("/v1/hr/benefits/plans", a.listBenefitPlans)
	r.With(authz.RequireDuty(dutyEssBenefits)).Get("/v1/hr/benefits/plans/{id}", benefitPlanRoute(a.getBenefitPlan))
	r.With(authz.RequireDuty(dutySetupAdmin)).Get("/v1/hr/benefits/plans/{id}/simulate", benefitPlanRoute(a.simulateBenefitPlan))
	r.With(authz.RequireDuty(dutyEssBenefits)).Post("/v1/hr/benefits/life-events", a.createLifeEvent)
	r.With(authz.RequireDuty(dutyEssBenefits)).Post("/v1/hr/benefits/enrollments", a.createBenefitEnrollment)
	r.With(authz.RequireDuty(dutyWorkerMaintain)).Post("/v1/hr/benefits/enrollments/{id}/end", enrollmentRoute(a.endBenefitEnrollment))
	r.With(authz.RequireDuty(dutyEssBenefits)).Get("/v1/hr/workers/{id}/benefits", workerRoute(a.workerBenefits))
	r.With(authz.RequireDuty(dutySetupAdmin)).Post("/v1/hr/skills", a.createSkill)
	r.With(authz.RequireDuty(dutyWorkerInquire)).Get("/v1/hr/skills", a.listSkills)
	r.With(authz.RequireDuty(dutyOrgInquire)).Get("/v1/hr/skills/search", a.searchSkills)
	r.With(authz.RequireDuty(dutyWorkerMaintain)).Post("/v1/hr/workers/{id}/skills", workerRoute(a.rateWorkerSkill))
	r.With(authz.RequireDuty(dutyWorkerInquire)).Get("/v1/hr/workers/{id}/skills", workerRoute(a.listWorkerSkills))
	r.With(authz.RequireDuty(dutyWorkerMaintain)).Post("/v1/hr/workers/{id}/certifications", workerRoute(a.issueCertification))
	r.With(authz.RequireDuty(dutyWorkerInquire)).Get("/v1/hr/workers/{id}/certifications", workerRoute(a.listCertifications))
	r.With(authz.RequireDuty(dutyWorkerInquire)).Get("/v1/hr/certifications/expiring", a.expiringCertifications)
	r.With(authz.RequireDuty(dutyOrgInquire)).Get("/v1/hr/workers/{id}/skill-gap", workerRoute(a.skillGap))
	r.With(authz.RequireDuty(dutyOrgInquire)).Get("/v1/hr/positions/{id}/skill-requirements", positionRoute(a.listRequirements))
	r.With(authz.RequireDuty(dutyOrgMaintain)).Post("/v1/hr/positions/{id}/skill-requirements", positionRoute(a.setRequirement))
	r.With(authz.RequireDuty(dutyOrgMaintain)).Delete("/v1/hr/positions/{id}/skill-requirements/{skillCode}", positionRoute(a.removeRequirement))
	jobRoute := func(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
		return idRoute(store.CodeJobNotFound, "job not found", next)
	}
	r.With(authz.RequireDuty(dutyOrgInquire)).Get("/v1/hr/jobs/{id}/skill-requirements", jobRoute(a.listJobRequirements))
	r.With(authz.RequireDuty(dutyOrgMaintain)).Post("/v1/hr/jobs/{id}/skill-requirements", jobRoute(a.setJobRequirement))
	r.With(authz.RequireDuty(dutyOrgMaintain)).Delete("/v1/hr/jobs/{id}/skill-requirements/{skillCode}", jobRoute(a.removeJobRequirement))
	r.With(authz.RequireDuty(dutyOrgInquire)).Get("/v1/hr/jobs/{id}/skill-gaps", jobRoute(a.jobSkillGaps))
	r.With(authz.RequireDuty(dutyLearningAdmin)).Post("/v1/hr/courses", a.createCourse)
	r.With(authz.RequireDuty(dutyEssLearning)).Get("/v1/hr/courses", a.listCourses)
	r.With(authz.RequireDuty(dutyEssLearning)).Get("/v1/hr/courses/{id}", courseRoute(a.getCourse))
	r.With(authz.RequireDuty(dutyLearningAdmin)).Post("/v1/hr/courses/{id}/sessions", courseRoute(a.createSession))
	r.With(authz.RequireDuty(dutyEssLearning)).Get("/v1/hr/courses/{id}/sessions", courseRoute(a.listSessions))
	r.With(authz.RequireDuty(dutyEssLearning)).Get("/v1/hr/courses/sessions/{id}", sessionRoute(a.getSession))
	r.With(authz.RequireDuty(dutyLearningAdmin)).Get("/v1/hr/courses/sessions/{id}/registrations", sessionRoute(a.listRegistrations))
	r.With(authz.RequireDuty(dutyEssLearning)).Post("/v1/hr/courses/sessions/{id}/registrations", sessionRoute(a.registerForSession))
	r.With(authz.RequireDuty(dutyLearningAdmin)).Post("/v1/hr/courses/sessions/{id}/complete", sessionRoute(a.completeSession))
	a.registerPersonnel(r)
	r.With(authz.RequireDuty(dutyMssApprove)).Post("/v1/hr/courses/registrations/{id}/approve", registrationRoute(a.approveRegistration))
	r.With(authz.RequireDuty(dutyEssLearning)).Post("/v1/hr/courses/registrations/{id}/cancel", registrationRoute(a.cancelRegistration))
	r.With(authz.RequireDuty(dutyIncidentReport)).Post("/v1/hr/incidents", a.reportIncident)
	r.With(authz.RequireDuty(dutyIncidentAdmin)).Get("/v1/hr/incidents", a.listIncidents)
	r.With(authz.RequireDuty(dutyIncidentAdmin)).Get("/v1/hr/incidents/osha-extract", a.oshaExtract)
	r.With(authz.RequireDuty(dutyIncidentAdmin)).Get("/v1/hr/incidents/{id}", incidentRoute(a.getIncident))
	r.With(authz.RequireDuty(dutyIncidentAdmin)).Post("/v1/hr/incidents/{id}/status", incidentRoute(a.advanceIncident))
	r.With(authz.RequireDuty(dutyWorkerMaintain)).Post("/v1/hr/workers/{id}/loaned-items", workerRoute(a.issueLoanedItem))
	r.With(authz.RequireDuty(dutyWorkerInquire)).Get("/v1/hr/workers/{id}/loaned-items", workerRoute(a.listWorkerLoans))
	r.With(authz.RequireDuty(dutyWorkerInquire)).Get("/v1/hr/loaned-items/outstanding", a.outstandingLoans)
	r.With(authz.RequireDuty(dutyWorkerMaintain)).Post("/v1/hr/loaned-items/{id}/close", loanRoute(a.closeLoanedItem))
	r.With(authz.RequireDuty(dutySetupAdmin)).Post("/v1/hr/checklist-templates", a.createChecklistTemplate)
	r.With(authz.RequireDuty(dutySetupAdmin)).Get("/v1/hr/checklist-templates", a.listChecklistTemplates)
	r.With(authz.RequireDuty(dutyActionMaintain)).Post("/v1/hr/workers/{id}/checklists", workerRoute(a.startChecklist))
	r.With(authz.RequireDuty(dutyWorkerInquire)).Get("/v1/hr/workers/{id}/checklists", workerRoute(a.listWorkerChecklists))
	r.With(authz.RequireDuty(dutyWorkerInquire)).Get("/v1/hr/checklists/{id}", checklistRoute(a.getChecklist))
	r.With(authz.RequireDuty(dutyActionMaintain)).Post("/v1/hr/checklists/{id}/complete", checklistRoute(a.completeChecklist))
	r.With(authz.RequireDuty(dutyWorkerInquire)).Get("/v1/hr/tasks", a.listOwnedTasks)
	r.With(authz.RequireDuty(dutyActionMaintain)).Post("/v1/hr/tasks/{id}/finish", taskRoute(a.finishTask))
	r.With(authz.RequireDuty(dutyQuestionnaireAdmin)).Post("/v1/hr/questionnaires", a.createQuestionnaire)
	r.With(authz.RequireDuty(dutyQuestionnaireAdmin)).Get("/v1/hr/questionnaires", a.listQuestionnaires)
	r.With(authz.RequireDuty(dutyQuestionnaireAdmin)).Get("/v1/hr/questionnaires/{id}", questionnaireRoute(a.getQuestionnaire))
	r.With(authz.RequireDuty(dutyQuestionnaireAdmin)).Post("/v1/hr/questionnaires/schedules", a.createSchedule)
	r.With(authz.RequireDuty(dutyEssQuestionnaire)).Get("/v1/hr/questionnaires/schedules/open", a.openSchedules)
	r.With(authz.RequireDuty(dutyEssQuestionnaire)).Get("/v1/hr/questionnaires/schedules/{id}", scheduleRoute(a.getSchedule))
	r.With(authz.RequireDuty(dutyEssQuestionnaire)).Post("/v1/hr/questionnaires/schedules/{id}/answers", scheduleRoute(a.submitAnswers))
	r.With(authz.RequireDuty(dutyQuestionnaireAdmin)).Get("/v1/hr/questionnaires/schedules/{id}/results", scheduleRoute(a.scheduleResults))
	a.registerSelfService(r)
	a.registerPerformance(r)
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
		legacyOpens, openFailures := a.store.PIIOpenCounts()
		counter("hr_pii_legacy_opens_total", "P3 values that opened only with the legacy HR_PII_KEY; nonzero means hr reseal-pii has not finished.", legacyOpens)
		counter("hr_pii_open_failures_total", "P3 values that opened under neither fieldcrypt nor the legacy key (bad rotation or missing key).", openFailures)
		counter("hr_as_of_queries_total", "Worker as-of queries answered.", a.asOfQueries.Load())
		counter("hr_absence_plans_created_total", "Absence plans created; idempotent replays are not counted.", a.plansCreated.Load())
		counter("hr_absence_accrual_runs_total", "Accrual runs executed; replays of a stored (plan, period) are not counted.", a.accrualRuns.Load())
		counter("hr_absence_requests_created_total", "Absence requests submitted; idempotent replays are not counted.", a.requestsCreated.Load())
		counter("hr_absence_decisions_total", "Absence decisions that changed a request's status.", a.decisions.Load())
		counter("hr_absence_year_closes_total", "Year-end and carry-over expiry calls that closed at least one worker.", a.yearCloses.Load())
		counter("hr_benefit_plans_created_total", "Benefit plans created; idempotent replays are not counted.", a.benefitPlans.Load())
		counter("hr_benefit_simulations_total", "Whole-workforce eligibility simulations run.", a.simulations.Load())
		counter("hr_benefit_enrollments_total", "Benefit enrollments recorded; idempotent replays are not counted.", a.enrollments.Load())
		counter("hr_benefit_enrollments_ended_total", "Benefit enrollments ended; repeated ends are not counted.", a.enrollmentsEnded.Load())
		counter("hr_skills_created_total", "Catalog skills created; idempotent replays are not counted.", a.skillsCreated.Load())
		counter("hr_worker_skill_ratings_total", "Worker skill ratings that changed a stored rating or date.", a.skillRatings.Load())
		counter("hr_certifications_issued_total", "Certifications issued; idempotent replays are not counted.", a.certsIssued.Load())
		counter("hr_skill_searches_total", "Skill searches answered.", a.skillSearches.Load())
		counter("hr_courses_created_total", "Catalog courses created; idempotent replays are not counted.", a.coursesCreated.Load())
		counter("hr_course_sessions_created_total", "Course sessions scheduled; idempotent replays are not counted.", a.sessionsCreated.Load())
		counter("hr_course_registrations_total", "Course registrations placed (confirmed, waitlisted or requested); repeats are not counted.", a.registrations.Load())
		counter("hr_course_cancellations_total", "Course registrations canceled; repeated cancels are not counted.", a.cancellations.Load())
		counter("hr_course_passes_total", "Completions newly recorded with a PASS result.", a.passes.Load())
		a.personnel.write(counter)
		counter("hr_incidents_reported_total", "Incidents reported; idempotent replays are not counted.", a.incidentsReported.Load())
		counter("hr_incident_transitions_total", "Incident status changes (review, close).", a.incidentTransitions.Load())
		counter("hr_osha_extracts_total", "OSHA extracts produced.", a.oshaExtracts.Load())
		counter("hr_loaned_items_issued_total", "Loaned items issued; replays are not counted.", a.loansIssued.Load())
		counter("hr_loaned_items_closed_total", "Loaned items returned or recorded lost.", a.loansClosed.Load())
		counter("hr_checklists_started_total", "Checklists instantiated; repeats are not counted.", a.checklistsStarted.Load())
		counter("hr_checklists_completed_total", "Checklists completed.", a.checklistsCompleted.Load())
		counter("hr_worker_tasks_finished_total", "Worker tasks marked done or skipped.", a.tasksFinished.Load())
		counter("hr_questionnaire_answers_total", "Questionnaire answer sessions recorded.", a.questionnaireAnswers.Load())
		counter("hr_goals_created_total", "Goals created; idempotent replays are not counted.", a.goalsCreated.Load())
		counter("hr_reviews_opened_total", "Reviews opened; repeats for a period are not counted.", a.reviewsOpened.Load())
		counter("hr_reviews_self_submitted_total", "Self-assessments newly submitted.", a.reviewsSelfSubmitted.Load())
		counter("hr_reviews_signed_off_total", "Reviews newly signed off by a manager.", a.reviewsSignedOff.Load())
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
