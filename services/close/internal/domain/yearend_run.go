package domain

import (
	"encoding/json"
	"sort"
	"time"
)

const (
	RunPending   = "pending"
	RunCompleted = "completed"
	RunAbandoned = "abandoned"

	JournalClosing           = "closing"
	JournalReverseClosing    = "reverseClosing"
	EventYearEndCompleted    = "dev.passage.close.yearend.completed.v1"
	EventPeriodStatusChanged = "dev.passage.close.period.statuschanged.v1"
	EventCloseRunCreated     = "dev.passage.close.run.created.v1"
	EventCloseTaskCompleted  = "dev.passage.close.task.completed.v1"
	EventAllocationCompleted = "dev.passage.close.allocation.completed.v1"
	EventAccrualScheduled    = "dev.passage.close.accrual.scheduled.v1"
	EventRegroupingCompleted = "dev.passage.close.regrouping.completed.v1"
	maxDimensionAttributes   = 5
)

type YearEndRun struct {
	ID               string            `json:"id"`
	LegalEntityID    string            `json:"legalEntityId"`
	LedgerID         string            `json:"ledgerId"`
	FiscalYear       string            `json:"fiscalYear"`
	Sequence         int               `json:"sequence"`
	SupersedesRunID  string            `json:"supersedesRunId,omitempty"`
	Status           string            `json:"status"`
	Currency         string            `json:"currency"`
	ClosingDate      string            `json:"closingDate"`
	OpeningDate      string            `json:"openingDate"`
	RetainedEarnings string            `json:"retainedEarningsAccount"`
	RetainDimensions bool              `json:"retainDimensions"`
	Proposals        *YearEnd          `json:"proposals,omitempty"`
	Journals         map[string]string `json:"journals"`
	Dispatched       bool              `json:"-"`
	CreatedBy        string            `json:"createdBy"`
	RequestHash      string            `json:"-"`
}

type YearEndCompleted struct {
	RunID            string `json:"runId"`
	LegalEntityID    string `json:"legalEntityId"`
	LedgerID         string `json:"ledgerId"`
	FiscalYear       string `json:"fiscalYear"`
	ClosingJournalID string `json:"closingJournalId,omitempty"`
}

type Period struct {
	FiscalPeriodID string
	Start          time.Time
	End            time.Time
	Rows           []Row
}

type YearPlan struct {
	ClosingDate time.Time
	OpeningDate time.Time
}

func PlanYear(year string, periods []Period) (YearPlan, error) {
	if len(periods) == 0 {
		return YearPlan{}, fail(CodeNoPeriod, "no fiscal period is registered for "+year+" on this ledger")
	}
	sorted := append([]Period(nil), periods...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start.Before(sorted[j].Start) })
	if err := checkCoverage(year, sorted); err != nil {
		return YearPlan{}, err
	}
	for i, p := range sorted {
		final := i == len(sorted)-1
		sealed := false
		for _, r := range p.Rows {
			closed := r.Status == StatusClosed || (r.Status == StatusPermanentlyClosed && !final)
			held := (r.Module == ModuleGL || r.Module == ModuleAll) && r.Status == StatusOnHold
			sealed = sealed || (r.Module == ModuleAll && (r.Status == StatusClosed || r.Status == StatusOnHold))
			if closed || held {
				continue
			}
			return YearPlan{}, fail(CodeInvalidTransition, "period "+p.FiscalPeriodID+" module "+string(r.Module)+" is "+string(r.Status)+"; every module must be closed, except general ledger which may be on hold, and the last period of the year cannot be permanently closed because the closing entry posts into it")
		}
		if final && !sealed {
			return YearPlan{}, fail(CodeInvalidTransition, "period "+p.FiscalPeriodID+" must have its all-modules row closed or on hold before the closing entry posts into it")
		}
	}
	closing := sorted[len(sorted)-1].End
	return YearPlan{ClosingDate: closing, OpeningDate: closing.AddDate(0, 0, 1)}, nil
}

func checkCoverage(year string, sorted []Period) error {
	const layout = "2006-01-02"
	first, last := year+"-01-01", year+"-12-31"
	if got := sorted[0].Start.Format(layout); got != first {
		return fail(CodeNoPeriod, "the fiscal periods of "+year+" start on "+got+", not "+first)
	}
	if got := sorted[len(sorted)-1].End.Format(layout); got != last {
		return fail(CodeNoPeriod, "the fiscal periods of "+year+" end on "+got+", not "+last)
	}
	for i := 1; i < len(sorted); i++ {
		prev, cur := sorted[i-1], sorted[i]
		next := prev.End.AddDate(0, 0, 1)
		switch {
		case cur.Start.Before(next):
			return fail(CodeAmbiguousPeriod, "fiscal periods "+prev.FiscalPeriodID+" and "+cur.FiscalPeriodID+" overlap")
		case cur.Start.After(next):
			return fail(CodeNoPeriod, "no fiscal period covers "+next.Format(layout)+" to "+cur.Start.AddDate(0, 0, -1).Format(layout))
		}
	}
	return nil
}

func ClassOf(accountType string) (Class, error) {
	switch accountType {
	case "revenue", "expense":
		return ClassProfitLoss, nil
	case "asset", "liability", "equity":
		return ClassBalanceSheet, nil
	}
	return "", fail(CodeLedgerData, "account type "+accountType+" cannot carry a year-end balance")
}

func DimensionKey(dimensions map[string]string) string {
	if len(dimensions) == 0 {
		return ""
	}
	raw, _ := json.Marshal(dimensions)
	return string(raw)
}

func ParseDimensions(key string) map[string]string {
	if key == "" {
		return nil
	}
	var out map[string]string
	_ = json.Unmarshal([]byte(key), &out)
	return out
}

func CheckDimensionAttributes(attributes []string) error {
	if len(attributes) > maxDimensionAttributes {
		return fail(CodeLedgerData, "the ledger has more dimension attributes than a balance inquiry can group by")
	}
	return nil
}
