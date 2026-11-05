package domain

import (
	"testing"
	"time"

	"pgregory.net/rapid"
)

func baseRequest() ElevatedRequest {
	return ElevatedRequest{
		RequesterID:  "u-1",
		TargetUserID: "u-1",
		Roles:        []string{"QualityInspector"},
		ReasonCode:   "PRODUCTION_OUTAGE",
		TicketRef:    "INC-4411",
		Start:        time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC),
	}
}

func TestPlanElevatedSession_DefaultsAndApproval(t *testing.T) {
	plan, err := PlanElevatedSession(baseRequest())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Minutes != BreakGlassDefaultMinutes {
		t.Fatalf("default minutes: got %d", plan.Minutes)
	}
	if !plan.NeedsApproval || plan.ApproverDuty != ApproverBreakGlass || plan.RequiresPostReview {
		t.Fatalf("default path needs approval by a compliance.break-glass.approve holder: %+v", plan)
	}
	if !plan.EndsAt.Equal(plan.StartsAt.Add(60 * time.Minute)) {
		t.Fatalf("end must be start + 60 min: %+v", plan)
	}
}

func TestPlanElevatedSession_ReviewDueInThreeBusinessDays(t *testing.T) {
	// AC-48.18: a 60 min session ending Monday 2026-11-02 is reviewed by Thursday 2026-11-05.
	r := baseRequest()
	r.Start = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	plan, err := PlanElevatedSession(r)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	if !plan.ReviewDueAt.Equal(want) {
		t.Fatalf("review due: want %s, got %s", want, plan.ReviewDueAt)
	}
}

func TestPlanElevatedSession_Rejections(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ElevatedRequest)
		code   string
	}{
		{"unknown reason", func(r *ElevatedRequest) { r.ReasonCode = "BORED" }, CodeBreakGlassRequest},
		{"no ticket", func(r *ElevatedRequest) { r.TicketRef = "" }, CodeBreakGlassRequest},
		{"no roles", func(r *ElevatedRequest) { r.Roles = nil }, CodeBreakGlassRequest},
		{"over tenant cap", func(r *ElevatedRequest) { r.RequestedMinutes = 90 }, CodeBreakGlassRequest},
		{"under minimum", func(r *ElevatedRequest) { r.RequestedMinutes = 5 }, CodeBreakGlassRequest},
		{"tenant cap above hard limit", func(r *ElevatedRequest) { r.TenantMaxMinutes = 300 }, CodeBreakGlassRequest},
		{"blank role", func(r *ElevatedRequest) { r.Roles = []string{"QualityInspector", "  "} }, CodeBreakGlassRequest},
		{"no requester", func(r *ElevatedRequest) { r.RequesterID = "" }, CodeBreakGlassRequest},
		{"review window above 10 days", func(r *ElevatedRequest) { r.ReviewDays = 11 }, CodeBreakGlassRequest},
		{"self grant padded name", func(r *ElevatedRequest) { r.Roles = []string{" compliance.security.admin "} }, CodeBreakGlassSelfGrant},
		{"self grant compliance.security.admin", func(r *ElevatedRequest) { r.Roles = []string{"compliance.security.admin"} }, CodeBreakGlassSelfGrant},
		{"self grant platform.global.admin mixed", func(r *ElevatedRequest) { r.Roles = []string{"QualityInspector", "platform.global.admin"} }, CodeBreakGlassSelfGrant},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := baseRequest()
			tc.mutate(&r)
			_, err := PlanElevatedSession(r)
			if got := codeOf(err); got != tc.code {
				t.Fatalf("want %s, got %q (%v)", tc.code, got, err)
			}
		})
	}
}

func TestPlanElevatedSession_OtherUserMayGrantProtectedRole(t *testing.T) {
	// BR-48.19 blocks self-grant only; a second platform.global.admin holder may grant to another user.
	r := baseRequest()
	r.RequesterID = "admin-2"
	r.TargetUserID = "u-9"
	r.Roles = []string{"platform.global.admin"}
	r.SelfActivate = true
	plan, err := PlanElevatedSession(r)
	if err != nil {
		t.Fatalf("grant to another user should plan: %v", err)
	}
	// Even with self-activation on, a protected role waits for a second platform.global.admin.
	if !plan.NeedsApproval || plan.ApproverDuty != ApproverPlatformAdmin || plan.RequiresPostReview {
		t.Fatalf("protected role must need platform.global.admin approval: %+v", plan)
	}
}

func TestPlanElevatedSession_RolesTrimmedAndDeduplicated(t *testing.T) {
	r := baseRequest()
	r.Roles = []string{"QualityInspector", " QualityInspector ", "APClerk"}
	plan, err := PlanElevatedSession(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Roles) != 2 || plan.Roles[0] != "QualityInspector" || plan.Roles[1] != "APClerk" {
		t.Fatalf("roles: %v", plan.Roles)
	}
}

func TestPlanElevatedSession_TenantReviewDays(t *testing.T) {
	// breakglass.review_days = 1: a session ending Friday 10:00 is due Monday 10:00.
	r := baseRequest()
	r.Start = time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	r.ReviewDays = 1
	plan, err := PlanElevatedSession(r)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC); !plan.ReviewDueAt.Equal(want) {
		t.Fatalf("review due: want %s, got %s", want, plan.ReviewDueAt)
	}
}

func TestPlanElevatedSession_SelfActivatePostReview(t *testing.T) {
	r := baseRequest()
	r.SelfActivate = true
	r.TenantMaxMinutes = 240
	r.RequestedMinutes = 240
	plan, err := PlanElevatedSession(r)
	if err != nil {
		t.Fatal(err)
	}
	if plan.NeedsApproval || plan.ApproverDuty != "" || !plan.RequiresPostReview || plan.Minutes != 240 {
		t.Fatalf("self-activate path: %+v", plan)
	}
}

func TestReviewOverdue(t *testing.T) {
	ended := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC) // Monday
	reviewed := ended.AddDate(0, 0, 1)
	tests := []struct {
		name     string
		reviewed *time.Time
		now      time.Time
		days     int
		want     bool
	}{
		{"within SLA", nil, time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC), 0, false},
		{"past SLA unreviewed", nil, time.Date(2026, 11, 5, 10, 1, 0, 0, time.UTC), 0, true},
		{"past SLA but reviewed", &reviewed, time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC), 0, false},
		{"tenant SLA of 5 days not yet passed", nil, time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC), 5, false},
		{"tenant SLA of 5 days passed", nil, time.Date(2026, 11, 9, 10, 1, 0, 0, time.UTC), 5, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReviewOverdue(ended, tc.reviewed, tc.now, tc.days); got != tc.want {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
		})
	}
}

func TestAddBusinessDays_SkipsWeekend(t *testing.T) {
	friday := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	if got := AddBusinessDays(friday, 1); got.Weekday() != time.Monday {
		t.Fatalf("one business day after Friday is Monday, got %s", got.Weekday())
	}
}

func TestPlanElevatedSession_PropertyLengthNeverExceeds240(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := baseRequest()
		r.RequestedMinutes = rapid.IntRange(0, 1000).Draw(t, "minutes")
		r.TenantMaxMinutes = rapid.IntRange(0, 1000).Draw(t, "cap")
		plan, err := PlanElevatedSession(r)
		if err != nil {
			return
		}
		if plan.Minutes > BreakGlassHardMaxMinutes || plan.EndsAt.Sub(plan.StartsAt) > 240*time.Minute {
			t.Fatalf("session longer than 240 minutes: %+v", plan)
		}
	})
}
