package api

import (
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/lean/internal/domain"
)

// CodeNotFixedRule refuses the count check of a rule that is not fixed: only a
// fixed rule has a number of kanbans to check (BR-24.7).
const CodeNotFixedRule = "LEAN-0113"

// maxSafetyPct bounds the safety percentage of the count check.
const maxSafetyPct = 1000

// CountCheck is the fixed-kanban count check of PRD 4.24.16 for one rule and
// demand: the rule's number of kanbans against the BR-24.7 suggestion, the
// deviation in percent (two places) and whether it is above the 25 % limit.
type CountCheck struct {
	RuleID       string `json:"ruleId"`
	RuleCode     string `json:"ruleCode"`
	DailyDemand  string `json:"dailyDemand"`
	SafetyPct    string `json:"safetyPct"`
	LeadTimeH    int    `json:"leadTimeH"`
	FixedQty     string `json:"fixedQty"`
	Configured   int    `json:"configured"`
	Suggested    int64  `json:"suggested"`
	DeviationPct string `json:"deviationPct"`
	LimitPct     int    `json:"limitPct"`
	Reportable   bool   `json:"reportable"`
}

// countQuery reads a non-negative decimal query parameter with at most six
// places below max; def is used when the parameter is absent and optional.
func countQuery(r *http.Request, fields map[string]string, name string, required bool, max decimal.Decimal) decimal.Decimal {
	raw := r.URL.Query().Get(name)
	if raw == "" && !required {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(raw)
	if err != nil || d.IsNegative() || d.Exponent() < -6 || !d.LessThan(max) {
		fields[name] = "must be a number from 0 below " + max.String() + " with at most 6 decimals"
	}
	return d
}

// countCheck runs the weekly fixed-kanban count suggestion of PRD 4.24.16 on
// demand for one fixed rule: the suggestion for the daily demand given, from
// the rule's lead time and fixed quantity, against its number of kanbans. It
// writes nothing and never changes the rule.
func (a *API) countCheck(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	fields := map[string]string{}
	demand := countQuery(r, fields, "dailyDemand", true, maxQty())
	safety := countQuery(r, fields, "safetyPct", false, decimal.NewFromInt(maxSafetyPct))
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	rule, err := a.store.GetRule(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if domain.Replenishment(rule.Replenishment) != domain.ReplenishFixed || rule.NumberOfKanbans == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeNotFixedRule,
			"kanban rule "+rule.Code+" is not a fixed rule, so it has no number of kanbans to check"))
		return
	}
	cc, err := domain.CheckFixedCount(*rule.NumberOfKanbans, demand, rule.LeadTimeH, safety, rule.FixedQty)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, CountCheck{
		RuleID: rule.ID, RuleCode: rule.Code, DailyDemand: demand.String(), SafetyPct: safety.String(),
		LeadTimeH: rule.LeadTimeH, FixedQty: rule.FixedQty.String(), Configured: cc.Configured, Suggested: cc.Suggested,
		DeviationPct: cc.DeviationPct.StringFixed(2), LimitPct: domain.CountDeviationLimitPct, Reportable: cc.Reportable,
	})
}
