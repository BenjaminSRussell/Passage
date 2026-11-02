package domain

import (
	"encoding/json"
	"time"
)

const (
	RunPending   = "pending"
	RunCompleted = "completed"

	JournalClosing         = "closing"
	JournalOpening         = "opening"
	JournalReverseClosing  = "reverseClosing"
	JournalReverseOpening  = "reverseOpening"
	EventYearEndCompleted  = "dev.passage.close.yearend.completed.v1"
	maxDimensionAttributes = 5
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
	CreatedBy        string            `json:"createdBy"`
	RequestHash      string            `json:"-"`
}

type YearEndCompleted struct {
	RunID            string `json:"runId"`
	LegalEntityID    string `json:"legalEntityId"`
	LedgerID         string `json:"ledgerId"`
	FiscalYear       string `json:"fiscalYear"`
	ClosingJournalID string `json:"closingJournalId,omitempty"`
	OpeningJournalID string `json:"openingJournalId,omitempty"`
}

type Period struct {
	FiscalPeriodID string
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
	var plan YearPlan
	for _, p := range periods {
		for _, r := range p.Rows {
			closed := r.Status == StatusClosed || r.Status == StatusPermanentlyClosed
			if closed || (r.Module == ModuleGL && r.Status == StatusOnHold) {
				continue
			}
			return YearPlan{}, fail(CodeInvalidTransition, "period "+p.FiscalPeriodID+" module "+string(r.Module)+" is "+string(r.Status)+"; every module must be closed, except general ledger which may be on hold")
		}
		if p.End.After(plan.ClosingDate) {
			plan.ClosingDate = p.End
		}
	}
	plan.OpeningDate = plan.ClosingDate.AddDate(0, 0, 1)
	return plan, nil
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
