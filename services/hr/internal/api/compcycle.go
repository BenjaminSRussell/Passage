package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const (
	maxCycleBudgets  = 500
	maxWorksheetRows = 200
)

func (a *API) registerCompCycles(r chi.Router) {
	admin := r.With(authz.RequireDuty(dutyCompAdmin))
	notFound := func(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
		return idRoute(store.CodeCycleNotFound, "compensation cycle not found", next)
	}
	admin.Post("/v1/hr/compensation/cycles", a.createCycle)
	admin.Get("/v1/hr/compensation/cycles", a.listCycles)
	admin.Get("/v1/hr/compensation/cycles/{id}", notFound(a.getCycle))
	admin.Post("/v1/hr/compensation/cycles/{id}/advance", notFound(a.advanceCycle))
	admin.Post("/v1/hr/compensation/cycles/{id}/apply", notFound(a.applyCycle))
	admin.Put("/v1/hr/compensation/cycles/{id}/worksheets/{managerId}", notFound(a.saveWorksheet))
	admin.Get("/v1/hr/compensation/cycles/{id}/worksheets/{managerId}", notFound(a.getWorksheet))
	admin.Post("/v1/hr/compensation/cycles/{id}/worksheets/{managerId}/submit", notFound(a.submitWorksheet))
}

type cycleRequest struct {
	Name            string                `json:"name"`
	Kind            string                `json:"kind"`
	EffectiveDate   string                `json:"effectiveDate"`
	Currency        string                `json:"currency"`
	AllowOverBudget bool                  `json:"allowOverBudget"`
	GuidelineMatrix []domain.GuidelineRow `json:"guidelineMatrix"`
	Budgets         []store.CycleBudget   `json:"budgets"`
}

func (a *API) createCycle(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req cycleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	in := store.CompCycle{Kind: req.Kind, Currency: req.Currency, AllowOverBudget: req.AllowOverBudget, GuidelineMatrix: req.GuidelineMatrix}
	var ok bool
	if in.Name, ok = pnText(req.Name, 120); !ok {
		fields["name"] = "is required, at most 120 characters"
	}
	if !contains([]string{"MERIT", "BONUS", "BOTH"}, req.Kind) {
		fields["kind"] = "must be MERIT, BONUS or BOTH"
	}
	if !pnCurrency.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	if d, valid := parseDate(req.EffectiveDate); valid {
		in.EffectiveDate = d.Format(dateLayout)
	} else {
		fields["effectiveDate"] = "is required and must be a date as YYYY-MM-DD"
	}
	if len(req.Budgets) == 0 || len(req.Budgets) > maxCycleBudgets {
		fields["budgets"] = "needs 1 to 500 department budgets"
	} else if pnCurrency.MatchString(req.Currency) {
		seen := map[string]bool{}
		for _, b := range req.Budgets {
			amount, valid := pnMoney(b.BudgetAmount, req.Currency)
			if !ids.Valid(b.DepartmentID) || !valid || seen[b.DepartmentID] {
				fields["budgets"] = "each budget needs a distinct department UUID and a non-negative amount in the cycle currency"
				break
			}
			seen[b.DepartmentID] = true
			in.Budgets = append(in.Budgets, store.CycleBudget{DepartmentID: b.DepartmentID, BudgetAmount: amount.String()})
		}
	}
	if err := domain.ValidateMatrix(req.GuidelineMatrix); err != nil {
		fields["guidelineMatrix"] = err.(*domain.Error).Detail
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, inserted, err := a.store.CreateCompCycle(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		a.fail(w, r, "create compensation cycle", err)
		return
	}
	writeCreated(w, out, inserted, &a.personnel.cycles)
}

func (a *API) listCycles(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, ok := pageParams(r)
	if !ok {
		badPage(w, r)
		return
	}
	items, next, err := a.store.ListCompCycles(r.Context(), after, limit)
	if err != nil {
		a.fail(w, r, "list compensation cycles", err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.CompCycle]{Items: items, NextCursor: cursor(next)})
}

func (a *API) getCycle(w http.ResponseWriter, r *http.Request, id string) {
	out, err := a.store.GetCompCycle(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get compensation cycle", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) advanceCycle(w http.ResponseWriter, r *http.Request, id string) {
	to, ok := decodeMove(w, r, "MANAGER_INPUT", "APPROVAL", "CANCELED")
	if !ok {
		return
	}
	out, err := a.store.AdvanceCompCycle(r.Context(), id, to)
	if err != nil {
		a.fail(w, r, "advance compensation cycle", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) applyCycle(w http.ResponseWriter, r *http.Request, id string) {
	out, actionIDs, err := a.store.ApplyCompCycle(r.Context(), id)
	if err != nil {
		a.fail(w, r, "apply compensation cycle", err)
		return
	}
	a.personnel.cycleApplies.Add(1)
	httpx.JSON(w, http.StatusOK, struct {
		store.CompCycle
		ActionIDs []string `json:"actionIds"`
	}{out, actionIDs})
}

type worksheetRowRequest struct {
	WorkerID      string  `json:"workerId"`
	Rating        int     `json:"rating"`
	ProposedPct   string  `json:"proposedPct"`
	Justification *string `json:"justification"`
}

type worksheetRequest struct {
	Rows []worksheetRowRequest `json:"rows"`
}

var (
	pctLow  = decimal.NewFromInt(-100)
	pctHigh = decimal.NewFromInt(100)
)

func managerParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "managerId")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeWorkerNotFound, "manager not found"))
		return "", false
	}
	return id, true
}

func (a *API) saveWorksheet(w http.ResponseWriter, r *http.Request, cycleID string) {
	manager, ok := managerParam(w, r)
	if !ok {
		return
	}
	var req worksheetRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(req.Rows) == 0 || len(req.Rows) > maxWorksheetRows {
		validationFailed(w, r, map[string]string{"rows": "needs 1 to 200 rows"})
		return
	}
	fields := map[string]string{}
	seen := map[string]bool{}
	in := make([]store.WorksheetRowInput, 0, len(req.Rows))
	for _, row := range req.Rows {
		pct, err := decimal.NewFromString(strings.TrimSpace(row.ProposedPct))
		justification, jOK := pnOptText(row.Justification, 1000)
		switch {
		case !ids.Valid(row.WorkerID) || seen[row.WorkerID]:
			fields["rows"] = "each row needs a distinct worker UUID"
		case row.Rating < 1 || row.Rating > 5:
			fields["rows"] = "rating must be 1 to 5"
		case err != nil || pct.LessThan(pctLow) || pct.GreaterThan(pctHigh) || !pct.Equal(pct.Round(3)):
			fields["rows"] = "proposedPct must be a decimal string from -100 to 100 with at most three decimals"
		case !jOK:
			fields["rows"] = "justification must be at most 1000 characters"
		default:
			seen[row.WorkerID] = true
			in = append(in, store.WorksheetRowInput{WorkerID: row.WorkerID, Rating: row.Rating, ProposedPct: pct.String(), Justification: justification})
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	rows, err := a.store.SaveWorksheet(r.Context(), cycleID, manager, in)
	if err != nil {
		a.fail(w, r, "save worksheet", err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.WorksheetRow]{Items: rows})
}

func (a *API) getWorksheet(w http.ResponseWriter, r *http.Request, cycleID string) {
	manager, ok := managerParam(w, r)
	if !ok {
		return
	}
	rows, err := a.store.GetWorksheet(r.Context(), cycleID, manager)
	if err != nil {
		a.fail(w, r, "get worksheet", err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.WorksheetRow]{Items: rows})
}

func (a *API) submitWorksheet(w http.ResponseWriter, r *http.Request, cycleID string) {
	manager, ok := managerParam(w, r)
	if !ok {
		return
	}
	rows, err := a.store.SubmitWorksheet(r.Context(), cycleID, manager)
	if err != nil {
		a.fail(w, r, "submit worksheet", err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.WorksheetRow]{Items: rows})
}
