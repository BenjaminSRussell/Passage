package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/sources"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/store"
)

// Problem codes of plan generation from the drivers (FR-38.14).
const (
	codeSourceOff         = "BUD-0063" // the service the source reads is not configured
	codeSourceUnavailable = "BUD-0064" // the service did not answer, or answered something the plan cannot use
	codeSourceUnmapped    = "BUD-0065" // a department of HR has no cost center in the request
	maxSourceRows         = 200
)

type sourceRate struct {
	JobCode     string          `json:"jobCode"`
	MonthlyCost decimal.Decimal `json:"monthlyCost"`
}

type sourceDepartment struct {
	DepartmentID string `json:"departmentId"`
	CostCenter   string `json:"costCenter"`
}

type sourceProject struct {
	ProjectID  string `json:"projectId"`
	ModelID    string `json:"modelId"`
	CostCenter string `json:"costCenter"`
	// Amount is cost (the default) or sales: which amount of the forecast lines is planned.
	Amount string `json:"amount"`
}

type sourceUnitPrice struct {
	ItemID string          `json:"itemId"`
	Price  decimal.Decimal `json:"price"`
}

// PlanSources reads the drivers a plan is generated from (internal/sources).
type PlanSources interface {
	Depreciation(ctx context.Context, legalEntityID string, year int, currency, book string) (sources.Months, int, error)
	Positions(ctx context.Context, legalEntityID string, year int, rates []sources.PositionRate) ([]sources.PositionCost, error)
	ProjectForecast(ctx context.Context, legalEntityID, projectID, modelID string, year int, currency string, sales bool) (sources.Months, error)
	Demand(ctx context.Context, legalEntityID, modelCode string, year int, prices []sources.ItemPrice) (sources.Months, error)
}

// WithPlanSources gives plan generation the services it reads the fixed asset, position, project and demand drivers from.
func (a *API) WithPlanSources(s PlanSources) *API {
	a.sources = s
	return a
}

func isDriverSource(s string) bool {
	return s == generateAssets || s == generatePositions || s == generateProjects || s == generateDemand
}

// usesCostCenterWeights is true for the sources whose one monthly amount is shared over cost centers by weight.
func usesCostCenterWeights(s string) bool { return s == generateAssets || s == generateDemand }

// validateSourceParams checks what the request must say for its driver source.
func validateSourceParams(req generateRequest) map[string]string {
	f := map[string]string{}
	if n := len([]rune(req.Account)); n < 1 || n > 100 {
		f["account"] = "name the account the amounts are planned on (1 to 100 characters)"
	}
	if len(req.Accounts) > 0 {
		f["accounts"] = "is not used with this source; name the account in account"
	}
	switch req.Source {
	case generateAssets:
		if len(req.Book) > 50 {
			f["book"] = "at most 50 characters"
		}
	case generatePositions:
		if len(req.Rates) == 0 || len(req.Rates) > maxSourceRows {
			f["rates"] = fmt.Sprintf("give the monthly cost of a full-time position for each job code (1 to %d rows)", maxSourceRows)
		}
		jobs := map[string]bool{}
		for i, r := range req.Rates {
			if r.JobCode == "" || jobs[r.JobCode] {
				f[fmt.Sprintf("rates[%d].jobCode", i)] = "must be set once per job"
			}
			jobs[r.JobCode] = true
			if r.MonthlyCost.Sign() <= 0 || r.MonthlyCost.Exponent() < -4 {
				f[fmt.Sprintf("rates[%d].monthlyCost", i)] = "must be above 0 with at most 4 decimal places"
			}
		}
		if len(req.Departments) == 0 || len(req.Departments) > maxSourceRows {
			f["departments"] = fmt.Sprintf("map each HR department to a cost center (1 to %d rows)", maxSourceRows)
		}
		deps := map[string]bool{}
		for i, d := range req.Departments {
			if !ids.Valid(d.DepartmentID) || deps[d.DepartmentID] {
				f[fmt.Sprintf("departments[%d].departmentId", i)] = "must be a department id, once"
			}
			deps[d.DepartmentID] = true
			if n := len([]rune(d.CostCenter)); n < 1 || n > 100 {
				f[fmt.Sprintf("departments[%d].costCenter", i)] = "must be 1 to 100 characters"
			}
		}
	case generateProjects:
		if len(req.Projects) == 0 || len(req.Projects) > maxSourceRows {
			f["projects"] = fmt.Sprintf("name 1 to %d projects with their forecast model", maxSourceRows)
		}
		pairs := map[[2]string]bool{}
		for i, p := range req.Projects {
			if pairs[[2]string{p.ProjectID, p.ModelID}] {
				f[fmt.Sprintf("projects[%d].modelId", i)] = "the project and forecast model appear twice"
			}
			pairs[[2]string{p.ProjectID, p.ModelID}] = true
			if !ids.Valid(p.ProjectID) {
				f[fmt.Sprintf("projects[%d].projectId", i)] = "must be a project id"
			}
			if !ids.Valid(p.ModelID) {
				f[fmt.Sprintf("projects[%d].modelId", i)] = "must be a forecast model id"
			}
			if n := len([]rune(p.CostCenter)); n < 1 || n > 100 {
				f[fmt.Sprintf("projects[%d].costCenter", i)] = "must be 1 to 100 characters"
			}
			if p.Amount != "" && p.Amount != "cost" && p.Amount != "sales" {
				f[fmt.Sprintf("projects[%d].amount", i)] = "must be cost or sales"
			}
		}
	case generateDemand:
		if req.ModelCode == "" || len(req.ModelCode) > 100 {
			f["modelCode"] = "name the planning forecast model"
		}
		if len(req.UnitPrices) == 0 || len(req.UnitPrices) > maxSourceRows {
			f["unitPrices"] = fmt.Sprintf("give the price of one unit of each item (1 to %d rows)", maxSourceRows)
		}
		items := map[string]bool{}
		for i, p := range req.UnitPrices {
			if !ids.Valid(p.ItemID) || items[p.ItemID] {
				f[fmt.Sprintf("unitPrices[%d].itemId", i)] = "must be an item id, once"
			}
			items[p.ItemID] = true
			if p.Price.Sign() <= 0 || p.Price.Exponent() < -4 {
				f[fmt.Sprintf("unitPrices[%d].price", i)] = "must be above 0 with at most 4 decimal places"
			}
		}
	}
	return f
}

func sourceProblem(err error) error {
	var se *sources.Error
	switch {
	case errors.Is(err, sources.ErrNotConfigured):
		return httpx.NewProblem(http.StatusServiceUnavailable, codeSourceOff, "the service this source reads is not configured for budgeting")
	case errors.As(err, &se):
		return httpx.NewProblem(http.StatusUnprocessableEntity, codeSourceUnavailable, se.Detail)
	}
	return err
}

// driverCells turns the months a driver source answers into plan cells on the request's account: shared over cost centers by
// weight (fixed assets, demand) or on the cost center each department or project maps to (positions, projects). Every amount is
// scaled by the adjustment and rounded once to four decimals; a source that holds nothing for the year gives no cells.
func (a *API) driverCells(ctx context.Context, plan store.Plan, req generateRequest, factor decimal.Decimal, weights []decimal.Decimal) ([]domain.PlanCell, error) {
	if a.sources == nil {
		return nil, httpx.NewProblem(http.StatusServiceUnavailable, codeSourceOff, "plan generation from the fixed assets, positions, projects and demand is not configured for budgeting")
	}
	year, le := plan.FiscalYear, plan.LegalEntityID
	byCenter := map[string]sources.Months{}
	add := func(center string, m sources.Months) {
		if byCenter[center] == nil {
			byCenter[center] = sources.Months{}
		}
		for p, v := range m {
			byCenter[center].Add(p, v)
		}
	}
	var cells []domain.PlanCell
	switch req.Source {
	case generateAssets, generateDemand:
		var months sources.Months
		var err error
		if req.Source == generateAssets {
			months, _, err = a.sources.Depreciation(ctx, le, year, plan.Currency, req.Book)
		} else {
			prices := make([]sources.ItemPrice, len(req.UnitPrices))
			for i, p := range req.UnitPrices {
				prices[i] = sources.ItemPrice{ItemID: p.ItemID, Price: p.Price}
			}
			months, err = a.sources.Demand(ctx, le, req.ModelCode, year, prices)
		}
		if err != nil {
			return nil, sourceProblem(err)
		}
		for _, period := range months.Periods() {
			for i, share := range domain.SpreadWeighted(months[period].Mul(factor).Round(4), weights) {
				cells = append(cells, domain.PlanCell{Account: req.Account, CostCenter: req.CostCenters[i].CostCenter, Period: period, Amount: share})
			}
		}
		return nonZero(cells), nil
	case generatePositions:
		rates := make([]sources.PositionRate, len(req.Rates))
		for i, r := range req.Rates {
			rates[i] = sources.PositionRate{JobCode: r.JobCode, MonthlyCost: r.MonthlyCost}
		}
		costs, err := a.sources.Positions(ctx, le, year, rates)
		if err != nil {
			return nil, sourceProblem(err)
		}
		center := map[string]string{}
		for _, d := range req.Departments {
			center[d.DepartmentID] = d.CostCenter
		}
		for _, c := range costs {
			cc, ok := center[c.DepartmentID]
			if !ok {
				return nil, httpx.NewProblem(http.StatusUnprocessableEntity, codeSourceUnmapped, "HR department "+c.DepartmentID+" has positions in this year and no cost center in departments")
			}
			add(cc, c.Months)
		}
	case generateProjects:
		for _, p := range req.Projects {
			m, err := a.sources.ProjectForecast(ctx, le, p.ProjectID, p.ModelID, year, plan.Currency, p.Amount == "sales")
			if err != nil {
				return nil, sourceProblem(err)
			}
			add(p.CostCenter, m)
		}
	}
	centers := make([]string, 0, len(byCenter))
	for c := range byCenter {
		centers = append(centers, c)
	}
	sort.Strings(centers)
	for _, c := range centers {
		for _, period := range byCenter[c].Periods() {
			cells = append(cells, domain.PlanCell{Account: req.Account, CostCenter: c, Period: period, Amount: byCenter[c][period].Mul(factor).Round(4)})
		}
	}
	return nonZero(cells), nil
}

func nonZero(cells []domain.PlanCell) []domain.PlanCell {
	out := cells[:0]
	for _, c := range cells {
		if !c.Amount.IsZero() {
			out = append(out, c)
		}
	}
	return out
}
