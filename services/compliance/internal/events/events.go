// Package events holds the compliance event types and payloads (see events/asyncapi.yaml).
package events

import (
	"time"

	"github.com/BenjaminSRussell/passage/platform/events"
)

// Context is the event context of segregation of duties, as the PRD names its events.
const Context = "sod"

// TradeContext is the event context of trade compliance (PRD §9.48.5).
const TradeContext = "trade"

var (
	// ConflictDetected is emitted when an evaluation raises a conflict, or raises a RESOLVED one again.
	ConflictDetected = events.Type(Context, "conflict", "detected", 1)
	// MitigationApproved is emitted when a non-conflicted approver mitigates a conflict.
	MitigationApproved = events.Type(Context, "mitigation", "approved", 1)
	// ScreeningHit is emitted when screening opens a hit on a party or document;
	// sales, procurement and payments hold the subject until it is decided (FR-48.19).
	ScreeningHit = events.Type(TradeContext, "screening", "hit", 1)
)

// All lists every event type this service publishes.
func All() []string { return []string{ConflictDetected, MitigationApproved, ScreeningHit} }

// ConflictDetectedData is the data member of sod.conflict.detected.v1
// (PRD §9.48.5: user_id, rule_id, severity, roles[]).
type ConflictDetectedData struct {
	ConflictID string   `json:"conflict_id"`
	UserID     string   `json:"user_id"`
	RuleID     string   `json:"rule_id"`
	RiskID     string   `json:"risk_id"`
	Severity   string   `json:"severity"`
	DutyA      string   `json:"duty_a"`
	DutyB      string   `json:"duty_b"`
	Roles      []string `json:"roles"`
}

// MitigationApprovedData is the data member of sod.mitigation.approved.v1
// (PRD §9.48.5: conflict_id, approver, expires_at).
type MitigationApprovedData struct {
	ConflictID   string    `json:"conflict_id"`
	MitigationID string    `json:"mitigation_id"`
	UserID       string    `json:"user_id"`
	RuleID       string    `json:"rule_id"`
	ControlRef   string    `json:"control_ref"`
	Approver     string    `json:"approver"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// ScreeningHitData is the data member of trade.screening.hit.v1
// (PRD §9.48.5: subject_type, subject_id, list_code, score). The score is a
// decimal string with three places, as stored in screening_hit.score.
type ScreeningHitData struct {
	HitID         string   `json:"hit_id"`
	SubjectType   string   `json:"subject_type"`
	SubjectID     string   `json:"subject_id"`
	ListCode      string   `json:"list_code"`
	ListEntryID   string   `json:"list_entry_id"`
	Score         string   `json:"score"`
	MatchedFields []string `json:"matched_fields"`
}
