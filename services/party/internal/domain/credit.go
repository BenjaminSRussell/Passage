package domain

import "time"

const (
	AggregateCustomer = "customer"
	FieldCreditLimit  = "credit_limit"
	ChangePending     = "pending"
	ChangeApproved    = "approved"
	ChangeRejected    = "rejected"
)

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
}

type CreditLimitResult struct {
	CreditLimit *string        `json:"creditLimit"`
	Pending     *PendingChange `json:"pendingChange,omitempty"`
}
