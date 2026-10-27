package events

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
)

// Context is the bounded context segment of every event type this service emits.
const Context = "shopfloor"

var (
	RegistrationPosted = events.Type(Context, "registration", "posted", 1)
	PaydayApproved     = events.Type(Context, "payday", "approved", 1)
	PaydayExported     = events.Type(Context, "payday", "exported", 1)
	AndonRaised        = events.Type(Context, "andon", "raised", 1)
)

// All lists every event type the service publishes (used by the startup registry check).
func All() []string {
	return []string{RegistrationPosted, PaydayApproved, PaydayExported, AndonRaised}
}

// PaydayExportedData is the payload of dev.passage.shopfloor.payday.exported.v1:
// one payroll export batch with every pay line it carries (FR-25.8, w43).
type PaydayExportedData struct {
	BatchID   string             `json:"batchId"`
	FromDate  string             `json:"fromDate"`
	ToDate    string             `json:"toDate"`
	LineCount int                `json:"lineCount"`
	CreatedBy string             `json:"createdBy"`
	Lines     []ExportedLineData `json:"lines"`
}

// ExportedLineData is one approved pay line inside an export batch.
type ExportedLineData struct {
	WorkerID   string          `json:"workerId"`
	WorkDate   string          `json:"workDate"`
	Type       string          `json:"type"`
	Hours      decimal.Decimal `json:"hours"`
	Multiplier decimal.Decimal `json:"multiplier"`
	Amount     decimal.Decimal `json:"amount"`
	Currency   string          `json:"currency"`
}

// AndonRaisedData is the payload of dev.passage.shopfloor.andon.raised.v1.
type AndonRaisedData struct {
	AlertID    string  `json:"alertId"`
	ResourceID string  `json:"resourceId"`
	Kind       string  `json:"kind"`
	Severity   string  `json:"severity"`
	JobID      *string `json:"jobId"`
	StartedAt  string  `json:"startedAt"`
}

// RegistrationPostedData is the payload of dev.passage.shopfloor.registration.posted.v1.
type RegistrationPostedData struct {
	RegistrationID string           `json:"registrationId"`
	WorkerID       string           `json:"workerId"`
	Kind           string           `json:"kind"`
	JobID          *string          `json:"jobId"`
	Hours          *decimal.Decimal `json:"hours"`
	QtyGood        decimal.Decimal  `json:"qtyGood"`
	QtyScrap       decimal.Decimal  `json:"qtyScrap"`
	Offline        bool             `json:"offline"`
}

// PayLineData is one pay line inside a payday approval.
type PayLineData struct {
	Type       string          `json:"type"`
	Hours      decimal.Decimal `json:"hours"`
	Multiplier decimal.Decimal `json:"multiplier"`
	Amount     decimal.Decimal `json:"amount"`
	Currency   string          `json:"currency"`
}

// PaydayApprovedData is the payload of dev.passage.shopfloor.payday.approved.v1.
type PaydayApprovedData struct {
	WorkerID   string        `json:"workerId"`
	WorkDate   string        `json:"workDate"`
	PayLines   []PayLineData `json:"payLines"`
	ApprovedBy string        `json:"approvedBy"`
}
