// Package events holds the event types and payloads the intercompany service
// publishes through the outbox (events/asyncapi.yaml).
package events

import "github.com/BenjaminSRussell/passage/platform/events"

// Context is the bounded-context segment of every event type.
const Context = "interco"

// Source is the CloudEvents source of every event the service publishes.
const Source = "/passage/intercompany"

// PartnerCreated and PartnerUpdated are not in the PRD 7.40.5 event table.
// They are added so the mirror-saga worker (interco-svc, a separate
// deployable) and the sales and procurement services see every pairing and
// policy version the saga will apply (FR-40.1: policies versioned).
var (
	PartnerCreated = events.Type(Context, "partner", "created", 1)
	PartnerUpdated = events.Type(Context, "partner", "updated", 1)
)

// PartnerCreatedData is the payload of PartnerCreated.
type PartnerCreatedData struct {
	ID              string `json:"id"`
	SellerEntity    string `json:"sellerEntity"`
	BuyerEntity     string `json:"buyerEntity"`
	CustomerAccount string `json:"customerAccount"`
	VendorAccount   string `json:"vendorAccount"`
	Active          bool   `json:"active"`
	Pricing         string `json:"pricing"`
	Version         int    `json:"version"`
}

// PartnerUpdatedData is the payload of PartnerUpdated. ChangedFields names the
// API fields that changed (customerAccount, vendorAccount, active, actions,
// pricing, markupPct, tolerance).
type PartnerUpdatedData struct {
	ID            string   `json:"id"`
	SellerEntity  string   `json:"sellerEntity"`
	BuyerEntity   string   `json:"buyerEntity"`
	Active        bool     `json:"active"`
	ChangedFields []string `json:"changedFields"`
	Version       int      `json:"version"`
}
