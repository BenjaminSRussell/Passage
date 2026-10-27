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
)

// All lists every event type the service publishes (used by the startup registry check).
func All() []string {
	return []string{RegistrationPosted, PaydayApproved}
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
