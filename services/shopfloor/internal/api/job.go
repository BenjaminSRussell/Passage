package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

const (
	maxJobChecks      = 50
	maxCheckValue     = 64
	maxJobRef         = 64
	maxScrapReason    = 64
	maxOverrideReason = 500
	// maxQtyScale matches numeric(18,4): more decimals would be rounded silently.
	maxQtyScale = 4
)

// maxQty is the largest quantity numeric(18,4) holds.
var maxQty = decimal.New(1, 14)

type jobRequest struct {
	ID              string           `json:"id"`
	OrderRef        string           `json:"orderRef"`
	OperationRef    string           `json:"operationRef"`
	ResourceID      string           `json:"resourceId"`
	PlannedQuantity *decimal.Decimal `json:"plannedQuantity"`
	SequenceStart   time.Time        `json:"sequenceStart"`
	Checks          []domain.Check   `json:"checks"`
}

type overrideRequest struct {
	Reason string `json:"reason"`
}

type feedbackRequest struct {
	FeedbackID    string              `json:"feedbackId"`
	GoodQuantity  *decimal.Decimal    `json:"goodQuantity"`
	ScrapQuantity *decimal.Decimal    `json:"scrapQuantity"`
	ScrapReasonID *string             `json:"scrapReasonId"`
	Finished      bool                `json:"finished"`
	Checks        []domain.CheckInput `json:"checks"`
	Override      *overrideRequest    `json:"override"`
}

// registerJobs mounts the dispatch projection, list, detail and feedback endpoints
// (FR-25.3, FR-25.4, FR-25.5).
func (a *API) registerJobs(r chi.Router) {
	r.With(authz.RequireDuty(DutyJobSchedule)).Post("/api/v1/shopfloor/jobs", a.upsertJob)
	r.With(authz.RequireDuty(DutyRegister)).Get("/api/v1/shopfloor/jobs", a.listJobs)
	r.With(authz.RequireDuty(DutyRegister)).Get("/api/v1/shopfloor/jobs/{id}", a.getJob)
	r.With(authz.RequireDuty(DutyRegister)).Post("/api/v1/shopfloor/jobs/{id}:feedback", a.recordFeedback)
}

// upsertJob writes the plan of a job from the schedule (w23 feeds this endpoint). The
// job id is the key, so a resend is naturally idempotent: 201 when new, 200 after.
func (a *API) upsertJob(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req jobRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := validateJob(req); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	job, created, err := a.store.UpsertJob(r.Context(), store.Job{
		ID: req.ID, OrderRef: strings.TrimSpace(req.OrderRef), OperationRef: strings.TrimSpace(req.OperationRef), ResourceID: req.ResourceID,
		PlannedQty: *req.PlannedQuantity, SequenceStart: req.SequenceStart, Checks: req.Checks,
	})
	if err != nil {
		a.fail(w, r, "upsert job", err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, job)
}

func validateJob(req jobRequest) map[string]string {
	fields := map[string]string{}
	if !ids.Valid(req.ID) {
		fields["id"] = "must be a UUID"
	}
	if !ids.Valid(req.ResourceID) {
		fields["resourceId"] = "must be a UUID"
	}
	for k, v := range map[string]string{"orderRef": req.OrderRef, "operationRef": req.OperationRef} {
		if n := utf8.RuneCountInString(strings.TrimSpace(v)); n < 1 || n > maxJobRef {
			fields[k] = fmt.Sprintf("must be 1 to %d characters", maxJobRef)
		}
	}
	if msg := quantityProblem(req.PlannedQuantity, true); msg != "" {
		fields["plannedQuantity"] = msg
	}
	if req.SequenceStart.IsZero() {
		fields["sequenceStart"] = "is required"
	}
	if len(req.Checks) > maxJobChecks {
		fields["checks"] = fmt.Sprintf("at most %d checks per job", maxJobChecks)
	}
	seen := map[string]bool{}
	for i, c := range req.Checks {
		for k, v := range domain.ValidateCheck(c) {
			fields[fmt.Sprintf("checks[%d].%s", i, k)] = v
		}
		if seen[c.Code] {
			fields[fmt.Sprintf("checks[%d].code", i)] = "check codes must be unique on a job"
		}
		seen[c.Code] = true
	}
	return fields
}

// quantityProblem checks a quantity against numeric(18,4); it returns "" when fine.
func quantityProblem(q *decimal.Decimal, required bool) string {
	switch {
	case q == nil && required:
		return "is required"
	case q == nil:
		return ""
	case q.IsNegative():
		return "must not be negative"
	case q.Exponent() < -maxQtyScale && !q.Equal(q.Truncate(maxQtyScale)):
		return fmt.Sprintf("at most %d decimal places", maxQtyScale)
	case q.GreaterThanOrEqual(maxQty):
		return "is too large"
	}
	return ""
}

// listJobs is the dispatch list: open jobs per resource in start order (FR-25.3).
func (a *API) listJobs(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	if rid := q.Get("resourceId"); rid != "" && !ids.Valid(rid) {
		fields["resourceId"] = "must be a UUID"
	}
	switch q.Get("status") {
	case "", store.JobReleased, store.JobStarted, store.JobFinished:
	default:
		fields["status"] = "must be released, started or finished"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	limit, ok := limitParam(w, r, 500, 1000)
	if !ok {
		return
	}
	jobs, err := a.store.ListJobs(r.Context(), store.JobFilter{ResourceID: q.Get("resourceId"), Status: q.Get("status"), Limit: limit})
	if err != nil {
		a.fail(w, r, "list jobs", err)
		return
	}
	if jobs == nil {
		jobs = []store.Job{}
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.Job]{Items: jobs})
}

// getJob is the kiosk job detail: plan, reported quantities, checks and results.
func (a *API) getJob(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		validationFailed(w, r, map[string]string{"id": "must be a UUID"})
		return
	}
	job, err := a.store.GetJob(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get job", err)
		return
	}
	httpx.JSON(w, http.StatusOK, job)
}

// recordFeedback posts good and scrap quantities, the finished flag and check values
// for one job (FR-25.4, FR-25.5). The client feedbackId is the idempotency key (offline
// replay, FR-25.12): a replay returns 200 with duplicate true and changes nothing.
func (a *API) recordFeedback(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	jobID := chi.URLParam(r, "id")
	var req feedbackRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	fields := validateFeedback(jobID, req)
	var override *string
	if req.Override != nil && len(fields) == 0 {
		// FR-25.5: only a supervisor finishes over a failed mandatory check, and the
		// override names who did it.
		if !p.HasDuty(DutyCheckOverride) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "missing duty "+DutyCheckOverride))
			return
		}
		if p.UserID == "" {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "an override needs an authenticated user"))
			return
		}
		reason := strings.TrimSpace(req.Override.Reason)
		override = &reason
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	var scrapReason *string
	if req.ScrapReasonID != nil {
		v := strings.TrimSpace(*req.ScrapReasonID)
		scrapReason = &v
	}
	res, err := a.store.RecordFeedback(r.Context(), jobID, store.FeedbackInput{
		ID: req.FeedbackID, GoodQty: decimalOrZero(req.GoodQuantity), ScrapQty: decimalOrZero(req.ScrapQuantity), ScrapReasonID: scrapReason,
		Finished: req.Finished, Checks: req.Checks, OverrideReason: override, RecordedBy: p.UserID,
	})
	if err != nil {
		if p := (*httpx.Problem)(nil); errors.As(err, &p) && p.Code == store.CodeMandatoryCheck {
			a.finishBlocked.Add(1)
		}
		a.fail(w, r, "record job feedback", err)
		return
	}
	if res.Duplicate {
		a.feedbackReplays.Add(1)
		httpx.JSON(w, http.StatusOK, res)
		return
	}
	a.feedbackStored.Add(1)
	httpx.JSON(w, http.StatusCreated, res)
}

func validateFeedback(jobID string, req feedbackRequest) map[string]string {
	fields := map[string]string{}
	if !ids.Valid(jobID) {
		fields["id"] = "must be a UUID"
	}
	if !ids.Valid(req.FeedbackID) {
		fields["feedbackId"] = "must be a UUID"
	}
	if msg := quantityProblem(req.GoodQuantity, false); msg != "" {
		fields["goodQuantity"] = msg
	}
	if msg := quantityProblem(req.ScrapQuantity, false); msg != "" {
		fields["scrapQuantity"] = msg
	}
	scrap := decimalOrZero(req.ScrapQuantity)
	// FR-25.4: scrap is reported with a reason code, as on registrations.
	switch {
	case req.ScrapReasonID != nil && (utf8.RuneCountInString(strings.TrimSpace(*req.ScrapReasonID)) < 1 || utf8.RuneCountInString(*req.ScrapReasonID) > maxScrapReason):
		fields["scrapReasonId"] = fmt.Sprintf("must be 1 to %d characters", maxScrapReason)
	case scrap.IsPositive() && req.ScrapReasonID == nil:
		fields["scrapReasonId"] = "is required when scrapQuantity is above zero"
	}
	if len(req.Checks) > maxJobChecks {
		fields["checks"] = fmt.Sprintf("at most %d check values per feedback", maxJobChecks)
	}
	seen := map[string]bool{}
	for i, c := range req.Checks {
		if c.Code == "" {
			fields[fmt.Sprintf("checks[%d].code", i)] = "is required"
		} else if seen[c.Code] {
			fields[fmt.Sprintf("checks[%d].code", i)] = "is given twice"
		}
		seen[c.Code] = true
		if n := utf8.RuneCountInString(strings.TrimSpace(c.Value)); n < 1 || n > maxCheckValue {
			fields[fmt.Sprintf("checks[%d].value", i)] = fmt.Sprintf("must be 1 to %d characters", maxCheckValue)
		}
	}
	if req.Override != nil {
		switch n := utf8.RuneCountInString(strings.TrimSpace(req.Override.Reason)); {
		case !req.Finished:
			fields["override"] = "applies only when finished is true"
		case n < 1 || n > maxOverrideReason:
			fields["override.reason"] = fmt.Sprintf("must be 1 to %d characters", maxOverrideReason)
		}
	}
	if len(fields) == 0 && !req.Finished && len(req.Checks) == 0 && decimalOrZero(req.GoodQuantity).IsZero() && scrap.IsZero() {
		fields["goodQuantity"] = "report a quantity, a check value or finished"
	}
	return fields
}

func decimalOrZero(d *decimal.Decimal) decimal.Decimal {
	if d == nil {
		return decimal.Zero
	}
	return *d
}
