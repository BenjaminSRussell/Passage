package events

import (
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
)

const Context = "party"

var (
	PartyCreated = events.Type(Context, "party", "created", 1)
	PartyUpdated = events.Type(Context, "party", "updated", 1)

	CustomerUpdated = events.Type(Context, "customer", "updated", 1)
	ChangeRequested = events.Type(Context, "change", "requested", 1)
	ChangeApproved  = events.Type(Context, "change", "approved", 1)
	ChangeRejected  = events.Type(Context, "change", "rejected", 1)
)

func All() []string {
	return []string{PartyCreated, PartyUpdated, domain.PartyMergedType, CustomerUpdated, ChangeRequested, ChangeApproved, ChangeRejected}
}

type CustomerUpdatedData struct {
	CustomerID    string   `json:"customer_id"`
	LegalEntityID string   `json:"legal_entity_id"`
	ChangedFields []string `json:"changed_fields"`
	CreditLimit   string   `json:"credit_limit"`
}

type ChangeData struct {
	ChangeID       string  `json:"change_id"`
	CustomerID     string  `json:"customer_id"`
	LegalEntityID  string  `json:"legal_entity_id"`
	Field          string  `json:"field"`
	CurrentValue   *string `json:"current_value"`
	RequestedValue string  `json:"requested_value"`
	RequestedBy    string  `json:"requested_by"`
	DecidedBy      string  `json:"decided_by,omitempty"`
}

type PartyCreatedData struct {
	PartyID     string `json:"party_id"`
	PartyNumber string `json:"party_number"`
	PartyType   string `json:"party_type"`
	Name        string `json:"name"`
}

type PartyUpdatedData struct {
	PartyID       string   `json:"party_id"`
	ChangedFields []string `json:"changed_fields"`
	Version       int      `json:"version"`
}
