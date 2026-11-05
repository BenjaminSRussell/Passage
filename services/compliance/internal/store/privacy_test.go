package store

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
	cevents "github.com/BenjaminSRussell/passage/services/compliance/internal/events"
)

// AC-48.7 at the store: an ERASURE under a legal hold goes to HELD at discovery,
// cannot be approved while the hold stands, and only after release moves on;
// closing emits privacy.request.completed once.
func TestSubjectRequest_ErasureUnderHoldLifecycle(t *testing.T) {
	s, admin := migrated(t)
	officer := ids.New()
	ctx := as(ids.New(), officer, "compliance.privacy-request.create", "compliance.privacy-request.process")

	req, err := s.Intake(ctx, IntakeInput{Type: domain.RequestErasure, Regulation: domain.RegulationGDPR, SubjectRef: " party:42 "}, "k-intake", t0)
	if err != nil {
		t.Fatal(err)
	}
	if req.State != domain.PrivacyReceived || req.Version != 1 || req.SubjectRef != "party:42" || !req.ReceivedAt.Equal(t0) ||
		!req.DeadlineAt.Equal(t0.AddDate(0, 0, 30)) || req.CreatedBy != officer || len(req.HoldIDs) != 0 {
		t.Fatalf("intake %+v", req)
	}
	replay, err := s.Intake(ctx, IntakeInput{Type: domain.RequestErasure, Regulation: domain.RegulationGDPR, SubjectRef: " party:42 "}, "k-intake", t0.Add(time.Minute))
	if err != nil || replay.RequestID != req.RequestID {
		t.Fatalf("replay must return the first request: %+v %v", replay, err)
	}
	if _, err := s.Intake(ctx, IntakeInput{Type: domain.RequestAccess, Regulation: domain.RegulationGDPR, SubjectRef: "party:42"}, "k-intake", t0); code(err) != CodeIdempotencyReuse {
		t.Fatalf("key reuse with another body: %v", err)
	}

	step := func(version int, in RequestActionInput, at time.Time) (SubjectRequest, error) {
		return s.ActOnRequest(ctx, req.RequestID, version, in, at)
	}
	if _, err := step(1, RequestActionInput{Action: domain.ActionVerify, MatchedAttributes: []string{"email"}, EmailLinkVerified: true}, t0); code(err) != domain.CodePrivacyVerify {
		t.Fatalf("one attribute must not verify: %v", err)
	}
	v, err := step(1, RequestActionInput{Action: domain.ActionVerify, MatchedAttributes: []string{"email", "postal_code"}, EmailLinkVerified: true}, t0.Add(time.Hour))
	if err != nil || v.State != domain.PrivacyVerified || v.Version != 2 || v.VerifiedAt == nil {
		t.Fatalf("verify %+v %v", v, err)
	}
	if _, err := step(1, RequestActionInput{Action: domain.ActionDiscover}, t0); code(err) != CodeRequestVersion {
		t.Fatalf("stale version: %v", err)
	}
	held, err := step(2, RequestActionInput{Action: domain.ActionDiscover, HoldIDs: []string{"HOLD-7", " HOLD-7 ", "HOLD-3"}}, t0.Add(2*time.Hour))
	if err != nil || held.State != domain.PrivacyHeld || len(held.HoldIDs) != 2 || held.HoldIDs[0] != "HOLD-3" {
		t.Fatalf("erasure under hold must be HELD with its holds: %+v %v", held, err)
	}
	if _, err := step(3, RequestActionInput{Action: domain.ActionApprove}, t0); code(err) != domain.CodePrivacyTransition {
		t.Fatalf("HELD cannot be approved: %v", err)
	}
	if _, err := step(3, RequestActionInput{Action: domain.ActionReleaseHold, HoldIDs: []string{"HOLD-7"}}, t0); code(err) != domain.CodePrivacyLegalHold {
		t.Fatalf("release with a hold standing: %v", err)
	}
	if cur, _ := s.GetRequest(ctx, req.RequestID); cur.Version != 3 || cur.State != domain.PrivacyHeld || len(cur.HoldIDs) != 2 {
		t.Fatalf("a refused action must change nothing: %+v", cur)
	}
	disc, err := step(3, RequestActionInput{Action: domain.ActionReleaseHold}, t0.Add(3*time.Hour))
	if err != nil || disc.State != domain.PrivacyDiscovered || len(disc.HoldIDs) != 0 {
		t.Fatalf("release %+v %v", disc, err)
	}
	app, err := step(4, RequestActionInput{Action: domain.ActionApprove}, t0.Add(4*time.Hour))
	if err != nil || app.State != domain.PrivacyApproved || app.PrivacyOfficerID != officer || app.ApprovedAt == nil {
		t.Fatalf("approve %+v %v", app, err)
	}
	del, err := step(5, RequestActionInput{Action: domain.ActionDeliver}, t0.Add(5*time.Hour))
	if err != nil || del.DeliveredAt == nil {
		t.Fatalf("deliver %+v %v", del, err)
	}
	if got := outboxTypes(t, admin, req.RequestID); len(got) != 0 {
		t.Fatalf("no event before close: %v", got)
	}
	closed, err := step(6, RequestActionInput{Action: domain.PrivacyActionClose}, t0.AddDate(0, 0, 10))
	if err != nil || closed.State != domain.PrivacyClosed || closed.Late || closed.ClosedAt == nil || closed.Version != 7 {
		t.Fatalf("close %+v %v", closed, err)
	}
	if got := outboxTypes(t, admin, req.RequestID); len(got) != 1 || got[0] != cevents.RequestCompleted {
		t.Fatalf("outbox after close: %v", got)
	}
}

// A request closed after its deadline needs a justification, which is kept,
// and is marked late.
func TestSubjectRequest_LateCloseAndReject(t *testing.T) {
	s, _ := migrated(t)
	ctx := as(ids.New(), ids.New(), "compliance.privacy-request.create", "compliance.privacy-request.process")
	req, err := s.Intake(ctx, IntakeInput{Type: domain.RequestAccess, Regulation: domain.RegulationCCPA, SubjectRef: "worker:9", ReceivedAt: t0, DeadlineDays: 20}, "k1", t0)
	if err != nil || !req.DeadlineAt.Equal(t0.AddDate(0, 0, 20)) {
		t.Fatalf("intake with a shorter override: %+v %v", req, err)
	}
	if _, err := s.Intake(ctx, IntakeInput{Type: domain.RequestAccess, Regulation: domain.RegulationCCPA, SubjectRef: "worker:9", DeadlineDays: 46}, "k2", t0); code(err) != domain.CodePrivacyDeadline {
		t.Fatalf("override past 45 days: %v", err)
	}
	actions := []RequestActionInput{
		{Action: domain.ActionVerify, MatchedAttributes: []string{"email", "employee_no"}, EmailLinkVerified: true},
		{Action: domain.ActionDiscover, HoldIDs: []string{"HOLD-1"}},
		{Action: domain.ActionApprove},
		{Action: domain.ActionDeliver},
	}
	version := 1
	for _, a := range actions {
		r, err := s.ActOnRequest(ctx, req.RequestID, version, a, t0.AddDate(0, 0, 1))
		if err != nil {
			t.Fatalf("%s: %v", a.Action, err)
		}
		version = r.Version
	}
	late := t0.AddDate(0, 0, 21)
	if _, err := s.ActOnRequest(ctx, req.RequestID, version, RequestActionInput{Action: domain.PrivacyActionClose}, late); code(err) != domain.CodePrivacyLate {
		t.Fatalf("late close without justification: %v", err)
	}
	closed, err := s.ActOnRequest(ctx, req.RequestID, version, RequestActionInput{Action: domain.PrivacyActionClose, Justification: "  bundle compile waited on payroll  "}, late)
	if err != nil || !closed.Late || closed.Justification != "bundle compile waited on payroll" {
		t.Fatalf("late close %+v %v", closed, err)
	}

	other, err := s.Intake(ctx, IntakeInput{Type: domain.RequestRectification, Regulation: domain.RegulationGDPR, SubjectRef: "contact:5"}, "k3", t0)
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := s.ActOnRequest(ctx, other.RequestID, 1, RequestActionInput{Action: domain.ActionReject, Justification: "identity could not be proven"}, t0)
	if err != nil || rejected.State != domain.PrivacyRejected || rejected.ClosedAt == nil {
		t.Fatalf("reject %+v %v", rejected, err)
	}
	if _, err := s.GetRequest(ctx, ids.New()); code(err) != CodeRequestNotFound {
		t.Fatalf("missing request: %v", err)
	}
}

func TestListRequests_DeadlineOrderFiltersPagesAndTenants(t *testing.T) {
	s, _ := migrated(t)
	ctx := as(ids.New(), ids.New(), "compliance.privacy-request.create", "compliance.privacy-request.process")
	for i, typ := range []domain.PrivacyRequestType{domain.RequestAccess, domain.RequestErasure, domain.RequestAccess} {
		if _, err := s.Intake(ctx, IntakeInput{Type: typ, Regulation: domain.RegulationGDPR, SubjectRef: "party:1", ReceivedAt: t0.AddDate(0, 0, -i)}, ids.New(), t0); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListRequests(ctx, RequestFilter{Limit: 2})
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" || !page.Items[0].DeadlineAt.Before(page.Items[1].DeadlineAt) {
		t.Fatalf("first page %+v %v", page, err)
	}
	rest, err := s.ListRequests(ctx, RequestFilter{Limit: 2, Cursor: page.NextCursor})
	if err != nil || len(rest.Items) != 1 || rest.NextCursor != "" || !rest.Items[0].DeadlineAt.After(page.Items[1].DeadlineAt) {
		t.Fatalf("second page %+v %v", rest, err)
	}
	erasures, err := s.ListRequests(ctx, RequestFilter{Type: string(domain.RequestErasure), State: string(domain.PrivacyReceived)})
	if err != nil || len(erasures.Items) != 1 {
		t.Fatalf("filter %+v %v", erasures, err)
	}
	if _, err := s.ListRequests(ctx, RequestFilter{Cursor: "not-a-cursor"}); code(err) != "COM-0002" {
		t.Fatalf("bad cursor: %v", err)
	}
	other, err := s.ListRequests(as(ids.New(), ids.New(), "compliance.privacy-request.process"), RequestFilter{})
	if err != nil || len(other.Items) != 0 {
		t.Fatalf("another tenant sees requests: %+v %v", other, err)
	}
}
