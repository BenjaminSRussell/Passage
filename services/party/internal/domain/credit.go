package domain

import (
	"slices"
	"time"
)

const (
	AggregateCustomer = "customer"
	FieldCreditLimit  = "credit_limit"
	ChangePending     = "pending"
	ChangeApproved    = "approved"
	ChangeRejected    = "rejected"
)

var ProtectableFields = []string{FieldCreditLimit}

func DefaultProtectedFields() []string { return slices.Clone(ProtectableFields) }

type PendingChange struct {
	ID             string     `json:"id"`
	CustomerID     string     `json:"customerId"`
	LegalEntityID  string     `json:"legalEntityId"`
	Field          string     `json:"field"`
	CurrentValue   *string    `json:"currentValue"`
	RequestedValue string     `json:"requestedValue"`
	Status         string     `json:"status"`
	RequestedBy    string     `json:"requestedBy"`
	DecidedBy      *string    `json:"decidedBy,omitempty"`
	Reason         *string    `json:"reason,omitempty"`
	RequestedAt    time.Time  `json:"requestedAt"`
	DecidedAt      *time.Time `json:"decidedAt,omitempty"`
	Replayed       bool       `json:"-"`
}

type CreditLimitResult struct {
	CreditLimit *string        `json:"creditLimit"`
	Pending     *PendingChange `json:"pendingChange,omitempty"`
}
