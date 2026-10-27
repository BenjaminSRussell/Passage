package events

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
)

// Context is the bounded context segment of every event type this service emits.
const Context = "shopfloor"

var (
	RegistrationPosted = events.Type(Context, "registration", "posted", 1)
	PaydayApproved     = events.Type(Context, "payday", "approved", 1)
	PaydayExported     = events.Type(Context, "payday", "exported", 1)
	AndonRaised        = events.Type(Context, "andon", "raised", 1)
	// The entity is one word: the naming rule allows only [a-z0-9] per segment.
	ProcessMessageReceived  = events.Type(Context, "processmessage", "received", 1)
	ProcessMessageDelivered = events.Type(Context, "processmessage", "delivered", 1)
	ProcessMessageDeleted   = events.Type(Context, "processmessage", "deleted", 1)
)

// All lists every event type the service publishes (used by the startup registry check).
func All() []string {
	return []string{RegistrationPosted, PaydayApproved, PaydayExported, AndonRaised, ProcessMessageReceived, ProcessMessageDelivered, ProcessMessageDeleted}
}

// ProcessMessageReceivedData is the payload of dev.passage.shopfloor.processmessage.received.v1,
// published when a process message is stored, again for one destination when it is
// reprocessed, and again by the retry sweep for the destinations whose wait elapsed
// (FR-25.17). Destinations then names only those destinations. Each consuming context (w21 journal, w33 quality, w24 EBR,
// w35 webhook) acts on the destinations it owns and reports the outcome back.
// OutOfLimit tells the EBR consumer to record an exception (BR-25.13).
type ProcessMessageReceivedData struct {
	MessageID       string                  `json:"messageId"`
	Source          string                  `json:"source"`
	SourceMessageID string                  `json:"sourceMessageId"`
	CategoryCode    string                  `json:"categoryCode"`
	OrderID         string                  `json:"orderId"`
	PhasePath       string                  `json:"phasePath"`
	Destinations    []string                `json:"destinations"`
	Values          []domain.Characteristic `json:"values"`
	OutOfLimit      bool                    `json:"outOfLimit"`
	OccurredAt      time.Time               `json:"occurredAt"`
}

// ProcessMessageDeliveredData is the payload of dev.passage.shopfloor.processmessage.delivered.v1,
// published once when every destination of a message is delivered.
type ProcessMessageDeliveredData struct {
	MessageID    string `json:"messageId"`
	CategoryCode string `json:"categoryCode"`
	OrderID      string `json:"orderId"`
}

// ProcessMessageDeletedData is the payload of dev.passage.shopfloor.processmessage.deleted.v1,
// published when a supervisor deletes a message with a destination in error (FR-25.17).
// Undelivered names the destinations that never received it, so the EBR consumer can
// record the missed message as an exception (BR-24.15).
type ProcessMessageDeletedData struct {
	MessageID    string    `json:"messageId"`
	CategoryCode string    `json:"categoryCode"`
	OrderID      string    `json:"orderId"`
	Reason       string    `json:"reason"`
	Undelivered  []string  `json:"undelivered"`
	DeletedBy    string    `json:"deletedBy"`
	DeletedAt    time.Time `json:"deletedAt"`
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
	BundleID       *string          `json:"bundleId"`
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
