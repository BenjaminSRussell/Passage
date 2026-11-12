package domain

import "time"

// Break-glass for ops.failover.execute (PSG-50, FR-48.23 pattern). An operator who does not hold the duty opens a
// time-boxed window for themselves with a reason code and a ticket; while it lasts the failover routes accept them, every
// use is logged, and another operator reviews the window afterwards.
const (
	BreakGlassMinMinutes     = 15
	BreakGlassMaxMinutes     = 120
	BreakGlassDefaultMinutes = 60
	// BreakGlassReviewDays is how long after the window ends the review is due.
	BreakGlassReviewDays = 3

	CodeBreakGlassInvalid   = "OPS-1716"
	CodeBreakGlassActive    = "OPS-1717"
	CodeBreakGlassReview    = "OPS-1718"
	CodeBreakGlassUnknown   = "OPS-1719"
	CodeBreakGlassRequired  = "OPS-1720"
	ReviewApproved          = "APPROVED"
	ReviewFlagged           = "FLAGGED"
	maxBreakGlassTicketLen  = 128
	maxBreakGlassNoteLength = 2000
)

var breakGlassReasons = map[string]bool{
	"INCIDENT_RESPONSE":      true,
	"PRODUCTION_OUTAGE":      true,
	"REGION_LOSS":            true,
	"APPROVER_UNAVAILABLE":   true,
	"AUDIT_EVIDENCE_REQUEST": true,
}

// BreakGlassRequest is the input of PlanBreakGlass. UserID is the verified caller.
type BreakGlassRequest struct {
	UserID     string
	ReasonCode string
	TicketRef  string
	Minutes    int // 0 means the default
	Start      time.Time
}

// BreakGlassWindow is the validated window.
type BreakGlassWindow struct {
	UserID      string    `json:"userId"`
	ReasonCode  string    `json:"reasonCode"`
	TicketRef   string    `json:"ticketRef"`
	Minutes     int       `json:"minutes"`
	StartsAt    time.Time `json:"startsAt"`
	EndsAt      time.Time `json:"endsAt"`
	ReviewDueAt time.Time `json:"reviewDueAt"`
}

// PlanBreakGlass validates a request: a known reason, a ticket and 15 to 120 minutes. The review is due three days after
// the window ends.
func PlanBreakGlass(r BreakGlassRequest) (BreakGlassWindow, error) {
	if r.UserID == "" {
		return BreakGlassWindow{}, fail(CodeBreakGlassInvalid, "break-glass needs a named user")
	}
	if !breakGlassReasons[r.ReasonCode] {
		return BreakGlassWindow{}, fail(CodeBreakGlassInvalid, "reasonCode must be one of INCIDENT_RESPONSE, PRODUCTION_OUTAGE, REGION_LOSS, APPROVER_UNAVAILABLE, AUDIT_EVIDENCE_REQUEST")
	}
	if r.TicketRef == "" || len(r.TicketRef) > maxBreakGlassTicketLen {
		return BreakGlassWindow{}, fail(CodeBreakGlassInvalid, "ticketRef (the incident or change ticket, up to %d characters) is required", maxBreakGlassTicketLen)
	}
	minutes := r.Minutes
	if minutes == 0 {
		minutes = BreakGlassDefaultMinutes
	}
	if minutes < BreakGlassMinMinutes || minutes > BreakGlassMaxMinutes {
		return BreakGlassWindow{}, fail(CodeBreakGlassInvalid, "minutes must be between %d and %d", BreakGlassMinMinutes, BreakGlassMaxMinutes)
	}
	ends := r.Start.Add(time.Duration(minutes) * time.Minute)
	return BreakGlassWindow{
		UserID: r.UserID, ReasonCode: r.ReasonCode, TicketRef: r.TicketRef, Minutes: minutes,
		StartsAt: r.Start, EndsAt: ends, ReviewDueAt: ends.AddDate(0, 0, BreakGlassReviewDays),
	}, nil
}

// ValidateReview checks a review: the outcome is APPROVED or FLAGGED, a flag needs a note, and the reviewer is not the
// person who used the window.
func ValidateReview(user, reviewer, outcome, note string) error {
	if reviewer == "" {
		return fail(CodeBreakGlassReview, "a review needs a named reviewer")
	}
	if reviewer == user {
		return fail(CodeBreakGlassReview, "the operator who used the window cannot review it")
	}
	if outcome != ReviewApproved && outcome != ReviewFlagged {
		return fail(CodeBreakGlassReview, "outcome must be APPROVED or FLAGGED")
	}
	if outcome == ReviewFlagged && note == "" {
		return fail(CodeBreakGlassReview, "a flagged window needs a note saying what was wrong")
	}
	if len(note) > maxBreakGlassNoteLength {
		return fail(CodeBreakGlassReview, "note is limited to %d characters", maxBreakGlassNoteLength)
	}
	return nil
}
