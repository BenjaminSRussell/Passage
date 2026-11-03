// Package events holds the event types and payloads the costing service
// publishes through the outbox (events/asyncapi.yaml).
package events

import (
	"time"

	"github.com/BenjaminSRussell/passage/platform/events"
)

// Context is the bounded-context segment of every event type (PRD 7.39.5).
const Context = "costing"

// Slice 5 events. They are not in the PRD event table: they announce the cost
// structure and the costing version lifecycle, which inventory costing (wave
// 20), production (wave 21) and budgeting read to know which version and split
// apply.
var (
	CostComponentCreated   = events.Type(Context, "costcomponent", "created", 1)
	CostGroupCreated       = events.Type(Context, "costgroup", "created", 1)
	VersionCreated         = events.Type(Context, "version", "created", 1)
	VersionTransitioned    = events.Type(Context, "version", "transitioned", 1)
	StandardCostActivated  = events.Type(Context, "standardcost", "activated", 1)
	PostingAccountsChanged = events.Type(Context, "postingaccounts", "changed", 1)
	// ActivationAttemptResolved records an operator's decision on a pending
	// activation attempt too old for the ledger to answer for (issue 435).
	ActivationAttemptResolved = events.Type(Context, "activationattempt", "resolved", 1)
)

// ActivationAttemptResolvedData is the payload of ActivationAttemptResolved.
// Outcome is "posted" (the operator confirmed the ledger entry JournalEntryID
// exists and the activation was recorded) or "not-posted" (the operator
// confirmed nothing landed and the attempt was discarded).
type ActivationAttemptResolvedData struct {
	CostingVersionID     string `json:"costingVersionId"`
	LegalEntityID        string `json:"legalEntityId"`
	ActivationID         string `json:"activationId"`
	ReversesActivationID string `json:"reversesActivationId,omitempty"`
	Outcome              string `json:"outcome"`
	JournalEntryID       string `json:"journalEntryId,omitempty"`
	Reason               string `json:"reason"`
	Actor                string `json:"actor"`
}

// PostingAccountsChangedData is the payload of PostingAccountsChanged: the
// ledger accounts activations of the legal entity post to from now on.
type PostingAccountsChangedData struct {
	LegalEntityID     string `json:"legalEntityId"`
	InventoryAccount  string `json:"inventoryAccount"`
	CostChangeAccount string `json:"costChangeAccount"`
	Actor             string `json:"actor,omitempty"`
}

// StandardCostActivatedData is the payload of StandardCostActivated (PRD 7.39.5:
// costingVersionId, itemCount, revaluationJournalId, totalRevaluation). A
// reversal is a new activation that names the one it reverses.
type StandardCostActivatedData struct {
	ActivationID             string `json:"activationId"`
	CostingVersionID         string `json:"costingVersionId"`
	LegalEntityID            string `json:"legalEntityId"`
	ReversesActivationID     string `json:"reversesActivationId,omitempty"`
	ItemCount                int    `json:"itemCount"`
	RevaluationJournalID     string `json:"revaluationJournalId"`
	TotalRevaluation         string `json:"totalRevaluation"`
	Currency                 string `json:"currency"`
	PostingDate              string `json:"postingDate"`
	NewStandardForOpenOrders bool   `json:"newStandardForOpenOrders"`
	Actor                    string `json:"actor,omitempty"`
}

// CostComponentCreatedData is the payload of CostComponentCreated.
type CostComponentCreatedData struct {
	ID            string `json:"id"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	CostGroupType string `json:"costGroupType"`
	Behaviour     string `json:"behaviour"`
}

// CostGroupCreatedData is the payload of CostGroupCreated.
type CostGroupCreatedData struct {
	ID            string `json:"id"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	Behaviour     string `json:"behaviour"`
	ComponentID   string `json:"componentId"`
	ComponentCode string `json:"componentCode"`
}

// VersionCreatedData is the payload of VersionCreated. A new version is Open.
type VersionCreatedData struct {
	ID                string     `json:"id"`
	LegalEntityID     string     `json:"legalEntityId"`
	Code              string     `json:"code"`
	Type              string     `json:"type"`
	Status            string     `json:"status"`
	Fallback          string     `json:"fallback"`
	FallbackVersionID *string    `json:"fallbackVersionId"`
	SiteIDs           []string   `json:"siteIds"`
	EffectiveFrom     *time.Time `json:"effectiveFrom"`
}

// VersionTransitionedData is the payload of VersionTransitioned: one lifecycle
// action (block, unblock, activate, close) and the status it moved between.
type VersionTransitionedData struct {
	ID            string     `json:"id"`
	LegalEntityID string     `json:"legalEntityId"`
	Code          string     `json:"code"`
	Type          string     `json:"type"`
	Action        string     `json:"action"`
	FromStatus    string     `json:"fromStatus"`
	ToStatus      string     `json:"toStatus"`
	Version       int        `json:"version"`
	ActivatedAt   *time.Time `json:"activatedAt"`
	Actor         string     `json:"actor,omitempty"`
}
