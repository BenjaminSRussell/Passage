package api

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const requisitionSequence = "REQ"

func (a *API) registerRecruitment(r chi.Router) {
	recruit := r.With(authz.RequireDuty(dutyRecruitMaintain))
	recruit.Post("/v1/hr/requisitions", a.createRequisition)
	recruit.Get("/v1/hr/requisitions", a.listRequisitions)
	recruit.Get("/v1/hr/requisitions/{id}", idRoute(store.CodeRequisitionNotFound, "requisition not found", a.getRequisition))
	recruit.Post("/v1/hr/requisitions/{id}/transition", idRoute(store.CodeRequisitionNotFound, "requisition not found", a.moveRequisition))
	recruit.Post("/v1/hr/requisitions/{id}/candidates", idRoute(store.CodeRequisitionNotFound, "requisition not found", a.createCandidate))
	recruit.Get("/v1/hr/requisitions/{id}/candidates", idRoute(store.CodeRequisitionNotFound, "requisition not found", a.listCandidates))
	recruit.Get("/v1/hr/candidates/{id}", idRoute(store.CodeCandidateNotFound, "candidate not found", a.getCandidate))
	recruit.Post("/v1/hr/candidates/{id}/move", idRoute(store.CodeCandidateNotFound, "candidate not found", a.moveCandidate))
	recruit.Post("/v1/hr/candidates/{id}/offers", idRoute(store.CodeCandidateNotFound, "candidate not found", a.createOffer))
	recruit.Get("/v1/hr/offers/{id}", idRoute(store.CodeOfferNotFound, "offer not found", a.getOffer))
	recruit.Post("/v1/hr/offers/{id}/transition", idRoute(store.CodeOfferNotFound, "offer not found", a.moveOffer))
	recruit.Post("/v1/hr/consents", a.grantConsent)
	recruit.Get("/v1/hr/consents", a.listConsents)
	recruit.Post("/v1/hr/consents/{id}/withdraw", idRoute(store.CodeConsentNotFound, "consent record not found", a.withdrawConsent))
	r.With(authz.RequireDuty(dutyActionMaintain)).Post("/v1/hr/offers/{id}/hire", idRoute(store.CodeOfferNotFound, "offer not found", a.hireFromOffer))
	r.With(authz.RequireDuty(dutySetupAdmin)).Get("/v1/hr/retention-policies", a.listRetention)
	r.With(authz.RequireDuty(dutySetupAdmin)).Put("/v1/hr/retention-policies/{dataClass}", a.setRetention)
	r.With(authz.RequireDuty(dutySetupAdmin)).Post("/v1/hr/candidates/purge", a.purgeCandidates)
}

type requisitionRequest struct {
	RequisitionNumber     string `json:"requisitionNumber"`
	PositionID            string `json:"positionId"`
	Headcount             *int   `json:"headcount"`
	HiringManagerWorkerID string `json:"hiringManagerWorkerId"`
	PlannedStartDate      string `json:"plannedStartDate"`
	AllowOverfill         bool   `json:"allowOverfill"`
}

func (a *API) createRequisition(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req requisitionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	in := store.Requisition{RequisitionNumber: strings.ToUpper(strings.TrimSpace(req.RequisitionNumber)), PositionID: req.PositionID,
		HiringManagerWorkerID: req.HiringManagerWorkerID, AllowOverfill: req.AllowOverfill}
	generate := in.RequisitionNumber == ""
	if !generate && !personnelNumber.MatchString(in.RequisitionNumber) {
		fields["requisitionNumber"] = "must be 1 to 20 characters of A-Z, 0-9 or -, or omitted to take the next number from the REQ sequence"
	}
	if !ids.Valid(req.PositionID) {
		fields["positionId"] = "must be a UUID"
	}
	if !ids.Valid(req.HiringManagerWorkerID) {
		fields["hiringManagerWorkerId"] = "must be a UUID"
	}
	if req.Headcount == nil || *req.Headcount < 1 || *req.Headcount > 500 {
		fields["headcount"] = "is required, 1 to 500"
	} else {
		in.Headcount = *req.Headcount
	}
	if d, ok := parseDate(req.PlannedStartDate); ok {
		in.PlannedStartDate = d.Format(dateLayout)
	} else {
		fields["plannedStartDate"] = "is required and must be a date as YYYY-MM-DD"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key := r.Header.Get(idempotencyKey)
	if generate {
		numberingKey := key
		if numberingKey == "" {
			numberingKey = ids.New()
		}
		formatted, err := a.sequence.Next(r.Context(), requisitionSequence, numberingKey)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if in.RequisitionNumber = strings.ToUpper(formatted); !personnelNumber.MatchString(in.RequisitionNumber) {
			logFailure(r, "requisition number allocation", errors.New("numbering issued "+formatted))
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, "HCM-REQ-NUMBER-INVALID", "the REQ sequence issued a number outside 1 to 20 characters of A-Z, 0-9 or -"))
			return
		}
	}
	out, inserted, err := a.store.CreateRequisition(r.Context(), in, key)
	if err != nil {
		a.fail(w, r, "create requisition", err)
		return
	}
	writeCreated(w, out, inserted, &a.personnel.requisitions)
}

func (a *API) listRequisitions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, ok := pageParams(r)
	if !ok {
		badPage(w, r)
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && !contains([]string{"DRAFT", "APPROVED", "OPEN", "FILLED", "CANCELED"}, status) {
		validationFailed(w, r, map[string]string{"status": "must be DRAFT, APPROVED, OPEN, FILLED or CANCELED"})
		return
	}
	items, next, err := a.store.ListRequisitions(r.Context(), status, after, limit)
	if err != nil {
		a.fail(w, r, "list requisitions", err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.Requisition]{Items: items, NextCursor: cursor(next)})
}

func (a *API) getRequisition(w http.ResponseWriter, r *http.Request, id string) {
	out, err := a.store.GetRequisition(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get requisition", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type moveRequest struct {
	To string `json:"to"`
}

func decodeMove(w http.ResponseWriter, r *http.Request, allowed ...string) (string, bool) {
	var req moveRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return "", false
	}
	if !contains(allowed, req.To) {
		validationFailed(w, r, map[string]string{"to": "must be one of " + strings.Join(allowed, ", ")})
		return "", false
	}
	return req.To, true
}

func (a *API) moveRequisition(w http.ResponseWriter, r *http.Request, id string) {
	to, ok := decodeMove(w, r, "APPROVED", "OPEN", "CANCELED")
	if !ok {
		return
	}
	out, err := a.store.MoveRequisition(r.Context(), id, to)
	if err != nil {
		a.fail(w, r, "move requisition", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type candidateRequest struct {
	FullName       string  `json:"fullName"`
	Email          string  `json:"email"`
	Phone          string  `json:"phone"`
	Source         *string `json:"source"`
	ConsentGranted bool    `json:"consentGranted"`
}

func (a *API) createCandidate(w http.ResponseWriter, r *http.Request, requisitionID string) {
	var req candidateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	in := store.CandidateInput{RequisitionID: requisitionID, ConsentGranted: req.ConsentGranted}
	var ok bool
	if in.FullName, ok = pnText(req.FullName, 120); !ok {
		fields["fullName"] = "is required, at most 120 characters"
	}
	if email := strings.TrimSpace(req.Email); email != "" {
		if _, err := mail.ParseAddress(email); err != nil || len(email) > 200 {
			fields["email"] = "must be an email address of at most 200 characters"
		}
		in.Email = email
	}
	if in.Phone = strings.TrimSpace(req.Phone); len(in.Phone) > 40 {
		fields["phone"] = "must be at most 40 characters"
	}
	if in.Source, ok = pnOptText(req.Source, 60); !ok {
		fields["source"] = "must be at most 60 characters"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, inserted, err := a.store.CreateCandidate(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		a.fail(w, r, "create candidate", err)
		return
	}
	writeCreated(w, out, inserted, &a.personnel.candidates)
}

func (a *API) listCandidates(w http.ResponseWriter, r *http.Request, requisitionID string) {
	limit, after, ok := pageParams(r)
	if !ok {
		badPage(w, r)
		return
	}
	stage := r.URL.Query().Get("stage")
	if stage != "" && !contains([]string{"APPLIED", "SCREEN", "INTERVIEW", "OFFER", "HIRED", "REJECTED"}, stage) {
		validationFailed(w, r, map[string]string{"stage": "must be APPLIED, SCREEN, INTERVIEW, OFFER, HIRED or REJECTED"})
		return
	}
	items, next, err := a.store.ListCandidates(r.Context(), requisitionID, stage, after, limit)
	if err != nil {
		a.fail(w, r, "list candidates", err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.Candidate]{Items: items, NextCursor: cursor(next)})
}

func (a *API) getCandidate(w http.ResponseWriter, r *http.Request, id string) {
	out, err := a.store.GetCandidate(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get candidate", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) moveCandidate(w http.ResponseWriter, r *http.Request, id string) {
	to, ok := decodeMove(w, r, "SCREEN", "INTERVIEW", "OFFER", "REJECTED")
	if !ok {
		return
	}
	out, err := a.store.MoveCandidate(r.Context(), id, to)
	if err != nil {
		a.fail(w, r, "move candidate", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type offerRequest struct {
	Amount    string `json:"amount"`
	Currency  string `json:"currency"`
	StartDate string `json:"startDate"`
}

func (a *API) createOffer(w http.ResponseWriter, r *http.Request, candidateID string) {
	var req offerRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	in := store.Offer{CandidateID: candidateID, Currency: req.Currency}
	if !pnCurrency.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	} else if amount, ok := pnMoney(req.Amount, req.Currency); !ok {
		fields["amount"] = "must be a non-negative decimal string within the currency's minor units"
	} else {
		in.Amount = amount.String()
	}
	if d, ok := parseDate(req.StartDate); ok {
		in.StartDate = d.Format(dateLayout)
	} else {
		fields["startDate"] = "is required and must be a date as YYYY-MM-DD"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, inserted, err := a.store.CreateOffer(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		a.fail(w, r, "create offer", err)
		return
	}
	writeCreated(w, out, inserted, &a.personnel.offers)
}

func (a *API) getOffer(w http.ResponseWriter, r *http.Request, id string) {
	out, err := a.store.GetOffer(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get offer", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) moveOffer(w http.ResponseWriter, r *http.Request, id string) {
	to, ok := decodeMove(w, r, "SENT", "ACCEPTED", "DECLINED", "WITHDRAWN")
	if !ok {
		return
	}
	out, err := a.store.MoveOffer(r.Context(), id, to)
	if err != nil {
		a.fail(w, r, "move offer", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type hireRequest struct {
	WorkerID       string `json:"workerId"`
	LegalEntityID  string `json:"legalEntityId"`
	EmploymentType string `json:"employmentType"`
	FTEShare       string `json:"fteShare"`
	CompPlanID     string `json:"compPlanId"`
}

func (a *API) hireFromOffer(w http.ResponseWriter, r *http.Request, id string) {
	var req hireRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.CompPlanID != "" && !caller(r).HasDuty(dutyCompAdmin) {
		forbidPay(w, r)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.WorkerID) {
		fields["workerId"] = "must be a UUID"
	}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !contains(employmentTypes, req.EmploymentType) {
		fields["employmentType"] = "must be one of FULL_TIME, PART_TIME, TEMP, CONTRACTOR"
	}
	if req.CompPlanID != "" && !ids.Valid(req.CompPlanID) {
		fields["compPlanId"] = "must be a UUID"
	}
	share := ""
	if req.FTEShare != "" {
		d, ok := validFTE(req.FTEShare, decimal.RequireFromString("0.01"))
		if !ok {
			fields["fteShare"] = "must be a decimal string above 0 and at most 2.00 with at most two decimals"
		}
		share = d.StringFixed(2)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.HireFromOffer(r.Context(), id, store.HireInput{
		WorkerID: req.WorkerID, LegalEntityID: req.LegalEntityID, EmploymentType: req.EmploymentType, FTEShare: share, CompPlanID: req.CompPlanID,
	})
	if err != nil {
		a.fail(w, r, "hire from offer", err)
		return
	}
	a.personnel.hires.Add(1)
	httpx.JSON(w, http.StatusCreated, a.hidePay(r, out))
}

type consentRequest struct {
	SubjectType string `json:"subjectType"`
	SubjectID   string `json:"subjectId"`
	Purpose     string `json:"purpose"`
}

func (a *API) grantConsent(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req consentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	req.Purpose = strings.ToUpper(strings.TrimSpace(req.Purpose))
	if req.SubjectType != "CANDIDATE" {
		fields["subjectType"] = "must be CANDIDATE"
	}
	if !ids.Valid(req.SubjectID) {
		fields["subjectId"] = "must be a UUID"
	}
	if !pnLongCode.MatchString(req.Purpose) {
		fields["purpose"] = "must be 1 to 40 characters of A-Z, 0-9, _ or -"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, granted, err := a.store.GrantConsent(r.Context(), req.SubjectType, req.SubjectID, req.Purpose)
	if err != nil {
		a.fail(w, r, "grant consent", err)
		return
	}
	status := http.StatusOK
	if granted {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, out)
}

func (a *API) listConsents(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	if q.Get("subjectType") != "CANDIDATE" {
		fields["subjectType"] = "must be CANDIDATE"
	}
	if !ids.Valid(q.Get("subjectId")) {
		fields["subjectId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.ListConsents(r.Context(), q.Get("subjectType"), q.Get("subjectId"))
	if err != nil {
		a.fail(w, r, "list consents", err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.Consent]{Items: out})
}

func (a *API) withdrawConsent(w http.ResponseWriter, r *http.Request, id string) {
	out, err := a.store.WithdrawConsent(r.Context(), id)
	if err != nil {
		a.fail(w, r, "withdraw consent", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type retentionRequest struct {
	RetentionMonths *int `json:"retentionMonths"`
}

func (a *API) setRetention(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req retentionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	class := chi.URLParam(r, "dataClass")
	if class != store.ClassCandidate {
		fields["dataClass"] = "must be CANDIDATE"
	}
	if req.RetentionMonths == nil || *req.RetentionMonths < 1 || *req.RetentionMonths > 240 {
		fields["retentionMonths"] = "is required, 1 to 240"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.SetRetentionPolicy(r.Context(), class, *req.RetentionMonths)
	if err != nil {
		a.fail(w, r, "set retention policy", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listRetention(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	out, err := a.store.ListRetentionPolicies(r.Context())
	if err != nil {
		a.fail(w, r, "list retention policies", err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.RetentionPolicy]{Items: out})
}

func (a *API) purgeCandidates(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	if !a.purgeEnabled {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "HCM-RET-DISABLED", "the retention purge is disabled until legal holds exist (FR-48.11); set HR_RETENTION_PURGE_ENABLED=true to enable it"))
		return
	}
	n, err := a.store.PurgeCandidates(r.Context())
	if err != nil {
		a.fail(w, r, "purge candidates", err)
		return
	}
	a.personnel.purged.Add(int64(n))
	httpx.JSON(w, http.StatusOK, map[string]int{"erased": n})
}
