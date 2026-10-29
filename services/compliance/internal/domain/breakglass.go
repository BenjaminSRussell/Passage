package domain

import (
	"fmt"
	"strings"
	"time"
)

// Break-glass (emergency access) rules, FR-48.23 and BR-48.19.
const (
	BreakGlassHardMaxMinutes = 240 // invariant: ended_at - started_at <= 240 min
	BreakGlassMinMinutes     = 15
	BreakGlassDefaultMinutes = 60
	BreakGlassReviewDays     = 3 // breakglass.review_days default: reviewer sign-off SLA in business days
	BreakGlassMinReviewDays  = 1
	BreakGlassMaxReviewDays  = 10
)

// Approver duties. A session granting a protected role needs a second PlatformAdmin
// (BR-48.19); any other session is approved by a BreakGlassApprove holder.
const (
	ApproverBreakGlass    = "BreakGlassApprove"
	ApproverPlatformAdmin = "PlatformAdmin"
)

// Break-glass codes.
const (
	CodeBreakGlassSelfGrant = "SEC-0707" // cannot grant SecurityAdmin or PlatformAdmin to yourself
	CodeBreakGlassReview    = "SEC-0708" // session not reviewed within the SLA
	CodeBreakGlassRequest   = "SEC-0713" // request incomplete or out of range
)

// Reason codes accepted on an elevated session request.
var breakGlassReasons = map[string]bool{
	"INCIDENT_RESPONSE":        true,
	"PRODUCTION_OUTAGE":        true,
	"DATA_CORRECTION_APPROVED": true,
	"PERIOD_CLOSE_BLOCKER":     true,
	"AUDIT_EVIDENCE_REQUEST":   true,
}

// Roles a user can never be granted through break-glass by themselves (BR-48.19).
var protectedRoles = map[string]bool{"SecurityAdmin": true, "PlatformAdmin": true}

// ElevatedRequest is a break-glass request. RequesterID is the calling user, never a
// value the client chooses; TenantMaxMinutes, SelfActivate and ReviewDays are the tenant
// parameters breakglass.max_minutes, breakglass.self_activate and breakglass.review_days.
type ElevatedRequest struct {
	RequesterID      string
	TargetUserID     string
	Roles            []string
	ReasonCode       string
	TicketRef        string
	RequestedMinutes int // 0 means the default (60, or the tenant maximum when lower)
	TenantMaxMinutes int // 0 means 60
	SelfActivate     bool
	ReviewDays       int // 0 means 3
	Start            time.Time
}

// ElevatedPlan is the validated session the request would open.
type ElevatedPlan struct {
	Roles              []string  `json:"roles"`
	Minutes            int       `json:"minutes"`
	StartsAt           time.Time `json:"startsAt"`
	EndsAt             time.Time `json:"endsAt"`
	ReviewDueAt        time.Time `json:"reviewDueAt"`
	NeedsApproval      bool      `json:"needsApproval"`
	ApproverDuty       string    `json:"approverDuty,omitempty"`
	RequiresPostReview bool      `json:"requiresPostReview"`
}

// PlanElevatedSession validates a request and computes the session window, the approval
// path and the reviewer deadline. The session length is capped at min(tenant max, 240).
// Roles are trimmed and de-duplicated. A protected role is never self-activated: it waits
// for a second PlatformAdmin (BR-48.19). Every session, approved or self-activated, gets a
// reviewer deadline counted from its end (FR-48.23). SoD evaluation for the session is run
// by the caller on the granted roles.
func PlanElevatedSession(r ElevatedRequest) (ElevatedPlan, error) {
	if !breakGlassReasons[r.ReasonCode] {
		return ElevatedPlan{}, &Error{Code: CodeBreakGlassRequest, Detail: fmt.Sprintf("reason code %q is not allowed", r.ReasonCode)}
	}
	if r.TicketRef == "" {
		return ElevatedPlan{}, &Error{Code: CodeBreakGlassRequest, Detail: "a ticket reference is required"}
	}
	var roles []string
	seen := map[string]bool{}
	for _, role := range r.Roles {
		role = strings.TrimSpace(role)
		if role == "" {
			return ElevatedPlan{}, &Error{Code: CodeBreakGlassRequest, Detail: "role names must not be empty"}
		}
		if !seen[role] {
			seen[role] = true
			roles = append(roles, role)
		}
	}
	if len(roles) == 0 {
		return ElevatedPlan{}, &Error{Code: CodeBreakGlassRequest, Detail: "at least one role is required"}
	}
	if r.RequesterID == "" || r.TargetUserID == "" {
		return ElevatedPlan{}, &Error{Code: CodeBreakGlassRequest, Detail: "requester and target user are required"}
	}
	maxMinutes := r.TenantMaxMinutes
	if maxMinutes == 0 {
		maxMinutes = BreakGlassDefaultMinutes
	}
	if maxMinutes < BreakGlassMinMinutes || maxMinutes > BreakGlassHardMaxMinutes {
		return ElevatedPlan{}, &Error{Code: CodeBreakGlassRequest, Detail: "tenant maximum must be between 15 and 240 minutes"}
	}
	minutes := r.RequestedMinutes
	if minutes == 0 {
		minutes = min(BreakGlassDefaultMinutes, maxMinutes)
	}
	if minutes < BreakGlassMinMinutes || minutes > maxMinutes {
		return ElevatedPlan{}, &Error{Code: CodeBreakGlassRequest, Detail: fmt.Sprintf("requested minutes must be %d..%d", BreakGlassMinMinutes, maxMinutes)}
	}
	reviewDays := r.ReviewDays
	if reviewDays == 0 {
		reviewDays = BreakGlassReviewDays
	}
	if reviewDays < BreakGlassMinReviewDays || reviewDays > BreakGlassMaxReviewDays {
		return ElevatedPlan{}, &Error{Code: CodeBreakGlassRequest, Detail: fmt.Sprintf("review window must be %d..%d business days", BreakGlassMinReviewDays, BreakGlassMaxReviewDays)}
	}
	protected := false
	for _, role := range roles {
		if protectedRoles[role] {
			if r.RequesterID == r.TargetUserID {
				return ElevatedPlan{}, &Error{Code: CodeBreakGlassSelfGrant, Detail: fmt.Sprintf("break-glass cannot grant %s to yourself", role)}
			}
			protected = true
		}
	}
	start := r.Start.UTC()
	end := start.Add(time.Duration(minutes) * time.Minute)
	plan := ElevatedPlan{
		Roles:       roles,
		Minutes:     minutes,
		StartsAt:    start,
		EndsAt:      end,
		ReviewDueAt: AddBusinessDays(end, reviewDays),
	}
	switch {
	case protected:
		plan.NeedsApproval, plan.ApproverDuty = true, ApproverPlatformAdmin
	case r.SelfActivate:
		plan.RequiresPostReview = true
	default:
		plan.NeedsApproval, plan.ApproverDuty = true, ApproverBreakGlass
	}
	return plan, nil
}

// AddBusinessDays adds n Monday-to-Friday days to t (UTC). Public holidays are not
// modelled here; the calendar pack supplies them in the caller when needed.
func AddBusinessDays(t time.Time, n int) time.Time {
	d := t.UTC()
	for n > 0 {
		d = d.AddDate(0, 0, 1)
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday {
			n--
		}
	}
	return d
}

// ReviewOverdue reports whether an ended session has no reviewer sign-off past its SLA of
// reviewDays business days (0 means 3; SEC-0708, FR-48.23). A reviewed session is never
// overdue.
func ReviewOverdue(endedAt time.Time, reviewedAt *time.Time, now time.Time, reviewDays int) bool {
	if reviewedAt != nil {
		return false
	}
	if reviewDays == 0 {
		reviewDays = BreakGlassReviewDays
	}
	return now.UTC().After(AddBusinessDays(endedAt, reviewDays))
}
