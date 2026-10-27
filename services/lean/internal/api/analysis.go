package api

import (
	"net/http"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/lean/internal/domain"
	"github.com/BenjaminSRussell/passage/services/lean/internal/store"
)

// defaultAnalysisDays is the period of a rule analysis without dates: the 30
// days up to and including today (UTC).
const defaultAnalysisDays = 30

// RuleAnalysis is the kanban rule analysis of PRD 4.24.14 for one rule and
// period. Lead time is measured from planning to completion (the rule's lead
// time, TargetLeadTimeH, is the target); a cycle is late when it completed
// after its due time. Decimal figures are strings; AvgLeadTimeH and LatePct
// are null when nothing was measured. MaxKanbans is the fixed rule's number
// of kanbans, null for other rules.
type RuleAnalysis struct {
	RuleID          string  `json:"ruleId"`
	RuleCode        string  `json:"ruleCode"`
	From            string  `json:"from"`
	To              string  `json:"to"`
	Cycles          int     `json:"cycles"`
	TimedCycles     int     `json:"timedCycles"`
	AvgLeadTimeH    *string `json:"avgLeadTimeH"`
	TargetLeadTimeH int     `json:"targetLeadTimeH"`
	DueCycles       int     `json:"dueCycles"`
	LateCycles      int     `json:"lateCycles"`
	LatePct         *string `json:"latePct"`
	ActiveKanbans   int     `json:"activeKanbans"`
	MaxKanbans      *int    `json:"maxKanbans"`
	OverdueOpen     int     `json:"overdueOpen"`
}

// analysisPeriod reads from and to (inclusive UTC days). Without them the
// period is the last 30 days; it may not run backwards or span more than
// domain.MaxAnalysisDays days.
func analysisPeriod(w http.ResponseWriter, r *http.Request) (time.Time, time.Time, bool) {
	fields := map[string]string{}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	to, toOK := today, true
	if v := r.URL.Query().Get("to"); v != "" {
		to, toOK = parseDate(fields, "to", v)
	}
	from, fromOK := to.AddDate(0, 0, 1-defaultAnalysisDays), true
	if v := r.URL.Query().Get("from"); v != "" {
		from, fromOK = parseDate(fields, "from", v)
	}
	if fromOK && toOK {
		switch {
		case to.Before(from):
			fields["to"] = "must not be before from"
		case to.Sub(from) >= domain.MaxAnalysisDays*24*time.Hour:
			fields["from"] = "the period may span at most 366 days"
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return from, to, false
	}
	return from, to, true
}

// ruleAnalysis returns the KPI of a kanban rule over a period (PRD 4.24.14):
// cycles, average lead time against the rule's, the share of late cycles,
// and active cards against the maximum with how many are overdue now.
func (a *API) ruleAnalysis(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	from, to, ok := analysisPeriod(w, r)
	if !ok {
		return
	}
	act, err := a.store.RuleActivity(r.Context(), id, from, to)
	if err != nil {
		fail(w, r, err)
		return
	}
	kpi := domain.AnalyzeCycles(act.Cycles)
	out := RuleAnalysis{RuleID: act.Rule.ID, RuleCode: act.Rule.Code, From: from.Format(store.DateLayout), To: to.Format(store.DateLayout),
		Cycles: kpi.Cycles, TimedCycles: kpi.TimedCycles, TargetLeadTimeH: act.Rule.LeadTimeH, DueCycles: kpi.DueCycles, LateCycles: kpi.LateCycles,
		ActiveKanbans: act.Rule.ActiveKanbans, OverdueOpen: act.OverdueOpen}
	if kpi.AvgLeadTimeH != nil {
		s := kpi.AvgLeadTimeH.StringFixed(2)
		out.AvgLeadTimeH = &s
	}
	if kpi.LatePct != nil {
		s := kpi.LatePct.StringFixed(2)
		out.LatePct = &s
	}
	if domain.Replenishment(act.Rule.Replenishment) == domain.ReplenishFixed {
		out.MaxKanbans = act.Rule.NumberOfKanbans
	}
	httpx.JSON(w, http.StatusOK, out)
}
