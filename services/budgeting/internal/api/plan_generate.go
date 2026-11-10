package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
)

const (
	generatePriorPlan  = "prior-plan"
	generateGLActuals  = "gl-actuals"
	codeNoSourceData   = "BUD-0061"
	codeTooManyCells   = "BUD-0062"
	maxGenerateAccount = 50
)

type generateRequest struct {
	// Source is prior-plan (the approved scenario of the previous fiscal year) or gl-actuals (the posted balances of the
	// previous fiscal year read from the ledger).
	Source string `json:"source"`
	// AdjustmentPct scales every amount: 5 adds five percent, -10 takes ten off. Above -100 and at most 1000.
	AdjustmentPct *decimal.Decimal `json:"adjustmentPct"`
	// Accounts limits a prior-plan generation to these accounts, and names the accounts of a gl-actuals generation.
	Accounts []string `json:"accounts"`
	// CostCenters is how a gl-actuals amount (per account in the ledger) is shared over cost centers, by weight.
	CostCenters []struct {
		CostCenter string          `json:"costCenter"`
		Weight     decimal.Decimal `json:"weight"`
	} `json:"costCenters"`
}

// generateGrid fills a scenario from another source (FR-38.14): last year's approved plan or last year's posted
// actuals, scaled by a percentage, written like a grid write under If-Match.
func (a *API) generateGrid(w http.ResponseWriter, r *http.Request) {
	var req generateRequest
	id, composite, ok := planCommand(w, r, &req)
	if !ok {
		return
	}
	sid, ok := scenarioPathID(w, r)
	if !ok {
		return
	}
	version, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	fields := map[string]string{}
	if req.Source != generatePriorPlan && req.Source != generateGLActuals {
		fields["source"] = "must be prior-plan or gl-actuals"
	}
	factor := decimal.NewFromInt(1)
	if req.AdjustmentPct != nil {
		pct := *req.AdjustmentPct
		if !pct.GreaterThan(decimal.NewFromInt(-100)) || pct.GreaterThan(decimal.NewFromInt(1000)) || pct.Exponent() < -4 {
			fields["adjustmentPct"] = "must be above -100 and at most 1000, with at most 4 decimal places"
		} else {
			factor = decimal.NewFromInt(1).Add(pct.Div(decimal.NewFromInt(100)))
		}
	}
	seen := map[string]bool{}
	for i, acc := range req.Accounts {
		if n := len([]rune(acc)); n < 1 || n > 100 {
			fields[fmt.Sprintf("accounts[%d]", i)] = "must be 1 to 100 characters"
		}
		if seen[acc] {
			fields[fmt.Sprintf("accounts[%d]", i)] = "the account appears twice"
		}
		seen[acc] = true
	}
	if len(req.Accounts) > maxGenerateAccount {
		fields["accounts"] = fmt.Sprintf("at most %d accounts", maxGenerateAccount)
	}
	weights := make([]decimal.Decimal, len(req.CostCenters))
	if req.Source == generateGLActuals {
		if len(req.Accounts) == 0 {
			fields["accounts"] = "name the accounts to read from the ledger"
		}
		weightSum := decimal.Zero
		centers := map[string]bool{}
		for i, c := range req.CostCenters {
			if n := len([]rune(c.CostCenter)); n < 1 || n > 100 || centers[c.CostCenter] {
				fields[fmt.Sprintf("costCenters[%d].costCenter", i)] = "must be 1 to 100 characters and appear once"
			}
			centers[c.CostCenter] = true
			if c.Weight.Sign() < 0 || c.Weight.Exponent() < -4 {
				fields[fmt.Sprintf("costCenters[%d].weight", i)] = "must not be negative and have at most 4 decimal places"
			}
			weights[i] = c.Weight
			weightSum = weightSum.Add(c.Weight)
		}
		switch {
		case len(req.CostCenters) == 0 || len(req.CostCenters) > maxCostCenters:
			fields["costCenters"] = fmt.Sprintf("must hold 1 to %d cost centers", maxCostCenters)
		case weightSum.Sign() <= 0 && len(fields) == 0:
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeZeroWeights, "the cost center weights sum to zero: give at least one cost center a positive weight"))
			return
		}
		if len(req.Accounts)*len(req.CostCenters)*12 > maxGridCells {
			fields["costCenters"] = fmt.Sprintf("accounts times cost centers times 12 periods must not exceed %d cells", maxGridCells)
		}
	} else if len(req.CostCenters) > 0 {
		fields["costCenters"] = "is only used with source gl-actuals"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, hash := splitKey(composite)
	replayed, err := a.plans.Replayed(r.Context(), key, hash)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		a.replayGrid(w, r)(a.plans.SpreadCells(r.Context(), key, hash, id, sid, version, nil, false))
		return
	}
	plan, err := a.plans.GetPlan(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var cells []domain.PlanCell
	fallback := false
	switch req.Source {
	case generatePriorPlan:
		prior, err := a.plans.PriorPlanCells(r.Context(), id, req.Accounts)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		for _, c := range prior {
			// The previous year's YYYY-MM becomes the same month of this plan's year.
			c.Period = fmt.Sprintf("%d-%s", plan.FiscalYear, c.Period[5:])
			c.Amount = c.Amount.Mul(factor).Round(4)
			cells = append(cells, c)
		}
	default:
		periods := domain.Periods(plan.FiscalYear)
		for _, account := range req.Accounts {
			monthly, none, err := a.periodWeights(r.Context(), plan.Plan, account, spreadPriorYear)
			if err != nil {
				httpx.Fail(w, r, err)
				return
			}
			fallback = fallback || none
			if none {
				continue // nothing posted to this account last year: no cells, so no invented budget
			}
			for j, actual := range monthly {
				for i, share := range domain.SpreadWeighted(actual.Mul(factor).Round(4), weights) {
					cells = append(cells, domain.PlanCell{Account: account, CostCenter: req.CostCenters[i].CostCenter, Period: periods[j], Amount: share})
				}
			}
		}
	}
	if len(cells) == 0 {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeNoSourceData, noSourceDetail(req.Source, plan.FiscalYear-1)))
		return
	}
	if len(cells) > maxGridCells {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeTooManyCells, fmt.Sprintf("the source holds %d cells; a write takes at most %d: name fewer accounts", len(cells), maxGridCells)))
		return
	}
	a.replayGrid(w, r)(a.plans.SpreadCells(r.Context(), key, hash, id, sid, version, cells, fallback))
}

func noSourceDetail(source string, priorYear int) string {
	if source == generatePriorPlan {
		return fmt.Sprintf("no approved plan of fiscal year %d on this rule version holds amounts for these accounts", priorYear)
	}
	return strings.Join([]string{"the ledger holds no net debit posted to these accounts in fiscal year ", fmt.Sprint(priorYear)}, "")
}
