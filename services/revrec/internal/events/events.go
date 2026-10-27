// Package events holds the event type constants and payloads revrec publishes.
// The contract is described in events/asyncapi.yaml.
package events

// Event types (dev.passage.<context>.<entity>.<verb>.v<n>).
const (
	// ContractAllocated is published when a revenue contract is created and its
	// transaction price is allocated to obligations (FR-29.7).
	ContractAllocated = "dev.passage.revrec.contract.allocated.v1"
	// ScheduleRecognized is published when a deferred schedule line is
	// recognized (Dr 2400 / Cr 4000): by a milestone satisfaction or by the
	// period recognition run (FR-29.8). POINT_IN_TIME lines recognized at
	// contract creation emit nothing: the invoice already credited 4000.
	ScheduleRecognized = "dev.passage.revrec.schedule.recognized.v1"
)

// ObligationAllocated is one obligation of contract.allocated.
type ObligationAllocated struct {
	ID        string `json:"id"`
	ProductID string `json:"product_id"`
	SSP       string `json:"ssp"`
	SSPSource string `json:"ssp_source"`
	Allocated string `json:"allocated"`
	Method    string `json:"method"`
}

// ContractAllocatedData is the data member of contract.allocated.
type ContractAllocatedData struct {
	ContractID       string                `json:"contract_id"`
	LegalEntityID    string                `json:"legal_entity_id"`
	OrderID          string                `json:"order_id"`
	CustomerID       string                `json:"customer_id"`
	Currency         string                `json:"currency"`
	TransactionPrice string                `json:"transaction_price"`
	ContractDate     string                `json:"contract_date"`
	Obligations      []ObligationAllocated `json:"obligations"`
}

// ScheduleRecognizedData is the data member of schedule.recognized. PeriodID is
// the calendar month (YYYY-MM) of RecognizedOn until the fiscal calendar (wave
// 14) supplies period ids. JournalPostingID is null until the ledger posting
// exists (handoff to wave 11).
type ScheduleRecognizedData struct {
	ScheduleID       string  `json:"schedule_id"`
	ObligationID     string  `json:"obligation_id"`
	ContractID       string  `json:"contract_id"`
	LegalEntityID    string  `json:"legal_entity_id"`
	PeriodID         string  `json:"period_id"`
	RecognizedOn     string  `json:"recognized_on"`
	Currency         string  `json:"currency"`
	Amount           string  `json:"amount"`
	DeferredAccount  string  `json:"deferred_account"`
	RevenueAccount   string  `json:"revenue_account"`
	JournalPostingID *string `json:"journal_posting_id"`
}
