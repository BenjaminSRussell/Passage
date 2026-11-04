package domain

import "time"

type PeriodStatus struct {
	LegalEntityID   string     `json:"legalEntityId"`
	LedgerID        string     `json:"ledgerId"`
	FiscalPeriodID  string     `json:"fiscalPeriodId"`
	Module          Module     `json:"module"`
	Status          Status     `json:"status"`
	AllowedGroupID  string     `json:"allowedGroupId,omitempty"`
	ReopenCount     int        `json:"reopenCount"`
	ReopenExpiresAt *time.Time `json:"reopenExpiresAt,omitempty"`
	ChangedBy       string     `json:"changedBy"`
	StartDate       string     `json:"startDate,omitempty"`
	EndDate         string     `json:"endDate,omitempty"`
}

type PeriodTransition struct {
	LegalEntityID  string
	LedgerID       string
	FiscalPeriodID string
	Module         Module
	To             Status
	AllowedGroupID string
	Reason         string
	Actor          string
	LockWait       time.Duration
}

type PeriodChanged struct {
	LedgerID       string           `json:"ledgerId"`
	FiscalPeriodID string           `json:"fiscalPeriodId"`
	Module         Module           `json:"module"`
	PreviousStatus Status           `json:"previousStatus"`
	NewStatus      Status           `json:"newStatus"`
	ChangedBy      string           `json:"changedBy"`
	Reason         string           `json:"reason,omitempty"`
	Approvals      []ReopenApproval `json:"approvals,omitempty"`
}

type StoredReopen struct {
	ID             string           `json:"id"`
	LegalEntityID  string           `json:"legalEntityId"`
	LedgerID       string           `json:"ledgerId"`
	FiscalPeriodID string           `json:"fiscalPeriodId"`
	Module         Module           `json:"module"`
	Reason         string           `json:"reason"`
	RequestedBy    string           `json:"requestedBy"`
	Approvals      []ReopenApproval `json:"approvals"`
	Required       int              `json:"requiredApprovals"`
	Status         string           `json:"status"`
	ExpiresDays    int              `json:"expiresDays"`
	IdempotencyKey string           `json:"-"`
	RequestHash    string           `json:"-"`
}

type TemplateTask struct {
	Key              string   `json:"key"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Module           Module   `json:"module"`
	AssigneeRole     string   `json:"assigneeRole"`
	DueOffsetDays    int      `json:"dueOffsetDays"`
	DependsOn        []string `json:"dependsOn"`
	EvidenceRequired bool     `json:"evidenceRequired"`
	ManualOverride   bool     `json:"manualOverrideAllowed"`
}

type Template struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Version int            `json:"version"`
	Tasks   []TemplateTask `json:"tasks"`
}

type CloseRun struct {
	ID             string   `json:"id"`
	TemplateID     string   `json:"templateId"`
	FiscalPeriodID string   `json:"fiscalPeriodId"`
	PeriodEnd      string   `json:"periodEnd"`
	LegalEntityIDs []string `json:"legalEntityIds"`
	Status         string   `json:"status"`
	TaskCount      int      `json:"taskCount"`
	CreatedBy      string   `json:"createdBy"`
}

type BoardTask struct {
	ID             string    `json:"id"`
	RunID          string    `json:"runId"`
	LegalEntityID  string    `json:"legalEntityId"`
	TaskKey        string    `json:"taskKey"`
	Module         Module    `json:"module"`
	Name           string    `json:"name"`
	AssigneeRole   string    `json:"assigneeRole"`
	AssigneeUserID string    `json:"assigneeUserId,omitempty"`
	DueAt          time.Time `json:"dueAt"`
	DependsOn      []string  `json:"dependsOn"`
	Status         string    `json:"status"`
	EvidenceURI    string    `json:"evidenceUri,omitempty"`
	SkipReason     string    `json:"skipReason,omitempty"`
}

type AllocationRule struct {
	ID               string           `json:"id"`
	LegalEntityID    string           `json:"legalEntityId"`
	LedgerID         string           `json:"ledgerId"`
	Name             string           `json:"name"`
	Method           string           `json:"method"`
	SourceAccounts   []string         `json:"sourceAccounts"`
	Destinations     []AllocationDest `json:"destinations"`
	OffsetAccount    string           `json:"offsetAccount"`
	OffsetDimensions string           `json:"offsetDimensions"`
	AllocateFromZero bool             `json:"allocateFromZero"`
}

type AllocationRun struct {
	ID              string             `json:"id"`
	LegalEntityID   string             `json:"legalEntityId"`
	LedgerID        string             `json:"ledgerId"`
	FiscalPeriodID  string             `json:"fiscalPeriodId"`
	RuleID          string             `json:"ruleId"`
	PostingDate     string             `json:"postingDate"`
	Currency        string             `json:"currency"`
	SourceAmount    string             `json:"sourceAmount"`
	Mode            string             `json:"mode"`
	Status          string             `json:"status"`
	Proposal        AllocationProposal `json:"proposal"`
	EntryID         string             `json:"entryId,omitempty"`
	ReversalEntryID string             `json:"reversalEntryId,omitempty"`
	CreatedBy       string             `json:"createdBy"`
}

type AccrualDefinition struct {
	LegalEntityID string       `json:"legalEntityId"`
	LedgerID      string       `json:"ledgerId"`
	Code          string       `json:"code"`
	DebitAccount  string       `json:"debitAccount"`
	CreditAccount string       `json:"creditAccount"`
	Amount        string       `json:"amount"`
	Frequency     string       `json:"frequency"`
	Occurrences   int          `json:"occurrences"`
	Spread        SpreadMethod `json:"spread"`
	Custom        []string     `json:"custom,omitempty"`
	AutoReverse   bool         `json:"autoReverse"`
	UpdatedBy     string       `json:"updatedBy,omitempty"`
}

type AccrualRun struct {
	ID            string              `json:"id"`
	LegalEntityID string              `json:"legalEntityId"`
	LedgerID      string              `json:"ledgerId"`
	SchemeCode    string              `json:"schemeCode"`
	Amount        string              `json:"amount"`
	Currency      string              `json:"currency"`
	StartDate     string              `json:"startDate"`
	Status        string              `json:"status"`
	DebitAccount  string              `json:"debitAccount"`
	CreditAccount string              `json:"creditAccount"`
	AutoReverse   bool                `json:"autoReverse"`
	Occurrences   []AccrualOccurrence `json:"occurrences"`
	EntryIDs      map[string]string   `json:"entryIds"`
	CreatedBy     string              `json:"createdBy"`
	LastError     string              `json:"lastError,omitempty"`
	NextDue       *time.Time          `json:"nextDue,omitempty"`
	TenantID      string              `json:"-"`
}

type StoredRegroupingRule struct {
	LegalEntityID     string       `json:"legalEntityId"`
	LedgerID          string       `json:"ledgerId"`
	Code              string       `json:"code"`
	SortMethod        SortMethod   `json:"sortMethod"`
	SourceAccount     string       `json:"sourceAccount"`
	Buckets           []TermBucket `json:"buckets,omitempty"`
	AdjustmentAccount string       `json:"adjustmentAccount,omitempty"`
	TargetAccount     string       `json:"targetAccount,omitempty"`
	NormalBalance     string       `json:"normalBalance,omitempty"`
	Sequence          int          `json:"sequence"`
}

type RegroupingRun struct {
	ID              string        `json:"id"`
	LegalEntityID   string        `json:"legalEntityId"`
	LedgerID        string        `json:"ledgerId"`
	KeyDate         string        `json:"keyDate"`
	RuleCodes       []string      `json:"ruleCodes"`
	Mode            string        `json:"mode"`
	Status          string        `json:"status"`
	Proposal        []ReclassLine `json:"proposal"`
	EntryID         string        `json:"entryId,omitempty"`
	ReversalEntryID string        `json:"reversalEntryId,omitempty"`
	CreatedBy       string        `json:"createdBy"`
}

type RunCreated struct {
	RunID          string   `json:"runId"`
	TemplateID     string   `json:"templateId"`
	FiscalPeriodID string   `json:"fiscalPeriodId"`
	LegalEntityIDs []string `json:"legalEntityIds"`
	TaskCount      int      `json:"taskCount"`
}

type TaskCompletedEvent struct {
	RunID  string `json:"runId"`
	TaskID string `json:"taskId"`
	Status string `json:"status"`
	Actor  string `json:"actor"`
	Late   bool   `json:"late"`
}

type TaskReassigned struct {
	RunID    string `json:"runId"`
	TaskID   string `json:"taskId"`
	Assignee string `json:"assignee"`
	Actor    string `json:"actor"`
}

type AllocationCompleted struct {
	RunID    string `json:"runId"`
	LedgerID string `json:"ledgerId"`
	EntryID  string `json:"entryId"`
	Reversed bool   `json:"reversed"`
}

type AccrualScheduled struct {
	RunID      string `json:"runId"`
	SchemeCode string `json:"schemeCode"`
	LedgerID   string `json:"ledgerId"`
}

type RegroupingCompleted struct {
	RunID    string `json:"runId"`
	LedgerID string `json:"ledgerId"`
	KeyDate  string `json:"keyDate"`
	EntryID  string `json:"entryId"`
	Reversed bool   `json:"reversed"`
}
