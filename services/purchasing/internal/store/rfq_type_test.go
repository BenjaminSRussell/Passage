package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

func newRFQType(code string) domain.RFQType {
	return domain.RFQType{LegalEntityID: legalEntity, Code: code, Name: code + " bid", Sealed: true, AmendmentsAllowed: true,
		RankingRule: domain.RankWeightedScore, Status: domain.RFQTypeActive,
		Criteria: []domain.ScoringCriterion{{Name: "Quality", Weight: dec("2")}, {Name: "Delivery", Weight: dec("1.5")}}}
}

// FR-17.19: an RFQ type keeps its default criteria, replays an Idempotency-Key, refuses a duplicate code,
// changes under If-Match (criteria replaced as a set) and writes one event per save.
func TestRFQType_CreateReplayDuplicateAndUpdate(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(80)
	made, err := s.CreateRFQType(ctx, newRFQType("SEALED"), store.Idem{Key: "t-1", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if made.Version != 1 || !made.Sealed || len(made.Criteria) != 2 || made.Criteria[1].Seq != 2 || !made.Criteria[1].Weight.Equal(dec("1.5")) {
		t.Fatalf("created %+v", made)
	}
	again, err := s.CreateRFQType(ctx, newRFQType("SEALED"), store.Idem{Key: "t-1", Hash: "h"})
	if err != nil || again.ID != made.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if _, err := s.CreateRFQType(ctx, newRFQType("SEALED"), store.Idem{Key: "t-2", Hash: "h2"}); problemCode(err) != store.CodeRFQTypeExists {
		t.Fatalf("duplicate code = %v", err)
	}
	next := made
	next.Name, next.Sealed, next.AmendmentsAllowed, next.RankingRule = "Open bid", false, false, domain.RankLowestPrice
	next.Criteria = []domain.ScoringCriterion{{Name: "Price", Weight: dec("1")}}
	if _, err := s.UpdateRFQType(ctx, made.ID, 9, next); problemCode(err) != store.CodeRFQTypeStale {
		t.Fatalf("stale version = %v", err)
	}
	upd, err := s.UpdateRFQType(ctx, made.ID, 1, next)
	if err != nil || upd.Version != 2 || upd.Sealed || upd.AmendmentsAllowed || len(upd.Criteria) != 1 || upd.Criteria[0].Name != "Price" || upd.Code != "SEALED" {
		t.Fatalf("updated %+v, %v", upd, err)
	}
	list, err := s.ListRFQTypes(ctx, store.RFQTypeFilter{LegalEntityID: legalEntity, Status: domain.RFQTypeActive}, "", 0)
	if err != nil || len(list.Items) != 1 || len(list.Items[0].Criteria) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if _, err := s.GetRFQType(ctx, ids.New()); problemCode(err) != store.CodeRFQTypeNotFound {
		t.Fatalf("unknown type = %v", err)
	}
	if n := countEvents(t, ctx, pool, "dev.passage.procurement.rfqtype.created.v1") + countEvents(t, ctx, pool, "dev.passage.procurement.rfqtype.changed.v1"); n != 2 {
		t.Fatalf("rfqtype events = %d, want 2 (created, changed)", n)
	}
	// Another tenant sees none of it.
	if other, err := s.ListRFQTypes(principalContext(81), store.RFQTypeFilter{}, "", 0); err != nil || len(other.Items) != 0 {
		t.Fatalf("other tenant sees %+v, %v", other, err)
	}
}

// FR-17.19: a DRAFT RFQ's deadline, ranking rule, sealed flag and criteria are edited under If-Match; a sent
// RFQ refuses it, and an RFQ whose type requires sealed bids keeps the flag.
func TestRFQ_EditDraft(t *testing.T) {
	s := store.New(migrated(t))
	ctx := principalContext(82)
	now := rfqDeadline.Add(-48 * time.Hour)
	typ, err := s.CreateRFQType(ctx, newRFQType("SEALED"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	in := draftRFQ(t)
	in.RFQTypeID, in.AmendmentsAllowed, in.SealedRequired = typ.ID, true, true
	in.Sealed, in.RankingRule, in.Criteria = true, domain.RankWeightedScore, typ.Criteria
	r, err := s.CreateRFQ(ctx, in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if r.RFQTypeID != typ.ID || !r.SealedRequired || !r.AmendmentsAllowed {
		t.Fatalf("created %+v", r)
	}
	edit := domain.RFQEdit{ReplyDeadline: rfqDeadline.Add(24 * time.Hour), RankingRule: domain.RankWeightedScore, Sealed: true,
		Criteria: []domain.ScoringCriterion{{Name: "Price", Weight: dec("3")}}}
	if _, err := s.EditRFQ(ctx, r.ID, 9, edit, now); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version = %v", err)
	}
	unsealed := edit
	unsealed.Sealed = false
	if _, err := s.EditRFQ(ctx, r.ID, r.Version, unsealed, now); problemCode(err) != domain.CodeRFQEdit {
		t.Fatalf("clearing a required seal = %v", err)
	}
	if _, err := s.EditRFQ(ctx, r.ID, r.Version, domain.RFQEdit{ReplyDeadline: now.Add(-time.Hour), RankingRule: domain.RankLowestPrice}, now); err == nil {
		t.Fatal("a deadline in the past was accepted")
	}
	got, err := s.EditRFQ(ctx, r.ID, r.Version, edit, now)
	if err != nil || got.Version != r.Version+1 || !got.ReplyDeadline.Equal(edit.ReplyDeadline) || len(got.Criteria) != 1 || got.Criteria[0].Name != "Price" {
		t.Fatalf("edited %+v, %v", got, err)
	}
	sent, err := s.SendRFQ(ctx, r.ID, got.Version, []string{ids.New()}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditRFQ(ctx, r.ID, sent.Version, edit, now); problemCode(err) != domain.CodeRFQState {
		t.Fatalf("editing a sent RFQ = %v", err)
	}
}

// FR-17.19: an amendment numbers itself, moves the deadline, replaces the criteria while nobody is scored,
// owes an acknowledgement from every invited vendor, un-flags replies that were late only for the old
// deadline and writes rfq.amended.v1.
func TestRFQ_AmendAndAcknowledge(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(83)
	now := rfqDeadline.Add(-time.Hour)
	in := draftRFQ(t)
	in.AmendmentsAllowed = true
	in.RankingRule, in.Criteria = domain.RankWeightedScore, []domain.ScoringCriterion{{Name: "Quality", Weight: dec("2")}}
	r, err := s.CreateRFQ(ctx, in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	vendorA, vendorB := ids.New(), ids.New()
	if _, err := s.AmendRFQ(ctx, r.ID, r.Version, domain.AmendmentInput{Summary: "too early"}, now, store.Idem{}); problemCode(err) != domain.CodeRFQState {
		t.Fatalf("amending a draft = %v", err)
	}
	r, err = s.SendRFQ(ctx, r.ID, r.Version, []string{vendorA, vendorB}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	// Vendor B replied one hour late; moving the deadline makes the reply on time.
	if r, err = s.RecordRFQReplies(ctx, r.ID, vendorA, []domain.RFQReply{replyTo(r.Lines[0].ID, "10", 5, rfqDeadline.Add(-time.Hour))}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if r, err = s.RecordRFQReplies(ctx, r.ID, vendorB, []domain.RFQReply{replyTo(r.Lines[0].ID, "9", 5, rfqDeadline.Add(time.Hour))}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	for _, rp := range r.Replies {
		if rp.VendorID == vendorB && !rp.Late {
			t.Fatal("vendor B's reply should be late")
		}
	}
	later := rfqDeadline.Add(48 * time.Hour)
	// The replies of this unsealed RFQ are open to the buyer, so its criteria can no longer be re-weighted.
	reweigh := domain.AmendmentInput{Summary: "Re-weigh", ReplyDeadline: &later, ReplaceCriteria: true,
		Criteria: []domain.ScoringCriterion{{Name: "Price", Weight: dec("1")}, {Name: "Delivery", Weight: dec("1")}}}
	if _, err := s.AmendRFQ(ctx, r.ID, r.Version, reweigh, rfqDeadline.Add(2*time.Hour), store.Idem{}); problemCode(err) != domain.CodeRFQAmend {
		t.Fatalf("replacing the criteria of an RFQ with readable replies = %v", err)
	}
	amend := domain.AmendmentInput{Summary: "Deadline moves two days", ReplyDeadline: &later}
	if _, err := s.AmendRFQ(ctx, r.ID, r.Version-1, amend, rfqDeadline.Add(2*time.Hour), store.Idem{}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version = %v", err)
	}
	// Past the old deadline the amendment must give a later one; the new one must be after now.
	noMove := domain.AmendmentInput{Summary: "no new date"}
	if _, err := s.AmendRFQ(ctx, r.ID, r.Version, noMove, rfqDeadline.Add(2*time.Hour), store.Idem{}); problemCode(err) != domain.CodeRFQAmend {
		t.Fatalf("amendment past the deadline without a new one = %v", err)
	}
	got, err := s.AmendRFQ(ctx, r.ID, r.Version, amend, rfqDeadline.Add(2*time.Hour), store.Idem{Key: "am-1", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if got.AmendmentNo != 1 || len(got.Amendments) != 1 || got.Version != r.Version+1 || !got.ReplyDeadline.Equal(later) ||
		len(got.Criteria) != 1 || got.Criteria[0].Name != "Quality" {
		t.Fatalf("amended %+v", got)
	}
	am := got.Amendments[0]
	if am.Summary != "Deadline moves two days" || am.PreviousDeadline == nil || !am.PreviousDeadline.Equal(rfqDeadline) || am.CriteriaChanged || len(am.Acknowledgements) != 2 {
		t.Fatalf("amendment row %+v", am)
	}
	// The un-flagging is audited: only vendor B's reply was late, so only B is listed.
	if len(am.LateClearedVendorIDs) != 1 || am.LateClearedVendorIDs[0] != vendorB {
		t.Fatalf("late cleared vendors = %v, want [%s]", am.LateClearedVendorIDs, vendorB)
	}
	for _, a := range am.Acknowledgements {
		if a.AcknowledgedAt != nil {
			t.Fatalf("acknowledged before anyone did: %+v", a)
		}
	}
	for _, rp := range got.Replies {
		if rp.Late {
			t.Fatalf("reply of %s still late after the deadline moved", rp.VendorID)
		}
	}
	replay, err := s.AmendRFQ(ctx, r.ID, r.Version, amend, rfqDeadline.Add(2*time.Hour), store.Idem{Key: "am-1", Hash: "h"})
	if err != nil || replay.AmendmentNo != 1 {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	acked, err := s.AcknowledgeAmendment(ctx, r.ID, 1, vendorA, rfqDeadline.Add(3*time.Hour))
	if err != nil || acked.Version != got.Version {
		t.Fatalf("acknowledge = %+v, %v", acked, err)
	}
	var when *time.Time
	for _, a := range acked.Amendments[0].Acknowledgements {
		if a.VendorID == vendorA {
			when = a.AcknowledgedAt
		} else if a.AcknowledgedAt != nil {
			t.Fatalf("vendor B acknowledged: %+v", a)
		}
	}
	if when == nil {
		t.Fatal("vendor A's acknowledgement was not recorded")
	}
	again, err := s.AcknowledgeAmendment(ctx, r.ID, 1, vendorA, rfqDeadline.Add(9*time.Hour))
	if err != nil || !again.Amendments[0].Acknowledgements[0].AcknowledgedAt.Equal(*when) && !again.Amendments[0].Acknowledgements[1].AcknowledgedAt.Equal(*when) {
		t.Fatalf("a second acknowledgement moved the time: %+v, %v", again.Amendments[0].Acknowledgements, err)
	}
	if _, err := s.AcknowledgeAmendment(ctx, r.ID, 2, vendorA, now); problemCode(err) != store.CodeAmendmentNotFound {
		t.Fatalf("unknown amendment = %v", err)
	}
	if _, err := s.AcknowledgeAmendment(ctx, r.ID, 1, ids.New(), now); problemCode(err) != store.CodeAmendmentNotFound {
		t.Fatalf("uninvited vendor = %v", err)
	}
	if n := countEvents(t, ctx, pool, "dev.passage.procurement.rfq.amended.v1"); n != 1 {
		t.Fatalf("rfq.amended events = %d, want 1 (the replay writes none)", n)
	}
	for _, ev := range outbox(t, ctx, pool) {
		if ev.Type != "dev.passage.procurement.rfq.amended.v1" {
			continue
		}
		if cleared, _ := ev.Data["late_cleared_vendor_ids"].([]any); len(cleared) != 1 || cleared[0] != vendorB {
			t.Fatalf("rfq.amended late_cleared_vendor_ids = %v, want [%s]", ev.Data["late_cleared_vendor_ids"], vendorB)
		}
	}

	// Scores recorded: the criteria cannot be replaced any more, a plain amendment still works.
	scored, err := s.SetRFQScores(ctx, r.ID, got.Version, vendorA, []domain.VendorScore{{CriterionID: got.Criteria[0].ID, Score: 7}}, now)
	if err != nil {
		t.Fatal(err)
	}
	evenLater := later.Add(24 * time.Hour)
	if _, err := s.AmendRFQ(ctx, r.ID, scored.Version, domain.AmendmentInput{Summary: "scores in", ReplaceCriteria: true,
		Criteria: []domain.ScoringCriterion{{Name: "X", Weight: dec("1")}}}, now, store.Idem{}); problemCode(err) != domain.CodeRFQAmend {
		t.Fatalf("replacing scored criteria = %v", err)
	}
	if out, err := s.AmendRFQ(ctx, r.ID, scored.Version, domain.AmendmentInput{Summary: "one more day", ReplyDeadline: &evenLater}, now, store.Idem{}); err != nil || out.AmendmentNo != 2 {
		t.Fatalf("second amendment = %+v, %v", out, err)
	}

	// A type without amendments refuses them; a sealed RFQ past its deadline too.
	closed := draftRFQ(t)
	closed.AmendmentsAllowed = false
	c, err := s.CreateRFQ(ctx, closed, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if c, err = s.SendRFQ(ctx, c.ID, c.Version, []string{vendorA}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AmendRFQ(ctx, c.ID, c.Version, domain.AmendmentInput{Summary: "x"}, now, store.Idem{}); problemCode(err) != domain.CodeRFQAmend {
		t.Fatalf("type without amendments = %v", err)
	}
}

// FR-17.19, BR-17.16: a sealed RFQ before its deadline may replace its criteria although a reply is in (the
// buyer cannot read it and the vendor may still revise it); the amendment lists no late vendor because a
// sealed RFQ refuses late replies.
func TestRFQ_AmendSealedBeforeDeadline(t *testing.T) {
	s := store.New(migrated(t))
	ctx := principalContext(84)
	in := draftRFQ(t)
	in.Sealed, in.AmendmentsAllowed = true, true
	in.RankingRule, in.Criteria = domain.RankWeightedScore, []domain.ScoringCriterion{{Name: "Quality", Weight: dec("2")}}
	r, err := s.CreateRFQ(ctx, in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	vendor := ids.New()
	if r, err = s.SendRFQ(ctx, r.ID, r.Version, []string{vendor}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if r, err = s.RecordRFQReplies(ctx, r.ID, vendor, []domain.RFQReply{replyTo(r.Lines[0].ID, "10", 5, rfqDeadline.Add(-2*time.Hour))}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	later := rfqDeadline.Add(24 * time.Hour)
	got, err := s.AmendRFQ(ctx, r.ID, r.Version, domain.AmendmentInput{Summary: "Delivery now weighs more", ReplyDeadline: &later,
		ReplaceCriteria: true, Criteria: []domain.ScoringCriterion{{Name: "Delivery", Weight: dec("3")}}}, rfqDeadline.Add(-time.Hour), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Criteria) != 1 || got.Criteria[0].Name != "Delivery" || !got.Amendments[0].CriteriaChanged || len(got.Amendments[0].LateClearedVendorIDs) != 0 {
		t.Fatalf("amended %+v", got)
	}
	// Past the (moved) deadline the sealed replies may be opened, so no amendment is accepted any more.
	if _, err := s.AmendRFQ(ctx, r.ID, got.Version, domain.AmendmentInput{Summary: "too late", ReplyDeadline: &later}, later.Add(time.Minute), store.Idem{}); problemCode(err) != domain.CodeRFQAmend {
		t.Fatalf("amending an opened sealed RFQ = %v", err)
	}
}

// FR-17.19: an RFQ type and the amendments of an RFQ stay in their legal entity, and the database refuses an
// RFQ that names the type of another legal entity even if the API check were skipped.
func TestRFQType_LegalEntityScope(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	entityA, entityB := ids.New(), ids.New()
	ctxA := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(85), LegalEntityID: entityA, UserID: ids.New()})
	ctxB := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(85), LegalEntityID: entityB, UserID: ids.New()})
	typA := newRFQType("STD")
	typA.LegalEntityID = entityA
	madeA, err := s.CreateRFQType(ctxA, typA, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	typB := newRFQType("STD")
	typB.LegalEntityID = entityB
	madeB, err := s.CreateRFQType(ctxB, typB, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if list, err := s.ListRFQTypes(ctxB, store.RFQTypeFilter{}, "", 0); err != nil || len(list.Items) != 1 || list.Items[0].ID != madeB.ID {
		t.Fatalf("legal entity B lists %+v, %v", list.Items, err)
	}
	if _, err := s.GetRFQType(ctxB, madeA.ID); problemCode(err) != store.CodeRFQTypeNotFound {
		t.Fatalf("legal entity B reads A's type: %v", err)
	}

	in := draftRFQ(t)
	in.LegalEntityID, in.RFQTypeID, in.AmendmentsAllowed = entityA, madeB.ID, true
	var pe *pgconn.PgError
	if _, err := s.CreateRFQ(principalContext(85), in, store.Idem{}); !errors.As(err, &pe) || pe.ConstraintName != "purchase_rfq_type_fk" {
		t.Fatalf("an RFQ of legal entity A with the type of legal entity B: %v", err)
	}
	in.RFQTypeID = madeA.ID
	r, err := s.CreateRFQ(ctxA, in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	vendor := ids.New()
	if r, err = s.SendRFQ(ctxA, r.ID, r.Version, []string{vendor}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if r, err = s.AmendRFQ(ctxA, r.ID, r.Version, domain.AmendmentInput{Summary: "Drawing rev B"}, rfqDeadline.Add(-time.Hour), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcknowledgeAmendment(ctxB, r.ID, 1, vendor, rfqDeadline.Add(-time.Hour)); problemCode(err) != store.CodeRFQNotFound {
		t.Fatalf("legal entity B acknowledges A's amendment: %v", err)
	}
	if _, err := s.AmendRFQ(ctxB, r.ID, r.Version, domain.AmendmentInput{Summary: "x"}, rfqDeadline.Add(-time.Hour), store.Idem{}); problemCode(err) != store.CodeRFQNotFound {
		t.Fatalf("legal entity B amends A's RFQ: %v", err)
	}
}
