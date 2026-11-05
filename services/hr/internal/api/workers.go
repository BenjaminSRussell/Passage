package api

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

type createWorkerRequest struct {
	PersonnelNumber string  `json:"personnelNumber"`
	WorkerType      string  `json:"workerType"`
	PartyPersonID   string  `json:"partyPersonId"`
	LegalName       string  `json:"legalName"`
	PreferredName   *string `json:"preferredName"`
	UserID          *string `json:"userId"`
	NationalID      string  `json:"nationalId"`
	BankAccount     *string `json:"bankAccount"`
}

// workerDetail is the worker with its PII in masked form. Unmasked values come
// only from the audited /pii route.
type workerDetail struct {
	store.Worker
	PII store.PIIView `json:"pii"`
}

// validateWorker checks a worker request, writing problems to fields under
// prefix. With nameOptional an empty legalName is allowed (a hire from an
// offer takes the candidate's name).
func validateWorker(req createWorkerRequest, nameOptional bool, prefix string, fields map[string]string) store.WorkerInput {
	req.PersonnelNumber = strings.ToUpper(strings.TrimSpace(req.PersonnelNumber))
	if !personnelNumber.MatchString(req.PersonnelNumber) {
		fields[prefix+"personnelNumber"] = "must be 1 to 20 characters of A-Z, 0-9 or -"
	}
	if !contains(workerTypes, req.WorkerType) {
		fields[prefix+"workerType"] = "must be EMPLOYEE or CONTRACTOR"
	}
	if !ids.Valid(req.PartyPersonID) {
		fields[prefix+"partyPersonId"] = "must be a UUID"
	}
	legalName := strings.TrimSpace(req.LegalName)
	if (legalName == "" && !nameOptional) || utf8.RuneCountInString(legalName) > 200 {
		fields[prefix+"legalName"] = "must be 1 to 200 characters"
	}
	nationalID := strings.TrimSpace(req.NationalID)
	if nationalID == "" || utf8.RuneCountInString(nationalID) > 32 {
		fields[prefix+"nationalId"] = "is required and must be at most 32 characters"
	}
	if req.UserID != nil && !ids.Valid(*req.UserID) {
		fields[prefix+"userId"] = "must be a UUID"
	}
	if req.BankAccount != nil && utf8.RuneCountInString(*req.BankAccount) > 34 {
		fields[prefix+"bankAccount"] = "must be at most 34 characters"
	}
	in := store.WorkerInput{
		Worker: store.Worker{PersonnelNumber: req.PersonnelNumber, WorkerType: req.WorkerType, PartyPersonID: req.PartyPersonID,
			LegalName: legalName, PreferredName: req.PreferredName, UserID: req.UserID},
		NationalID: nationalID,
	}
	if req.BankAccount != nil {
		in.BankAccount = strings.TrimSpace(*req.BankAccount)
	}
	return in
}

func (a *API) createWorker(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req createWorkerRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	in := validateWorker(req, false, "", fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	created, inserted, err := a.store.CreateWorker(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		logFailure(r, "create worker", err)
		httpx.Fail(w, r, err)
		return
	}
	if inserted {
		a.workersCreated.Add(1)
	}
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *API) getWorker(w http.ResponseWriter, r *http.Request, id string) {
	worker, err := a.store.GetWorker(r.Context(), id)
	if err != nil {
		logFailure(r, "get worker", err)
		httpx.Fail(w, r, err)
		return
	}
	pii, err := a.store.ReadPII(r.Context(), id, false, "", middleware.GetReqID(r.Context()))
	if err != nil {
		logFailure(r, "masked pii read", err)
		httpx.Fail(w, r, err)
		return
	}
	a.piiMaskedReads.Add(1)
	httpx.JSON(w, http.StatusOK, workerDetail{Worker: worker, PII: pii})
}

// revealPII is FR-42.1's unmasked read. It needs HcmPIIRead and a reason of at
// least 10 characters, and the audit row is written in the same transaction as
// the decryption, so a failed audit returns no value (8.42.17).
func (a *API) revealPII(w http.ResponseWriter, r *http.Request, id string) {
	p, _ := tenant.FromContext(r.Context())
	reason := r.URL.Query().Get("reason")
	if err := domain.CheckReveal(p.HasDuty(dutyPIIRead), reason, domain.DefaultReasonMinLength); err != nil {
		failDomain(w, r, err)
		return
	}
	if utf8.RuneCountInString(reason) > maxReasonLength {
		validationFailed(w, r, map[string]string{"reason": fmt.Sprintf("must be at most %d characters", maxReasonLength)})
		return
	}
	pii, err := a.store.ReadPII(r.Context(), id, true, reason, middleware.GetReqID(r.Context()))
	if err != nil {
		logFailure(r, "reveal pii", err)
		httpx.Fail(w, r, err)
		return
	}
	a.piiReveals.Add(1)
	httpx.JSON(w, http.StatusOK, pii)
}

func (a *API) workerAsOf(w http.ResponseWriter, r *http.Request, id string) {
	raw := r.URL.Query().Get("date")
	date, ok := parseDate(raw)
	if !ok {
		validationFailed(w, r, map[string]string{"date": "is required and must be a date as YYYY-MM-DD"})
		return
	}
	view, err := a.store.WorkerAsOf(r.Context(), id, domain.AsOfDay(date), raw)
	if err != nil {
		logFailure(r, "worker as-of", err)
		httpx.Fail(w, r, err)
		return
	}
	a.asOfQueries.Add(1)
	httpx.JSON(w, http.StatusOK, view)
}

type createEmploymentRequest struct {
	LegalEntityID     string  `json:"legalEntityId"`
	EmploymentType    string  `json:"employmentType"`
	AssignmentKind    *string `json:"assignmentKind"`
	HomeLegalEntityID *string `json:"homeLegalEntityId"`
	PlannedEndDate    *string `json:"plannedEndDate"`
	HireDate          string  `json:"hireDate"`
	TerminationDate   *string `json:"terminationDate"`
}

func (a *API) createEmployment(w http.ResponseWriter, r *http.Request, workerID string) {
	var req createEmploymentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !contains(employmentTypes, req.EmploymentType) {
		fields["employmentType"] = "must be FULL_TIME, PART_TIME, TEMP or CONTRACTOR"
	}
	kind := "PRIMARY"
	if req.AssignmentKind != nil {
		kind = *req.AssignmentKind
	}
	if !contains(assignmentKinds, kind) {
		fields["assignmentKind"] = "must be PRIMARY, CONCURRENT or GLOBAL_HOST"
	}
	hire, hireOK := parseDate(req.HireDate)
	if !hireOK {
		fields["hireDate"] = "is required and must be a date as YYYY-MM-DD"
	}
	term, termOK := optionalDate(req.TerminationDate)
	if !termOK {
		fields["terminationDate"] = "must be a date as YYYY-MM-DD"
	}
	planned, plannedOK := optionalDate(req.PlannedEndDate)
	if !plannedOK {
		fields["plannedEndDate"] = "must be a date as YYYY-MM-DD"
	}
	if hireOK && planned != nil && planned.Before(hire) {
		fields["plannedEndDate"] = "must not be before hireDate"
	}
	if kind == "GLOBAL_HOST" {
		if req.HomeLegalEntityID == nil || !ids.Valid(*req.HomeLegalEntityID) || *req.HomeLegalEntityID == req.LegalEntityID {
			fields["homeLegalEntityId"] = "is required for a global host and must differ from the legal entity"
		}
		if planned == nil {
			fields["plannedEndDate"] = "is required for a global host"
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if term != nil && term.Before(hire) {
		failDomain(w, r, &domain.Error{Code: "HCM-EMP-002", Detail: "Termination date " + *req.TerminationDate + " is before hire date " + req.HireDate})
		return
	}
	in := store.Employment{
		LegalEntityID: req.LegalEntityID, EmploymentType: req.EmploymentType, AssignmentKind: kind,
		HomeLegalEntityID: req.HomeLegalEntityID, HireDate: hire.Format(dateLayout), ValidFrom: domain.AsOfDay(hire),
	}
	if planned != nil {
		s := planned.Format(dateLayout)
		in.PlannedEndDate = &s
	}
	if term != nil {
		// The termination date is the last day employed (inclusive), so the
		// exclusive range end is the next day.
		last := term.Format(dateLayout)
		end := domain.DayAfter(*term)
		in.TerminationDate, in.ValidTo = &last, &end
	}
	created, inserted, err := a.store.CreateEmployment(r.Context(), workerID, in, r.Header.Get(idempotencyKey))
	if err != nil {
		logFailure(r, "create employment", err)
		httpx.Fail(w, r, err)
		return
	}
	if inserted {
		a.employments.Add(1)
	}
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *API) listWorkers(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, ok := pageParams(r)
	if !ok {
		badPage(w, r)
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(search) > maxSearchLength {
		validationFailed(w, r, map[string]string{"q": fmt.Sprintf("must be at most %d characters", maxSearchLength)})
		return
	}
	page, err := a.store.ListWorkers(r.Context(), search, after, limit)
	if err != nil {
		logFailure(r, "list workers", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.Worker]{Items: page.Items, NextCursor: cursor(page.NextAfter)})
}

func (a *API) listEmployments(w http.ResponseWriter, r *http.Request, workerID string) {
	items, err := a.store.ListEmployments(r.Context(), workerID)
	if err != nil {
		logFailure(r, "list employments", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.Employment]{Items: items})
}
