package api

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const (
	dutyAbsenceAdmin = "HcmAbsenceAdmin"
	dutyEssAbsence   = "EssAbsence"
	dutyMssApprove   = "MssApprove"

	absMaxPeriodDays = 366
	absMaxReason     = 500
)

var (
	absPlanCode    = regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{0,19}$`)
	absUnits       = []string{"HOURS", "DAYS"}
	absFrequencies = []string{"MONTHLY", "BIWEEKLY", "ANNUAL", "PER_HOUR_WORKED"}
	absAccrualDays = []string{"LAST_DAY", "FIRST_DAY", "PAYDATE"}
	absProrations  = []string{"DAYS_IN_PERIOD", "NONE"}
	absDecisions   = []string{domain.DecisionApprove, domain.DecisionReject}
	absMaxRate     = decimal.NewFromInt(10000)     // accrual_rate is numeric(8,4)
	absMaxQuantity = decimal.NewFromInt(100000000) // quantities are numeric(12,4)
)

// absencePlanRequest is the plan body. Omitted optional fields take the
// defaults in 8.42.13.
type absencePlanRequest struct {
	Code                   string  `json:"code"`
	Unit                   string  `json:"unit"`
	AccrualFrequency       *string `json:"accrualFrequency"`
	AccrualRate            string  `json:"accrualRate"`
	AccrualDay             *string `json:"accrualDay"`
	Proration              *string `json:"proration"`
	MaxBalance             string  `json:"maxBalance"`
	CarryOverMax           *string `json:"carryOverMax"`
	CarryOverExpiryMonths  *int    `json:"carryOverExpiryMonths"`
	PlanYearStart          *string `json:"planYearStart"`
	NegativeBalanceAllowed bool    `json:"negativeBalanceAllowed"`
	MinServiceDays         *int    `json:"minServiceDays"`
}

// createAbsencePlan is POST /v1/hr/absence/plans (HcmAbsenceAdmin).
func (a *API) createAbsencePlan(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req absencePlanRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if !absPlanCode.MatchString(code) {
		fields["code"] = "must be 1-20 characters: A-Z, 0-9 and hyphen"
	}
	if !contains(absUnits, req.Unit) {
		fields["unit"] = "must be HOURS or DAYS"
	}
	freq := absOrDefault(req.AccrualFrequency, "MONTHLY")
	if !contains(absFrequencies, freq) {
		fields["accrualFrequency"] = "must be MONTHLY, BIWEEKLY, ANNUAL or PER_HOUR_WORKED"
	}
	accDay := absOrDefault(req.AccrualDay, "LAST_DAY")
	if !contains(absAccrualDays, accDay) {
		fields["accrualDay"] = "must be LAST_DAY, FIRST_DAY or PAYDATE"
	}
	proration := absOrDefault(req.Proration, "DAYS_IN_PERIOD")
	if !contains(absProrations, proration) {
		fields["proration"] = "must be DAYS_IN_PERIOD or NONE"
	}
	rate, rateOK := absDecimal(req.AccrualRate, 4, absMaxRate, true)
	if !rateOK {
		fields["accrualRate"] = "must be a non-negative decimal below 10000 with at most 4 places"
	}
	maxBal, maxOK := absDecimal(req.MaxBalance, 4, absMaxQuantity, true)
	if !maxOK {
		fields["maxBalance"] = "must be a non-negative decimal with at most 4 places"
	}
	carryMax, carryOK := absDecimal(absOrDefault(req.CarryOverMax, "0"), 4, absMaxQuantity, true)
	if !carryOK {
		fields["carryOverMax"] = "must be a non-negative decimal with at most 4 places"
	}
	expiry := 3
	if req.CarryOverExpiryMonths != nil {
		expiry = *req.CarryOverExpiryMonths
	}
	if expiry < 0 || expiry > 36 {
		fields["carryOverExpiryMonths"] = "must be between 0 and 36"
	}
	pys := absOrDefault(req.PlanYearStart, "01-01")
	if _, _, ok := domain.ParsePlanYearStart(pys); !ok {
		fields["planYearStart"] = "must be MM-DD and a day every year has (not 02-29)"
	}
	minDays := 0
	if req.MinServiceDays != nil {
		minDays = *req.MinServiceDays
	}
	if minDays < 0 || minDays > 3650 {
		fields["minServiceDays"] = "must be between 0 and 3650"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}

	plan, inserted, err := a.store.CreateAbsencePlan(r.Context(), store.AbsencePlan{
		Code: code, Unit: req.Unit, AccrualFrequency: freq, AccrualRate: rate.String(), AccrualDay: accDay,
		Proration: proration, MaxBalance: maxBal.String(), CarryOverMax: carryMax.String(),
		CarryOverExpiryMonths: expiry, PlanYearStart: pys, NegativeBalanceAllowed: req.NegativeBalanceAllowed,
		MinServiceDays: minDays,
	}, r.Header.Get(idempotencyKey))
	if err != nil {
		logFailure(r, "create absence plan", err)
		httpx.Fail(w, r, err)
		return
	}
	if inserted {
		a.plansCreated.Add(1)
	}
	httpx.JSON(w, http.StatusCreated, plan)
}

// absenceRoute checks the tenant and the id shape of a plan or request route.
// A malformed id is a 404, the same answer an unknown id gets.
func absenceRoute(code, detail string, next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, code, detail))
			return
		}
		next(w, r, id)
	}
}

func planRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return absenceRoute(store.CodeAbsencePlanNotFound, "absence plan not found", next)
}

func absenceRequestRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return absenceRoute(store.CodeAbsenceRequestNotFound, "absence request not found", next)
}

// caller is the principal of a request that passed validTenant.
func caller(r *http.Request) tenant.Principal {
	p, _ := tenant.FromContext(r.Context())
	return p
}

// listAbsencePlans is GET /v1/hr/absence/plans (EssAbsence): every employee
// picks a plan when requesting absence.
func (a *API) listAbsencePlans(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, ok := pageParams(r)
	if !ok {
		badPage(w, r)
		return
	}
	page, err := a.store.ListAbsencePlans(r.Context(), after, limit)
	if err != nil {
		logFailure(r, "list absence plans", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.AbsencePlan]{Items: page.Items, NextCursor: cursor(page.NextAfter)})
}

// getAbsencePlan is GET /v1/hr/absence/plans/{id} (HcmAbsenceAdmin): the plan
// with its latest accrual periods, year ends and carry-over expiries.
func (a *API) getAbsencePlan(w http.ResponseWriter, r *http.Request, id string) {
	plan, err := a.store.GetAbsencePlan(r.Context(), id)
	if err != nil {
		logFailure(r, "get absence plan", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, plan)
}

// accrualRequest runs one plan for one whole accrual period.
type accrualRequest struct {
	PlanID      string `json:"planId"`
	PeriodStart string `json:"periodStart"`
	PeriodEnd   string `json:"periodEnd"`
}

// accrualRunResponse adds whether this call found every worker already accrued.
type accrualRunResponse struct {
	store.AccrualRun
	Replayed bool `json:"replayed"`
}

// runAccrual is POST /v1/hr/absence/accrue (HcmAbsenceAdmin). The PRD's key per
// (plan, period) is the natural key: each worker is accrued once per period,
// so a retried call credits nobody twice and needs no Idempotency-Key.
func (a *API) runAccrual(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req accrualRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.PlanID) {
		fields["planId"] = "must be a UUID"
	}
	from, fromOK := parseDate(req.PeriodStart)
	if !fromOK {
		fields["periodStart"] = "must be a YYYY-MM-DD date"
	}
	to, toOK := parseDate(req.PeriodEnd)
	if !toOK {
		fields["periodEnd"] = "must be a YYYY-MM-DD date"
	}
	if fromOK && toOK && (to.Before(from) || domain.InclusiveDays(from, to) > absMaxPeriodDays) {
		fields["periodEnd"] = "must be on or after periodStart and within 366 days"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}

	run, err := a.store.RunAccrual(r.Context(), req.PlanID, from, to)
	if err != nil {
		logFailure(r, "run absence accrual", err)
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if run.Credited > 0 {
		status = http.StatusCreated
		a.accrualRuns.Add(1)
	}
	httpx.JSON(w, status, accrualRunResponse{AccrualRun: run, Replayed: run.Credited == 0 && run.AlreadyAccrued > 0})
}

type planYearRequest struct {
	PlanYearStart string `json:"planYearStart"`
}

// closePlanYear serves the year-end and carry-over expiry routes: both take the
// first day of a plan year and are keyed per (plan, year, worker).
func (a *API) closePlanYear(op string, run func(ctx context.Context, planID string, yearStart time.Time) (store.CloseRun, error)) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, id string) {
		var req planYearRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		yearStart, ok := parseDate(req.PlanYearStart)
		if !ok {
			validationFailed(w, r, map[string]string{"planYearStart": "must be a YYYY-MM-DD date"})
			return
		}
		out, err := run(r.Context(), id, yearStart)
		if err != nil {
			logFailure(r, op, err)
			httpx.Fail(w, r, err)
			return
		}
		status := http.StatusOK
		if out.Workers > 0 {
			status = http.StatusCreated
			a.yearCloses.Add(1)
		}
		httpx.JSON(w, status, out)
	}
}

// absenceRequestBody is the request as an employee submits it (ESS).
type absenceRequestBody struct {
	WorkerID string `json:"workerId"`
	PlanID   string `json:"planId"`
	From     string `json:"from"`
	To       string `json:"to"`
	Quantity string `json:"quantity"`
	Reason   string `json:"reason"`
}

// absenceRequestResponse is the stored request plus any non-blocking warnings.
type absenceRequestResponse struct {
	store.AbsenceRequest
	Warnings []domain.RuleWarning `json:"warnings"`
}

// createAbsenceRequest is POST /v1/hr/absence/requests (EssAbsence). An
// employee requests for their own worker; HcmAbsenceAdmin may request for
// anyone.
func (a *API) createAbsenceRequest(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req absenceRequestBody
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.WorkerID) {
		fields["workerId"] = "must be a UUID"
	}
	if !ids.Valid(req.PlanID) {
		fields["planId"] = "must be a UUID"
	}
	from, fromOK := parseDate(req.From)
	if !fromOK {
		fields["from"] = "must be a YYYY-MM-DD date"
	}
	to, toOK := parseDate(req.To)
	if !toOK {
		fields["to"] = "must be a YYYY-MM-DD date"
	}
	if fromOK && toOK && (to.Before(from) || domain.InclusiveDays(from, to) > absMaxPeriodDays) {
		fields["to"] = "must be on or after from and within 366 days"
	}
	qty, qtyOK := absDecimal(req.Quantity, 2, absMaxQuantity, false)
	if !qtyOK {
		fields["quantity"] = "must be a positive decimal with at most 2 places"
	}
	reason := strings.TrimSpace(req.Reason)
	if len(reason) > absMaxReason {
		fields["reason"] = "must be at most 500 characters"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}

	ownOnly := !caller(r).HasDuty(dutyAbsenceAdmin)
	stored, warnings, inserted, err := a.store.CreateAbsenceRequest(r.Context(), store.RequestInput{
		WorkerID: req.WorkerID, PlanID: req.PlanID, From: from, To: to, Quantity: qty, Reason: reason,
	}, r.Header.Get(idempotencyKey), ownOnly)
	if err != nil {
		logFailure(r, "create absence request", err)
		httpx.Fail(w, r, err)
		return
	}
	if warnings == nil {
		warnings = []domain.RuleWarning{}
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
		a.requestsCreated.Add(1)
	}
	httpx.JSON(w, status, absenceRequestResponse{AbsenceRequest: stored, Warnings: warnings})
}

var absStatuses = []string{domain.RequestSubmitted, domain.RequestApproved, domain.RequestRejected, domain.RequestCanceled}

// listAbsenceRequests is GET /v1/hr/absence/requests (EssAbsence). An employee
// sees only their own requests; MssApprove and HcmAbsenceAdmin see the
// tenant's (team scoping by the position hierarchy is the OPA handoff).
func (a *API) listAbsenceRequests(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, ok := pageParams(r)
	if !ok {
		badPage(w, r)
		return
	}
	q := r.URL.Query()
	f := store.RequestFilter{Status: q.Get("status"), WorkerID: q.Get("workerId")}
	fields := map[string]string{}
	if f.Status != "" && !contains(absStatuses, f.Status) {
		fields["status"] = "must be SUBMITTED, APPROVED, REJECTED or CANCELED"
	}
	if f.WorkerID != "" && !ids.Valid(f.WorkerID) {
		fields["workerId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	p := caller(r)
	if !p.HasDuty(dutyMssApprove) && !p.HasDuty(dutyAbsenceAdmin) {
		if p.UserID == "" {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, store.CodeNotOwnAbsence, "you can only see your own absence"))
			return
		}
		f.OwnUserID = p.UserID
	} else if !p.HasDuty(dutyAbsenceAdmin) {
		a.listTeamAbsenceRequests(w, r, f, after, limit)
		return
	}
	page, err := a.store.ListAbsenceRequests(r.Context(), f, after, limit)
	if err != nil {
		logFailure(r, "list absence requests", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.AbsenceRequest]{Items: page.Items, NextCursor: cursor(page.NextAfter)})
}

type absenceDecisionRequest struct {
	Decision string `json:"decision"`
}

// decideAbsenceRequest is POST /v1/hr/absence/requests/{id}/decide (MssApprove).
// Replaying the decision the request already has returns it unchanged.
func (a *API) decideAbsenceRequest(w http.ResponseWriter, r *http.Request, id string) {
	var req absenceDecisionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !contains(absDecisions, req.Decision) {
		validationFailed(w, r, map[string]string{"decision": "must be APPROVE or REJECT"})
		return
	}
	if !a.allowTeamRequest(w, r, id) {
		return
	}
	stored, changed, err := a.store.DecideAbsenceRequest(r.Context(), id, req.Decision)
	if err != nil {
		logFailure(r, "decide absence request", err)
		httpx.Fail(w, r, err)
		return
	}
	if changed {
		a.decisions.Add(1)
	}
	httpx.JSON(w, http.StatusOK, stored)
}

// cancelAbsenceRequest is POST /v1/hr/absence/requests/{id}/cancel (EssAbsence):
// the employee withdraws their own SUBMITTED request; HcmAbsenceAdmin may
// withdraw anyone's.
func (a *API) cancelAbsenceRequest(w http.ResponseWriter, r *http.Request, id string) {
	stored, _, err := a.store.CancelAbsenceRequest(r.Context(), id, !caller(r).HasDuty(dutyAbsenceAdmin))
	if err != nil {
		logFailure(r, "cancel absence request", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, stored)
}

// absenceBalances is GET /v1/hr/workers/{id}/absence/balances?asOf= (EssAbsence).
// An employee reads their own; MssApprove and HcmAbsenceAdmin read anyone's.
func (a *API) absenceBalances(w http.ResponseWriter, r *http.Request, workerID string) {
	asOf := time.Now().UTC()
	if raw := r.URL.Query().Get("asOf"); raw != "" {
		d, ok := parseDate(raw)
		if !ok {
			validationFailed(w, r, map[string]string{"asOf": "must be a YYYY-MM-DD date"})
			return
		}
		asOf = d
	}
	asOf = time.Date(asOf.Year(), asOf.Month(), asOf.Day(), 0, 0, 0, 0, time.UTC)
	p := caller(r)
	if !a.allowTeamWorker(w, r, workerID, store.CodeWorkerNotFound, "worker not found") {
		return
	}
	ownOnly := !p.HasDuty(dutyMssApprove) && !p.HasDuty(dutyAbsenceAdmin)
	balances, err := a.store.Balances(r.Context(), workerID, asOf, ownOnly)
	if err != nil {
		logFailure(r, "read absence balances", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"workerId": workerID, "asOf": asOf.Format(dateLayout), "balances": balances})
}

// absDecimal reads a decimal string with at most scale places, below max.
// Signs are refused; zero is refused unless allowZero. Floats never reach here.
func absDecimal(s string, scale int32, max decimal.Decimal, allowZero bool) (decimal.Decimal, bool) {
	d, err := decimal.NewFromString(s)
	if err != nil || d.IsNegative() || d.GreaterThanOrEqual(max) {
		return decimal.Decimal{}, false
	}
	if !allowZero && d.IsZero() {
		return decimal.Decimal{}, false
	}
	if !d.Round(scale).Equal(d) {
		return decimal.Decimal{}, false
	}
	return d, true
}

// absOrDefault returns the value when present, otherwise the default.
func absOrDefault(v *string, def string) string {
	if v == nil {
		return def
	}
	return *v
}
