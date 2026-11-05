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

// LoadReleased is published when a planner releases a load for tendering
// (FR-34.5). Consumer: workflow-ts, which starts tenderWorkflow from it and
// mints the tender's scoped service token from releasedBy and legalEntityScope
// (contract: services/workflow-ts/events/asyncapi.yaml). Both come from the
// verified caller of the release, never from the request body, and the event is
// not published for a load outside that scope. The subject is the load id.
const LoadReleased = "dev.passage.tms.load.released.v1"

// ReleasedCarrier is one carrier of LoadReleasedPayload, in rank order.
type ReleasedCarrier struct {
	CarrierID  string `json:"carrierId"`
	RateQuoted string `json:"rateQuoted,omitempty"`
}

// LoadReleasedPayload is the data of LoadReleased. ReleaseID names the release;
// the workflow keys the tender start on it, so a redelivered event replays the
// same tender and a new release starts a new one.
type LoadReleasedPayload struct {
	ReleaseID        string            `json:"releaseId"`
	LoadID           string            `json:"loadId"`
	LegalEntityID    string            `json:"legalEntityId"`
	ReleasedBy       string            `json:"releasedBy"`
	LegalEntityScope []string          `json:"legalEntityScope"`
	Carriers         []ReleasedCarrier `json:"carriers"`
	Currency         string            `json:"currency,omitempty"`
	TimeoutMin       int               `json:"timeoutMin"`
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

// FreightBillApproved is published when a freight bill is approved (BR-34.4,
// BR-34.5). AP (wave 19) consumes it to post the vendor invoice for the invoiced
// amount and the ledger to reverse the accrual; the variance goes to account
// 5250. The subject is the freight bill id, the shared journal reference.
const FreightBillApproved = "dev.passage.tms.freightbill.approved.v1"

// FreightBillApprovedPayload is the data of FreightBillApproved. Amounts are
// decimal strings with two places; invoicedTotal = expectedTotal + variance.
type FreightBillApprovedPayload struct {
	FreightBillID    string `json:"freightBillId"`
	LoadID           string `json:"loadId"`
	CarrierID        string `json:"carrierId"`
	CarrierInvoiceID string `json:"carrierInvoiceId"`
	Currency         string `json:"currency"`
	ExpectedTotal    string `json:"expectedTotal"`
	InvoicedTotal    string `json:"invoicedTotal"`
	Variance         string `json:"variance"`
	FromStatus       string `json:"fromStatus"`
	Reason           string `json:"reason,omitempty"`
	ApprovedBy       string `json:"approvedBy"`
	ApprovedAt       string `json:"approvedAt"`
}

// GateCheckedIn is published when a driver and trailer check in at the gate
// (FR-34.14); wms releases the door work from it. The subject is the visit id.
// It carries no driver PII.
const GateCheckedIn = "dev.passage.tms.gate.checkedin.v1"

// GateCheckedInPayload is the data of GateCheckedIn. ArrivalStatus is empty for
// a walk-in.
type GateCheckedInPayload struct {
	VisitID       string `json:"visitId"`
	SiteID        string `json:"siteId"`
	Direction     string `json:"direction"`
	LoadID        string `json:"loadId,omitempty"`
	CarrierSCAC   string `json:"carrierScac"`
	TrailerNo     string `json:"trailerNo"`
	AppointmentID string `json:"appointmentId,omitempty"`
	ArrivalStatus string `json:"arrivalStatus,omitempty"`
	ArrivalDelta  int    `json:"arrivalDeltaMin"`
	CheckedInAt   string `json:"checkedInAt"`
}

// GoodsInTransitShipped is published when a goods-in-transit order ships at the
// Incoterm transfer event and ownership of its receipt lines moves to the
// in-transit warehouse (BR-34.11). Consumers: inventory and costing (wave 20),
// which carry the goods value and the estimated landed cost on the in-transit
// account. The subject is the order id.
const GoodsInTransitShipped = "dev.passage.tms.goodsintransit.shipped.v1"

// GITCostPayload is one cost type's estimate on a receipt line.
type GITCostPayload struct {
	CostType string `json:"costType"`
	Amount   string `json:"amount"`
}

// GITLinePayload is one receipt line of GoodsInTransitShippedPayload.
type GITLinePayload struct {
	Line      int              `json:"line"`
	Value     string           `json:"value"`
	Quantity  string           `json:"quantity"`
	Estimated string           `json:"estimated"`
	Costs     []GITCostPayload `json:"costs"`
}

// GoodsInTransitShippedPayload is the data of GoodsInTransitShipped.
// estimatedTotal equals the sum of lines[].estimated.
type GoodsInTransitShippedPayload struct {
	OrderID            string           `json:"orderId"`
	VoyageID           string           `json:"voyageId"`
	SiteID             string           `json:"siteId"`
	Incoterm           string           `json:"incoterm"`
	TransferEvent      string           `json:"transferEvent"`
	InTransitWarehouse string           `json:"inTransitWarehouse"`
	Currency           string           `json:"currency"`
	ShippedAt          string           `json:"shippedAt"`
	GoodsValue         string           `json:"goodsValue"`
	EstimatedTotal     string           `json:"estimatedTotal"`
	Lines              []GITLinePayload `json:"lines"`
}
