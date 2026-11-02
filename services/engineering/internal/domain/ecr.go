package domain

import (
	"slices"
	"strings"

	"github.com/BenjaminSRussell/passage/services/engineering/internal/events"
)

// EcrState is the FR-9.7 request workflow: draft, submitted, under_review, then approved or rejected.
type EcrState string

const (
	EcrDraft       EcrState = "draft"
	EcrSubmitted   EcrState = "submitted"
	EcrUnderReview EcrState = "under_review"
	EcrApproved    EcrState = "approved"
	EcrRejected    EcrState = "rejected"
	EcrCancelled   EcrState = "cancelled"
)

var ecrTransitions = map[EcrState][]EcrState{
	EcrDraft:       {EcrSubmitted, EcrCancelled},
	EcrSubmitted:   {EcrUnderReview, EcrCancelled},
	EcrUnderReview: {EcrApproved, EcrRejected, EcrCancelled},
}

var ecrPriorities = []string{"low", "medium", "high", "critical"}

const (
	MaxEcrTitle    = 200
	MaxEcrReason   = 2000
	MaxEcrProducts = 100
	MaxEcrProduct  = 64
)

// Ecr is an engineering change request: why a product's engineering data should change. An approved
// ECR may be named by one live ECO, which carries the actual version replacements.
type Ecr struct {
	ID             string
	Number         string
	Title          string
	Reason         string
	Priority       string
	State          EcrState
	Products       []string
	RequestedBy    string
	DecidedBy      string
	DecisionReason string
	EcoID          string
}

// Validate checks the caller-supplied fields and returns them by field name.
func (e *Ecr) Validate() map[string]string {
	fields := map[string]string{}
	e.Number, e.Title, e.Reason = strings.TrimSpace(e.Number), strings.TrimSpace(e.Title), strings.TrimSpace(e.Reason)
	if n := len(e.Number); n == 0 || n > 40 {
		fields["number"] = "must be 1 to 40 characters"
	}
	if n := len([]rune(e.Title)); n == 0 || n > MaxEcrTitle {
		fields["title"] = "must be 1 to 200 characters"
	}
	if len([]rune(e.Reason)) > MaxEcrReason {
		fields["reason"] = "must be at most 2000 characters"
	}
	if !slices.Contains(ecrPriorities, e.Priority) {
		fields["priority"] = "must be low, medium, high or critical"
	}
	if len(e.Products) == 0 || len(e.Products) > MaxEcrProducts {
		fields["products"] = "must list 1 to 100 affected products"
	}
	seen := map[string]bool{}
	for i, p := range e.Products {
		p = strings.TrimSpace(p)
		e.Products[i] = p
		if n := len(p); n == 0 || n > MaxEcrProduct || seen[p] {
			fields["products"] = "each product must be 1 to 64 characters and listed once"
		}
		seen[p] = true
	}
	return fields
}

func (e *Ecr) Transition(to EcrState) error {
	if !slices.Contains(ecrTransitions[e.State], to) {
		return errf(CodeEcrState, "cannot move ECR %s from %s to %s", e.Number, e.State, to)
	}
	e.State = to
	return nil
}

func (e *Ecr) Submit() error { return e.Transition(EcrSubmitted) }

// Cancel withdraws a request that has not been decided. Only the requester withdraws; a reviewer ends a
// request by rejecting it, so a decision is never taken under the withdrawal command.
func (e *Ecr) Cancel(actor, reason string) error {
	if !slices.Contains(ecrTransitions[e.State], EcrCancelled) {
		return e.Transition(EcrCancelled)
	}
	switch {
	case actor == "":
		return errf(CodeEcrActorRequired, "withdrawing an ECR needs the acting user")
	case actor != e.RequestedBy:
		return errf(CodeEcrNotRequester, "only %s, who requested ECR %s, can withdraw it; a reviewer rejects it instead", e.RequestedBy, e.Number)
	}
	if err := e.Transition(EcrCancelled); err != nil {
		return err
	}
	e.DecidedBy, e.DecisionReason = actor, reason
	return nil
}

// StartReview moves a submitted request to under_review. The requester cannot review their own request.
func (e *Ecr) StartReview(actor string) error {
	if err := e.checkSeparation(actor); err != nil {
		return err
	}
	return e.Transition(EcrUnderReview)
}

// Decide approves or rejects a request under review. The requester cannot decide it.
func (e *Ecr) Decide(to EcrState, actor, reason string) error {
	if to != EcrApproved && to != EcrRejected {
		return errf(CodeEcrState, "an ECR is decided as approved or rejected, not %s", to)
	}
	if e.State != EcrUnderReview {
		return errf(CodeEcrState, "ECR %s is %s, only an ECR under review can be decided", e.Number, e.State)
	}
	if err := e.checkSeparation(actor); err != nil {
		return err
	}
	e.DecidedBy, e.DecisionReason = actor, reason
	return e.Transition(to)
}

func (e *Ecr) checkSeparation(actor string) error {
	switch {
	case actor == "":
		return errf(CodeEcrActorRequired, "reviewing an ECR needs the acting user")
	case actor == e.RequestedBy:
		return errf(CodeEcrSelfApproval, "the user who requested ECR %s cannot review or decide it", e.Number)
	}
	return nil
}

// CheckEcoSource applies the ECR to ECO link: the ECR must be approved, not already named by a live
// ECO, and every product an ECO replaces must be one the ECR lists (ENG-ECR-PRODUCT).
func (e Ecr) CheckEcoSource(replacedProducts []string) error {
	if e.State != EcrApproved {
		return errf(CodeEcrNotApproved, "ECR %s is %s, only an approved ECR can be the source of an ECO", e.Number, e.State)
	}
	if e.EcoID != "" {
		return errf(CodeEcrConverted, "ECR %s already has ECO %s", e.Number, e.EcoID)
	}
	for _, p := range replacedProducts {
		if !slices.Contains(e.Products, p) {
			return errf(CodeEcrProduct, "product %s is not among the affected products of ECR %s", p, e.Number)
		}
	}
	return nil
}

func (e Ecr) EventData() events.EcrData {
	return events.EcrData{EcrID: e.ID, EcrNumber: e.Number, Title: e.Title, Priority: e.Priority, Products: slices.Clone(e.Products), RequestedBy: e.RequestedBy, DecidedBy: e.DecidedBy}
}
