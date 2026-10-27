package api

import (
	"fmt"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/store"
)

const (
	// dutyProgram is InvestmentProgramMaintain (PRD 7.38.17).
	dutyProgram    = "budget.program.maintain"
	maxAllocations = 200
	maxObjectRef   = 80
)

var positionCode = regexp.MustCompile(`^[A-Z0-9_.-]{1,40}$`)

// registerPrograms mounts the investment program routes (FR-38.21).
func (a *API) registerPrograms(r chi.Router) {
	r.With(authz.RequireDuty(dutyProgram)).Post("/v1/budget/investment-programs", a.createProgram)
	r.With(authz.RequireDuty(dutyProgram)).Post("/v1/budget/investment-programs/{id}:distribute", a.distributeProgram)
	r.With(authz.RequireDuty(dutyProgram)).Post("/v1/budget/investment-programs/{id}:assign", a.assignToProgram)
	r.With(authz.RequireDuty(dutyProgram)).Post("/v1/budget/investment-programs/{id}:carry-forward", a.carryForwardProgram)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/budget/investment-programs", a.listPrograms)
	r.With(authz.RequireDuty(dutyView)).Get("/v1/budget/investment-programs/{id}", a.getProgram)
}

type createProgramRequest struct {
	LegalEntityID string          `json:"legalEntityId"`
	Code          string          `json:"code"`
	Name          string          `json:"name"`
	ApprovalYear  int             `json:"approvalYear"`
	Currency      string          `json:"currency"`
	Budget        decimal.Decimal `json:"budget"`
}

type allocationRequest struct {
	PositionCode string          `json:"positionCode"`
	Category     string          `json:"category"`
	Budget       decimal.Decimal `json:"budget"`
}

type distributeRequest struct {
	ParentPositionID string              `json:"parentPositionId"`
	Allocations      []allocationRequest `json:"allocations"`
}

type assignRequest struct {
	PositionID string          `json:"positionId"`
	ObjectType string          `json:"objectType"`
	ObjectRef  string          `json:"objectRef"`
	Plan       decimal.Decimal `json:"plan"`
	Actual     decimal.Decimal `json:"actual"`
	Commitment decimal.Decimal `json:"commitment"`
}

type carryForwardRequest struct {
	ToYear int `json:"toYear"`
}

// moneyField reports a problem with a non-negative amount of at most 4 decimal places, or "".
func moneyField(v decimal.Decimal) string {
	if v.Sign() < 0 || v.Exponent() < -4 {
		return "must not be negative and have at most 4 decimal places"
	}
	return ""
}

func validYear(y int) bool { return y >= 1900 && y <= 2999 }

// programID reads the {id} path parameter; false means a 404 was written.
func programID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeProgramNotFound, "investment program not found"))
		return "", false
	}
	return id, true
}

func (a *API) createProgram(w http.ResponseWriter, r *http.Request) {
	body, composite, ok := readCommand(w, r)
	if !ok {
		return
	}
	var req createProgramRequest
	if err := decodeBody(body, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ruleCode.MatchString(req.Code) {
		fields["code"] = "must be 2 to 40 characters of A-Z, 0-9, _ or -"
	}
	if len(req.Name) > 200 {
		fields["name"] = "must be at most 200 characters"
	}
	if !validYear(req.ApprovalYear) {
		fields["approvalYear"] = "must be a year between 1900 and 2999"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if msg := moneyField(req.Budget); msg != "" {
		fields["budget"] = msg
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, hash := splitKey(composite)
	a.replay(w, r)(a.store.CreateProgram(r.Context(), key, hash, store.NewProgram{
		LegalEntityID: req.LegalEntityID, Code: req.Code, Name: req.Name, ApprovalYear: req.ApprovalYear, Currency: req.Currency, Budget: req.Budget,
	}))
}

func (a *API) listPrograms(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if le != "" && !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	rows, err := a.store.ListPrograms(r.Context(), le)
	list(w, r, rows, err)
}

func (a *API) getProgram(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := programID(w, r)
	if !ok {
		return
	}
	view, err := a.store.GetProgram(r.Context(), id)
	one(w, r, view, err)
}

func (a *API) distributeProgram(w http.ResponseWriter, r *http.Request) {
	body, composite, ok := readCommand(w, r)
	if !ok {
		return
	}
	id, ok := programID(w, r)
	if !ok {
		return
	}
	var req distributeRequest
	if err := decodeBody(body, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.ParentPositionID != "" && !ids.Valid(req.ParentPositionID) {
		fields["parentPositionId"] = "must be a UUID when set"
	}
	if len(req.Allocations) == 0 || len(req.Allocations) > maxAllocations {
		fields["allocations"] = fmt.Sprintf("must hold 1 to %d entries", maxAllocations)
	}
	seen := map[string]bool{}
	allocations := make([]store.Allocation, 0, len(req.Allocations))
	for i, al := range req.Allocations {
		prefix := fmt.Sprintf("allocations[%d].", i)
		switch {
		case !positionCode.MatchString(al.PositionCode):
			fields[prefix+"positionCode"] = "must be 1 to 40 characters of A-Z, 0-9, _, . or -"
		case seen[al.PositionCode]:
			fields[prefix+"positionCode"] = "must appear once per request"
		}
		seen[al.PositionCode] = true
		if al.Category != "" && !domain.ValidCategory(al.Category) {
			fields[prefix+"category"] = "must be Capex, Opex-related or Contingency"
		}
		if msg := moneyField(al.Budget); msg != "" {
			fields[prefix+"budget"] = msg
		}
		allocations = append(allocations, store.Allocation{PositionCode: al.PositionCode, Category: al.Category, Budget: al.Budget})
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, hash := splitKey(composite)
	a.replay(w, r)(a.store.DistributePositions(r.Context(), key, hash, id, store.DistributeRequest{ParentPositionID: req.ParentPositionID, Allocations: allocations}))
}

func (a *API) assignToProgram(w http.ResponseWriter, r *http.Request) {
	body, composite, ok := readCommand(w, r)
	if !ok {
		return
	}
	id, ok := programID(w, r)
	if !ok {
		return
	}
	var req assignRequest
	if err := decodeBody(body, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.PositionID) {
		fields["positionId"] = "must be a UUID"
	}
	if !domain.ValidObjectType(req.ObjectType) {
		fields["objectType"] = "must be CapexRequest, AssetUnderConstruction or Project"
	}
	if req.ObjectRef == "" || len(req.ObjectRef) > maxObjectRef {
		fields["objectRef"] = fmt.Sprintf("must be 1 to %d characters", maxObjectRef)
	}
	for name, v := range map[string]decimal.Decimal{"plan": req.Plan, "actual": req.Actual, "commitment": req.Commitment} {
		if msg := moneyField(v); msg != "" {
			fields[name] = msg
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, hash := splitKey(composite)
	a.replay(w, r)(a.store.AssignObject(r.Context(), key, hash, id, store.AssignRequest{
		PositionID: req.PositionID, ObjectType: req.ObjectType, ObjectRef: req.ObjectRef, Plan: req.Plan, Actual: req.Actual, Commitment: req.Commitment,
	}))
}

func (a *API) carryForwardProgram(w http.ResponseWriter, r *http.Request) {
	body, composite, ok := readCommand(w, r)
	if !ok {
		return
	}
	id, ok := programID(w, r)
	if !ok {
		return
	}
	var req carryForwardRequest
	if err := decodeBody(body, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !validYear(req.ToYear) {
		validationFailed(w, r, map[string]string{"toYear": "must be a year between 1900 and 2999"})
		return
	}
	key, hash := splitKey(composite)
	a.replay(w, r)(a.store.CarryForward(r.Context(), key, hash, id, req.ToYear))
}
