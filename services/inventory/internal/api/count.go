package api

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// Cycle-count routes (FR-16.9). The duties stand for CycleCountMaintain,
// CycleCountPost and CycleCountApprove (3.16.17).
const (
	dutyCountWrite   = "inventory.count.write"
	dutyCountPost    = "inventory.count.post"
	dutyCountApprove = "inventory.count.approve"
	countPlansPath   = "/v1/inventory/count-plans"
	countSheetsPath  = "/v1/inventory/count-sheets"
)

func (a *API) registerCount(r chi.Router) {
	r.With(authz.RequireDuty(dutyCountWrite)).Post(countPlansPath, a.createCountPlan)
	r.With(authz.RequireDuty(dutyRead)).Get(countPlansPath, a.listCountPlans)
	r.With(authz.RequireDuty(dutyCountWrite)).Post(countPlansPath+"/{id}:generate", a.generateCountSheet)
	r.With(authz.RequireDuty(dutyRead)).Get(countSheetsPath, a.listCountSheets)
	r.With(authz.RequireDuty(dutyRead)).Get(countSheetsPath+"/{id}", a.getCountSheet)
	r.With(authz.RequireDuty(dutyCountWrite)).Put(countSheetsPath+"/{id}/lines/{lineId}", a.enterCount)
	r.With(authz.RequireDuty(dutyCountPost)).Post(countSheetsPath+"/{id}:post", a.postCountSheet)
}

type countPlanRequest struct {
	LegalEntityID  string `json:"legalEntityId"`
	Name           string `json:"name"`
	Warehouse      string `json:"warehouse"`
	Currency       string `json:"currency"`
	Unit           string `json:"unit"`
	TolerancePct   string `json:"tolerancePct"`
	ToleranceValue string `json:"toleranceValue"`
	Blind          bool   `json:"blind"`
}

type countPlanResponse struct {
	ID             string    `json:"id"`
	LegalEntityID  string    `json:"legalEntityId"`
	Name           string    `json:"name"`
	Warehouse      string    `json:"warehouse,omitempty"`
	Currency       string    `json:"currency"`
	Unit           string    `json:"unit"`
	TolerancePct   string    `json:"tolerancePct"`
	ToleranceValue string    `json:"toleranceValue"`
	Blind          bool      `json:"blind"`
	CreatedAt      time.Time `json:"createdAt"`
}

type countLineRequest struct {
	CountedQty string `json:"countedQty"`
}

type countLineResponse struct {
	ID             string            `json:"id"`
	LineNo         int               `json:"lineNo"`
	ProductID      string            `json:"productId"`
	Dimensions     map[string]string `json:"dimensions"`
	ExpectedQty    *string           `json:"expectedQty"`
	CountedQty     *string           `json:"countedQty"`
	CountedBy      string            `json:"countedBy,omitempty"`
	CountedAt      *time.Time        `json:"countedAt,omitempty"`
	PostedVariance *string           `json:"postedVariance"`
}

type countSheetSummary struct {
	ID            string     `json:"id"`
	LegalEntityID string     `json:"legalEntityId"`
	PlanID        string     `json:"planId"`
	PlanName      string     `json:"planName"`
	Number        string     `json:"number"`
	Status        string     `json:"status"`
	Blind         bool       `json:"blind"`
	ApprovedBy    string     `json:"approvedBy,omitempty"`
	JournalID     string     `json:"journalId,omitempty"`
	Version       int        `json:"version"`
	LineCount     int        `json:"lineCount"`
	CountedCount  int        `json:"countedCount"`
	CreatedAt     time.Time  `json:"createdAt"`
	PostedAt      *time.Time `json:"postedAt,omitempty"`
}

type countSheetResponse struct {
	countSheetSummary
	Lines []countLineResponse `json:"lines"`
}

func (a *API) createCountPlan(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req countPlanRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	req.Name = strings.TrimSpace(req.Name)
	if n := utf8.RuneCountInString(req.Name); n == 0 || n > 120 {
		fields["name"] = "is required (1 to 120 characters)"
	}
	req.Warehouse = strings.TrimSpace(req.Warehouse)
	if utf8.RuneCountInString(req.Warehouse) > maxIdentifierLen {
		fields["warehouse"] = "must be at most 60 characters"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if !unitCode.MatchString(req.Unit) {
		fields["unit"] = "is required (1 to 12 characters of A-Z, 0-9, _ or -)"
	}
	pct, err := decimalOr(req.TolerancePct, domain.DefaultTolerancePct)
	if err != nil || pct.IsNegative() || pct.GreaterThan(decimal.NewFromInt(100)) {
		fields["tolerancePct"] = "must be a decimal number from 0 to 100"
	}
	val, err := decimalOr(req.ToleranceValue, domain.DefaultToleranceValue)
	if err != nil || val.IsNegative() {
		fields["toleranceValue"] = "must be a decimal number of zero or more"
	}
	key := idempotencyKey(r, fields)
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	out, inserted, err := a.store.CreateCountPlan(r.Context(), key, domain.CountPlan{LegalEntityID: req.LegalEntityID, Name: req.Name,
		Warehouse: req.Warehouse, Currency: req.Currency, Unit: req.Unit, TolerancePct: pct, ToleranceValue: val, Blind: req.Blind})
	if err != nil {
		a.failed(w, r, "create count plan", err)
		return
	}
	status := http.StatusCreated
	if !inserted {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, status, toCountPlanResponse(out))
}

func (a *API) listCountPlans(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	plans, err := a.store.ListCountPlans(r.Context())
	if err != nil {
		a.failed(w, r, "list count plans", err)
		return
	}
	items := make([]countPlanResponse, 0, len(plans))
	for _, p := range plans {
		items = append(items, toCountPlanResponse(p))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// generateCountSheet creates an OPEN sheet from the plan's current stock.
func (a *API) generateCountSheet(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeCountNotFound, "count plan not found"))
		return
	}
	fields := map[string]string{}
	key := idempotencyKey(r, fields)
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	sheet, replayed, err := a.store.GenerateCountSheet(r.Context(), id, key)
	if err != nil {
		a.failed(w, r, "generate count sheet", err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeCountSheet(w, r, status, sheet)
}

// listCountSheets lists sheet headers, newest first, by plan and status.
func (a *API) listCountSheets(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.CountSheetFilter{PlanID: q.Get("planId"), Status: q.Get("status")}
	fields := map[string]string{}
	if f.PlanID != "" && !ids.Valid(f.PlanID) {
		fields["planId"] = "must be a UUID"
	}
	if f.Status != "" && !slices.Contains(domain.CountSheetStatuses, f.Status) {
		fields["status"] = "must be OPEN or POSTED"
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	sheets, err := a.store.ListCountSheets(r.Context(), f)
	if err != nil {
		a.failed(w, r, "list count sheets", err)
		return
	}
	items := make([]countSheetSummary, 0, len(sheets))
	for _, s := range sheets {
		items = append(items, summaryOfSheet(s))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getCountSheet(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeCountNotFound, "count sheet not found"))
		return
	}
	sheet, err := a.store.GetCountSheet(r.Context(), id)
	if err != nil {
		a.failed(w, r, "get count sheet", err)
		return
	}
	writeCountSheet(w, r, http.StatusOK, sheet)
}

// enterCount records one counted quantity; the sheet version comes from If-Match.
func (a *API) enterCount(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, lineID := chi.URLParam(r, "id"), chi.URLParam(r, "lineId")
	if !ids.Valid(id) || !ids.Valid(lineID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeCountNotFound, "count line not found"))
		return
	}
	version, err := ifMatchVersion(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req countLineRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	counted, err := decimal.NewFromString(strings.TrimSpace(req.CountedQty))
	if err != nil || counted.IsNegative() {
		fieldProblem(w, r, map[string]string{"countedQty": "must be a decimal number of zero or more"})
		return
	}
	sheet, err := a.store.EnterCount(r.Context(), id, lineID, version, counted)
	if err != nil {
		a.failed(w, r, "enter count", err)
		return
	}
	writeCountSheet(w, r, http.StatusOK, sheet)
}

// postCountSheet posts the variances. A caller holding inventory.count.approve
// approves variances above tolerance; without it they are refused (INV-CNT-001).
func (a *API) postCountSheet(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeCountNotFound, "count sheet not found"))
		return
	}
	version, err := ifMatchVersion(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	sheet, err := a.store.PostCountSheet(r.Context(), id, version, p.HasDuty(dutyCountApprove), a.post)
	if err != nil {
		a.failed(w, r, "post count sheet", err)
		return
	}
	writeCountSheet(w, r, http.StatusOK, sheet)
}

func decimalOr(raw string, def decimal.Decimal) (decimal.Decimal, error) {
	if strings.TrimSpace(raw) == "" {
		return def, nil
	}
	return decimal.NewFromString(strings.TrimSpace(raw))
}

func toCountPlanResponse(p domain.CountPlan) countPlanResponse {
	return countPlanResponse{ID: p.ID, LegalEntityID: p.LegalEntityID, Name: p.Name, Warehouse: p.Warehouse, Currency: p.Currency,
		Unit: p.Unit, TolerancePct: p.TolerancePct.String(), ToleranceValue: p.ToleranceValue.String(), Blind: p.Blind, CreatedAt: p.CreatedAt}
}

func summaryOfSheet(s domain.CountSheet) countSheetSummary {
	return countSheetSummary{ID: s.ID, LegalEntityID: s.LegalEntityID, PlanID: s.PlanID, PlanName: s.PlanName, Number: s.Number, Status: s.Status,
		Blind: s.Blind, ApprovedBy: s.ApprovedBy, JournalID: s.JournalID, Version: s.Version, LineCount: s.LineCount, CountedCount: s.CountedCount,
		CreatedAt: s.CreatedAt, PostedAt: s.PostedAt}
}

// writeCountSheet answers with the sheet. While a blind sheet is OPEN the
// expected quantities are withheld from callers without inventory.count.approve
// (FR-16.9 blind count).
func writeCountSheet(w http.ResponseWriter, r *http.Request, status int, s domain.CountSheet) {
	p, _ := tenant.FromContext(r.Context())
	hideExpected := s.Blind && s.Status == domain.CountOpen && !p.HasDuty(dutyCountApprove)
	resp := countSheetResponse{countSheetSummary: summaryOfSheet(s), Lines: []countLineResponse{}}
	for _, l := range s.Lines {
		line := countLineResponse{ID: l.ID, LineNo: l.LineNo, ProductID: l.ProductID, Dimensions: l.Dims, CountedBy: l.CountedBy, CountedAt: l.CountedAt}
		if !hideExpected {
			e := l.Expected.String()
			line.ExpectedQty = &e
		}
		if l.Counted != nil {
			c := l.Counted.String()
			line.CountedQty = &c
		}
		if l.PostedVariance != nil {
			v := l.PostedVariance.String()
			line.PostedVariance = &v
		}
		resp.Lines = append(resp.Lines, line)
	}
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(s.Version)))
	httpx.JSON(w, status, resp)
}
