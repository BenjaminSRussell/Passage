package api

import (
	"net/http"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

// defaultPositionFTE applies when a position is created without an FTE (FR-42.3).
const defaultPositionFTE = "1.00"

type createPositionRequest struct {
	PositionNumber    string  `json:"positionNumber"`
	JobCode           string  `json:"jobCode"`
	DepartmentID      string  `json:"departmentId"`
	LegalEntityID     string  `json:"legalEntityId"`
	FTE               string  `json:"fte"`
	AllowOverfill     bool    `json:"allowOverfill"`
	ValidFrom         string  `json:"validFrom"`
	ValidTo           *string `json:"validTo"`
	ReportsToPosition *string `json:"reportsToPositionId"`
}

type createAssignmentRequest struct {
	WorkerID     string  `json:"workerId"`
	EmploymentID string  `json:"employmentId"`
	FTEShare     string  `json:"fteShare"`
	IsPrimary    bool    `json:"isPrimary"`
	ValidFrom    string  `json:"validFrom"`
	ValidTo      *string `json:"validTo"`
}

// validFTE reads a decimal FTE from a JSON string (never a float) and checks
// the min to 2.00 range with at most two decimals (FR-42.3). Positions use a
// 0.05 floor; assignment shares use 0.01.
func validFTE(raw string, min decimal.Decimal) (decimal.Decimal, bool) {
	d, err := decimal.NewFromString(strings.TrimSpace(raw))
	if err != nil || d.LessThan(min) || d.GreaterThan(decimal.RequireFromString("2.00")) || !d.Equal(d.Round(2)) {
		return decimal.Zero, false
	}
	return d, true
}

func (a *API) createPosition(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req createPositionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	req.PositionNumber = strings.ToUpper(strings.TrimSpace(req.PositionNumber))
	if !personnelNumber.MatchString(req.PositionNumber) {
		fields["positionNumber"] = "must be 1 to 20 characters of A-Z, 0-9 or -"
	}
	req.JobCode = strings.ToUpper(strings.TrimSpace(req.JobCode))
	if !jobCode.MatchString(req.JobCode) {
		fields["jobCode"] = "must be 1 to 20 characters of A-Z, 0-9, _ or -"
	}
	if !ids.Valid(req.DepartmentID) {
		fields["departmentId"] = "must be a UUID"
	}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if strings.TrimSpace(req.FTE) == "" {
		req.FTE = defaultPositionFTE
	}
	fte, fteOK := validFTE(req.FTE, decimal.RequireFromString("0.05"))
	if !fteOK {
		fields["fte"] = "must be a decimal string from 0.05 to 2.00 with at most two decimals"
	}
	from, fromOK := parseDate(req.ValidFrom)
	if !fromOK {
		fields["validFrom"] = "is required and must be a date as YYYY-MM-DD"
	}
	to, toOK := optionalDate(req.ValidTo)
	if !toOK {
		fields["validTo"] = "must be a date as YYYY-MM-DD"
	} else if fromOK && to != nil && !to.After(from) {
		fields["validTo"] = "must be after validFrom"
	}
	if req.ReportsToPosition != nil && !ids.Valid(*req.ReportsToPosition) {
		fields["reportsToPositionId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	in := store.Position{
		PositionNumber: req.PositionNumber, JobCode: req.JobCode, DepartmentID: req.DepartmentID, LegalEntityID: req.LegalEntityID,
		FTE: fte.StringFixed(2), AllowOverfill: req.AllowOverfill, ValidFrom: domain.AsOfDay(from), ReportsToID: req.ReportsToPosition,
	}
	if to != nil {
		end := domain.AsOfDay(*to)
		in.ValidTo = &end
	}
	created, inserted, err := a.store.CreatePosition(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		logFailure(r, "create position", err)
		httpx.Fail(w, r, err)
		return
	}
	if inserted {
		a.positionsCreated.Add(1)
	}
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *API) createAssignment(w http.ResponseWriter, r *http.Request, positionID string) {
	var req createAssignmentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.WorkerID) {
		fields["workerId"] = "must be a UUID"
	}
	if !ids.Valid(req.EmploymentID) {
		fields["employmentId"] = "must be a UUID"
	}
	share, shareOK := validFTE(req.FTEShare, decimal.RequireFromString("0.01"))
	if !shareOK {
		fields["fteShare"] = "must be a decimal string above 0 and at most 2.00 with at most two decimals"
	}
	from, fromOK := parseDate(req.ValidFrom)
	if !fromOK {
		fields["validFrom"] = "is required and must be a date as YYYY-MM-DD"
	}
	to, toOK := optionalDate(req.ValidTo)
	if !toOK {
		fields["validTo"] = "must be a date as YYYY-MM-DD"
	} else if fromOK && to != nil && !to.After(from) {
		fields["validTo"] = "must be after validFrom"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	in := store.PositionAssignment{
		WorkerID: req.WorkerID, EmploymentID: req.EmploymentID, FTEShare: share.StringFixed(2),
		IsPrimary: req.IsPrimary, ValidFrom: domain.AsOfDay(from),
	}
	if to != nil {
		end := domain.AsOfDay(*to)
		in.ValidTo = &end
	}
	created, inserted, err := a.store.CreateAssignment(r.Context(), positionID, in, r.Header.Get(idempotencyKey))
	if err != nil {
		logFailure(r, "create assignment", err)
		httpx.Fail(w, r, err)
		return
	}
	if inserted {
		a.assignments.Add(1)
	}
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *API) listPositions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, ok := pageParams(r)
	if !ok {
		badPage(w, r)
		return
	}
	page, err := a.store.ListPositions(r.Context(), after, limit)
	if err != nil {
		logFailure(r, "list positions", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.Position]{Items: page.Items, NextCursor: cursor(page.NextAfter)})
}

func (a *API) getPosition(w http.ResponseWriter, r *http.Request, id string) {
	detail, err := a.store.GetPosition(r.Context(), id)
	if err != nil {
		logFailure(r, "get position", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, detail)
}
