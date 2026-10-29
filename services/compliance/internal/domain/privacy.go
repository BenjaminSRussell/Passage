package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Subject-request, erasure and retention codes (FR-48.10, FR-48.11, BR-48.5, BR-48.6).
const (
	CodePrivacyLegalHold  = "PRV-1201" // erasure or purge blocked by a legal hold
	CodePrivacyInput      = "PRV-1202" // unknown type, regulation, deadline or missing subject
	CodePrivacyVerify     = "PRV-1203" // identity not verified: two attributes and the e-mail link are required
	CodePrivacyTransition = "PRV-1204" // action not allowed from the current state
	CodePrivacyLate       = "PRV-1205" // closing after the deadline needs a justification
	CodePrivacyDeadline   = "PRV-1206" // deadline override is longer than the regulation allows
	CodePrivacyReason     = "PRV-1207" // rejection needs a reason
	CodePrivacyPurge      = "PRV-1208" // retention purge skipped (BR-48.6)

	privacyJustifyMin     = 10
	identityAttributesMin = 2
	privacyDueSoonDays    = 5
)

// PrivacyRequestType is the GDPR/CCPA request kind (FR-48.10).
type PrivacyRequestType string

const (
	RequestAccess        PrivacyRequestType = "ACCESS"
	RequestPortability   PrivacyRequestType = "PORTABILITY"
	RequestErasure       PrivacyRequestType = "ERASURE"
	RequestRectification PrivacyRequestType = "RECTIFICATION"
	RequestRestriction   PrivacyRequestType = "RESTRICTION"
)

// Regulation sets the response window of a subject request.
type Regulation string

const (
	RegulationGDPR Regulation = "GDPR"
	RegulationCCPA Regulation = "CCPA"
)

// PrivacyState is the subject-request state (9.48.4 subject_request).
type PrivacyState string

const (
	PrivacyReceived   PrivacyState = "RECEIVED"
	PrivacyVerified   PrivacyState = "VERIFIED"
	PrivacyDiscovered PrivacyState = "DISCOVERED"
	PrivacyHeld       PrivacyState = "HELD"
	PrivacyApproved   PrivacyState = "APPROVED"
	PrivacyDelivered  PrivacyState = "DELIVERED"
	PrivacyClosed     PrivacyState = "CLOSED"
	PrivacyRejected   PrivacyState = "REJECTED"
)

// PrivacyAction moves a request between states.
type PrivacyAction string

const (
	ActionVerify       PrivacyAction = "VERIFY"
	ActionDiscover     PrivacyAction = "DISCOVER"
	ActionReleaseHold  PrivacyAction = "RELEASE_HOLD"
	ActionApprove      PrivacyAction = "APPROVE"
	ActionDeliver      PrivacyAction = "DELIVER"
	PrivacyActionClose PrivacyAction = "CLOSE"
	ActionReject       PrivacyAction = "REJECT"
)

// ErasedFields are the PII fields pseudonymized by an erasure (BR-48.5). Amounts,
// dates and dimension values are kept so postings still reconcile.
var ErasedFields = []string{"name", "address", "email", "phone", "tax_id", "bank_account", "date_of_birth"}

// PrivacyPlanInput is the intake plan: deadline, days remaining and the step list.
type PrivacyPlanInput struct {
	Type         PrivacyRequestType `json:"type"`
	Regulation   Regulation         `json:"regulation"`
	ReceivedAt   time.Time          `json:"receivedAt"`
	DeadlineDays int                `json:"deadlineDays,omitempty"`
	Now          time.Time          `json:"-"`
}

// PrivacyPlan is the computed deadline for a request.
type PrivacyPlan struct {
	DeadlineAt    time.Time `json:"deadlineAt"`
	DaysRemaining int       `json:"daysRemaining"`
	DueSoon       bool      `json:"dueSoon"`
	Steps         []string  `json:"steps"`
}

// PlanPrivacyRequest computes the deadline (GDPR 30 days, CCPA 45 days). A shorter
// deadlineDays override is allowed; a longer one is refused (PRV-1206).
func PlanPrivacyRequest(in PrivacyPlanInput) (PrivacyPlan, error) {
	if !validPrivacyType(in.Type) {
		return PrivacyPlan{}, &Error{Code: CodePrivacyInput, Detail: fmt.Sprintf("request type %q is not supported", in.Type)}
	}
	base, ok := privacyRegulationDays(in.Regulation)
	if !ok {
		return PrivacyPlan{}, &Error{Code: CodePrivacyInput, Detail: fmt.Sprintf("regulation %q is not supported", in.Regulation)}
	}
	if in.ReceivedAt.IsZero() {
		return PrivacyPlan{}, &Error{Code: CodePrivacyInput, Detail: "receivedAt is required"}
	}
	days := base
	if in.DeadlineDays != 0 {
		if in.DeadlineDays < 1 || in.DeadlineDays > base {
			return PrivacyPlan{}, &Error{Code: CodePrivacyDeadline, Detail: fmt.Sprintf("deadline %d days is outside 1..%d for %s", in.DeadlineDays, base, in.Regulation)}
		}
		days = in.DeadlineDays
	}
	deadline := in.ReceivedAt.UTC().AddDate(0, 0, days)
	left := privacyDaysLeft(deadline, in.Now)
	return PrivacyPlan{DeadlineAt: deadline, DaysRemaining: left, DueSoon: left <= privacyDueSoonDays, Steps: privacyPlanSteps(in.Type)}, nil
}

func privacyRegulationDays(r Regulation) (int, bool) {
	switch r {
	case RegulationGDPR:
		return 30, true
	case RegulationCCPA:
		return 45, true
	}
	return 0, false
}

// privacyDaysLeft rounds up so a deadline due in 4.5 days shows 5 and an overdue one is negative.
func privacyDaysLeft(deadline, now time.Time) int {
	return int(math.Ceil(deadline.Sub(now).Hours() / 24))
}

// ValidPrivacyState reports whether s is one of the subject_request states.
func ValidPrivacyState(s PrivacyState) bool {
	switch s {
	case PrivacyReceived, PrivacyVerified, PrivacyDiscovered, PrivacyHeld, PrivacyApproved, PrivacyDelivered, PrivacyClosed, PrivacyRejected:
		return true
	}
	return false
}

// ValidPrivacyType reports whether t is a supported request type.
func ValidPrivacyType(t PrivacyRequestType) bool { return validPrivacyType(t) }

// ValidRegulation reports whether r is a supported regulation.
func ValidRegulation(r Regulation) bool {
	_, ok := privacyRegulationDays(r)
	return ok
}

func validPrivacyType(t PrivacyRequestType) bool {
	switch t {
	case RequestAccess, RequestPortability, RequestErasure, RequestRectification, RequestRestriction:
		return true
	}
	return false
}

func privacyPlanSteps(t PrivacyRequestType) []string {
	fanout := "fan out per domain (Temporal child workflow per domain)"
	switch t {
	case RequestErasure:
		fanout = "fan out per domain: pseudonymize PII fields, keep financial records with erased=true"
	case RequestAccess, RequestPortability:
		fanout = "fan out per domain: collect data; compile JSON and PDF bundle"
	}
	return []string{
		"intake (portal or admin)",
		"verify identity: two attributes and the e-mail link",
		"discover scope across domains via PII classification",
		"check legal holds and retention",
		fanout,
		"approve (privacy officer)",
		"deliver via signed link",
		"close",
	}
}

// PrivacyTransitionInput is one state change of a subject request.
type PrivacyTransitionInput struct {
	Type              PrivacyRequestType `json:"type"`
	From              PrivacyState       `json:"from"`
	Action            PrivacyAction      `json:"action"`
	HoldIDs           []string           `json:"holdIds,omitempty"`
	MatchedAttributes []string           `json:"matchedAttributes,omitempty"`
	EmailLinkVerified bool               `json:"emailLinkVerified"`
	Justification     string             `json:"justification,omitempty"`
	DeadlineAt        time.Time          `json:"deadlineAt"`
	Now               time.Time          `json:"-"`
}

// PrivacyTransitionOutcome is the state after a valid transition.
type PrivacyTransitionOutcome struct {
	State PrivacyState `json:"state"`
	Late  bool         `json:"late"`
}

// ApplyPrivacyTransition applies one action. An erasure with a covering legal hold
// moves to HELD at discovery and cannot be approved until the hold is released (AC-48.7).
func ApplyPrivacyTransition(in PrivacyTransitionInput) (PrivacyTransitionOutcome, error) {
	holds := len(privacyDistinct(in.HoldIDs)) > 0
	if !ValidPrivacyState(in.From) {
		return PrivacyTransitionOutcome{}, &Error{Code: CodePrivacyTransition, Detail: fmt.Sprintf("state %q is not a subject-request state", in.From)}
	}
	bad := &Error{Code: CodePrivacyTransition, Detail: fmt.Sprintf("action %s is not allowed from %s", in.Action, in.From)}
	next := func(s PrivacyState) (PrivacyTransitionOutcome, error) { return PrivacyTransitionOutcome{State: s}, nil }
	switch in.Action {
	case ActionReject:
		if in.From == PrivacyClosed || in.From == PrivacyRejected {
			return PrivacyTransitionOutcome{}, bad
		}
		if len(strings.TrimSpace(in.Justification)) < privacyJustifyMin {
			return PrivacyTransitionOutcome{}, &Error{Code: CodePrivacyReason, Detail: "a rejection needs a reason of at least 10 characters"}
		}
		return next(PrivacyRejected)
	case ActionVerify:
		if in.From != PrivacyReceived {
			return PrivacyTransitionOutcome{}, bad
		}
		if len(privacyDistinct(in.MatchedAttributes)) < identityAttributesMin || !in.EmailLinkVerified {
			return PrivacyTransitionOutcome{}, &Error{Code: CodePrivacyVerify, Detail: "identity needs two matching attributes and a verified e-mail link"}
		}
		return next(PrivacyVerified)
	case ActionDiscover:
		if in.From != PrivacyVerified {
			return PrivacyTransitionOutcome{}, bad
		}
		if in.Type == RequestErasure && holds {
			return next(PrivacyHeld)
		}
		return next(PrivacyDiscovered)
	case ActionReleaseHold:
		if in.From != PrivacyHeld {
			return PrivacyTransitionOutcome{}, bad
		}
		if holds {
			return PrivacyTransitionOutcome{}, privacyHoldError(in.HoldIDs)
		}
		return next(PrivacyDiscovered)
	case ActionApprove:
		if in.From != PrivacyDiscovered {
			return PrivacyTransitionOutcome{}, bad
		}
		if in.Type == RequestErasure && holds {
			return PrivacyTransitionOutcome{}, privacyHoldError(in.HoldIDs)
		}
		return next(PrivacyApproved)
	case ActionDeliver:
		if in.From != PrivacyApproved {
			return PrivacyTransitionOutcome{}, bad
		}
		return next(PrivacyDelivered)
	case PrivacyActionClose:
		if in.From != PrivacyDelivered {
			return PrivacyTransitionOutcome{}, bad
		}
		late := in.Now.After(in.DeadlineAt)
		if late && len(strings.TrimSpace(in.Justification)) < privacyJustifyMin {
			return PrivacyTransitionOutcome{}, &Error{Code: CodePrivacyLate, Detail: "closing after the deadline needs a justification of at least 10 characters"}
		}
		return PrivacyTransitionOutcome{State: PrivacyClosed, Late: late}, nil
	}
	return PrivacyTransitionOutcome{}, bad
}

func privacyHoldError(holdIDs []string) error {
	return &Error{Code: CodePrivacyLegalHold, Detail: fmt.Sprintf("erasure blocked: subject is under legal hold %s; no field was changed", strings.Join(PrivacyHolds(holdIDs), ", "))}
}

// PrivacyHolds trims, de-duplicates and sorts hold ids.
func PrivacyHolds(in []string) []string {
	out := []string{}
	for h := range privacyDistinct(in) {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// PseudonymizeInput asks for the pseudonymization of one subject's record. The
// caller has checked that the request is an ERASURE past discovery; HoldIDs are
// the holds recorded on the request.
type PseudonymizeInput struct {
	SubjectRef string            `json:"subjectRef"`
	HoldIDs    []string          `json:"holdIds,omitempty"`
	Record     map[string]string `json:"record"`
}

// PseudonymizeResult is the record after pseudonymization.
type PseudonymizeResult struct {
	Record        map[string]string `json:"record"`
	Pseudonymized []string          `json:"pseudonymized"`
}

// PseudonymizeSubject pseudonymizes PII fields in place of deleting rows (BR-48.5).
// A covering legal hold refuses the whole erasure, so no field changes (AC-48.7).
func PseudonymizeSubject(in PseudonymizeInput) (PseudonymizeResult, error) {
	if len(privacyDistinct(in.HoldIDs)) > 0 {
		return PseudonymizeResult{}, privacyHoldError(in.HoldIDs)
	}
	if strings.TrimSpace(in.SubjectRef) == "" {
		return PseudonymizeResult{}, &Error{Code: CodePrivacyInput, Detail: "subjectRef is required"}
	}
	out := make(map[string]string, len(in.Record)+1)
	for k, v := range in.Record {
		out[k] = v
	}
	changed := []string{}
	for _, f := range ErasedFields {
		if v, ok := in.Record[f]; ok && v != "" {
			out[f] = privacyPseudonym(in.SubjectRef, f)
			changed = append(changed, f)
		}
	}
	out["erased"] = "true"
	return PseudonymizeResult{Record: out, Pseudonymized: changed}, nil
}

// privacyPseudonym is deterministic per subject and field, so joins on the token still work.
func privacyPseudonym(subject, field string) string {
	sum := sha256.Sum256([]byte(subject + "|" + field))
	return "ERASED-" + hex.EncodeToString(sum[:8])
}

// PurgeCandidate is a record considered by the monthly retention purge.
type PurgeCandidate struct {
	RecordClass      string   `json:"recordClass"`
	HoldIDs          []string `json:"holdIds,omitempty"`
	FiscalPeriodOpen bool     `json:"fiscalPeriodOpen"`
	OpenDocumentRefs []string `json:"openDocumentRefs,omitempty"`
}

// PurgeDecision says whether a record may be purged and why not otherwise.
type PurgeDecision struct {
	Purgeable bool     `json:"purgeable"`
	Code      string   `json:"code,omitempty"`
	Reasons   []string `json:"reasons"`
}

// PurgeCheck applies BR-48.6: a record under hold, in an open fiscal period or
// referenced by an open document is skipped by the purge.
func PurgeCheck(c PurgeCandidate) PurgeDecision {
	d := PurgeDecision{Purgeable: true, Reasons: []string{}}
	if holds := PrivacyHolds(c.HoldIDs); len(holds) > 0 {
		d.Reasons = append(d.Reasons, "under legal hold "+strings.Join(holds, ", "))
	}
	if c.FiscalPeriodOpen {
		d.Reasons = append(d.Reasons, "record sits in an open fiscal period")
	}
	if refs := PrivacyHolds(c.OpenDocumentRefs); len(refs) > 0 {
		d.Reasons = append(d.Reasons, "referenced by open document "+strings.Join(refs, ", "))
	}
	if len(d.Reasons) > 0 {
		d.Purgeable = false
		d.Code = CodePrivacyPurge
	}
	return d
}

func privacyDistinct(in []string) map[string]bool {
	out := map[string]bool{}
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out[s] = true
		}
	}
	return out
}
