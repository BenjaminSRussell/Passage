// Package events holds the event types and payloads the quality service publishes.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

// Context is the bounded context name used in event types.
const Context = "quality"

var (
	OrderCreated        = events.Type(Context, "order", "created", 1)
	ResultRecorded      = events.Type(Context, "result", "recorded", 1)
	UsageDecisionPosted = events.Type(Context, "usagedecision", "posted", 1)
	// Slice 2.
	ComplianceListCreated    = events.Type(Context, "compliancelist", "created", 1)
	ComplianceResultRecorded = events.Type(Context, "complianceresult", "recorded", 1)
	StabilityStudyCreated    = events.Type(Context, "stabilitystudy", "created", 1)
	// Slice 3.
	NonconformanceCreated = events.Type(Context, "nonconformance", "created", 1)
	CAPAClosed            = events.Type(Context, "capa", "closed", 1)
	// Slice 5.
	StabilityStudyStatusChanged = events.Type(Context, "stabilitystudy", "statuschanged", 1)
	CoAIssued                   = events.Type(Context, "coa", "issued", 1)
)

// All lists every event type this service publishes.
func All() []string {
	return []string{OrderCreated, ResultRecorded, UsageDecisionPosted, ComplianceListCreated, ComplianceResultRecorded, StabilityStudyCreated,
		NonconformanceCreated, CAPAClosed, StabilityStudyStatusChanged, CoAIssued}
}

// OrderCreatedData is the data member of dev.passage.quality.order.created.v1.
type OrderCreatedData struct {
	QualityOrderID string            `json:"quality_order_id"`
	Trigger        string            `json:"trigger"`
	SourceRef      any               `json:"source_ref"`
	ItemID         string            `json:"item_id"`
	Dims           map[string]string `json:"dims"`
	LotQty         string            `json:"lot_qty"`
	SampleQty      string            `json:"sample_qty"`
}

// ResultItem is one result inside a batch event.
type ResultItem struct {
	TestID   string `json:"test_id"`
	SampleNo int    `json:"sample_no"`
	RetestNo int    `json:"retest_no"`
	Passed   bool   `json:"passed"`
}

// ResultRecordedData is the data member of dev.passage.quality.result.recorded.v1.
type ResultRecordedData struct {
	QualityOrderID string       `json:"quality_order_id"`
	Results        []ResultItem `json:"results"`
}

// UsageDecisionPostedData is the data member of dev.passage.quality.usagedecision.posted.v1.
type UsageDecisionPostedData struct {
	QualityOrderID string   `json:"quality_order_id"`
	Decisions      []string `json:"decisions"`
	AcceptQty      string   `json:"accept_qty"`
	RejectQty      string   `json:"reject_qty"`
	DeviationQty   string   `json:"deviation_qty"`
	User           string   `json:"user"`
	// NCRID is the nonconformance the decision raised for its rejected quantity.
	NCRID string `json:"ncr_id,omitempty"`
}

// ComplianceListCreatedData is the data member of dev.passage.quality.compliancelist.created.v1.
// A new version supersedes the previous one; consumers re-evaluate items against it.
type ComplianceListCreatedData struct {
	ListID    string   `json:"list_id"`
	Code      string   `json:"code"`
	Version   int      `json:"version"`
	Kind      string   `json:"kind"`
	BlockMode string   `json:"block_mode"`
	Regions   []string `json:"regions"`
	CASCount  int      `json:"cas_count"`
}

// ComplianceHit is one substance over its threshold inside a result event.
type ComplianceHit struct {
	CAS          string `json:"cas"`
	PercentWW    string `json:"percent_ww"`
	ThresholdPct string `json:"threshold_pct"`
}

// ComplianceResultRecordedData is the data member of
// dev.passage.quality.complianceresult.recorded.v1: a new result version for an
// item, list version and region. Sales, shipping and purchasing block or warn on it.
type ComplianceResultRecordedData struct {
	ItemID             string          `json:"item_id"`
	ListCode           string          `json:"list_code"`
	ListVersion        int             `json:"list_version"`
	Region             string          `json:"region"`
	Version            int             `json:"version"`
	CompositionVersion int             `json:"composition_version"`
	Restricted         bool            `json:"restricted"`
	Code               string          `json:"code"`
	Severity           string          `json:"severity"`
	Hits               []ComplianceHit `json:"hits"`
}

// StabilityPull is one scheduled pull inside a study event.
type StabilityPull struct {
	Months      int    `json:"months"`
	PullDate    string `json:"pull_date"`
	OrderDate   string `json:"order_date"`
	TestGroupID string `json:"test_group_id"`
}

// StabilityStudyCreatedData is the data member of dev.passage.quality.stabilitystudy.created.v1.
// The stability scheduling workflow starts from it (BR-33.14).
type StabilityStudyCreatedData struct {
	StudyID   string          `json:"study_id"`
	ItemID    string          `json:"item_id"`
	BatchID   string          `json:"batch_id"`
	Condition string          `json:"condition"`
	StartDate string          `json:"start_date"`
	Pulls     []StabilityPull `json:"pulls"`
}

// NonconformanceCreatedData is the data member of dev.passage.quality.nonconformance.created.v1.
// The vendor scorecard counts it by severity; quality_order_id links an NCR raised
// by a usage decision (or amending one, BR-33.4) to its order.
type NonconformanceCreatedData struct {
	NCRID          string `json:"ncr_id"`
	Source         string `json:"source"`
	Severity       string `json:"severity"`
	ProblemType    string `json:"problem_type"`
	ItemID         string `json:"item_id"`
	VendorID       string `json:"vendor_id,omitempty"`
	CustomerID     string `json:"customer_id,omitempty"`
	Qty            string `json:"qty"`
	UOM            string `json:"uom"`
	QualityOrderID string `json:"quality_order_id,omitempty"`
}

// CAPAClosedData is the data member of dev.passage.quality.capa.closed.v1: the 8D
// closed with an effective check.
type CAPAClosedData struct {
	NCRID               string `json:"ncr_id"`
	CorrectionID        string `json:"correction_id"`
	EffectivenessResult string `json:"effectiveness_result"`
	Reopened            int    `json:"reopened"`
	ClosedAt            string `json:"closed_at"`
}

// StabilityStudyStatusChangedData is the data member of
// dev.passage.quality.stabilitystudy.statuschanged.v1. The scheduling workflow
// stops on COMPLETED or TERMINATED; cancelled_pulls are the pull months a
// termination cancelled.
type StabilityStudyStatusChangedData struct {
	StudyID        string `json:"study_id"`
	From           string `json:"from"`
	To             string `json:"to"`
	Reason         string `json:"reason,omitempty"`
	User           string `json:"user"`
	CancelledPulls []int  `json:"cancelled_pulls"`
}

// CoAIssuedData is the data member of dev.passage.quality.coa.issued.v1: one
// immutable CoA revision (FR-33.9, BR-33.10). The shipment hook (wave 28) and the
// customer portal deliver it; supersedes names the revision it replaces.
type CoAIssuedData struct {
	CoAID          string `json:"coa_id"`
	QualityOrderID string `json:"quality_order_id"`
	ShipmentID     string `json:"shipment_id,omitempty"`
	CustomerID     string `json:"customer_id,omitempty"`
	ItemID         string `json:"item_id"`
	Batch          string `json:"batch"`
	Revision       int    `json:"revision"`
	Supersedes     string `json:"supersedes,omitempty"`
	AcceptQty      string `json:"accept_qty"`
	DeviationQty   string `json:"deviation_qty"`
	SignedBy       string `json:"signed_by"`
}
