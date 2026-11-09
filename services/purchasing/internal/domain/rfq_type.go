package domain

import (
	"regexp"
	"strings"
	"time"
)

// RFQ type and amendment problem codes (listed in the service README).
const (
	// CodeRFQAmend: the amendment breaks a rule (type, state of the deadline, scores recorded, ...).
	CodeRFQAmend = "PRC-RFQ-008"
	// CodeRFQEdit: a field of the RFQ cannot be changed in its status or type.
	CodeRFQEdit = "PRC-RFQ-009"
	// CodeRFQTypeInvalid: the RFQ type cannot be applied (inactive or another legal entity).
	CodeRFQTypeInvalid = "PRC-RFQ-TYPE-001"
)

// RFQ type statuses.
const (
	RFQTypeActive   = "ACTIVE"
	RFQTypeInactive = "INACTIVE"
)

const (
	maxRFQTypeName    = 200
	maxAmendmentNote  = 2000
	maxRFQTypesPerLE  = 200
	rfqTypeCodeFormat = `^[A-Z0-9_-]{1,20}$`
)

var rfqTypeCodeRe = regexp.MustCompile(rfqTypeCodeFormat)

// RFQType is the setup of a kind of RFQ (FR-17.19): STANDARD or SEALED, whether its RFQs may be amended,
// the ranking rule and the default scoring group. Version is the ETag value.
type RFQType struct {
	ID                string             `json:"id"`
	LegalEntityID     string             `json:"legalEntityId"`
	Code              string             `json:"code"`
	Name              string             `json:"name"`
	Sealed            bool               `json:"sealed"`
	AmendmentsAllowed bool               `json:"amendmentsAllowed"`
	RankingRule       string             `json:"rankingRule"`
	Status            string             `json:"status"`
	Criteria          []ScoringCriterion `json:"criteria"`
	Version           int                `json:"version"`
	CreatedBy         string             `json:"createdBy,omitempty"`
	CreatedAt         time.Time          `json:"createdAt"`
	UpdatedBy         string             `json:"updatedBy,omitempty"`
	UpdatedAt         time.Time          `json:"updatedAt"`
}

// Validate checks an RFQ type as entered.
func (t RFQType) Validate() error {
	if !rfqTypeCodeRe.MatchString(t.Code) {
		return fail(CodeValidation, "code must be 1 to 20 characters of A-Z, 0-9, _ or -")
	}
	if n := len(strings.TrimSpace(t.Name)); n == 0 || n > maxRFQTypeName {
		return fail(CodeValidation, "name must be 1 to %d characters", maxRFQTypeName)
	}
	if t.Status != RFQTypeActive && t.Status != RFQTypeInactive {
		return fail(CodeValidation, "status must be ACTIVE or INACTIVE")
	}
	if err := ValidRankingRule(t.RankingRule); err != nil {
		return err
	}
	return ValidateCriteria(t.RankingRule, t.Criteria)
}

// RFQAmendment is one addendum to a sent RFQ (RFQ-n-A<k>): the summary of the change, the deadline it moved
// and whether it replaced the scoring criteria, with the acknowledgement of each invited vendor.
type RFQAmendment struct {
	AmendmentNo      int        `json:"amendmentNo"`
	Summary          string     `json:"summary"`
	PreviousDeadline *time.Time `json:"previousDeadline,omitempty"`
	NewDeadline      *time.Time `json:"newDeadline,omitempty"`
	CriteriaChanged  bool       `json:"criteriaChanged"`
	// LateClearedVendorIDs are the vendors whose replies were late only against PreviousDeadline and count as
	// on time since this amendment moved the deadline (BR-17.11); kept as the audit of that change.
	LateClearedVendorIDs []string           `json:"lateClearedVendorIds"`
	IssuedBy             string             `json:"issuedBy,omitempty"`
	IssuedAt             time.Time          `json:"issuedAt"`
	Acknowledgements     []AmendmentAckInfo `json:"acknowledgements"`
}

// AmendmentAckInfo is one vendor's acknowledgement of an amendment; AcknowledgedAt is empty until recorded.
type AmendmentAckInfo struct {
	VendorID       string     `json:"vendorId"`
	AcknowledgedAt *time.Time `json:"acknowledgedAt,omitempty"`
}

// AmendmentInput is what the buyer enters: a summary, optionally a later reply deadline and optionally a
// replacement scoring group.
type AmendmentInput struct {
	Summary       string
	ReplyDeadline *time.Time
	Criteria      []ScoringCriterion
	// ReplaceCriteria is true when the request carried criteria (an empty list clears them).
	ReplaceCriteria bool
}

// CheckAmendment decides whether an RFQ may be amended at now (FR-17.19): it must be SENT or REPLIES_OPEN,
// its type must allow amendments, a sealed RFQ must still be sealed (its replies cannot be opened and then
// amended), a new deadline must lie after both now and the current deadline, an amendment that does not move
// the deadline must come before it, and the scoring group is replaced only while no vendor was scored and no
// reply is open to the buyer: once the buyer can read the prices of an open (unsealed) RFQ, re-weighting the
// criteria could steer the award, so the RFQ must be cancelled and re-issued instead. A sealed RFQ before its
// deadline may still replace them; its replies stay unreadable and vendors may revise them until the deadline.
func CheckAmendment(r RFQ, in AmendmentInput, now time.Time) error {
	if r.Status != RFQSent && r.Status != RFQRepliesOpen {
		return fail(CodeRFQState, "an RFQ in %s cannot be amended; only SENT or REPLIES_OPEN", r.Status)
	}
	if !r.AmendmentsAllowed {
		return fail(CodeRFQAmend, "the type of RFQ %s does not allow amendments", r.Number)
	}
	if n := len(strings.TrimSpace(in.Summary)); n == 0 || n > maxAmendmentNote {
		return fail(CodeRFQAmend, "the summary of the change must be 1 to %d characters", maxAmendmentNote)
	}
	if r.Sealed && !now.Before(r.ReplyDeadline) {
		return fail(CodeRFQAmend, "RFQ %s is sealed and past its deadline; its replies are open and cannot be amended", r.Number)
	}
	if in.ReplyDeadline != nil {
		if !in.ReplyDeadline.After(now) || !in.ReplyDeadline.After(r.ReplyDeadline) {
			return fail(CodeRFQAmend, "the new reply deadline must be after now and after the current deadline %s", r.ReplyDeadline.UTC().Format(time.RFC3339))
		}
	} else if !now.Before(r.ReplyDeadline) {
		return fail(CodeRFQAmend, "the reply deadline of RFQ %s has passed; the amendment must give a later deadline", r.Number)
	}
	if in.ReplaceCriteria {
		if len(r.Scores) > 0 {
			return fail(CodeRFQAmend, "scores are recorded on RFQ %s; its scoring criteria can no longer be replaced", r.Number)
		}
		if !r.Sealed && max(len(r.Replies), r.ReplyCount) > 0 {
			return fail(CodeRFQAmend, "RFQ %s is not sealed and its replies are already open to the buyer; its scoring criteria can no longer be replaced", r.Number)
		}
		if err := ValidateCriteria(r.RankingRule, in.Criteria); err != nil {
			return err
		}
	}
	return nil
}

// RFQEdit is the change of a DRAFT RFQ: the deadline, the ranking rule, the sealed flag and the scoring group.
type RFQEdit struct {
	ReplyDeadline time.Time
	RankingRule   string
	Sealed        bool
	Criteria      []ScoringCriterion
}

// CheckEdit validates the edit of a DRAFT RFQ (FR-17.19): the RFQ must be DRAFT (a sent RFQ changes only by
// amendment), the deadline must lie in the future, a type that requires sealing keeps the RFQ sealed, and the
// scoring group must suit the ranking rule.
func CheckEdit(r RFQ, in RFQEdit, now time.Time) error {
	if r.Status != RFQDraft {
		return fail(CodeRFQState, "an RFQ in %s cannot be edited; amend a sent RFQ instead", r.Status)
	}
	if !in.ReplyDeadline.After(now) {
		return fail(CodeValidation, "replyDeadline must be in the future")
	}
	if r.SealedRequired && !in.Sealed {
		return fail(CodeRFQEdit, "the type of RFQ %s requires sealed bids", r.Number)
	}
	if err := ValidRankingRule(in.RankingRule); err != nil {
		return err
	}
	return ValidateCriteria(in.RankingRule, in.Criteria)
}
