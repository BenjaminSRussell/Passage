package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
	cevents "github.com/BenjaminSRussell/passage/services/compliance/internal/events"
	"github.com/jackc/pgx/v5"
)

func asEntity(tenantID, legalEntityID, userID string, duties ...string) context.Context {
	p := tenant.Principal{TenantID: tenantID, LegalEntityID: legalEntityID, UserID: userID, Duties: map[string]struct{}{}}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	return tenant.With(context.Background(), p)
}

func holdInput(le, subject, class, record string) HoldInput {
	return HoldInput{HoldScope: domain.HoldScope{LegalEntityID: le, SubjectRef: subject, DataClass: class, RecordRef: record}, Reason: "litigation hold, matter 2026-114"}
}

func TestLegalHold_PlaceReleaseLifecycleWithOutbox(t *testing.T) {
	s, admin := migrated(t)
	tenantID, le := ids.New(), ids.New()
	ctx := asEntity(tenantID, le, ids.New())

	h, err := s.PlaceHold(ctx, holdInput(le, " party:42 ", "", ""), "k-1", t0)
	if err != nil || h.State != "ACTIVE" || h.Version != 1 || h.SubjectRef != "party:42" || h.LegalEntityID != le {
		t.Fatalf("place %+v %v", h, err)
	}
	replay, err := s.PlaceHold(ctx, holdInput(le, " party:42 ", "", ""), "k-1", t0.Add(time.Minute))
	if err != nil || replay.HoldID != h.HoldID {
		t.Fatalf("replay must return the first hold: %+v %v", replay, err)
	}
	if _, err := s.PlaceHold(ctx, holdInput(le, "party:43", "", ""), "k-1", t0); code(err) != CodeIdempotencyReuse {
		t.Fatalf("key reuse with another body: %v", err)
	}
	if _, err := s.PlaceHold(ctx, holdInput(le, "", "", ""), "k-2", t0); code(err) != domain.CodeHoldInvalid {
		t.Fatalf("a hold without scope: %v", err)
	}
	if _, err := s.PlaceHold(ctx, HoldInput{HoldScope: domain.HoldScope{LegalEntityID: le, SubjectRef: "party:1"}, Reason: "short"}, "k-3", t0); code(err) != domain.CodeHoldInvalid {
		t.Fatalf("a short reason: %v", err)
	}
	if got := outboxTypes(t, admin, h.HoldID); len(got) != 1 || got[0] != cevents.HoldPlaced {
		t.Fatalf("outbox after place: %v", got)
	}

	if _, err := s.ReleaseHold(ctx, h.HoldID, 9, "matter settled, counsel confirmed", t0); code(err) != CodeHoldVersion {
		t.Fatalf("stale version: %v", err)
	}
	if _, err := s.ReleaseHold(ctx, h.HoldID, 1, "done", t0); code(err) != domain.CodeHoldInvalid {
		t.Fatalf("short release reason: %v", err)
	}
	rel, err := s.ReleaseHold(ctx, h.HoldID, 1, "matter settled, counsel confirmed", t0.Add(time.Hour))
	if err != nil || rel.State != "RELEASED" || rel.Version != 2 || rel.ReleasedAt == nil {
		t.Fatalf("release %+v %v", rel, err)
	}
	if _, err := s.ReleaseHold(ctx, h.HoldID, 2, "matter settled, counsel confirmed", t0); code(err) != domain.CodeHoldState {
		t.Fatalf("second release: %v", err)
	}
	if got := outboxTypes(t, admin, h.HoldID); len(got) != 2 || got[1] != cevents.HoldReleased {
		t.Fatalf("outbox after release: %v", got)
	}
	active, err := s.ListHolds(ctx, HoldFilter{State: "ACTIVE"})
	if err != nil || len(active.Items) != 0 {
		t.Fatalf("active list: %+v %v", active, err)
	}
	all, err := s.ListHolds(ctx, HoldFilter{LegalEntityID: le})
	if err != nil || len(all.Items) != 1 || all.Items[0].State != "RELEASED" {
		t.Fatalf("list: %+v %v", all, err)
	}
	if _, err := s.GetHold(ctx, ids.New()); code(err) != CodeHoldNotFound {
		t.Fatalf("unknown hold: %v", err)
	}
}

func TestCheckHolds_CoverageRulesAndLegalEntityIsolation(t *testing.T) {
	s, _ := migrated(t)
	tenantID, le, other := ids.New(), ids.New(), ids.New()
	ctx := asEntity(tenantID, le, ids.New())
	for i, in := range []HoldInput{holdInput(le, "party:42", "", ""), holdInput(le, "", "FINANCIAL_RECORD", ""), holdInput(le, "", "", "payee-7")} {
		if _, err := s.PlaceHold(ctx, in, "k"+string(rune('a'+i)), t0); err != nil {
			t.Fatal(err)
		}
	}
	for name, tc := range map[string]struct {
		target domain.HoldScope
		held   bool
	}{
		"subject hold covers its subject":          {domain.HoldScope{LegalEntityID: le, SubjectRef: "party:42", DataClass: "AP_PAYEE"}, true},
		"subject hold does not cover another":      {domain.HoldScope{LegalEntityID: le, SubjectRef: "party:43", DataClass: "AP_PAYEE"}, false},
		"subject hold does not cover no subject":   {domain.HoldScope{LegalEntityID: le, DataClass: "AP_PAYEE", RecordRef: "payee-8"}, false},
		"class hold covers any subject of class":   {domain.HoldScope{LegalEntityID: le, SubjectRef: "party:43", DataClass: "FINANCIAL_RECORD"}, true},
		"class hold covers a target naming none":   {domain.HoldScope{LegalEntityID: le, SubjectRef: "party:43"}, true},
		"record hold covers its record":            {domain.HoldScope{LegalEntityID: le, DataClass: "AP_PAYEE", RecordRef: "payee-7"}, true},
		"record hold does not cover a subject":     {domain.HoldScope{LegalEntityID: le, SubjectRef: "party:99", DataClass: "AP_PAYEE"}, false},
		"holds do not cross legal entities":        {domain.HoldScope{LegalEntityID: other, SubjectRef: "party:42"}, false},
		"class hold of another class stays silent": {domain.HoldScope{LegalEntityID: le, SubjectRef: "party:43", DataClass: "CANDIDATE"}, false},
	} {
		got, err := s.CheckHolds(ctx, tc.target)
		if err != nil || got.Held != tc.held || (got.Held && len(got.HoldIDs) == 0) {
			t.Errorf("%s: %+v %v", name, got, err)
		}
	}
	hidden, err := s.CheckHolds(asEntity(tenantID, other, ids.New()), domain.HoldScope{LegalEntityID: le, SubjectRef: "party:42"})
	if err != nil || hidden.Held {
		t.Fatalf("RLS must hide another legal entity's holds from a scoped caller: %+v %v", hidden, err)
	}
	if _, err := s.GetHold(asEntity(tenantID, other, ids.New()), mustFirstHold(t, s, ctx).HoldID); code(err) != CodeHoldNotFound {
		t.Fatalf("a hold of another legal entity must read as not found: %v", err)
	}
	foreign, err := s.CheckHolds(asEntity(ids.New(), le, ids.New()), domain.HoldScope{LegalEntityID: le, SubjectRef: "party:42"})
	if err != nil || foreign.Held {
		t.Fatalf("another tenant must not see holds: %+v %v", foreign, err)
	}
}

func TestHolds_UUIDRefsMatchRegardlessOfCase(t *testing.T) {
	s, _ := migrated(t)
	le := ids.New()
	ctx := asEntity(ids.New(), le, ids.New())
	record, subject := ids.New(), ids.New()
	upper := func(s string) string { return strings.ToUpper(s) }
	h, err := s.PlaceHold(ctx, holdInput(le, " "+upper(subject)+" ", "", upper(record)), "k-case", t0)
	if err != nil || h.RecordRef != record || h.SubjectRef != subject {
		t.Fatalf("place must store the canonical form: %+v %v", h, err)
	}
	got, err := s.CheckHolds(ctx, domain.HoldScope{LegalEntityID: le, SubjectRef: subject, DataClass: "AP_PAYEE", RecordRef: record})
	if err != nil || !got.Held {
		t.Fatalf("a lowercase check must see the upper-case hold: %+v %v", got, err)
	}
	got, err = s.CheckHolds(ctx, domain.HoldScope{LegalEntityID: le, SubjectRef: upper(subject), RecordRef: upper(record)})
	if err != nil || !got.Held {
		t.Fatalf("an upper-case check must also match: %+v %v", got, err)
	}
	page, err := s.ListHolds(ctx, HoldFilter{SubjectRef: upper(subject)})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("list by upper-case subject: %+v %v", page, err)
	}
}

func mustFirstHold(t *testing.T, s *Postgres, ctx context.Context) Hold {
	t.Helper()
	page, err := s.ListHolds(ctx, HoldFilter{})
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("no holds: %v", err)
	}
	return page.Items[0]
}

func TestRequestErasure_RefusedUnderHoldThenEmittedOnceAfterRelease(t *testing.T) {
	s, admin := migrated(t)
	tenantID, le := ids.New(), ids.New()
	ctx := asEntity(tenantID, le, ids.New())
	hold, err := s.PlaceHold(ctx, holdInput(le, "party:42", "AP_PAYEE", ""), "k-hold", t0)
	if err != nil {
		t.Fatal(err)
	}
	erase := ErasureInput{LegalEntityID: le, SubjectRef: "party:42"}

	_, err = s.RequestErasure(ctx, erase, "e-1", t0)
	if code(err) != domain.CodePrivacyLegalHold {
		t.Fatalf("erasure under hold must be refused: %v", err)
	}
	var commands int
	if err := admin.QueryRow(context.Background(), `select count(*) from erasure_command`).Scan(&commands); err != nil || commands != 0 {
		t.Fatalf("a refused erasure stores nothing: %d %v", commands, err)
	}
	other, err := s.RequestErasure(ctx, ErasureInput{LegalEntityID: le, SubjectRef: "party:42", DataClasses: []string{"CANDIDATE"}}, "e-2", t0)
	if err != nil {
		t.Fatalf("a hold on another class must not block this class: %v", err)
	}
	if got := outboxTypes(t, admin, other.ErasureID); len(got) != 1 || got[0] != cevents.ErasureRequested {
		t.Fatalf("outbox: %v", got)
	}

	if _, err := s.ReleaseHold(ctx, hold.HoldID, 1, "matter settled, counsel confirmed", t0); err != nil {
		t.Fatal(err)
	}
	cmd, err := s.RequestErasure(ctx, erase, "e-1", t0.Add(time.Hour))
	if err != nil || cmd.ErasureID == "" || cmd.SubjectRef != "party:42" || cmd.LegalEntityID != le {
		t.Fatalf("erasure after release: %+v %v", cmd, err)
	}
	again, err := s.RequestErasure(ctx, erase, "e-1", t0.Add(2*time.Hour))
	if err != nil || again.ErasureID != cmd.ErasureID {
		t.Fatalf("replay: %+v %v", again, err)
	}
	if got := outboxTypes(t, admin, cmd.ErasureID); len(got) != 1 {
		t.Fatalf("a replay must not emit again: %v", got)
	}
	if _, err := s.RequestErasure(ctx, ErasureInput{LegalEntityID: le, SubjectRef: "party:43"}, "e-1", t0); code(err) != CodeIdempotencyReuse {
		t.Fatalf("key reuse with another body: %v", err)
	}
}

func TestRequestErasure_LinkedRequestMustBeAnApprovedErasureOfTheSameSubject(t *testing.T) {
	s, _ := migrated(t)
	tenantID, le := ids.New(), ids.New()
	ctx := asEntity(tenantID, le, ids.New(), "PrivacyIntake", "PrivacyOfficer")
	req, err := s.Intake(ctx, IntakeInput{Type: domain.RequestErasure, Regulation: domain.RegulationGDPR, SubjectRef: "party:42"}, "k-intake", t0)
	if err != nil {
		t.Fatal(err)
	}
	link := ErasureInput{LegalEntityID: le, SubjectRef: "party:42", RequestID: req.RequestID}
	if _, err := s.RequestErasure(ctx, link, "e-1", t0); code(err) != domain.CodePrivacyTransition {
		t.Fatalf("an unapproved request: %v", err)
	}
	version := 1
	for _, a := range []RequestActionInput{
		{Action: domain.ActionVerify, MatchedAttributes: []string{"email", "postal_code"}, EmailLinkVerified: true},
		{Action: domain.ActionDiscover},
		{Action: domain.ActionApprove},
	} {
		r, err := s.ActOnRequest(ctx, req.RequestID, version, a, t0)
		if err != nil {
			t.Fatalf("%s: %v", a.Action, err)
		}
		version = r.Version
	}
	if _, err := s.RequestErasure(ctx, ErasureInput{LegalEntityID: le, SubjectRef: "party:43", RequestID: req.RequestID}, "e-2", t0); code(err) != domain.CodePrivacyTransition {
		t.Fatalf("another subject: %v", err)
	}
	cmd, err := s.RequestErasure(ctx, link, "e-3", t0)
	if err != nil || cmd.RequestID != req.RequestID {
		t.Fatalf("approved request: %+v %v", cmd, err)
	}
}

func TestLegalHold_AppRoleCannotDeleteHoldsOrCommands(t *testing.T) {
	if os.Getenv(testkit.EnvAppURL) == "" {
		t.Skip("needs PASSAGE_TEST_APP_DATABASE_URL")
	}
	s, _ := migrated(t)
	tenantID, le := ids.New(), ids.New()
	ctx := asEntity(tenantID, le, ids.New())
	if _, err := s.PlaceHold(ctx, holdInput(le, "party:42", "", ""), "k", t0); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"legal_hold", "erasure_command"} {
		err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `delete from `+table)
			return err
		})
		if err == nil {
			t.Errorf("passage_app deleted from %s", table)
		}
	}
}

func TestLockLegalEntity_CaseInsensitiveAndPlaceHoldStoresCanonicalId(t *testing.T) {
	s, _ := migrated(t)
	tenantID, le := ids.New(), ids.New()
	upper := strings.ToUpper(le)
	ctx := asEntity(tenantID, le, ids.New())

	held, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback(ctx)
	if err := lockLegalEntity(ctx, held, tenantID, le); err != nil {
		t.Fatal(err)
	}
	waiter, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer waiter.Rollback(ctx)
	short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if err := lockLegalEntity(short, waiter, tenantID, upper); err == nil {
		t.Fatal("an upper-case id must take the same lock as the canonical id")
	}

	held.Rollback(ctx)
	waiter.Rollback(ctx)
	h, err := s.PlaceHold(ctx, holdInput(upper, "party:42", "", ""), "k-upper", t0)
	if err != nil || h.LegalEntityID != le {
		t.Fatalf("hold %+v %v", h, err)
	}
	got, err := s.CheckHolds(ctx, domain.HoldScope{LegalEntityID: le, SubjectRef: "party:42"})
	if err != nil || len(got.HoldIDs) != 1 {
		t.Fatalf("check %+v %v", got, err)
	}
}
