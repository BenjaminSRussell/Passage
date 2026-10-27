// Package events holds the event types and payloads the lean service publishes
// through its outbox. Types follow the kernel rule
// dev.passage.<context>.<entity>.<verb>.v<n>. PRD 4.24.5 names the card
// transition passage.lean.kanban.state-changed.v1; the kernel rule allows no
// hyphen, so the verb is "statechanged"; for the same reason the PRD's
// passage.lean.flow-period.settled.v1 is dev.passage.lean.flowperiod.settled.v1.
package events

// Source is the CloudEvents source of every event this service writes.
const Source = "lean"

// Event types.
const (
	FlowActivated      = "dev.passage.lean.flow.activated.v1"
	KanbanCreated      = "dev.passage.lean.kanban.created.v1"
	KanbanStateChanged = "dev.passage.lean.kanban.statechanged.v1"
	JobStateChanged    = "dev.passage.lean.kanbanjob.statechanged.v1"
	FlowPeriodSettled  = "dev.passage.lean.flowperiod.settled.v1"
)

// All lists every event type this service publishes.
func All() []string {
	return []string{FlowActivated, KanbanCreated, KanbanStateChanged, JobStateChanged, FlowPeriodSettled}
}

// FlowActivatedData is the payload of FlowActivated.
type FlowActivatedData struct {
	FlowID        string `json:"flowId"`
	ValueStreamID string `json:"valueStreamId"`
	SiteID        string `json:"siteId"`
	Name          string `json:"name"`
	ActiveFrom    string `json:"activeFrom"`
	Activities    int    `json:"activities"`
}

// KanbanCreatedData is the payload of KanbanCreated (PRD 4.24.5: kanbanId,
// ruleId, qty, eventRefType, eventRefId, due).
type KanbanCreatedData struct {
	KanbanID     string  `json:"kanbanId"`
	RuleID       string  `json:"ruleId"`
	CardNumber   string  `json:"cardNumber"`
	ItemID       string  `json:"itemId"`
	RuleType     string  `json:"ruleType"`
	Qty          string  `json:"qty"`
	State        string  `json:"state"`
	EventRefType *string `json:"eventRefType"`
	EventRefID   *string `json:"eventRefId"`
	Due          *string `json:"due"`
}

// KanbanStateChangedData is the payload of KanbanStateChanged (PRD 4.24.5:
// kanbanId, jobId, from, to, qty, scrap, handlingUnits). A manufacturing card
// reaching completed is the trigger for the production order's report as
// finished (wave 21); a withdrawal card reaching completed triggers the
// inventory transfer (wave 16). JobID is set when a job moved the card; when
// the card completes through its last job, ReportedQty, ScrapQty and
// HandlingUnits are that job's report.
type KanbanStateChangedData struct {
	KanbanID      string  `json:"kanbanId"`
	RuleID        string  `json:"ruleId"`
	CardNumber    string  `json:"cardNumber"`
	ItemID        string  `json:"itemId"`
	RuleType      string  `json:"ruleType"`
	From          string  `json:"from"`
	To            string  `json:"to"`
	Qty           string  `json:"qty"`
	Version       int     `json:"version"`
	JobID         *string `json:"jobId,omitempty"`
	ReportedQty   *string `json:"reportedQty,omitempty"`
	ScrapQty      *string `json:"scrapQty,omitempty"`
	HandlingUnits *int    `json:"handlingUnits,omitempty"`
}

// JobStateChangedData is the payload of JobStateChanged: a kanban job moved on
// the work-cell board (PRD 4.24.5 "job/kanban transition"). The quantities are
// set when the job completes. Shop floor and lean subcontracting (FR-24.20)
// read service-job completions from it.
type JobStateChangedData struct {
	JobID              string  `json:"jobId"`
	KanbanID           string  `json:"kanbanId"`
	CardNumber         string  `json:"cardNumber"`
	ItemID             string  `json:"itemId"`
	ActivityID         string  `json:"activityId"`
	ActivityKind       string  `json:"activityKind"`
	Sequence           int     `json:"sequence"`
	WorkCellResourceID *string `json:"workCellResourceId"`
	From               string  `json:"from"`
	To                 string  `json:"to"`
	ReportedQty        *string `json:"reportedQty,omitempty"`
	ScrapQty           *string `json:"scrapQty,omitempty"`
	HandlingUnits      *int    `json:"handlingUnits,omitempty"`
	Version            int     `json:"version"`
}

// FlowPeriodSettledData is the payload of FlowPeriodSettled (PRD 4.24.5:
// flowId, period, materialCost, conversionCost, outputValue, variance,
// journalEntryId). Amounts are decimal strings at the currency's minor unit;
// variance = materialCost + conversionCost − outputValue (BR-24.9). The ledger
// (wave 13) posts the variance to the flow's posting profile and the journal
// entry; until then JournalEntryID is null.
type FlowPeriodSettledData struct {
	FlowID         string  `json:"flowId"`
	PeriodID       string  `json:"periodId"`
	PeriodStart    string  `json:"periodStart"`
	PeriodEnd      string  `json:"periodEnd"`
	Currency       string  `json:"currency"`
	MaterialCost   string  `json:"materialCost"`
	ConversionCost string  `json:"conversionCost"`
	OutputValue    string  `json:"outputValue"`
	Variance       string  `json:"variance"`
	Kanbans        int     `json:"kanbans"`
	JournalEntryID *string `json:"journalEntryId"`
}
