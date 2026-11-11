package events

import (
	"time"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
)

const Context = "party"

var (
	PartyCreated = events.Type(Context, "party", "created", 1)
	PartyUpdated = events.Type(Context, "party", "updated", 1)

	CustomerCreated = events.Type(Context, "customer", "created", 1)
	CustomerUpdated = events.Type(Context, "customer", "updated", 1)
	ChangeRequested = events.Type(Context, "change", "requested", 1)
	ChangeApproved  = events.Type(Context, "change", "approved", 1)
	ChangeRejected  = events.Type(Context, "change", "rejected", 1)

	ProtectedFieldsUpdated = events.Type(Context, "protectedfields", "updated", 1)

	MergeRequested = events.Type(Context, "merge", "requested", 1)
	MergeRejected  = events.Type(Context, "merge", "rejected", 1)

	VendorBankRevealed = events.Type(Context, "vendorbank", "revealed", 1)
	VendorGroupCreated = events.Type(Context, "vendorgroup", "created", 1)

	VendorInviteCreated  = events.Type(Context, "vendorinvite", "created", 1)
	VendorInviteAccepted = events.Type(Context, "vendorinvite", "accepted", 1)
)

func All() []string {
	return []string{PartyCreated, PartyUpdated, domain.PartyMergedType, CustomerCreated, CustomerUpdated, ChangeRequested, ChangeApproved, ChangeRejected, ProtectedFieldsUpdated, MergeRequested, MergeRejected, VendorBankRevealed, VendorGroupCreated, VendorInviteCreated, VendorInviteAccepted}
}

type CustomerCreatedData struct {
	CustomerID      string  `json:"customer_id"`
	PartyID         string  `json:"party_id"`
	LegalEntityID   string  `json:"legal_entity_id"`
	AccountNumber   string  `json:"account_number"`
	CustomerGroupID string  `json:"customer_group_id"`
	CurrencyCode    string  `json:"currency_code"`
	CreditLimit     *string `json:"credit_limit"`
	Blocked         string  `json:"blocked"`
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
	Reason         *string `json:"reason,omitempty"`
}

type ProtectedFieldsUpdatedData struct {
	LegalEntityID string    `json:"legal_entity_id"`
	Before        []string  `json:"before"`
	After         []string  `json:"after"`
	ActorUserID   string    `json:"actor_user_id"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type VendorBankRevealedData struct {
	VendorID      string    `json:"vendor_id"`
	LegalEntityID string    `json:"legal_entity_id"`
	ActorUserID   string    `json:"actor_user_id"`
	ActorService  string    `json:"actor_service,omitempty"`
	AccountCount  int       `json:"account_count"`
	RevealedAt    time.Time `json:"revealed_at"`
}

type PartyCreatedData struct {
	PartyID     string `json:"party_id"`
	PartyNumber string `json:"party_number"`
	PartyType   string `json:"party_type"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Version     int    `json:"version"`
}

type PartyUpdatedData struct {
	PartyID       string   `json:"party_id"`
	PartyNumber   string   `json:"party_number"`
	PartyType     string   `json:"party_type"`
	Name          string   `json:"name"`
	Status        string   `json:"status"`
	ChangedFields []string `json:"changed_fields"`
	Version       int      `json:"version"`
}

type VendorGroupCreatedData struct {
	VendorGroupID string `json:"vendor_group_id"`
	Code          string `json:"code"`
	Name          string `json:"name"`
}

// MergeRequestData starts the workflow-ts merge approval (merge.requested) or
// closes it without a merge (merge.rejected); an approval is announced by party.merged.
type MergeRequestData struct {
	MergeRequestID  string  `json:"merge_request_id"`
	SurvivorPartyID string  `json:"survivor_party_id"`
	MergedPartyID   string  `json:"merged_party_id"`
	RequestedBy     string  `json:"requested_by"`
	DecidedBy       string  `json:"decided_by,omitempty"`
	Reason          *string `json:"reason,omitempty"`
}

// VendorInviteData is the payload of the invitation events. It names the address and never carries the token: the token is shown
// once to the vendor administrator who made the invitation.
type VendorInviteData struct {
	InviteID      string    `json:"invite_id"`
	VendorID      string    `json:"vendor_id"`
	LegalEntityID string    `json:"legal_entity_id"`
	Email         string    `json:"email"`
	ExpiresAt     time.Time `json:"expires_at"`
}
