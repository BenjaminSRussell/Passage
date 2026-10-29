package domain

import (
	"strings"
	"time"
)

// Codes raised by the invoice proposal rules. PROJ-PROP-002 is in §8.41.18;
// PROJ-PROP-003 and PROJ-PROP-004 are service-specific.
const (
	// CodeProposalEmpty: no billable transaction matched the selection.
	CodeProposalEmpty = "PROJ-PROP-002"
	// CodeProposalState: the proposal's status does not allow the step.
	CodeProposalState = "PROJ-PROP-003"
	// CodeProposalType: the project's type does not bill these transactions.
	CodeProposalType = "PROJ-PROP-004"
)

// ProposalStatus is the lifecycle of an invoice proposal (§8.41.7: DRAFT →
// APPROVED → POSTED; DRAFT or APPROVED → CANCELED).
type ProposalStatus string

// Proposal statuses.
const (
	ProposalDraft    ProposalStatus = "DRAFT"
	ProposalApproved ProposalStatus = "APPROVED"
	ProposalPosted   ProposalStatus = "POSTED"
	ProposalCanceled ProposalStatus = "CANCELED"
)

// ValidProposalStatus reports whether s is a known status.
func ValidProposalStatus(s ProposalStatus) bool {
	switch s {
	case ProposalDraft, ProposalApproved, ProposalPosted, ProposalCanceled:
		return true
	}
	return false
}

// InvoiceProposal bundles the billable transactions of one project selected for
// one date window and set of transaction types (FR-41.6, §8.41.4). Totals are the
// sums of the lines in the project currency.
type InvoiceProposal struct {
	ID              string         `json:"id"`
	ProjectID       string         `json:"projectId"`
	CustomerPartyID *string        `json:"customerPartyId,omitempty"`
	Status          ProposalStatus `json:"status"`
	Currency        string         `json:"currency"`
	FromDate        string         `json:"fromDate"`
	ToDate          string         `json:"toDate"`
	TxnTypes        []TxnType      `json:"txnTypes"`
	TotalCost       string         `json:"totalCost"`
	TotalSales      string         `json:"totalSales"`
	LineCount       int            `json:"lineCount"`
	// Lines is empty in list responses.
	Lines      []ProposalLine `json:"lines"`
	CreatedBy  string         `json:"createdBy"`
	ApprovedBy *string        `json:"approvedBy,omitempty"`
	Version    int            `json:"version"`
	CreatedAt  time.Time      `json:"createdAt"`
	UpdatedAt  time.Time      `json:"updatedAt"`
}

// ProposalLine is one selected transaction: its quantity, unit sales price and
// the sales amount billed, with the transaction facts a reviewer needs.
type ProposalLine struct {
	LineNo        int     `json:"lineNo"`
	TransactionID string  `json:"transactionId"`
	TxnType       TxnType `json:"txnType"`
	TxnDate       string  `json:"txnDate"`
	CategoryID    string  `json:"categoryId"`
	Description   string  `json:"description"`
	Quantity      string  `json:"quantity"`
	UnitPrice     string  `json:"unitPrice"`
	CostAmount    string  `json:"costAmount"`
	Amount        string  `json:"amount"`
}

// ProposalRequest is the selection of one build: the project, the inclusive
// date window and the transaction types (empty means every type the project
// bills).
type ProposalRequest struct {
	ProjectID string
	From      string
	To        string
	TxnTypes  []TxnType
}

// ValidateProposalRequest returns field-level errors for a build request.
func ValidateProposalRequest(in ProposalRequest) map[string]string {
	f := map[string]string{}
	if !projectIDPattern.MatchString(in.ProjectID) {
		f["projectId"] = "must be a project id"
	}
	from, errFrom := time.Parse(time.DateOnly, in.From)
	to, errTo := time.Parse(time.DateOnly, in.To)
	if errFrom != nil {
		f["from"] = "must be a date (YYYY-MM-DD)"
	}
	if errTo != nil {
		f["to"] = "must be a date (YYYY-MM-DD)"
	}
	if errFrom == nil && errTo == nil && to.Before(from) {
		f["to"] = "must not be before from"
	}
	seen := map[TxnType]bool{}
	for _, t := range in.TxnTypes {
		if !ValidTxnType(t) || seen[t] {
			f["txnTypes"] = "must list distinct types among HOUR, COST, ITEM, FEE and ON_ACCOUNT"
		}
		seen[t] = true
	}
	return f
}

// BillableTypes returns the transaction types a proposal on a project of type
// pt selects, in TxnTypes order. A TIME_MATERIAL project bills the requested
// types (all when none are given). A FIXED_PRICE project bills only ON_ACCOUNT
// transactions (its milestone and on-account billing, BR-41.3); asking for any
// other type is PROJ-PROP-004. The other project types bill no customer, so a
// proposal on them is PROJ-PROP-004.
func BillableTypes(pt ProjectType, requested []TxnType) ([]TxnType, error) {
	switch pt {
	case TypeTimeMaterial:
		if len(requested) == 0 {
			return append([]TxnType(nil), TxnTypes...), nil
		}
		out := []TxnType{}
		for _, t := range TxnTypes {
			for _, r := range requested {
				if r == t {
					out = append(out, t)
				}
			}
		}
		return out, nil
	case TypeFixedPrice:
		for _, r := range requested {
			if r != TxnOnAccount {
				return nil, rule(CodeProposalType, "A fixed-price project bills ON_ACCOUNT milestones only, not %s transactions", r)
			}
		}
		return []TxnType{TxnOnAccount}, nil
	}
	return nil, rule(CodeProposalType, "A %s project does not bill a customer", pt)
}

// BillsMilestones reports whether a proposal on a project of type pt selects
// only due billable milestones (BR-41.3: FIXED_PRICE).
func BillsMilestones(pt ProjectType) bool { return pt == TypeFixedPrice }

// TypeNames returns the types as strings (for SQL arrays and selection criteria).
func TypeNames(types []TxnType) []string {
	out := make([]string, len(types))
	for i, t := range types {
		out[i] = string(t)
	}
	return out
}

// CheckProposalMove allows DRAFT → APPROVED (with at least one line), DRAFT →
// CANCELED and APPROVED → CANCELED. POSTED is reached only by the hand-off to
// wave-29 invoicing, which this service does not do yet.
func CheckProposalMove(from, to ProposalStatus, lines int) error {
	switch {
	case from == ProposalDraft && to == ProposalApproved && lines == 0:
		return rule(CodeProposalEmpty, "The proposal has no lines left to approve; cancel it instead")
	case from == ProposalDraft && (to == ProposalApproved || to == ProposalCanceled),
		from == ProposalApproved && to == ProposalCanceled:
		return nil
	}
	return rule(CodeProposalState, "Proposal cannot move from %s to %s", from, to)
}

// CheckLineRemoval allows removing a line while the proposal is DRAFT or
// APPROVED (FR-41.6: editable until POSTED). Removing a line from an APPROVED
// proposal returns it to DRAFT, so the changed proposal is approved again.
func CheckLineRemoval(s ProposalStatus) (ProposalStatus, error) {
	switch s {
	case ProposalDraft, ProposalApproved:
		return ProposalDraft, nil
	}
	return s, rule(CodeProposalState, "Lines cannot be removed from a %s proposal", s)
}

// EmptySelection is PROJ-PROP-002 for a build that selected nothing; stage is
// named when the project's stage alone excludes every transaction, and the
// milestone condition when the project is FIXED_PRICE (BR-41.3).
func EmptySelection(projectID string, stage Stage, pt ProjectType, types []TxnType) error {
	if stage != StageInProcess && stage != StageFinished {
		return rule(CodeProposalEmpty, "No billable transactions matched the selection: project %s is %s, and only IN_PROCESS or FINISHED projects bill", projectID, stage)
	}
	milestone := ""
	if BillsMilestones(pt) {
		milestone = ", on a billable milestone scheduled to finish by the end date"
	}
	return rule(CodeProposalEmpty, "No billable transactions matched the selection (%s, chargeable, not yet proposed, in the date range%s)",
		strings.Join(TypeNames(types), ", "), milestone)
}
