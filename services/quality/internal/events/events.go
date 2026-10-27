// Package events holds the event types and payloads the quality service publishes.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

// Context is the bounded context name used in event types.
const Context = "quality"

var (
	OrderCreated        = events.Type(Context, "order", "created", 1)
	ResultRecorded      = events.Type(Context, "result", "recorded", 1)
	UsageDecisionPosted = events.Type(Context, "usagedecision", "posted", 1)
)

// All lists every event type this service publishes.
func All() []string { return []string{OrderCreated, ResultRecorded, UsageDecisionPosted} }

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
}
