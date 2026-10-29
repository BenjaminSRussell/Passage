// Package events holds the event types and payloads the expense service publishes.
// Schemas are documented in events/asyncapi.yaml.
package events

// Source is the CloudEvents source for this service (/passage/<context>/<service>).
const Source = "/passage/expense/expense"

// PolicyViolated is published when a submit is refused by a BLOCK outcome (PRD 8.43.5).
const PolicyViolated = "dev.passage.expense.policy.violated.v1"

// PolicyViolatedData is the payload of PolicyViolated. It carries ids and rule codes only.
type PolicyViolatedData struct {
	ReportID string `json:"reportId"`
	LineID   string `json:"lineId"`
	RuleID   string `json:"ruleId"`
	Outcome  string `json:"outcome"`
}

// CashAdvancePaid is published when an approved advance is recorded as paid by its AP
// prepayment (PRD 8.43.5 `cash-advance.paid.v1`). The kernel naming rule allows no
// hyphen in the entity, so the entity is "cashadvance".
const CashAdvancePaid = "dev.passage.expense.cashadvance.paid.v1"

// CashAdvancePaidData is the payload of CashAdvancePaid: ids and the paid amount, no bank data.
type CashAdvancePaidData struct {
	AdvanceID    string `json:"advanceId"`
	WorkerID     string `json:"workerId"`
	Amount       string `json:"amount"`
	Currency     string `json:"currency"`
	PrepaymentID string `json:"prepaymentId"`
}
