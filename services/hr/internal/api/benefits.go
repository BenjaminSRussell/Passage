package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/phiaudit"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const (
	dutySetupAdmin  = "hr.setup.admin"
	dutyEssBenefits = "hr.benefits.self"

	benMaxOptions  = 20
	benMaxWaiting  = 365
	benMaxDepend   = 20
	benMaxCoverage = 40
	benMaxDeduct   = 20
	// benMaxWindowDays bounds an open enrollment window; windows are weeks long.
	benMaxWindowDays = 366
)

var (
	benCode     = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)
	benCurrency = regexp.MustCompile(`^[A-Z]{3}$`)
	// benMaxRate keeps a rate inside numeric(20,6).
	benMaxRate = decimal.New(1, 14)
)

// benefitOptionRequest is one coverage level. Rates are decimal strings in the
// plan currency, with at most its minor-unit places.
type benefitOptionRequest struct {
	Code          string  `json:"code"`
	CoverageLevel string  `json:"coverageLevel"`
	EmployeeRate  string  `json:"employeeRate"`
	EmployerRate  string  `json:"employerRate"`
	RateBasis     *string `json:"rateBasis"`
}

// benefitPlanRequest is the plan body (FR-42.8, 8.42.4).
type benefitPlanRequest struct {
	Code                 string                 `json:"code"`
	PlanType             string                 `json:"planType"`
	ProviderPartyID      *string                `json:"providerPartyId"`
	Currency             string                 `json:"currency"`
	EligibilityRule      string                 `json:"eligibilityRule"`
	EnrollmentStart      string                 `json:"enrollmentStart"`
	EnrollmentEnd        string                 `json:"enrollmentEnd"`
	WaitingDays          *int                   `json:"waitingDays"`
	PreTax               bool                   `json:"preTax"`
	PayrollDeductionCode *string                `json:"payrollDeductionCode"`
	Options              []benefitOptionRequest `json:"options"`
}

// benefitPlanRoute and enrollmentRoute check the tenant and the id shape; a
// malformed id is a 404 with the benefit code, the answer an unknown id gets.
func benefitPlanRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return absenceRoute(store.CodeBenefitPlanNotFound, "benefit plan not found", next)
}

func enrollmentRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return absenceRoute(store.CodeBenefitEnrollmentNotFound, "benefit enrollment not found", next)
}

// blankToNil treats an empty optional string as absent.
func blankToNil(s *string) *string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	v := strings.TrimSpace(*s)
	return &v
}

// createBenefitPlan is POST /v1/hr/benefits/plans (hr.setup.admin).
func (a *API) createBenefitPlan(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req benefitPlanRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if !benCode.MatchString(code) {
		fields["code"] = "must be 1-20 characters: A-Z, 0-9, hyphen and underscore"
	}
	if !contains(domain.BenefitPlanTypes, req.PlanType) {
		fields["planType"] = "must be one of " + strings.Join(domain.BenefitPlanTypes, ", ")
	}
	provider := blankToNil(req.ProviderPartyID)
	if provider != nil && !ids.Valid(*provider) {
		fields["providerPartyId"] = "must be a UUID"
	}
	if !benCurrency.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code such as USD"
	}
	rule := strings.TrimSpace(req.EligibilityRule)
	if _, err := domain.CompileRule(rule); err != nil {
		fields["eligibilityRule"] = "must be a valid expression: " + err.Error()
	}
	start, startOK := parseDate(req.EnrollmentStart)
	if !startOK {
		fields["enrollmentStart"] = "must be a YYYY-MM-DD date"
	}
	end, endOK := parseDate(req.EnrollmentEnd)
	if !endOK {
		fields["enrollmentEnd"] = "must be a YYYY-MM-DD date"
	}
	if startOK && endOK && (end.Before(start) || domain.InclusiveDays(start, end) > benMaxWindowDays) {
		fields["enrollmentEnd"] = "must be on or after enrollmentStart and within 366 days"
	}
	waiting := 0
	if req.WaitingDays != nil {
		waiting = *req.WaitingDays
	}
	if waiting < 0 || waiting > benMaxWaiting {
		fields["waitingDays"] = fmt.Sprintf("must be between 0 and %d", benMaxWaiting)
	}
	deduction := blankToNil(req.PayrollDeductionCode)
	if deduction != nil && len(*deduction) > benMaxDeduct {
		fields["payrollDeductionCode"] = fmt.Sprintf("must be at most %d characters", benMaxDeduct)
	}
	options := benefitOptions(req.Options, req.Currency, fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}

	plan, inserted, err := a.store.CreateBenefitPlan(r.Context(), store.BenefitPlan{
		Code: code, PlanType: req.PlanType, ProviderPartyID: provider, Currency: req.Currency, EligibilityRule: rule,
		EnrollmentStart: req.EnrollmentStart, EnrollmentEnd: req.EnrollmentEnd, WaitingDays: waiting,
		PreTax: req.PreTax, PayrollDeductionCode: deduction, Options: options,
	}, r.Header.Get(idempotencyKey))
	if err != nil {
		logFailure(r, "create benefit plan", err)
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
		a.benefitPlans.Add(1)
	}
	httpx.JSON(w, status, plan)
}

// benefitOptions validates the option list into fields, keyed by path. Rates
// take at most the currency's minor-unit places (money.MinorUnits).
func benefitOptions(in []benefitOptionRequest, currency string, fields map[string]string) []store.BenefitOption {
	if len(in) == 0 || len(in) > benMaxOptions {
		fields["options"] = fmt.Sprintf("must hold between 1 and %d options", benMaxOptions)
		return nil
	}
	places := money.MinorUnits(currency)
	seen := map[string]bool{}
	out := make([]store.BenefitOption, 0, len(in))
	for i, o := range in {
		key := fmt.Sprintf("options[%d]", i)
		code := strings.ToUpper(strings.TrimSpace(o.Code))
		if !benCode.MatchString(code) || seen[code] {
			fields[key+".code"] = "must be unique in the plan, 1-20 characters: A-Z, 0-9, hyphen and underscore"
		}
		seen[code] = true
		level := strings.TrimSpace(o.CoverageLevel)
		if level == "" || len(level) > benMaxCoverage {
			fields[key+".coverageLevel"] = fmt.Sprintf("is required, at most %d characters", benMaxCoverage)
		}
		rateMsg := fmt.Sprintf("must be a non-negative decimal with at most %d places", places)
		emp, empOK := absDecimal(o.EmployeeRate, places, benMaxRate, true)
		if !empOK {
			fields[key+".employeeRate"] = rateMsg
		}
		er, erOK := absDecimal(o.EmployerRate, places, benMaxRate, true)
		if !erOK {
			fields[key+".employerRate"] = rateMsg
		}
		basis := absOrDefault(o.RateBasis, "PER_PAY_PERIOD")
		if !contains(domain.BenefitRateBases, basis) {
			fields[key+".rateBasis"] = "must be PER_PAY_PERIOD or MONTHLY"
		}
		out = append(out, store.BenefitOption{
			Code: code, CoverageLevel: level, EmployeeRate: emp.String(), EmployerRate: er.String(), RateBasis: basis,
		})
	}
	return out
}

// listBenefitPlans is GET /v1/hr/benefits/plans (hr.benefits.self): every employee
// browses the plans before enrolling.
func (a *API) listBenefitPlans(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, ok := pageParams(r)
	if !ok {
		badPage(w, r)
		return
	}
	page, err := a.store.ListBenefitPlans(r.Context(), after, limit)
	if err != nil {
		logFailure(r, "list benefit plans", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.BenefitPlan]{Items: page.Items, NextCursor: cursor(page.NextAfter)})
}

// getBenefitPlan is GET /v1/hr/benefits/plans/{id} (hr.benefits.self).
func (a *API) getBenefitPlan(w http.ResponseWriter, r *http.Request, planID string) {
	plan, err := a.store.GetBenefitPlan(r.Context(), planID)
	if err != nil {
		logFailure(r, "get benefit plan", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, plan)
}

// simulateBenefitPlan is GET /v1/hr/benefits/plans/{id}/simulate?asOf=
// (hr.setup.admin). It evaluates the plan for the whole workforce, exactly as
// enrollment would, and writes nothing.
func (a *API) simulateBenefitPlan(w http.ResponseWriter, r *http.Request, planID string) {
	asOf, ok := parseDate(r.URL.Query().Get("asOf"))
	if !ok {
		validationFailed(w, r, map[string]string{"asOf": "is required and must be a YYYY-MM-DD date"})
		return
	}
	sim, err := a.store.SimulateBenefitEligibility(r.Context(), planID, asOf)
	if err != nil {
		logFailure(r, "simulate benefit eligibility", err)
		httpx.Fail(w, r, err)
		return
	}
	a.simulations.Add(1)
	httpx.JSON(w, http.StatusOK, sim)
}

// lifeEventRequest records a qualifying event for a worker.
type lifeEventRequest struct {
	WorkerID  string `json:"workerId"`
	Kind      string `json:"kind"`
	EventDate string `json:"eventDate"`
}

// benOwnOnly is true for a caller who may act only on their own benefits:
// hr.worker.maintain (the HR administrator) acts for any worker.
func benOwnOnly(r *http.Request) bool { return !caller(r).HasDuty(dutyWorkerMaintain) }

// createLifeEvent is POST /v1/hr/benefits/life-events (hr.benefits.self). An
// employee records their own; hr.worker.maintain records anyone's.
func (a *API) createLifeEvent(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req lifeEventRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.WorkerID) {
		fields["workerId"] = "must be a UUID"
	}
	if !contains(domain.LifeEventKinds, req.Kind) {
		fields["kind"] = "must be one of " + strings.Join(domain.LifeEventKinds, ", ")
	}
	eventDate, ok := parseDate(req.EventDate)
	if !ok {
		fields["eventDate"] = "must be a YYYY-MM-DD date"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	ev, inserted, err := a.store.RecordLifeEvent(r.Context(), req.WorkerID, req.Kind, eventDate, r.Header.Get(idempotencyKey), benOwnOnly(r))
	if err != nil {
		logFailure(r, "record life event", err)
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, ev)
}

// enrollmentRequest enrolls a worker in one option of a plan.
type enrollmentRequest struct {
	WorkerID       string  `json:"workerId"`
	PlanID         string  `json:"planId"`
	OptionCode     string  `json:"optionCode"`
	EffectiveDate  string  `json:"effectiveDate"`
	LifeEventID    *string `json:"lifeEventId"`
	DependentCount *int    `json:"dependentCount"`
}

// createBenefitEnrollment is POST /v1/hr/benefits/enrollments (hr.benefits.self).
// An employee enrolls themselves; hr.worker.maintain enrolls anyone.
// Idempotency-Key replays return the stored enrollment with 200.
func (a *API) createBenefitEnrollment(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req enrollmentRequest
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
	option := strings.ToUpper(strings.TrimSpace(req.OptionCode))
	if !benCode.MatchString(option) {
		fields["optionCode"] = "must be an option code of the plan"
	}
	eff, ok := parseDate(req.EffectiveDate)
	if !ok {
		fields["effectiveDate"] = "must be a YYYY-MM-DD date"
	}
	lifeEventID := ""
	if le := blankToNil(req.LifeEventID); le != nil {
		if !ids.Valid(*le) {
			fields["lifeEventId"] = "must be a UUID"
		}
		lifeEventID = *le
	}
	dependents := 0
	if req.DependentCount != nil {
		dependents = *req.DependentCount
	}
	if dependents < 0 || dependents > benMaxDepend {
		fields["dependentCount"] = fmt.Sprintf("must be between 0 and %d", benMaxDepend)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	enr, inserted, err := a.store.CreateBenefitEnrollment(r.Context(), store.BenefitEnrollmentInput{
		WorkerID: req.WorkerID, PlanID: req.PlanID, OptionCode: option, EffectiveDate: eff,
		LifeEventID: lifeEventID, DependentCount: dependents,
	}, r.Header.Get(idempotencyKey), benOwnOnly(r))
	if err != nil {
		logFailure(r, "create benefit enrollment", err)
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
		a.enrollments.Add(1)
	}
	httpx.JSON(w, status, enr)
}

// endEnrollmentRequest closes an enrollment after its last covered day.
type endEnrollmentRequest struct {
	LastDay string `json:"lastDay"`
	Reason  string `json:"reason"`
}

// endBenefitEnrollment is POST /v1/hr/benefits/enrollments/{id}/end
// (hr.worker.maintain). A TERMINATION keeps coverage to the end of the month of
// lastDay (BR-42.6). Repeating the same end returns it unchanged.
func (a *API) endBenefitEnrollment(w http.ResponseWriter, r *http.Request, enrollmentID string) {
	var req endEnrollmentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	lastDay, ok := parseDate(req.LastDay)
	if !ok {
		fields["lastDay"] = "must be a YYYY-MM-DD date"
	}
	if !contains(domain.EnrollmentEndReasons, req.Reason) {
		fields["reason"] = "must be " + strings.Join(domain.EnrollmentEndReasons, ", ")
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	enr, changed, err := a.store.EndBenefitEnrollment(r.Context(), enrollmentID, lastDay, req.Reason)
	if err != nil {
		logFailure(r, "end benefit enrollment", err)
		httpx.Fail(w, r, err)
		return
	}
	if changed {
		a.enrollmentsEnded.Add(1)
	}
	httpx.JSON(w, http.StatusOK, enr)
}

// workerBenefits is GET /v1/hr/workers/{id}/benefits (hr.benefits.self): the
// worker's enrollments and life events. An employee reads their own;
// hr.worker.inquire or hr.worker.maintain reads anyone's. Health-plan elections and
// life events are protected health information: with read auditing on (the hipaa
// overlay, #606) a read that returns them is recorded in the audit service first
// and needs a ?purpose= (an employee reading their own defaults to self-service).
//
// Break-glass (HIPAA emergency access): a caller without hr.worker.inquire who
// holds hr.benefits.break-glass reads anyone's benefits by giving a purpose with
// the BREAK-GLASS reason code. Such a read is always recorded, the audit service
// queues it for review by a second person, and it is refused while read
// auditing is off, so emergency access is never unlogged. The BREAK-GLASS
// reason code is reserved for that case: a caller whose scope it would not
// widen gets 400, so the review queue holds only real emergency access.
func (a *API) workerBenefits(w http.ResponseWriter, r *http.Request, workerID string) {
	ownOnly := benOwnOnly(r) && !caller(r).HasDuty(dutyWorkerInquire)
	breakGlass := false
	if phiaudit.IsBreakGlass(r.URL.Query().Get("purpose")) {
		if !ownOnly || !caller(r).HasDuty(dutyBenefitsBreakGlass) {
			validationFailed(w, r, map[string]string{"purpose": "BREAK-GLASS is reserved for break-glass access: give the reason for this read instead"})
			return
		}
		if a.phiAudit == nil {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeBreakGlassUnaudited,
				"break-glass access needs PHI read auditing, which is off for this deployment"))
			return
		}
		ownOnly, breakGlass = false, true
	}
	out, err := a.listWorkerBenefits(r.Context(), workerID, ownOnly)
	if err != nil {
		logFailure(r, "list worker benefits", err)
		httpx.Fail(w, r, err)
		return
	}
	fields := benefitPHIFields(out)
	if breakGlass && len(fields) == 0 {
		// Emergency access is recorded whatever the record turned out to hold, so every use reaches the review queue.
		fields = []string{"enrollments", "lifeEvents"}
	}
	if !a.auditPHIRead(w, r, entityWorkerBenefits, workerID, fields, ownOnly) {
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
