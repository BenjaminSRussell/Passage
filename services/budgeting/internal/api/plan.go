package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/store"
)

const (
	dutyPlanMaintain = "budget.plan.maintain"
	dutyPlanWorkflow = "budget.plan.workflow"
	maxGridCells     = 5000
	maxOwners        = 200
	maxCostCenters   = 200

	codeLedgerNotSet     = "BUD-0054"
	codeLedgerRefused    = "BUD-0055"
	codeLedgerUnreadable = "BUD-0056"
	codePreconditionReq  = "COM-0428"
	codeFiscalCalendar   = "BUD-0058"
	codeZeroWeights      = "BUD-0060"
	spreadEven           = "even"
	spreadPriorYear      = "prior-year-actuals"
	spreadCustom         = "custom"
)

var (
	periodFormat = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}$`)
	stagePaths   = map[string]string{"review": domain.StageReview, "approve": domain.StageApprove}
)

// PlanStore is the planning persistence surface.
type PlanStore interface {
	CreatePlan(ctx context.Context, key, hash string, in store.NewPlan) (int, []byte, error)
	SetStageOwners(ctx context.Context, key, hash, planID string, owners []store.StageOwnerInput) (int, []byte, error)
	CreateScenario(ctx context.Context, key, hash, planID string, in store.NewScenario) (int, []byte, error)
	UpsertCells(ctx context.Context, key, hash, planID, scenarioID string, version int64, cells []domain.PlanCell) (int, []byte, error)
	SpreadCells(ctx context.Context, key, hash, planID, scenarioID string, version int64, cells []domain.PlanCell, priorYearFallback bool) (int, []byte, error)
	SubmitPlan(ctx context.Context, key, hash, planID string) (int, []byte, error)
	SignOff(ctx context.Context, key, hash string, in store.SignOffRequest) (int, []byte, error)
	RejectPlan(ctx context.Context, key, hash string, in store.RejectRequest) (int, []byte, error)
	ListPlans(ctx context.Context, legalEntityID string) ([]store.Plan, error)
	GetPlan(ctx context.Context, id string) (store.PlanView, error)
	GetGrid(ctx context.Context, planID, scenarioID string, f store.GridFilter) (store.Grid, error)
	CompareScenarios(ctx context.Context, planID, baseID, otherID, groupBy string) (store.Comparison, error)
	PriorPlanCells(ctx context.Context, planID string, accounts []string) ([]domain.PlanCell, error)
	Replayed(ctx context.Context, key, hash string) (bool, error)
}

// PriorActuals reads the ledger's posted balances for the prior-year spreading helper.
type PriorActuals interface {
	Balances(ctx context.Context, q ledgerclient.BalanceQuery) (ledgerclient.AccountBalances, error)
}

// WithPlanning mounts the budget planning routes. gl may be nil: the prior-year helper then answers 503.
func (a *API) WithPlanning(ps PlanStore, gl PriorActuals) *API {
	a.plans, a.ledger = ps, gl
	return a
}

func (a *API) registerPlans(r chi.Router) {
	maintain := r.With(authz.RequireDuty(dutyPlanMaintain))
	maintain.Post("/v1/budget/plans", a.createPlan)
	maintain.Put("/v1/budget/plans/{id}/stage-owners", a.setStageOwners)
	maintain.Post("/v1/budget/plans/{id}/scenarios", a.createScenario)
	maintain.Put("/v1/budget/plans/{id}/scenarios/{sid}/grid", a.putGrid)
	maintain.Post("/v1/budget/plans/{id}/scenarios/{sid}:spread", a.spreadGrid)
	maintain.Post("/v1/budget/plans/{id}/scenarios/{sid}:generate", a.generateGrid)
	maintain.Post("/v1/budget/plans/{id}:submit", a.submitPlan)

	workflow := r.With(authz.RequireDuty(dutyPlanWorkflow))
	workflow.Post("/v1/budget/plans/{id}/stages/{stage}:sign-off", a.signOff)
	workflow.Post("/v1/budget/plans/{id}:reject", a.rejectPlan)

	view := r.With(authz.RequireDuty(dutyView))
	view.Get("/v1/budget/plans", a.listPlans)
	view.Get("/v1/budget/plans/{id}", a.getPlan)
	view.Get("/v1/budget/plans/{id}/scenarios/{sid}/grid", a.getGrid)
	view.Get("/v1/budget/plans/{id}/scenarios:compare", a.compareScenarios)
}

func planPathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodePlanNotFound, "budget plan not found"))
		return "", false
	}
	return id, true
}

func scenarioPathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "sid")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeScenarioNotFound, "scenario not found in this plan"))
		return "", false
	}
	return id, true
}

// planCommand reads the plan id, the body and the idempotency key and decodes the body into v.
func planCommand(w http.ResponseWriter, r *http.Request, v any) (id, composite string, ok bool) {
	if id, ok = planPathID(w, r); !ok {
		return "", "", false
	}
	body, composite, ok := readCommand(w, r)
	if !ok {
		return "", "", false
	}
	if v == nil {
		return id, composite, true
	}
	if err := decodeBody(body, v); err != nil {
		httpx.Fail(w, r, err)
		return "", "", false
	}
	return id, composite, true
}

type createPlanRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	RuleVersionID string `json:"ruleVersionId"`
	LedgerID      string `json:"ledgerId"`
	Name          string `json:"name"`
	FiscalYear    int    `json:"fiscalYear"`

	FiscalYearStartMonth *int `json:"fiscalYearStartMonth"`
}

func (a *API) createPlan(w http.ResponseWriter, r *http.Request) {
	body, composite, ok := readCommand(w, r)
	if !ok {
		return
	}
	var req createPlanRequest
	if err := decodeBody(body, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.RuleVersionID) {
		fields["ruleVersionId"] = "must be a UUID"
	}
	if req.LedgerID != "" && !ids.Valid(req.LedgerID) {
		fields["ledgerId"] = "must be a UUID"
	}
	if n := len([]rune(strings.TrimSpace(req.Name))); n < 1 || n > 200 {
		fields["name"] = "must be 1 to 200 characters"
	}
	if !validYear(req.FiscalYear) {
		fields["fiscalYear"] = "must be a year between 1900 and 2999"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if req.FiscalYearStartMonth != nil && *req.FiscalYearStartMonth != 1 {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeFiscalCalendar, "plan periods are calendar months of the fiscal year number; a fiscal year that does not start in January is not supported"))
		return
	}
	key, hash := splitKey(composite)
	a.replay(w, r)(a.plans.CreatePlan(r.Context(), key, hash, store.NewPlan{
		LegalEntityID: req.LegalEntityID, RuleVersionID: req.RuleVersionID, LedgerID: req.LedgerID, Name: strings.TrimSpace(req.Name), FiscalYear: req.FiscalYear,
	}))
}

type stageOwnerRequest struct {
	Stage     string `json:"stage"`
	OrgUnitID string `json:"orgUnitId"`
	Owner     string `json:"owner"`
}

func (a *API) setStageOwners(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Owners []stageOwnerRequest `json:"owners"`
	}
	id, composite, ok := planCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	if len(req.Owners) > maxOwners {
		fields["owners"] = fmt.Sprintf("must hold at most %d entries", maxOwners)
	}
	owners := make([]store.StageOwnerInput, 0, len(req.Owners))
	for i, o := range req.Owners {
		prefix := fmt.Sprintf("owners[%d].", i)
		if !domain.ValidStage(o.Stage) {
			fields[prefix+"stage"] = "must be Review or Approve"
		}
		if !ids.Valid(o.OrgUnitID) {
			fields[prefix+"orgUnitId"] = "must be a UUID"
		}
		if n := len([]rune(o.Owner)); n < 1 || n > 200 {
			fields[prefix+"owner"] = "must be 1 to 200 characters"
		}
		owners = append(owners, store.StageOwnerInput{Stage: o.Stage, OrgUnitID: o.OrgUnitID, Owner: o.Owner})
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, hash := splitKey(composite)
	a.replay(w, r)(a.plans.SetStageOwners(r.Context(), key, hash, id, owners))
}

type createScenarioRequest struct {
	Name                 string          `json:"name"`
	Kind                 string          `json:"kind"`
	CopyFromScenarioID   string          `json:"copyFromScenarioId"`
	AdjustmentPercentage decimal.Decimal `json:"adjustmentPct"`
}

func (a *API) createScenario(w http.ResponseWriter, r *http.Request) {
	var req createScenarioRequest
	id, composite, ok := planCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	if n := len([]rune(strings.TrimSpace(req.Name))); n < 1 || n > 100 {
		fields["name"] = "must be 1 to 100 characters"
	}
	if !domain.ValidScenarioKind(req.Kind) {
		fields["kind"] = "must be Baseline, Optimistic, Pessimistic or Custom"
	}
	if req.CopyFromScenarioID != "" && !ids.Valid(req.CopyFromScenarioID) {
		fields["copyFromScenarioId"] = "must be a UUID"
	}
	if req.AdjustmentPercentage.LessThanOrEqual(decimal.NewFromInt(-100)) || req.AdjustmentPercentage.GreaterThan(decimal.NewFromInt(1000)) || req.AdjustmentPercentage.Exponent() < -4 {
		fields["adjustmentPct"] = "must be above -100 and at most 1000, with at most 4 decimal places"
	}
	if !req.AdjustmentPercentage.IsZero() && req.CopyFromScenarioID == "" {
		fields["adjustmentPct"] = "applies to a copied scenario only"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, hash := splitKey(composite)
	a.replay(w, r)(a.plans.CreateScenario(r.Context(), key, hash, id, store.NewScenario{
		Name: strings.TrimSpace(req.Name), Kind: req.Kind, CopyFromID: req.CopyFromScenarioID, AdjustmentPct: req.AdjustmentPercentage,
	}))
}

type gridCellRequest struct {
	Account    string          `json:"account"`
	CostCenter string          `json:"costCenter"`
	Period     string          `json:"period"`
	Amount     decimal.Decimal `json:"amount"`
}

func gridKey(account, costCenter, period string) string {
	return account + "\x00" + costCenter + "\x00" + period
}

// ifMatchVersion reads the grid ETag from If-Match; false means a problem was written.
func ifMatchVersion(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	if raw == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, codePreconditionReq, "If-Match is required: send the ETag of the grid you read"))
		return 0, false
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 1 {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "If-Match must carry the grid ETag"))
		return 0, false
	}
	return v, true
}

func etag(version int64) string { return `"` + strconv.FormatInt(version, 10) + `"` }

// replayGrid is replay for a grid write: the new ETag rides on the response.
func (a *API) replayGrid(w http.ResponseWriter, r *http.Request) func(int, []byte, error) {
	return func(status int, raw []byte, err error) {
		var written store.GridWritten
		if err == nil && json.Unmarshal(raw, &written) == nil && written.Version > 0 {
			w.Header().Set("ETag", etag(written.Version))
		}
		a.replay(w, r)(status, raw, err)
	}
}

func (a *API) putGrid(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cells []gridCellRequest `json:"cells"`
	}
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
	cells, fields := gridCells(req.Cells)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, hash := splitKey(composite)
	a.replayGrid(w, r)(a.plans.UpsertCells(r.Context(), key, hash, id, sid, version, cells))
}

func gridCells(in []gridCellRequest) ([]domain.PlanCell, map[string]string) {
	fields := map[string]string{}
	if len(in) == 0 || len(in) > maxGridCells {
		fields["cells"] = fmt.Sprintf("must hold 1 to %d cells", maxGridCells)
	}
	cells := make([]domain.PlanCell, 0, len(in))
	seen := make(map[string]bool, len(in))
	for i, c := range in {
		prefix := fmt.Sprintf("cells[%d].", i)
		if n := len([]rune(c.Account)); n < 1 || n > 100 {
			fields[prefix+"account"] = "must be 1 to 100 characters"
		}
		if n := len([]rune(c.CostCenter)); n < 1 || n > 100 {
			fields[prefix+"costCenter"] = "must be 1 to 100 characters"
		}
		if !periodFormat.MatchString(c.Period) {
			fields[prefix+"period"] = "must be a period as YYYY-MM"
		}
		if msg := moneyField(c.Amount); msg != "" {
			fields[prefix+"amount"] = msg
		}
		k := gridKey(c.Account, c.CostCenter, c.Period)
		if seen[k] {
			fields[prefix+"account"] = "the account, cost center and period appear twice in the request"
		}
		seen[k] = true
		cells = append(cells, domain.PlanCell{Account: c.Account, CostCenter: c.CostCenter, Period: c.Period, Amount: c.Amount})
	}
	return cells, fields
}

type spreadRequest struct {
	Account string          `json:"account"`
	Total   decimal.Decimal `json:"total"`
	Method  string          `json:"method"`
	// PeriodWeights is the distribution key for method custom: 12 weights, one per period of the fiscal year (a seasonal curve).
	PeriodWeights []decimal.Decimal `json:"periodWeights"`
	CostCenters   []struct {
		CostCenter string          `json:"costCenter"`
		Weight     decimal.Decimal `json:"weight"`
	} `json:"costCenters"`
}

// spreadGrid allocates a total over cost centers by weight and spreads each share over the 12 periods, evenly or in the
// proportions of the account's posted balances in the prior fiscal year, then writes the cells like a grid write.
func (a *API) spreadGrid(w http.ResponseWriter, r *http.Request) {
	var req spreadRequest
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
	if n := len([]rune(req.Account)); n < 1 || n > 100 {
		fields["account"] = "must be 1 to 100 characters"
	}
	if msg := moneyField(req.Total); msg != "" {
		fields["total"] = msg
	}
	if req.Method != spreadEven && req.Method != spreadPriorYear && req.Method != spreadCustom {
		fields["method"] = "must be even, prior-year-actuals or custom"
	}
	if req.Method == spreadCustom {
		if len(req.PeriodWeights) != 12 {
			fields["periodWeights"] = "must hold exactly 12 weights, one per period"
		} else {
			sum := decimal.Zero
			for _, pw := range req.PeriodWeights {
				if pw.Sign() < 0 || pw.Exponent() < -4 {
					fields["periodWeights"] = "must not be negative and have at most 4 decimal places"
				}
				sum = sum.Add(pw)
			}
			if fields["periodWeights"] == "" && sum.Sign() <= 0 {
				fields["periodWeights"] = "must not all be zero"
			}
		}
	} else if len(req.PeriodWeights) > 0 {
		fields["periodWeights"] = "is only used with method custom"
	}
	if len(req.CostCenters) == 0 || len(req.CostCenters) > maxCostCenters {
		fields["costCenters"] = fmt.Sprintf("must hold 1 to %d cost centers", maxCostCenters)
	}
	weights := make([]decimal.Decimal, len(req.CostCenters))
	weightSum := decimal.Zero
	seenCenters := make(map[string]bool, len(req.CostCenters))
	for i, c := range req.CostCenters {
		if n := len([]rune(c.CostCenter)); n < 1 || n > 100 {
			fields[fmt.Sprintf("costCenters[%d].costCenter", i)] = "must be 1 to 100 characters"
		}
		if seenCenters[c.CostCenter] {
			fields[fmt.Sprintf("costCenters[%d].costCenter", i)] = "the cost center appears twice in the request"
		}
		seenCenters[c.CostCenter] = true
		if c.Weight.Sign() < 0 || c.Weight.Exponent() < -4 {
			fields[fmt.Sprintf("costCenters[%d].weight", i)] = "must not be negative and have at most 4 decimal places"
		}
		weights[i] = c.Weight
		weightSum = weightSum.Add(c.Weight)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if weightSum.Sign() <= 0 {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeZeroWeights, "the cost center weights sum to zero: give at least one cost center a positive weight"))
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
	if plan.Status != domain.PlanDraft {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, store.CodePlanNotDraft, fmt.Sprintf("the plan is %s; only a Draft plan can be changed", plan.Status)))
		return
	}
	periodWeights, fallback := req.PeriodWeights, false
	if req.Method != spreadCustom {
		periodWeights, fallback, err = a.periodWeights(r.Context(), plan.Plan, req.Account, req.Method)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	periods := domain.Periods(plan.FiscalYear)
	cells := make([]domain.PlanCell, 0, len(req.CostCenters)*len(periods))
	for i, share := range domain.SpreadWeighted(req.Total, weights) {
		for j, amount := range domain.SpreadWeighted(share, periodWeights) {
			cells = append(cells, domain.PlanCell{Account: req.Account, CostCenter: req.CostCenters[i].CostCenter, Period: periods[j], Amount: amount})
		}
	}
	a.replayGrid(w, r)(a.plans.SpreadCells(r.Context(), key, hash, id, sid, version, cells, fallback))
}

// periodWeights is 1 for each period (even), or the net debit (debit less credit, never below zero) of the account's posted balance in each month of
// the prior fiscal year. The ledger balance is per account: it cannot be narrowed to a cost center. fallback reports that the prior year held no
// net debit at all, so the weights are the even ones.
func (a *API) periodWeights(ctx context.Context, plan store.Plan, account, method string) (weights []decimal.Decimal, fallback bool, err error) {
	periods := domain.Periods(plan.FiscalYear)
	weights = make([]decimal.Decimal, len(periods))
	if method == spreadEven {
		for i := range weights {
			weights[i] = decimal.NewFromInt(1)
		}
		return weights, false, nil
	}
	if plan.LedgerID == "" {
		return nil, false, httpx.NewProblem(http.StatusUnprocessableEntity, codeLedgerNotSet, "the plan names no ledger: set ledgerId on the plan to spread by prior-year actuals")
	}
	if a.ledger == nil {
		return nil, false, httpx.NewProblem(http.StatusServiceUnavailable, codeLedgerNotSet, "the general ledger is not configured for this service")
	}
	total := decimal.Zero
	for i, period := range domain.Periods(plan.FiscalYear - 1) {
		set, err := a.ledger.Balances(ctx, ledgerclient.BalanceQuery{LegalEntityID: plan.LegalEntityID, LedgerID: plan.LedgerID, Account: account, AccountTo: account,
			PeriodFrom: period, PeriodTo: period})
		if err != nil {
			slog.ErrorContext(ctx, "ledger balance read for prior-year spreading failed", "plan", plan.ID, "period", period, "err", err)
			if ledgerclient.Rejected(err) {
				return nil, false, httpx.NewProblem(http.StatusUnprocessableEntity, codeLedgerRefused, "the general ledger refused the balance read: "+ledgerclient.CodeOf(err))
			}
			return nil, false, httpx.NewProblem(http.StatusServiceUnavailable, codeLedgerUnreadable, "the general ledger could not be read")
		}
		for _, row := range set.Rows {
			if row.Account == account {
				weights[i] = weights[i].Add(decimal.Max(row.Net(), decimal.Zero))
			}
		}
		total = total.Add(weights[i])
	}
	return weights, total.IsZero(), nil
}

func (a *API) submitPlan(w http.ResponseWriter, r *http.Request) {
	id, composite, ok := planCommand(w, r, nil)
	if !ok {
		return
	}
	key, hash := splitKey(composite)
	a.replay(w, r)(a.plans.SubmitPlan(r.Context(), key, hash, id))
}

type signOffRequest struct {
	OrgUnitID  string `json:"orgUnitId"`
	ScenarioID string `json:"scenarioId"`
	Submission int    `json:"submission"`
}

func (a *API) signOff(w http.ResponseWriter, r *http.Request) {
	var req signOffRequest
	id, composite, ok := planCommand(w, r, &req)
	if !ok {
		return
	}
	stage, known := stagePaths[chi.URLParam(r, "stage")]
	if !known {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeStageNotOpen, "stage must be review or approve"))
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.OrgUnitID) {
		fields["orgUnitId"] = "must be a UUID"
	}
	if stage == domain.StageApprove && !ids.Valid(req.ScenarioID) {
		fields["scenarioId"] = "must be the UUID of the scenario being approved"
	}
	if stage == domain.StageReview && req.ScenarioID != "" {
		fields["scenarioId"] = "applies to the approve stage only"
	}
	if req.Submission < 1 {
		fields["submission"] = "must be the submission number the workflow was started for"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, hash := splitKey(composite)
	a.replay(w, r)(a.plans.SignOff(r.Context(), key, hash, store.SignOffRequest{PlanID: id, Stage: stage, OrgUnitID: req.OrgUnitID, ScenarioID: req.ScenarioID, Submission: req.Submission}))
}

type rejectRequest struct {
	Reason     string `json:"reason"`
	Submission int    `json:"submission"`
}

func (a *API) rejectPlan(w http.ResponseWriter, r *http.Request) {
	var req rejectRequest
	id, composite, ok := planCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	if n := len([]rune(strings.TrimSpace(req.Reason))); n < 1 || n > 500 {
		fields["reason"] = "must be 1 to 500 characters"
	}
	if req.Submission < 1 {
		fields["submission"] = "must be the submission number the workflow was started for"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, hash := splitKey(composite)
	a.replay(w, r)(a.plans.RejectPlan(r.Context(), key, hash, store.RejectRequest{PlanID: id, Reason: strings.TrimSpace(req.Reason), Submission: req.Submission}))
}

func (a *API) listPlans(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if le != "" && !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	rows, err := a.plans.ListPlans(r.Context(), le)
	list(w, r, rows, err)
}

func (a *API) getPlan(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := planPathID(w, r)
	if !ok {
		return
	}
	v, err := a.plans.GetPlan(r.Context(), id)
	one(w, r, v, err)
}

func (a *API) getGrid(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := planPathID(w, r)
	if !ok {
		return
	}
	sid, ok := scenarioPathID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	if p := q.Get("period"); p != "" && !periodFormat.MatchString(p) {
		validationFailed(w, r, map[string]string{"period": "must be a period as YYYY-MM"})
		return
	}
	g, err := a.plans.GetGrid(r.Context(), id, sid, store.GridFilter{Account: q.Get("account"), CostCenter: q.Get("costCenter"), Period: q.Get("period")})
	if err == nil {
		w.Header().Set("ETag", etag(g.Scenario.Version))
	}
	one(w, r, g, err)
}

// compareScenarios sets two scenarios of a plan side by side (FR-38.2): base and other are scenario ids, groupBy is
// account (default), costCenter or period.
func (a *API) compareScenarios(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := planPathID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	for _, name := range []string{"base", "other"} {
		if !ids.Valid(q.Get(name)) {
			fields[name] = "must be a scenario id (UUID)"
		}
	}
	groupBy := q.Get("groupBy")
	if groupBy == "" {
		groupBy = "account"
	}
	if !store.ValidComparisonGroup(groupBy) {
		fields["groupBy"] = "must be account, costCenter or period"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	c, err := a.plans.CompareScenarios(r.Context(), id, q.Get("base"), q.Get("other"), groupBy)
	one(w, r, c, err)
}
