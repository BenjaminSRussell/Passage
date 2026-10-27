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

// Tender response events (BR-34.3), one per attempt outcome. Consumers: wms, edi
// (204/990) and analytics. The subject is the tender id.
const (
	TenderAccepted = "dev.passage.tms.tender.accepted.v1"
	TenderDeclined = "dev.passage.tms.tender.declined.v1"
	TenderExpired  = "dev.passage.tms.tender.expired.v1"
)

// TenderPayload is the data of the tender events. Rate is the rate quoted to
// the carrier, absent when the tender named none.
type TenderPayload struct {
	TenderID  string `json:"tenderId"`
	LoadID    string `json:"loadId"`
	CarrierID string `json:"carrierId"`
	Rank      int    `json:"rank"`
	Response  string `json:"response"`
	Rate      string `json:"rate,omitempty"`
	Currency  string `json:"currency,omitempty"`
}

// TrackingEventRecorded is published once per stored status report (FR-34.6).
// Consumers: sales fulfilment and the customer portal. The subject is the load id.
const TrackingEventRecorded = "dev.passage.tms.tracking.event.v1"

// TrackingPayload is the data of TrackingEventRecorded.
type TrackingPayload struct {
	LoadID     string `json:"loadId"`
	StopNo     int    `json:"stopNo"`
	Code       string `json:"code"`
	OccurredAt string `json:"occurredAt"`
	Location   string `json:"location,omitempty"`
	Source     string `json:"source"`
}

// LandedCostPosted is published when a voyage's costs are allocated and posted
// (FR-34.9). Consumer: costing (wave 20). The subject is the voyage id.
const LandedCostPosted = "dev.passage.tms.landedcost.posted.v1"

// ReceiptAllocation is the landed cost allocated to one receipt line.
type ReceiptAllocation struct {
	Line   int    `json:"line"`
	Amount string `json:"amount"`
}

// LandedCostPostedPayload is the data of LandedCostPosted. Σ receiptLines.amount = total.
type LandedCostPostedPayload struct {
	VoyageID     string              `json:"voyageId"`
	Reference    string              `json:"reference"`
	Currency     string              `json:"currency"`
	Total        string              `json:"total"`
	ReceiptLines []ReceiptAllocation `json:"receiptLines"`
}
