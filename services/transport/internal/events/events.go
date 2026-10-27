// Package events holds the event type names and payloads this service publishes.
// Amounts are decimal strings with two places, never floats.
package events

// FreightBillMatched is published each time a carrier invoice is matched to a
// freight bill, whatever the outcome: matchStatus is MATCHED when every charge
// is within tolerance and VARIANCE otherwise. Consumers: AP (wave 19) and costing.
const FreightBillMatched = "dev.passage.tms.freightbill.matched.v1"

// MatchLine is one charge code's comparison.
type MatchLine struct {
	ChargeCode      string `json:"chargeCode"`
	Expected        string `json:"expected"`
	Invoiced        string `json:"invoiced"`
	Variance        string `json:"variance"`
	WithinTolerance bool   `json:"withinTolerance"`
}

// FreightBillMatchedPayload is the data of FreightBillMatched.
type FreightBillMatchedPayload struct {
	FreightBillID    string      `json:"freightBillId"`
	CarrierInvoiceID string      `json:"carrierInvoiceId"`
	MatchStatus      string      `json:"matchStatus"`
	Variance         string      `json:"variance"`
	Lines            []MatchLine `json:"lines"`
}
