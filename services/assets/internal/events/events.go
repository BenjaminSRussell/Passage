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
	// LeasePosted is published for every lease journal the ledger accepted: the
	// recognition at commencement and each schedule period (D4d). It is not in the
	// PRD event table; it tells the ledger's consumers and analytics which entry
	// carries which period of the liability and ROU asset.
	LeasePosted = events.Type(Context, "lease", "posted", 1)
	// LeaseTerminated is published when a lease is terminated early (D4c): the
	// liability and ROU asset are derecognized with the gain or loss. It is not in
	// the PRD event table; the ledger's consumers and analytics need it.
	LeaseTerminated = events.Type(Context, "lease", "terminated", 1)
	// CapexApproved is published when a capex request is approved (FR-37.17);
	// budget (wave 38) and the depreciation forecast consume it.
	CapexApproved = events.Type(Context, "capexrequest", "approved", 1)
	// AssetTransferred and AssetSplit are published for every transfer and
	// split (FR-37.7); they are not in the PRD event table and are added so
	// the ledger (intercompany settlement, wave 40) and analytics see every
	// move of cost and accumulated depreciation between assets.
	AssetTransferred = events.Type(Context, "asset", "transferred", 1)
	AssetSplit       = events.Type(Context, "asset", "split", 1)
)

// AssetTransferredData is the payload of AssetTransferred and AssetSplit: the
// source asset, the balances each of its books moved, and the targets with
// the share each took. Amounts are decimal strings; percentages have two
// decimals and total 100.00.
type AssetTransferredData struct {
	TransferID    string               `json:"transferId"`
	SourceAssetID string               `json:"sourceAssetId"`
	LegalEntityID string               `json:"legalEntityId"`
	Kind          string               `json:"kind"`
	TransferDate  string               `json:"transferDate"`
	Books         []TransferBookData   `json:"books"`
	Targets       []TransferTargetData `json:"targets"`
}

// TransferBookData is one source book's balances at the transfer.
type TransferBookData struct {
	BookCode                string `json:"bookCode"`
	Currency                string `json:"currency"`
	Cost                    string `json:"cost"`
	AccumulatedDepreciation string `json:"accumulatedDepreciation"`
	NBV                     string `json:"nbv"`
}

// TransferTargetData is one target asset of a transfer or split.
type TransferTargetData struct {
	AssetID       string `json:"assetId"`
	AssetNumber   string `json:"assetNumber"`
	LegalEntityID string `json:"legalEntityId"`
	Percentage    string `json:"percentage"`
}

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

// LeaseTerminatedData is the payload of LeaseTerminated. AfterPeriod is the last
// payment period in force and TerminationDate its last day. GainLoss is positive
// for a gain. Amounts are decimal strings in Currency.
type LeaseTerminatedData struct {
	LeaseID               string `json:"leaseId"`
	TerminationID         string `json:"terminationId"`
	LegalEntityID         string `json:"legalEntityId"`
	AfterPeriod           int    `json:"afterPeriod"`
	TerminationDate       string `json:"terminationDate"`
	LiabilityDerecognized string `json:"liabilityDerecognized"`
	ROUGross              string `json:"rouGross"`
	ROUAccumulated        string `json:"rouAccumulated"`
	ROUCarrying           string `json:"rouCarrying"`
	TerminationPayment    string `json:"terminationPayment"`
	GainLoss              string `json:"gainLoss"`
	Currency              string `json:"currency"`
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

// LeasePostedData is the payload of LeasePosted. Kind is Termination (Period is the last period in force; Liability and ROUAsset are the amounts derecognized, with TerminationPayment and GainLoss), Recognition (period 0:
// Liability and ROUAsset are set) or Period (Interest, Payment, ROUDepreciation
// and Remeasurement are set). Amounts are decimal strings in Currency.
type LeasePostedData struct {
	LeaseID         string `json:"leaseId"`
	LegalEntityID   string `json:"legalEntityId"`
	Kind            string `json:"kind"`
	Period          int    `json:"period"`
	PostingDate     string `json:"postingDate"`
	JournalEntryID  string `json:"journalEntryId"`
	LedgerID        string `json:"ledgerId"`
	Currency        string `json:"currency"`
	Liability       string `json:"liability,omitempty"`
	ROUAsset        string `json:"rouAsset,omitempty"`
	Interest        string `json:"interest,omitempty"`
	Payment         string `json:"payment,omitempty"`
	ROUDepreciation string `json:"rouDepreciation,omitempty"`
	Remeasurement   string `json:"remeasurement,omitempty"`
	// Termination only: the payment to end the lease and the gain (positive) or loss.
	TerminationPayment string `json:"terminationPayment,omitempty"`
	GainLoss           string `json:"gainLoss,omitempty"`
}
