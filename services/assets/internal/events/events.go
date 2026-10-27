// Package events holds the event types and payloads the assets service
// publishes through the outbox (events/asyncapi.yaml).
package events

import "github.com/BenjaminSRussell/passage/platform/events"

// Context is the bounded-context segment of every event type.
const Context = "assets"

var (
	AssetAcquired   = events.Type(Context, "asset", "acquired", 1)
	AssetDisposed   = events.Type(Context, "asset", "disposed", 1)
	LeaseRecognized = events.Type(Context, "lease", "recognized", 1)
	// AssetAdjusted is published for every AcquisitionAdj, DepreciationAdj,
	// Revaluation and Impairment (FR-37.9, FR-37.14); it is not in the PRD event
	// table and is added so consumers see every change of a book's balances.
	AssetAdjusted      = events.Type(Context, "asset", "adjusted", 1)
	DepreciationPosted = events.Type(Context, "depreciation", "posted", 1)
	// LeaseRemeasured is published for every lease modification (BR-37.8); it
	// is not in the PRD event table and is added so the ledger and analytics
	// see every change of the lease liability and ROU asset.
	LeaseRemeasured = events.Type(Context, "lease", "remeasured", 1)
	// CapexApproved is published when a capex request is approved (FR-37.17);
	// budget (wave 38) and the depreciation forecast consume it.
	CapexApproved = events.Type(Context, "capexrequest", "approved", 1)
)

// CapexApprovedData is the payload of CapexApproved: the approved variant's
// amounts per year (decimal strings keyed by four-digit year) with the request
// currency. ProgramPositionID stays null until the investment program
// (FR-38.21) links the request. The PRD lists requestId, variantId,
// amountsByYear and programPositionId; the other fields are added so a
// consumer can plan the acquisition without reading the request back.
type CapexApprovedData struct {
	RequestID         string            `json:"requestId"`
	LegalEntityID     string            `json:"legalEntityId"`
	Number            string            `json:"number"`
	VariantID         string            `json:"variantId"`
	AmountsByYear     map[string]string `json:"amountsByYear"`
	ApprovedAmount    string            `json:"approvedAmount"`
	Currency          string            `json:"currency"`
	InServiceDate     string            `json:"inServiceDate"`
	ProfileAssumption string            `json:"profileAssumption"`
	ProgramPositionID *string           `json:"programPositionId"`
}

// LeaseRemeasuredData is the payload of LeaseRemeasured. Amounts are decimal
// strings; Adjustment = LiabilityAfter - LiabilityBefore, the ROU moves by
// ROUAdjustment and ProfitLossExcess is the part recognized in P&L because the
// ROU would have gone below zero.
type LeaseRemeasuredData struct {
	LeaseID          string `json:"leaseId"`
	ModificationID   string `json:"modificationId"`
	EffectivePeriod  int    `json:"effectivePeriod"`
	TermMonths       int    `json:"termMonths"`
	Rate             string `json:"rate"`
	LiabilityBefore  string `json:"liabilityBefore"`
	LiabilityAfter   string `json:"liabilityAfter"`
	Adjustment       string `json:"adjustment"`
	ROUAdjustment    string `json:"rouAdjustment"`
	ProfitLossExcess string `json:"profitLossExcess"`
	Currency         string `json:"currency"`
}

// AssetAdjustedData is the payload of AssetAdjusted: one adjustment on one
// book. Deltas are signed decimal strings.
type AssetAdjustedData struct {
	AssetID          string  `json:"assetId"`
	BookID           string  `json:"bookId"`
	TransactionID    string  `json:"transactionId"`
	Type             string  `json:"type"`
	TransactionDate  string  `json:"transactionDate"`
	CostDelta        string  `json:"costDelta"`
	AccumulatedDelta string  `json:"accumulatedDelta"`
	ImpairmentDelta  string  `json:"impairmentDelta"`
	Currency         string  `json:"currency"`
	SourceType       string  `json:"sourceType"`
	SourceID         *string `json:"sourceId"`
}

// DepreciationPostedData is the payload of DepreciationPosted. BookCode stands
// in for the PRD's bookDefinitionId until book definitions exist;
// JournalEntryID stays null until the ledger integration posts the journal.
type DepreciationPostedData struct {
	RunID          string  `json:"runId"`
	LegalEntityID  string  `json:"legalEntityId"`
	BookCode       string  `json:"bookCode"`
	Period         string  `json:"period"`
	AssetCount     int     `json:"assetCount"`
	TotalAmount    string  `json:"totalAmount"`
	Currency       *string `json:"currency"`
	JournalEntryID *string `json:"journalEntryId"`
}

// AssetAcquiredData is the payload of AssetAcquired. Amounts are decimal strings.
type AssetAcquiredData struct {
	AssetID    string   `json:"assetId"`
	BookIDs    []string `json:"bookIds"`
	Cost       string   `json:"cost"`
	Currency   string   `json:"currency"`
	SourceType string   `json:"sourceType"`
	SourceID   *string  `json:"sourceId"`
}

// AssetDisposedData is the payload of AssetDisposed, one per disposed book.
type AssetDisposedData struct {
	AssetID         string `json:"assetId"`
	BookID          string `json:"bookId"`
	Kind            string `json:"kind"`
	TransactionDate string `json:"transactionDate"`
	Percentage      string `json:"percentage"`
	Proceeds        string `json:"proceeds"`
	NBV             string `json:"nbv"`
	GainLoss        string `json:"gainLoss"`
	Currency        string `json:"currency"`
}

// LeaseRecognizedData is the payload of LeaseRecognized.
type LeaseRecognizedData struct {
	LeaseID    string `json:"leaseId"`
	Liability  string `json:"liability"`
	ROUAsset   string `json:"rouAsset"`
	Currency   string `json:"currency"`
	TermMonths int    `json:"termMonths"`
	Rate       string `json:"rate"`
}
