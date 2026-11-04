// Package api exposes the shop floor HTTP surface: workers, batch registrations
// from the kiosk (client UUIDs), pay calculation, approval and the review lists
// the supervisor approval screen reads, plus machines, edge machine-event
// ingest, downtimes, andon, OEE and payroll export (floor.go), and process
// messages with their per-destination deliveries (process.go), kiosk login
// (kiosk.go), and the job dispatch list with feedback and checks (job.go).
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

// Duties (PRD 4.25.17).
const (
	DutyRegister  = "shopfloor.time.register"
	DutyCalculate = "shopfloor.pay.calculate"
	DutyApprove   = "shopfloor.pay.approve"
	DutyView      = "shopfloor.view"
	DutySetup     = "shopfloor.setup.maintain"
	DutyExport    = "shopfloor.pay.export"
	// DutyIngest is held by the edge gateway's service identity, not by people.
	DutyIngest = "shopfloor.machine.ingest"
	// DutyKioskLogin is the PRD duty "Kiosk login" (sessions:badge), held by operators.
	DutyKioskLogin = "shopfloor.kiosk.login"
	// DutyOEERollup is held by the platform scheduler's service identity that runs
	// the OEE rollup job; no person role should hold it.
	DutyOEERollup = "shopfloor.oee.rollup"
	// DutyJobSchedule is held by the service identity of the w23 schedule that writes
	// the dispatch projection; no person role should hold it.
	DutyJobSchedule = "shopfloor.job.schedule"
	// DutyCheckOverride lets a shop floor supervisor finish a job over a failed
	// mandatory quality check with a reason (FR-25.5, SF-0005).
	DutyCheckOverride = "shopfloor.check.override"
)

const (
	readyTimeout      = 500 * time.Millisecond
	maxBatch          = 500
	maxRangeDays      = 28
	maxListRangeDays  = 31
	maxBody           = 4 << 20
	maxRegistrationHr = 24
	dateLayout        = "2006-01-02"
	validationDetail  = "request failed validation"
	defaultZone       = "UTC"
	idempotencyHeader = "Idempotency-Key"
)

// Store is the persistence the handlers need. The Postgres repository satisfies it.
type Store interface {
	Ping(ctx context.Context) error
	LookupReplay(ctx context.Context, idem store.Idem) (*store.Replay, error)
	CreateWorker(ctx context.Context, in store.Worker, idem store.Idem) (store.Worker, error)
	GetWorker(ctx context.Context, id string) (store.Worker, error)
	UpdateWorker(ctx context.Context, workerID string, version int, in store.WorkerInput, savedBy string, idem store.Idem) (store.Worker, error)
	DeactivateWorker(ctx context.Context, workerID string, version int, reason, savedBy string, idem store.Idem) (store.Worker, error)
	ReactivateWorker(ctx context.Context, workerID string, version int, savedBy string, idem store.Idem) (store.Worker, error)
	ListWorkers(ctx context.Context, activeOnly bool, limit int) ([]store.Worker, error)
	CreateRegistrations(ctx context.Context, items []store.Registration) ([]store.RegistrationResult, error)
	ListRegistrations(ctx context.Context, workerID string, start, end time.Time, limit int) ([]store.RegistrationView, error)
	ListBundleRegistrations(ctx context.Context, bundleID string, limit int) ([]store.RegistrationView, error)
	LoadPunches(ctx context.Context, workerID string, start, end time.Time) (store.PunchInput, error)
	LoadPaySetup(ctx context.Context, workerID string) (domain.PaySetup, error)
	CreateTimeProfile(ctx context.Context, in store.TimeProfileInput, createdBy string, idem store.Idem) (store.TimeProfile, error)
	UpdateTimeProfile(ctx context.Context, id string, version int, in store.TimeProfileInput, updatedBy string) (store.TimeProfile, error)
	GetTimeProfile(ctx context.Context, id string) (store.TimeProfile, error)
	ListTimeProfiles(ctx context.Context, activeOnly bool, limit int) ([]store.TimeProfile, error)
	CreatePayAgreement(ctx context.Context, in store.PayAgreementInput, createdBy string, idem store.Idem) (store.PayAgreement, error)
	UpdatePayAgreement(ctx context.Context, id string, version int, in store.PayAgreementInput, updatedBy string) (store.PayAgreement, error)
	GetPayAgreement(ctx context.Context, id string) (store.PayAgreement, error)
	ListPayAgreements(ctx context.Context, activeOnly bool, limit int) ([]store.PayAgreement, error)
	CreateAbsenceCode(ctx context.Context, in store.AbsenceCodeInput, createdBy string, idem store.Idem) (store.AbsenceCode, error)
	UpdateAbsenceCode(ctx context.Context, id string, version int, in store.AbsenceCodeInput, updatedBy string) (store.AbsenceCode, error)
	GetAbsenceCode(ctx context.Context, id string) (store.AbsenceCode, error)
	ListAbsenceCodes(ctx context.Context, activeOnly bool, limit int) ([]store.AbsenceCode, error)
	CreateEdgeNode(ctx context.Context, in store.EdgeNodeInput, createdBy string, idem store.Idem) (store.EdgeNode, error)
	UpdateEdgeNode(ctx context.Context, id string, version int, in store.EdgeNodeInput, updatedBy string) (store.EdgeNode, error)
	GetEdgeNode(ctx context.Context, id string) (store.EdgeNode, error)
	ListEdgeNodes(ctx context.Context, ring, status string, limit int) ([]store.EdgeNode, error)
	PublishEdgeConfig(ctx context.Context, edgeID string, document json.RawMessage, publishedBy string, idem store.Idem) (store.EdgeConfigVersion, error)
	ListEdgeConfigs(ctx context.Context, edgeID string, limit int) ([]store.EdgeConfigVersion, error)
	CurrentEdgeConfig(ctx context.Context, edgeID, subject string) (store.EdgeConfigVersion, error)
	Heartbeat(ctx context.Context, edgeID, subject string, in store.HeartbeatInput) (store.HeartbeatResult, error)
	AssignWorkerSetup(ctx context.Context, workerID string, version int, profileID, agreementID *string, assignedBy string, idem store.Idem) (store.Worker, error)
	AssignWorkerResource(ctx context.Context, workerID string, version int, resourceID *string, assignedBy string, idem store.Idem) (store.Worker, error)
	SavePay(ctx context.Context, calc store.PayCalculation, idem store.Idem, response any) error
	CorrectRegistration(ctx context.Context, id string, req domain.CorrectionRequest, correctedBy string, idem store.Idem) (store.Correction, error)
	ListCorrections(ctx context.Context, workerID string, from, to time.Time, limit int) ([]store.Correction, error)
	AutoClockOut(ctx context.Context, now time.Time, afterHours, limit int) ([]store.AutoClosed, error)
	ListAutoClockOuts(ctx context.Context, workerID string, start, end time.Time, limit int) ([]store.AutoClockOutReview, error)
	RevisePay(ctx context.Context, calc store.PayCalculation, reason, revisedBy string, idem store.Idem) (store.RevisionResult, error)
	ApprovePay(ctx context.Context, workerID, fromDate, toDate, approvedBy string, idem store.Idem) (store.ApproveResult, error)
	ListWorkerDays(ctx context.Context, f store.DayFilter) ([]store.WorkerDay, error)
	RegisterMachine(ctx context.Context, m store.Machine, idem store.Idem) (store.Machine, error)
	ListMachines(ctx context.Context, limit int) ([]store.Machine, error)
	IngestMachineEvents(ctx context.Context, now time.Time, items []store.MachineEventIn) (store.IngestResult, error)
	ListDowntimes(ctx context.Context, f store.DowntimeFilter) ([]store.Downtime, error)
	CodeDowntime(ctx context.Context, id, reason, category string, idem store.Idem) (store.Downtime, error)
	OpenAndon(ctx context.Context, siteID string) ([]store.Andon, error)
	SweepAndon(ctx context.Context, now time.Time) (store.AndonSweepResult, error)
	ClearAndon(ctx context.Context, id, reason, clearedBy string, now time.Time, idem store.Idem) (store.Andon, error)
	LoadOEEStreams(ctx context.Context, resourceIDs []string, from, to time.Time) (map[string]domain.OEEStream, error)
	ExportPay(ctx context.Context, fromDate, toDate, createdBy string, idem store.Idem) (store.PayExport, error)
	ListPayExports(ctx context.Context, limit int) ([]store.PayExport, error)
	GetPayExport(ctx context.Context, id string) (store.PayExport, error)
	IngestProcessMessage(ctx context.Context, in domain.NewMessage, cat domain.Category, now time.Time) (store.ProcessIngest, error)
	StoredProcessMessage(ctx context.Context, source, sourceMessageID string) (store.ProcessMessage, bool, error)
	ListProcessMessages(ctx context.Context, f store.ProcessFilter) ([]store.ProcessMessage, error)
	GetProcessMessage(ctx context.Context, id string) (store.ProcessMessage, error)
	ReportDelivery(ctx context.Context, messageID, destination, outcome string, errText *string, now time.Time, idem store.Idem) (store.ProcessMessage, error)
	ReprocessDelivery(ctx context.Context, messageID, destination, reason string, now time.Time, idem store.Idem) (store.ProcessMessage, error)
	DeleteProcessMessage(ctx context.Context, messageID, reason string, now time.Time, idem store.Idem) (store.ProcessMessage, error)
	ClaimRetries(ctx context.Context, now time.Time, limit int) ([]store.RetryClaim, error)
	SaveOEEBuckets(ctx context.Context, r store.OEERollup) (store.OEERollupResult, error)
	BadgeLogin(ctx context.Context, badge string) (store.KioskSession, error)
	ClockedIn(ctx context.Context, limit int) ([]store.ClockedWorker, error)
	UpsertJob(ctx context.Context, j store.Job) (store.Job, bool, error)
	GetJob(ctx context.Context, id string) (store.Job, error)
	ListJobs(ctx context.Context, f store.JobFilter) ([]store.Job, error)
	RecordFeedback(ctx context.Context, jobID string, in store.FeedbackInput) (store.FeedbackResult, error)
	ReopenJob(ctx context.Context, jobID string, version int, reason, reopenedBy string, idem store.Idem) (store.Job, error)
	ListJobReopenings(ctx context.Context, jobID string, limit int) ([]store.JobReopening, error)
	CreateDowntimeReason(ctx context.Context, in store.DowntimeReasonInput, createdBy string, idem store.Idem) (store.DowntimeReason, error)
	UpdateDowntimeReason(ctx context.Context, id string, version int, in store.DowntimeReasonInput, updatedBy string) (store.DowntimeReason, error)
	GetDowntimeReason(ctx context.Context, id string) (store.DowntimeReason, error)
	ListDowntimeReasons(ctx context.Context, activeOnly bool, limit int) ([]store.DowntimeReason, error)
	CreateProcessCategory(ctx context.Context, in domain.CategoryInput, createdBy string, idem store.Idem) (store.ProcessCategory, error)
	UpdateProcessCategory(ctx context.Context, id string, version int, in domain.CategoryInput, updatedBy string) (store.ProcessCategory, error)
	GetProcessCategory(ctx context.Context, id string) (store.ProcessCategory, error)
	ListProcessCategories(ctx context.Context, activeOnly bool, limit int) ([]store.ProcessCategory, error)
	ResolveProcessCategory(ctx context.Context, code string) (domain.Category, error)
}

// API holds the handlers and the counters served on the admin port.
type API struct {
	store          Store
	now            func() time.Time
	registrations  atomic.Int64
	duplicates     atomic.Int64
	calculations   atomic.Int64
	approvedDays   atomic.Int64
	replays        atomic.Int64
	failed         atomic.Int64
	eventsAccepted atomic.Int64
	eventsDropped  atomic.Int64
	seqGaps        atomic.Int64
	andonsRaised   atomic.Int64
	exportedLines  atomic.Int64
	processStored  atomic.Int64
	processReplays atomic.Int64
	// processReprocessed counts manual reprocesses of destinations in error.
	processReprocessed atomic.Int64
	// processRetried counts destinations the retry sweep published again.
	processRetried atomic.Int64
	// autoClockedOut counts clock-ins the auto clock-out sweep closed.
	autoClockedOut atomic.Int64
	// processDeleted counts process messages deleted with a reason.
	processDeleted atomic.Int64
	// oeeChanged and oeeRemoved count stored OEE buckets the rollup wrote or removed.
	oeeChanged atomic.Int64
	oeeRemoved atomic.Int64
	// badgeRejected counts kiosk badge logins refused with SF-0001.
	badgeRejected atomic.Int64
	// feedbackStored and feedbackReplays count job feedback stored and replayed by
	// feedbackId; finishBlocked counts finishes refused by a mandatory check (SF-0005).
	feedbackStored  atomic.Int64
	feedbackReplays atomic.Int64
	finishBlocked   atomic.Int64
	// jobsReopened counts finished jobs a supervisor reopened (FR-25.3).
	jobsReopened atomic.Int64
	// corrections counts registration corrections; revisedDays approved days revised with reversal.
	corrections atomic.Int64
	revisedDays atomic.Int64
}

// New returns the API over s.
func New(s Store) *API { return &API{store: s, now: time.Now} }

type registrationItem struct {
	ID            string           `json:"id"`
	WorkerID      string           `json:"workerId"`
	Kind          string           `json:"kind"`
	JobID         *string          `json:"jobId"`
	BundleID      *string          `json:"bundleId"`
	AbsenceCodeID *string          `json:"absenceCodeId"`
	QtyGood       *decimal.Decimal `json:"qtyGood"`
	QtyScrap      *decimal.Decimal `json:"qtyScrap"`
	ScrapReasonID *string          `json:"scrapReasonId"`
	StartedAt     time.Time        `json:"startedAt"`
	StoppedAt     *time.Time       `json:"stoppedAt"`
	DeviceID      *string          `json:"deviceId"`
	Offline       bool             `json:"offline"`
	Note          *string          `json:"note"`
}

type registrationBatch struct {
	Items []registrationItem `json:"items"`
	// DeviceSentAt is the kiosk's own clock when it sent the batch. The server
	// measures the device clock offset from it (BR-25.7).
	DeviceSentAt *time.Time `json:"deviceSentAt"`
}

type registrationResponse struct {
	Items []store.RegistrationResult `json:"items"`
	// ClockOffsetSeconds is server time minus device time, measured when the
	// batch carried deviceSentAt; ClockCorrected says it was applied (over 5 min).
	ClockOffsetSeconds *int64 `json:"clockOffsetSeconds,omitempty"`
	ClockCorrected     bool   `json:"clockCorrected"`
}

type payRangeRequest struct {
	WorkerID string `json:"workerId"`
	From     string `json:"from"`
	To       string `json:"to"`
	TimeZone string `json:"timeZone"`
}

type payLineView struct {
	Type       string          `json:"type"`
	Hours      decimal.Decimal `json:"hours"`
	Multiplier decimal.Decimal `json:"multiplier"`
	Amount     decimal.Decimal `json:"amount"`
	Currency   string          `json:"currency"`
}

type payDayView struct {
	WorkDate string          `json:"workDate"`
	Hours    decimal.Decimal `json:"hours"`
	Flags    []string        `json:"flags"`
	PayLines []payLineView   `json:"payLines"`
}

type payCalculateResponse struct {
	WorkerID string       `json:"workerId"`
	From     string       `json:"from"`
	To       string       `json:"to"`
	TimeZone string       `json:"timeZone"`
	Days     []payDayView `json:"days"`
}

type workerRequest struct {
	PartyID  string  `json:"partyId"`
	Name     string  `json:"name"`
	BadgeID  *string `json:"badgeId"`
	UserID   *string `json:"userId"`
	CostRate string  `json:"costRate"`
	Currency string  `json:"currency"`
}

type itemsResponse[T any] struct {
	Items []T `json:"items"`
}

// Register mounts the routes on r. The kernel router already serves /readyz;
// this overrides it with the store-backed check.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	a.registerAbsence(r)
	a.registerWorkerMaintenance(r)
	a.registerDowntimeReasons(r)
	a.registerProcessCategories(r)
	a.registerEdge(r)
	r.With(authz.RequireDuty(DutySetup)).Post("/v1/shopfloor/workers", a.createWorker)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/workers", a.listWorkers)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/workers/{id}", a.getWorker)
	r.With(authz.RequireDuty(DutyRegister)).Post("/v1/shopfloor/registrations", a.createRegistrations)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/registrations", a.listRegistrations)
	r.With(authz.RequireDuty(DutyCalculate)).Post("/v1/shopfloor/pay:calculate", a.calculatePay)
	r.With(authz.RequireDuty(DutyApprove)).Post("/v1/shopfloor/pay:approve", a.approvePay)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/worker-days", a.listWorkerDays)
	a.registerFloor(r)
	a.registerProcess(r)
	a.registerKiosk(r)
	a.registerJobs(r)
	a.registerCorrections(r)
	a.registerAutoClock(r)
	a.registerSetup(r)
}

// Metrics serves the counters in Prometheus text format on the admin port.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		counter := func(name, help string, v int64) {
			fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
		}
		counter("shopfloor_registrations_accepted_total", "Registrations stored from batches.", a.registrations.Load())
		counter("shopfloor_registrations_duplicate_total", "Registrations replayed by client UUID and ignored.", a.duplicates.Load())
		counter("shopfloor_pay_calculations_total", "Pay calculations persisted.", a.calculations.Load())
		counter("shopfloor_pay_days_approved_total", "Worker-days newly approved.", a.approvedDays.Load())
		counter("shopfloor_idempotent_replays_total", "Commands answered from a stored Idempotency-Key outcome.", a.replays.Load())
		counter("shopfloor_machine_events_accepted_total", "Machine events stored from edge batches.", a.eventsAccepted.Load())
		counter("shopfloor_machine_events_dropped_total", "Machine events dropped as duplicate, regressed or older than 7 days (BR-25.4).", a.eventsDropped.Load())
		counter("shopfloor_machine_seq_gaps_total", "Missing machine sequence runs reported to the edge (SF-0008).", a.seqGaps.Load())
		counter("shopfloor_andons_raised_total", "Andon alerts raised from downtimes and scrap rates (machine ingest and the andon sweep).", a.andonsRaised.Load())
		counter("shopfloor_pay_export_lines_total", "Approved pay lines written to payroll export batches.", a.exportedLines.Load())
		counter("shopfloor_process_messages_stored_total", "Process messages stored (FR-25.17).", a.processStored.Load())
		counter("shopfloor_process_message_replays_total", "Process messages replayed by (source, sourceMessageId) and not stored again.", a.processReplays.Load())
		counter("shopfloor_process_deliveries_reprocessed_total", "Process message destinations in error sent again by a supervisor.", a.processReprocessed.Load())
		counter("shopfloor_auto_clock_outs_total", "Clock-ins closed by the auto clock-out sweep.", a.autoClockedOut.Load())
		counter("shopfloor_process_deliveries_retried_total", "Process message destinations published again by the retry sweep.", a.processRetried.Load())
		counter("shopfloor_process_messages_deleted_total", "Process messages deleted with a reason by a supervisor.", a.processDeleted.Load())
		counter("shopfloor_oee_buckets_changed_total", "Stored OEE buckets the rollup inserted or changed (FR-25.10).", a.oeeChanged.Load())
		counter("shopfloor_oee_buckets_removed_total", "Stored OEE buckets the rollup removed because their planned time is gone.", a.oeeRemoved.Load())
		counter("shopfloor_kiosk_badge_rejected_total", "Kiosk badge logins refused as not recognized (SF-0001).", a.badgeRejected.Load())
		counter("shopfloor_jobs_reopened_total", "Finished jobs a supervisor reopened (FR-25.3).", a.jobsReopened.Load())
		counter("shopfloor_job_feedback_stored_total", "Job feedback postings stored (FR-25.4).", a.feedbackStored.Load())
		counter("shopfloor_job_feedback_replays_total", "Job feedback replayed by feedbackId and not stored again.", a.feedbackReplays.Load())
		counter("shopfloor_job_finish_blocked_total", "Job finishes refused because a mandatory check has no passing result (SF-0005).", a.finishBlocked.Load())
		counter("shopfloor_registration_corrections_total", "Registrations corrected by a supervisor with a reason (FR-25.8).", a.corrections.Load())
		counter("shopfloor_pay_days_revised_total", "Approved worker-days revised with reversal lines (AC-25.8).", a.revisedDays.Load())
		counter("shopfloor_failed_total", "Requests that failed in the store.", a.failed.Load())
	})
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// fail logs a store failure with the correlation id and writes the problem.
func (a *API) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status >= http.StatusInternalServerError {
		a.failed.Add(1)
		slog.Error(op+" failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
	}
	httpx.Fail(w, r, err)
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	return true
}

func validationFailed(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

// readIdem reads the body once (so it can still be decoded), requires an
// Idempotency-Key and fingerprints method, path and body.
func readIdem(w http.ResponseWriter, r *http.Request) (store.Idem, bool) {
	key := r.Header.Get(idempotencyHeader)
	if key == "" || len(key) > 200 {
		validationFailed(w, r, map[string]string{idempotencyHeader: "header is required (1 to 200 characters)"})
		return store.Idem{}, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request body could not be read"))
		return store.Idem{}, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	sum := sha256.Sum256([]byte(r.Method + " " + r.URL.Path + "\n" + string(body)))
	return store.Idem{Key: key, Fingerprint: hex.EncodeToString(sum[:])}, true
}

// replayed answers from a stored outcome when the key was used before. It
// returns true when the response has been written.
func (a *API) replayed(w http.ResponseWriter, r *http.Request, idem store.Idem) bool {
	rep, err := a.store.LookupReplay(r.Context(), idem)
	if err != nil {
		a.fail(w, r, "idempotency lookup", err)
		return true
	}
	if rep == nil {
		return false
	}
	a.replays.Add(1)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Idempotent-Replayed", "true")
	w.WriteHeader(rep.Status)
	_, _ = w.Write(rep.Body)
	return true
}

var currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)

func (a *API) createWorker(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req workerRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.PartyID) {
		fields["partyId"] = "must be a UUID"
	}
	name := strings.TrimSpace(req.Name)
	if n := len([]rune(name)); n < 1 || n > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if req.BadgeID != nil && (len(*req.BadgeID) < 1 || len(*req.BadgeID) > 64) {
		fields["badgeId"] = "must be 1 to 64 characters"
	}
	if req.UserID != nil && (len(*req.UserID) < 1 || len(*req.UserID) > 200) {
		fields["userId"] = "must be 1 to 200 characters"
	}
	rate, err := decimal.NewFromString(req.CostRate)
	if err != nil || rate.IsNegative() {
		fields["costRate"] = "must be a non-negative decimal string"
	}
	if !currencyRe.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.CreateWorker(r.Context(), store.Worker{PartyID: req.PartyID, Name: name, BadgeID: req.BadgeID, UserID: req.UserID,
		CostRate: rate, Currency: req.Currency}, idem)
	if err != nil {
		a.fail(w, r, "create worker", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// limitParam reads ?limit= with a default and an upper bound.
func limitParam(w http.ResponseWriter, r *http.Request, def, max int) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return def, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > max {
		validationFailed(w, r, map[string]string{"limit": fmt.Sprintf("must be 1 to %d", max)})
		return 0, false
	}
	return n, true
}

func (a *API) listWorkers(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := limitParam(w, r, 200, 1000)
	if !ok {
		return
	}
	activeOnly := false
	switch v := r.URL.Query().Get("active"); v {
	case "", "false":
	case "true":
		activeOnly = true
	default:
		validationFailed(w, r, map[string]string{"active": "must be true or false"})
		return
	}
	items, err := a.store.ListWorkers(r.Context(), activeOnly, limit)
	if err != nil {
		a.fail(w, r, "list workers", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.Worker]{Items: items})
}

func (a *API) getWorker(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeWorkerNotFound, "worker not found"))
		return
	}
	out, err := a.store.GetWorker(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get worker", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

var kinds = map[string]bool{"clock-in": true, "clock-out": true, "job-start": true, "job-stop": true, "absence": true, "indirect": true}

// validateRegistration returns field errors keyed by the item's JSON field name.
func validateRegistration(it registrationItem) map[string]string {
	fields := map[string]string{}
	if !ids.Valid(it.ID) {
		fields["id"] = "must be a UUID"
	}
	if !ids.Valid(it.WorkerID) {
		fields["workerId"] = "must be a UUID"
	}
	if !kinds[it.Kind] {
		fields["kind"] = "must be one of clock-in, clock-out, job-start, job-stop, absence, indirect"
	}
	if it.StartedAt.IsZero() {
		fields["startedAt"] = "is required"
	}
	if it.StoppedAt != nil {
		switch {
		case it.StoppedAt.Before(it.StartedAt):
			fields["stoppedAt"] = "must not be before startedAt"
		case it.StoppedAt.Sub(it.StartedAt) > maxRegistrationHr*time.Hour:
			fields["stoppedAt"] = "a registration cannot exceed 24 hours"
		}
	}
	for name, v := range map[string]*string{"jobId": it.JobID, "bundleId": it.BundleID, "absenceCodeId": it.AbsenceCodeID} {
		if v != nil && !ids.Valid(*v) {
			fields[name] = "must be a UUID"
		}
	}
	// FR-25.2: an absence names its code and covers a closed interval, so its hours are known.
	switch {
	case it.Kind == domain.KindAbsence && it.AbsenceCodeID == nil:
		fields["absenceCodeId"] = "is required for absence registrations"
	case it.Kind != domain.KindAbsence && it.AbsenceCodeID != nil:
		fields["absenceCodeId"] = "only absence registrations carry an absence code"
	}
	if it.Kind == domain.KindAbsence && (it.StoppedAt == nil || !it.StoppedAt.After(it.StartedAt)) {
		if _, bad := fields["stoppedAt"]; !bad {
			fields["stoppedAt"] = "is required for absence registrations and must be after startedAt"
		}
	}
	if jobKind(it.Kind) && it.JobID == nil {
		fields["jobId"] = "is required for job registrations"
	}
	if it.BundleID != nil && !jobKind(it.Kind) {
		fields["bundleId"] = "only job registrations can be bundled"
	}
	for name, v := range map[string]*decimal.Decimal{"qtyGood": it.QtyGood, "qtyScrap": it.QtyScrap} {
		if v != nil && v.IsNegative() {
			fields[name] = "must not be negative"
		}
	}
	// FR-25.4: scrap is reported with a reason code.
	if it.QtyScrap != nil && it.QtyScrap.IsPositive() && (it.ScrapReasonID == nil || *it.ScrapReasonID == "") {
		fields["scrapReasonId"] = "is required when qtyScrap is above zero"
	}
	if it.Note != nil && len([]rune(*it.Note)) > 500 {
		fields["note"] = "must be at most 500 characters"
	}
	return fields
}

func jobKind(kind string) bool { return kind == domain.KindJobStart || kind == "job-stop" }

// splitBundles applies BR-25.1 to the batch: the closed job registrations that
// share a bundleId must belong to one worker, cover the same interval and name
// each job once, and the interval's hours are split across them by good quantity
// (the site setting "Bundle split basis" defaults to quantity and is not yet
// configurable). Each item's hours become its share, so Σ hours of the bundle is
// the time worked once, not once per job. Problems are added to fields.
func splitBundles(items []registrationItem, regs []store.Registration, fields map[string]string) {
	groups := map[string][]int{}
	var order []string
	for i, it := range items {
		if it.BundleID == nil || it.StoppedAt == nil || !jobKind(it.Kind) {
			continue
		}
		if _, ok := groups[*it.BundleID]; !ok {
			order = append(order, *it.BundleID)
		}
		groups[*it.BundleID] = append(groups[*it.BundleID], i)
	}
	for _, bundle := range order {
		idx := groups[bundle]
		first := items[idx[0]]
		jobs := map[string]bool{}
		members := make([]domain.BundleMember, 0, len(idx))
		valid := true
		for _, i := range idx {
			it := items[i]
			key := fmt.Sprintf("items[%d].bundleId", i)
			switch {
			case it.JobID == nil:
				valid = false // reported as items[i].jobId
				continue
			case it.WorkerID != first.WorkerID:
				fields[key] = "a bundle belongs to one worker"
			case !it.StartedAt.Equal(first.StartedAt) || !it.StoppedAt.Equal(*first.StoppedAt):
				fields[key] = "the closed jobs of a bundle share startedAt and stoppedAt"
			case jobs[*it.JobID]:
				fields[key] = "a job appears twice in the bundle"
			default:
				jobs[*it.JobID] = true
				members = append(members, domain.BundleMember{JobID: *it.JobID, QtyGood: regs[i].QtyGood})
				continue
			}
			valid = false
		}
		if !valid {
			continue
		}
		shares, err := domain.SplitBundleHours(*regs[idx[0]].Hours, members, domain.SplitByQuantity)
		if err != nil {
			fields[fmt.Sprintf("items[%d].bundleId", idx[0])] = err.Error()
			continue
		}
		for k, i := range idx {
			share := shares[k]
			regs[i].Hours = &share
		}
	}
}

func (it registrationItem) toStore() store.Registration {
	r := store.Registration{ID: it.ID, WorkerID: it.WorkerID, Kind: it.Kind, JobID: it.JobID, BundleID: it.BundleID, AbsenceCodeID: it.AbsenceCodeID,
		ScrapReasonID: it.ScrapReasonID, StartedAt: it.StartedAt, StoppedAt: it.StoppedAt, DeviceID: it.DeviceID,
		Offline: it.Offline, Note: it.Note}
	if it.QtyGood != nil {
		r.QtyGood = *it.QtyGood
	}
	if it.QtyScrap != nil {
		r.QtyScrap = *it.QtyScrap
	}
	if it.StoppedAt != nil {
		h := domain.ElapsedHours(it.StartedAt, *it.StoppedAt)
		r.Hours = &h
	}
	return r
}

func (a *API) createRegistrations(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req registrationBatch
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if n := len(req.Items); n < 1 || n > maxBatch {
		validationFailed(w, r, map[string]string{"items": fmt.Sprintf("must hold 1 to %d registrations", maxBatch)})
		return
	}
	fields := map[string]string{}
	// BR-25.7: a device clock more than 5 minutes off is corrected by the offset
	// measured now, and the offset is recorded on every registration it moved.
	var offsetSeconds *int64
	var correction time.Duration
	if req.DeviceSentAt != nil {
		if req.DeviceSentAt.IsZero() {
			fields["deviceSentAt"] = "must be a date-time"
		} else {
			offset := domain.ClockOffset(*req.DeviceSentAt, a.now()).Round(time.Second)
			secs := int64(offset / time.Second)
			offsetSeconds = &secs
			if domain.SkewExceeded(offset) {
				correction = offset
			}
		}
	}
	seen := make(map[string]int, len(req.Items))
	items := make([]store.Registration, 0, len(req.Items))
	for i := range req.Items {
		it := &req.Items[i]
		if correction != 0 && !it.StartedAt.IsZero() {
			it.StartedAt = domain.CorrectedClientTime(it.StartedAt, correction)
			if it.StoppedAt != nil {
				stopped := domain.CorrectedClientTime(*it.StoppedAt, correction)
				it.StoppedAt = &stopped
			}
		}
		for field, rule := range validateRegistration(*it) {
			fields[fmt.Sprintf("items[%d].%s", i, field)] = rule
		}
		if j, dup := seen[it.ID]; dup {
			fields[fmt.Sprintf("items[%d].id", i)] = fmt.Sprintf("repeats items[%d].id", j)
		}
		seen[it.ID] = i
		reg := it.toStore()
		if correction != 0 {
			reg.ClockOffsetSeconds = offsetSeconds
		}
		items = append(items, reg)
	}
	splitBundles(req.Items, items, fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	results, err := a.store.CreateRegistrations(r.Context(), items)
	if err != nil {
		a.fail(w, r, "create registrations", err)
		return
	}
	for _, res := range results {
		if res.Status == store.StatusDuplicate {
			a.duplicates.Add(1)
		} else {
			a.registrations.Add(1)
		}
	}
	httpx.JSON(w, http.StatusOK, registrationResponse{Items: results, ClockOffsetSeconds: offsetSeconds, ClockCorrected: correction != 0})
}

// rangeWindow is a validated worker and date range with the local instants that bound it.
type rangeWindow struct {
	workerID, fromDate, toDate, zone string
	loc                              *time.Location
	from, to                         time.Time // local midnights of the first and last day
	start, end                       time.Time // [start, end) covers the range
}

// parseRange validates a date range of at most maxDays days. requireWorker
// makes workerId mandatory; otherwise an empty workerId selects every worker.
func parseRange(req payRangeRequest, maxDays int, requireWorker bool) (rangeWindow, map[string]string) {
	fields := map[string]string{}
	if (requireWorker || req.WorkerID != "") && !ids.Valid(req.WorkerID) {
		fields["workerId"] = "must be a UUID"
	}
	from, errFrom := time.Parse(dateLayout, req.From)
	to, errTo := time.Parse(dateLayout, req.To)
	if errFrom != nil {
		fields["from"] = "must be a date YYYY-MM-DD"
	}
	if errTo != nil {
		fields["to"] = "must be a date YYYY-MM-DD"
	}
	if errFrom == nil && errTo == nil {
		switch {
		case to.Before(from):
			fields["to"] = "must not be before from"
		case to.Sub(from) > time.Duration(maxDays-1)*24*time.Hour:
			fields["to"] = fmt.Sprintf("range must be at most %d days", maxDays)
		}
	}
	zone := req.TimeZone
	if zone == "" {
		zone = defaultZone
	}
	loc, err := time.LoadLocation(zone)
	if err != nil || zone == "Local" {
		fields["timeZone"] = "must be an IANA time zone name"
	}
	if len(fields) > 0 {
		return rangeWindow{}, fields
	}
	win := rangeWindow{workerID: req.WorkerID, fromDate: from.Format(dateLayout), toDate: to.Format(dateLayout), zone: zone, loc: loc,
		from: time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc),
		to:   time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc),
	}
	win.start, win.end = win.from, win.to.AddDate(0, 0, 1)
	return win, nil
}

func decodeRange(w http.ResponseWriter, r *http.Request) (rangeWindow, bool) {
	var req payRangeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return rangeWindow{}, false
	}
	win, fields := parseRange(req, maxRangeDays, true)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return rangeWindow{}, false
	}
	return win, true
}

func queryRange(w http.ResponseWriter, r *http.Request, requireWorker bool) (rangeWindow, bool) {
	q := r.URL.Query()
	win, fields := parseRange(payRangeRequest{WorkerID: q.Get("workerId"), From: q.Get("from"), To: q.Get("to"), TimeZone: q.Get("timeZone")},
		maxListRangeDays, requireWorker)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return rangeWindow{}, false
	}
	return win, true
}

func (a *API) listRegistrations(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	if bundleID := r.URL.Query().Get("bundleId"); bundleID != "" {
		a.listBundle(w, r, bundleID)
		return
	}
	win, ok := queryRange(w, r, true)
	if !ok {
		return
	}
	limit, ok := limitParam(w, r, 500, 1000)
	if !ok {
		return
	}
	items, err := a.store.ListRegistrations(r.Context(), win.workerID, win.start, win.end, limit)
	if err != nil {
		a.fail(w, r, "list registrations", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.RegistrationView]{Items: items})
}

// listBundle returns every registration of one bundle (BR-25.1), oldest first.
func (a *API) listBundle(w http.ResponseWriter, r *http.Request, bundleID string) {
	if !ids.Valid(bundleID) {
		validationFailed(w, r, map[string]string{"bundleId": "must be a UUID"})
		return
	}
	limit, ok := limitParam(w, r, 500, 1000)
	if !ok {
		return
	}
	items, err := a.store.ListBundleRegistrations(r.Context(), bundleID, limit)
	if err != nil {
		a.fail(w, r, "list bundle registrations", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.RegistrationView]{Items: items})
}

var dayStatuses = map[string]bool{"": true, "calculated": true, "approved": true}

func (a *API) listWorkerDays(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	win, ok := queryRange(w, r, false)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if !dayStatuses[status] {
		validationFailed(w, r, map[string]string{"status": "must be calculated or approved"})
		return
	}
	limit, ok := limitParam(w, r, 500, 1000)
	if !ok {
		return
	}
	items, err := a.store.ListWorkerDays(r.Context(), store.DayFilter{WorkerID: win.workerID, Status: status, FromDate: win.fromDate, ToDate: win.toDate, Limit: limit})
	if err != nil {
		a.fail(w, r, "list worker-days", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.WorkerDay]{Items: items})
}

// calculatePay recomputes the worker-days of a range. Punches are read from the
// day before the start of the pay week that holds `from` (so the weekly
// threshold sees the earlier days of that week, BR-25.8, and a clock-out just
// after the range start pairs with its clock-in) to the day after `to` (so a
// night shift that ends after midnight closes). Only days in [from, to] are saved.
func (a *API) calculatePay(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	win, ok := decodeRange(w, r)
	if !ok {
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	ctx := r.Context()
	days, currency, err := a.recalculate(ctx, win)
	if err != nil {
		a.fail(w, r, "calculate pay", err)
		return
	}
	resp := payCalculateResponse{WorkerID: win.workerID, From: win.fromDate, To: win.toDate, TimeZone: win.zone, Days: dayViews(days, currency)}
	if err := a.store.SavePay(ctx, store.PayCalculation{WorkerID: win.workerID, FromDate: win.fromDate, ToDate: win.toDate, TimeZone: win.zone, Days: days}, idem, resp); err != nil {
		a.fail(w, r, "save pay", err)
		return
	}
	a.calculations.Add(1)
	httpx.JSON(w, http.StatusOK, resp)
}

// recalculate builds the worker-days of a window from the stored clock registrations.
// A rule violation (more than 24 hours in a day) comes back as a 422 problem.
func (a *API) recalculate(ctx context.Context, win rangeWindow) ([]domain.DayPay, string, error) {
	setup, err := a.store.LoadPaySetup(ctx, win.workerID)
	if err != nil {
		return nil, "", err
	}
	rules := setup.EffectiveRules()
	loadFrom := domain.WeekStartOf(win.from, rules.WeekStart).AddDate(0, 0, -1)
	in, err := a.store.LoadPunches(ctx, win.workerID, loadFrom, win.end.AddDate(0, 0, 1))
	if err != nil {
		return nil, "", err
	}
	profileFor := func(t time.Time) domain.Profile { return setup.Profiles.For(t, win.loc) }
	calc := domain.Calculation{ProfileFor: profileFor, Location: win.loc, Rules: rules, Rate: in.Rate, Currency: in.Currency}
	all, err := calc.Run(domain.AddAbsences(domain.BuildDaysFor(in.Punches, win.loc, profileFor), in.Absences, win.loc))
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) {
			return nil, "", httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail)
		}
		return nil, "", err
	}
	days := make([]domain.DayPay, 0, len(all))
	for _, d := range all {
		if !d.Date.Before(win.from) && !d.Date.After(win.to) {
			days = append(days, d)
		}
	}
	return days, in.Currency, nil
}

func dayViews(days []domain.DayPay, currency string) []payDayView {
	out := make([]payDayView, 0, len(days))
	for _, d := range days {
		v := payDayView{WorkDate: d.Date.Format(dateLayout), Hours: d.Hours, Flags: d.Flags, PayLines: []payLineView{}}
		if v.Flags == nil {
			v.Flags = []string{}
		}
		for _, l := range d.Lines {
			v.PayLines = append(v.PayLines, payLineView{Type: l.Type, Hours: l.Hours, Multiplier: l.Multiplier, Amount: l.Amount, Currency: currency})
		}
		out = append(out, v)
	}
	return out
}

func (a *API) approvePay(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	if p.UserID == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "approval needs an authenticated user"))
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	win, ok := decodeRange(w, r)
	if !ok {
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.ApprovePay(r.Context(), win.workerID, win.fromDate, win.toDate, p.UserID, idem)
	if err != nil {
		a.fail(w, r, "approve pay", err)
		return
	}
	for _, d := range out.Days {
		if d.NewlyApproved {
			a.approvedDays.Add(1)
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}
