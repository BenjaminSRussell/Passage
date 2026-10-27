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
