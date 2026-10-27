// Package events holds the event types and payloads this service publishes.
// Schemas are documented in events/asyncapi.yaml.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

// Context is the bounded-context segment of every event type.
const Context = "service"

// Event types published by the field-service slice.
var (
	CaseOpened      = events.Type(Context, "case", "opened", 1)
	WorkOrderPosted = events.Type(Context, "workorder", "posted", 1)
)

// CaseOpenedData is the payload of dev.passage.service.case.opened.v1.
type CaseOpenedData struct {
	CaseID          string  `json:"caseId"`
	Number          string  `json:"number"`
	CustomerPartyID string  `json:"customerId"`
	Priority        int     `json:"priority"`
	AgreementID     *string `json:"agreementId"`
	ResponseDue     string  `json:"responseDue"`
	ResolutionDue   string  `json:"resolutionDue"`
}

// WorkOrderPostedData is the payload of dev.passage.service.workorder.posted.v1.
// It carries what wave-29 invoicing needs to bill the BILLABLE lines (customer,
// legal entity, lines) and what the GL needs for warranty cost. Amounts are
// decimal strings in Currency; they are never floats.
type WorkOrderPostedData struct {
	WorkOrderID         string       `json:"workOrderId"`
	Number              string       `json:"number"`
	CustomerPartyID     string       `json:"customerId"`
	LegalEntityID       string       `json:"legalEntityId"`
	CaseID              *string      `json:"caseId"`
	AgreementID         *string      `json:"agreementId"`
	Currency            string       `json:"currency"`
	BillableTotal       string       `json:"billableTotal"`
	WarrantyCost        string       `json:"warrantyCost"`
	EntitlementConsumed string       `json:"entitlementConsumed"`
	Lines               []PostedLine `json:"lines"`
}

// PostedLine is one posted piece of a work-order line with its coverage.
type PostedLine struct {
	LineID         string `json:"lineId"`
	Kind           string `json:"kind"`
	Coverage       string `json:"coverage"`
	Qty            string `json:"qty"`
	Amount         string `json:"amount"`
	WarrantyCost   string `json:"warrantyCost"`
	AgreementHours string `json:"agreementHours"`
}
