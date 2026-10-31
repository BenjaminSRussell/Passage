package api

import (
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const (
	dutyCompAdmin       = "HcmCompAdmin"
	dutyRecruitMaintain = "HcmRecruitMaintain"
)

var (
	pnCode     = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)
	pnLongCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,39}$`)
	pnCurrency = regexp.MustCompile(`^[A-Z]{3}$`)
	pnMaxMoney = decimal.New(1, 14)
	pnActions  = []string{hrevents.ActionHire, hrevents.ActionTransfer, hrevents.ActionTerminate, hrevents.ActionCompChange}
)

type personnelCounters struct {
	jobs, compPlans, actionsSubmitted, actionsApplied, actionsFailed atomic.Int64
	requisitions, candidates, offers, hires, purged                  atomic.Int64
	cycles, cycleApplies                                             atomic.Int64
}

func (c *personnelCounters) write(counter func(name, help string, v int64)) {
	counter("hr_jobs_created_total", "Jobs created; idempotent replays are not counted.", c.jobs.Load())
	counter("hr_compensation_plans_created_total", "Compensation plans created; idempotent replays are not counted.", c.compPlans.Load())
	counter("hr_personnel_actions_submitted_total", "Personnel actions submitted; idempotent replays are not counted.", c.actionsSubmitted.Load())
	counter("hr_personnel_actions_applied_total", "Pending personnel actions applied by the due sweep.", c.actionsApplied.Load())
	counter("hr_personnel_actions_failed_total", "Pending personnel actions the due sweep marked FAILED.", c.actionsFailed.Load())
	counter("hr_requisitions_created_total", "Requisitions created; idempotent replays are not counted.", c.requisitions.Load())
	counter("hr_candidates_created_total", "Candidates created; idempotent replays are not counted.", c.candidates.Load())
	counter("hr_offers_created_total", "Offers created; idempotent replays are not counted.", c.offers.Load())
	counter("hr_offer_hires_total", "Offers converted to a hire action.", c.hires.Load())
	counter("hr_candidates_erased_total", "Candidates whose personal data was erased by the retention purge.", c.purged.Load())
	counter("hr_compensation_cycles_created_total", "Compensation cycles created; idempotent replays are not counted.", c.cycles.Load())
	counter("hr_compensation_cycles_applied_total", "Compensation cycles applied.", c.cycleApplies.Load())
}

func (a *API) registerPersonnel(r chi.Router) {
	r.With(authz.RequireDuty(dutyCompAdmin)).Post("/v1/hr/jobs", a.createJob)
	r.With(authz.RequireDuty(dutyOrgInquire)).Get("/v1/hr/jobs", a.listJobs)
	r.With(authz.RequireDuty(dutyOrgInquire)).Get("/v1/hr/jobs/{id}", idRoute(store.CodeJobNotFound, "job not found", a.getJob))
	r.With(authz.RequireDuty(dutyCompAdmin)).Post("/v1/hr/compensation/plans", a.createCompPlan)
	r.With(authz.RequireDuty(dutyCompAdmin)).Get("/v1/hr/compensation/plans", a.listCompPlans)
	r.With(authz.RequireDuty(dutyCompAdmin)).Get("/v1/hr/workers/{id}/compensation", workerRoute(a.workerCompensation))
	r.With(authz.RequireDuty(dutyActionMaintain)).Post("/v1/hr/actions", a.submitAction)
	r.With(authz.RequireDuty(dutyActionMaintain)).Get("/v1/hr/actions", a.listActions)
	r.With(authz.RequireDuty(dutyActionMaintain)).Post("/v1/hr/actions/apply-due", a.applyDueActions)
	r.With(authz.RequireDuty(dutyActionMaintain)).Get("/v1/hr/actions/{id}", idRoute(store.CodeActionNotFound, "personnel action not found", a.getAction))
	r.With(authz.RequireDuty(dutyActionMaintain)).Post("/v1/hr/actions/{id}/withdraw", idRoute(store.CodeActionNotFound, "personnel action not found", a.withdrawAction))
	a.registerRecruitment(r)
	a.registerCompCycles(r)
}

func (a *API) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	logFailure(r, op, err)
	httpx.Fail(w, r, err)
}

func pnText(s string, max int) (string, bool) {
	v := strings.TrimSpace(s)
	return v, v != "" && len([]rune(v)) <= max
}

func pnOptText(s *string, max int) (*string, bool) {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil, true
	}
	v := strings.TrimSpace(*s)
	return &v, len([]rune(v)) <= max
}

func pnMoney(raw, currency string) (decimal.Decimal, bool) {
	d, err := decimal.NewFromString(strings.TrimSpace(raw))
	if err != nil || d.IsNegative() || !d.LessThan(pnMaxMoney) || !d.Equal(d.Round(money.MinorUnits(currency))) {
		return decimal.Zero, false
	}
	return d, true
}

type jobRequest struct {
	Code        string  `json:"code"`
	Title       string  `json:"title"`
	JobType     string  `json:"jobType"`
	JobFunction *string `json:"jobFunction"`
	PayCurrency *string `json:"payCurrency"`
	PayMin      *string `json:"payMin"`
	PayMax      *string `json:"payMax"`
}

func (a *API) createJob(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req jobRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	in := store.Job{Code: strings.ToUpper(strings.TrimSpace(req.Code)), JobType: strings.ToUpper(strings.TrimSpace(req.JobType))}
	if !pnCode.MatchString(in.Code) {
		fields["code"] = "must be 1 to 20 characters of A-Z, 0-9, _ or -"
	}
	var ok bool
	if in.Title, ok = pnText(req.Title, 120); !ok {
		fields["title"] = "is required, at most 120 characters"
	}
	if !pnLongCode.MatchString(in.JobType) {
		fields["jobType"] = "must be 1 to 40 characters of A-Z, 0-9, _ or -"
	}
	if in.JobFunction, ok = pnOptText(req.JobFunction, 60); !ok {
		fields["jobFunction"] = "must be at most 60 characters"
	}
	anyPay := req.PayMin != nil || req.PayMax != nil || req.PayCurrency != nil
	if anyPay {
		if req.PayMin == nil || req.PayMax == nil || req.PayCurrency == nil || !pnCurrency.MatchString(*req.PayCurrency) {
			fields["payMin"] = "payMin, payMax and payCurrency (ISO 4217) are given together"
		} else {
			lo, loOK := pnMoney(*req.PayMin, *req.PayCurrency)
			hi, hiOK := pnMoney(*req.PayMax, *req.PayCurrency)
			switch {
			case !loOK:
				fields["payMin"] = "must be a non-negative decimal string within the currency's minor units"
			case !hiOK:
				fields["payMax"] = "must be a non-negative decimal string within the currency's minor units"
			case hi.LessThan(lo):
				fields["payMax"] = "must not be below payMin"
			default:
				in.PayCurrency, in.PayMin, in.PayMax = req.PayCurrency, req.PayMin, req.PayMax
			}
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, inserted, err := a.store.CreateJob(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		a.fail(w, r, "create job", err)
		return
	}
	writeCreated(w, out, inserted, &a.personnel.jobs)
}

func writeCreated(w http.ResponseWriter, body any, inserted bool, counter *atomic.Int64) {
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
		counter.Add(1)
	}
	httpx.JSON(w, status, body)
}

func (a *API) listJobs(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, ok := pageParams(r)
	if !ok {
		badPage(w, r)
		return
	}
	page, err := a.store.ListJobs(r.Context(), after, limit)
	if err != nil {
		a.fail(w, r, "list jobs", err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.Job]{Items: page.Items, NextCursor: cursor(page.NextAfter)})
}

func (a *API) getJob(w http.ResponseWriter, r *http.Request, id string) {
	out, err := a.store.GetJob(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get job", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type compPlanRequest struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	PayBasis string `json:"payBasis"`
	Currency string `json:"currency"`
}

func (a *API) createCompPlan(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req compPlanRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	in := store.CompensationPlan{Code: strings.ToUpper(strings.TrimSpace(req.Code)), PayBasis: req.PayBasis, Currency: req.Currency}
	if !pnCode.MatchString(in.Code) {
		fields["code"] = "must be 1 to 20 characters of A-Z, 0-9, _ or -"
	}
	var ok bool
	if in.Name, ok = pnText(req.Name, 120); !ok {
		fields["name"] = "is required, at most 120 characters"
	}
	if !contains([]string{"ANNUAL", "HOURLY"}, in.PayBasis) {
		fields["payBasis"] = "must be ANNUAL or HOURLY"
	}
	if !pnCurrency.MatchString(in.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, inserted, err := a.store.CreateCompensationPlan(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		a.fail(w, r, "create compensation plan", err)
		return
	}
	writeCreated(w, out, inserted, &a.personnel.compPlans)
}

func (a *API) listCompPlans(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, ok := pageParams(r)
	if !ok {
		badPage(w, r)
		return
	}
	items, next, err := a.store.ListCompensationPlans(r.Context(), after, limit)
	if err != nil {
		a.fail(w, r, "list compensation plans", err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.CompensationPlan]{Items: items, NextCursor: cursor(next)})
}

func (a *API) workerCompensation(w http.ResponseWriter, r *http.Request, workerID string) {
	out, err := a.store.ListWorkerCompensation(r.Context(), workerID)
	if err != nil {
		a.fail(w, r, "list worker compensation", err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.FixedCompensation]{Items: out})
}

type actionRequest struct {
	ActionType    string              `json:"actionType"`
	WorkerID      string              `json:"workerId"`
	EmploymentID  *string             `json:"employmentId"`
	EffectiveDate string              `json:"effectiveDate"`
	Reason        string              `json:"reason"`
	Details       store.ActionPayload `json:"details"`
}

func validateAction(req actionRequest) (store.ActionInput, map[string]string) {
	fields := map[string]string{}
	in := store.ActionInput{ActionType: req.ActionType, WorkerID: req.WorkerID, EmploymentID: req.EmploymentID, Payload: req.Details}
	pl := &in.Payload
	if !contains(pnActions, req.ActionType) {
		fields["actionType"] = "must be one of HIRE, TRANSFER, TERMINATE, COMP_CHANGE"
		return in, fields
	}
	if !ids.Valid(req.WorkerID) {
		fields["workerId"] = "must be a UUID"
	}
	if d, ok := parseDate(req.EffectiveDate); ok {
		in.EffectiveDate = d
	} else {
		fields["effectiveDate"] = "is required and must be a date as YYYY-MM-DD"
	}
	if reason := strings.TrimSpace(req.Reason); len([]rune(reason)) > maxReasonLength {
		fields["reason"] = "must be at most 500 characters"
	} else {
		in.Reason = reason
	}
	if req.ActionType == hrevents.ActionHire {
		if req.EmploymentID != nil {
			fields["employmentId"] = "is not allowed on a HIRE"
		}
	} else if req.EmploymentID == nil || !ids.Valid(*req.EmploymentID) {
		fields["employmentId"] = "must be a UUID"
	}
	if pl.PositionID != "" && !ids.Valid(pl.PositionID) {
		fields["details.positionId"] = "must be a UUID"
	}
	if pl.FTEShare != "" {
		share, ok := validFTE(pl.FTEShare, decimal.RequireFromString("0.01"))
		if !ok {
			fields["details.fteShare"] = "must be a decimal string above 0 and at most 2.00 with at most two decimals"
		} else {
			pl.FTEShare = share.StringFixed(2)
		}
	}
	switch req.ActionType {
	case hrevents.ActionHire:
		if !ids.Valid(pl.LegalEntityID) {
			fields["details.legalEntityId"] = "must be a UUID"
		}
		if !contains(employmentTypes, pl.EmploymentType) {
			fields["details.employmentType"] = "must be one of FULL_TIME, PART_TIME, TEMP, CONTRACTOR"
		}
		if pl.AssignmentKind != "" && pl.AssignmentKind != "PRIMARY" && pl.AssignmentKind != "CONCURRENT" {
			fields["details.assignmentKind"] = "must be PRIMARY or CONCURRENT"
		}
		if pl.CompPlanID == "" && (pl.Amount != "" || pl.Currency != "") {
			fields["details.compPlanId"] = "is required with an amount"
		}
	case hrevents.ActionTransfer:
		if !ids.Valid(pl.PositionID) {
			fields["details.positionId"] = "must be a UUID"
		}
	case hrevents.ActionTerminate:
		pl.TerminationReason = strings.ToUpper(strings.TrimSpace(pl.TerminationReason))
		if !pnCode.MatchString(pl.TerminationReason) {
			fields["details.terminationReason"] = "must be 1 to 20 characters of A-Z, 0-9, _ or -"
		}
	case hrevents.ActionCompChange:
		if pl.CompPlanID == "" {
			fields["details.compPlanId"] = "is required"
		}
	}
	if pl.CompPlanID != "" {
		if !ids.Valid(pl.CompPlanID) {
			fields["details.compPlanId"] = "must be a UUID"
		}
		if !pnCurrency.MatchString(pl.Currency) {
			fields["details.currency"] = "must be an ISO 4217 code"
		} else if amount, ok := pnMoney(pl.Amount, pl.Currency); !ok {
			fields["details.amount"] = "must be a non-negative decimal string within the currency's minor units"
		} else {
			pl.Amount = amount.StringFixed(money.MinorUnits(pl.Currency))
		}
	}
	return in, fields
}

func (a *API) hidePay(r *http.Request, act store.PersonnelAction) store.PersonnelAction {
	if caller(r).HasDuty(dutyCompAdmin) {
		return act
	}
	return act.WithoutPay()
}

func (a *API) submitAction(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req actionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := validateAction(req)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, inserted, err := a.store.SubmitAction(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		a.fail(w, r, "submit personnel action", err)
		return
	}
	writeCreated(w, a.hidePay(r, out), inserted, &a.personnel.actionsSubmitted)
}

func (a *API) listActions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, ok := pageParams(r)
	if !ok {
		badPage(w, r)
		return
	}
	q := r.URL.Query()
	f := store.ActionFilter{Status: q.Get("status"), WorkerID: q.Get("workerId")}
	fields := map[string]string{}
	if f.Status != "" && !contains([]string{store.ActionStatusPending, store.ActionStatusApplied, store.ActionStatusWithdrawn, store.ActionStatusFailed}, f.Status) {
		fields["status"] = "must be PENDING, APPLIED, WITHDRAWN or FAILED"
	}
	if f.WorkerID != "" && !ids.Valid(f.WorkerID) {
		fields["workerId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	page, err := a.store.ListActions(r.Context(), f, after, limit)
	if err != nil {
		a.fail(w, r, "list personnel actions", err)
		return
	}
	for i := range page.Items {
		page.Items[i] = a.hidePay(r, page.Items[i])
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.PersonnelAction]{Items: page.Items, NextCursor: cursor(page.NextAfter)})
}

func (a *API) getAction(w http.ResponseWriter, r *http.Request, id string) {
	out, err := a.store.GetAction(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get personnel action", err)
		return
	}
	httpx.JSON(w, http.StatusOK, a.hidePay(r, out))
}

func (a *API) withdrawAction(w http.ResponseWriter, r *http.Request, id string) {
	out, _, err := a.store.WithdrawAction(r.Context(), id)
	if err != nil {
		a.fail(w, r, "withdraw personnel action", err)
		return
	}
	httpx.JSON(w, http.StatusOK, a.hidePay(r, out))
}

func (a *API) applyDueActions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	res, err := a.store.ApplyDueActions(r.Context(), domain.AsOfDay(time.Now()))
	if err != nil {
		a.fail(w, r, "apply due personnel actions", err)
		return
	}
	a.personnel.actionsApplied.Add(int64(res.Applied))
	a.personnel.actionsFailed.Add(int64(res.Failed))
	httpx.JSON(w, http.StatusOK, res)
}
