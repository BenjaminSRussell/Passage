package store

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
	cevents "github.com/BenjaminSRussell/passage/services/compliance/internal/events"
)

var sdn = []domain.ListEntry{
	{ListCode: "OFAC_SDN", EntryID: "SDN-7001", Version: 2, Name: "Volga Maritime Holdings", Country: "RU"},
	{ListCode: "UN_SC", EntryID: "UN-0042", Version: 1, Name: "Northern Star Shipping"},
}

func hitEvents(t *testing.T, admin *pgxpool.Pool, hits []ScreeningHit) int {
	t.Helper()
	n := 0
	for _, h := range hits {
		for _, typ := range outboxTypes(t, admin, h.HitID) {
			if typ == cevents.ScreeningHit {
				n++
			}
		}
	}
	return n
}

// AC-48.16 at the store: a customer named like an SDN entry is blocked with an
// OPEN hit; after clearance as false positive it screens clean and the hit does
// not recur; a change of name re-opens it (BR-48.15).
func TestScreen_HitClearanceAndRecurrence(t *testing.T) {
	s, admin := migrated(t)
	tenantID := ids.New()
	ctx := as(tenantID, ids.New(), "TradeComplianceOfficer")
	party := domain.Subject{Type: domain.SubjectParty, ID: ids.New(), Name: "Volga Maritim Holdings", Country: "RU"}

	first, err := s.Screen(ctx, party, sdn, domain.DefaultScreeningThreshold, t0)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Blocked || len(first.Hits) != 1 {
		t.Fatalf("want one blocking hit, got %+v", first)
	}
	hit := first.Hits[0]
	if hit.State != domain.HitOpen || hit.ListEntryID != "SDN-7001" || hit.ListEntryName != "Volga Maritime Holdings" ||
		hit.Score < 0.88 || hit.Score > 1 || hit.Version != 1 || hit.SubjectID != party.ID {
		t.Fatalf("stored hit %+v", hit)
	}
	if got := outboxTypes(t, admin, hit.HitID); len(got) != 1 || got[0] != cevents.ScreeningHit {
		t.Fatalf("outbox for the hit: %v", got)
	}

	// Screening again neither duplicates the hit nor emits again.
	again, err := s.Screen(ctx, party, sdn, domain.DefaultScreeningThreshold, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Hits) != 1 || again.Hits[0].HitID != hit.HitID || again.Hits[0].Version != 1 || hitEvents(t, admin, again.Hits) != 1 {
		t.Fatalf("rescreen changed the hit: %+v", again)
	}

	decided, err := s.DecideHit(ctx, hit.HitID, 1, DecisionInput{Decision: domain.DecisionClearFalsePositive, Reason: "different registry number"}, "k-clear", t0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if decided.State != domain.HitFalsePositive || decided.Version != 2 || decided.Reason != "different registry number" || decided.DecidedAt == nil {
		t.Fatalf("decided %+v", decided)
	}

	clean, err := s.Screen(ctx, party, sdn, domain.DefaultScreeningThreshold, t0.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if clean.Blocked || len(clean.Hits) != 0 {
		t.Fatalf("cleared party must screen clean: %+v", clean)
	}
	page, err := s.ListHits(ctx, HitFilter{SubjectID: party.ID})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("the cleared hit must not recur: %v %+v", err, page)
	}

	// A new list entry version re-opens the question.
	newer := append([]domain.ListEntry(nil), sdn...)
	newer[0].Version = 3
	reopened, err := s.Screen(ctx, party, newer, domain.DefaultScreeningThreshold, t0.Add(4*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.Blocked || len(reopened.Hits) != 1 || reopened.Hits[0].HitID == hit.HitID || reopened.Hits[0].ListEntryVersion != 3 {
		t.Fatalf("a changed list entry must flag again: %+v", reopened)
	}
	if hitEvents(t, admin, reopened.Hits) != 1 {
		t.Fatal("the new hit must be emitted")
	}
}

func TestScreen_RenamedPartyIsFlaggedAgain(t *testing.T) {
	s, _ := migrated(t)
	ctx := as(ids.New(), ids.New())
	party := domain.Subject{Type: domain.SubjectParty, ID: ids.New(), Name: "Northern Star Shipping"}
	first, err := s.Screen(ctx, party, sdn, 0.9, t0)
	if err != nil || len(first.Hits) != 1 {
		t.Fatalf("%v %+v", err, first)
	}
	if _, err := s.DecideHit(ctx, first.Hits[0].HitID, 1, DecisionInput{Decision: domain.DecisionClearFalsePositive, Reason: "local ferry operator"}, "k1", t0); err != nil {
		t.Fatal(err)
	}
	party.Name = "Northern Star Shipping Co"
	renamed, err := s.Screen(ctx, party, sdn, 0.9, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !renamed.Blocked || len(renamed.Hits) != 1 || renamed.Hits[0].SubjectName != "Northern Star Shipping Co" {
		t.Fatalf("a renamed party must be screened again: %+v", renamed)
	}
}

func TestDecideHit_ConfirmVersionAndIdempotency(t *testing.T) {
	s, _ := migrated(t)
	tenantID := ids.New()
	officer := ids.New()
	ctx := as(tenantID, officer)
	doc := domain.Subject{Type: domain.SubjectDocument, ID: ids.New(), Name: "Volga Maritime Holdings"}
	res, err := s.Screen(ctx, doc, sdn, domain.DefaultScreeningThreshold, t0)
	if err != nil || len(res.Hits) != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	id := res.Hits[0].HitID
	confirm := DecisionInput{Decision: domain.DecisionConfirm, Reason: "same vessel IMO number"}

	if _, err := s.DecideHit(ctx, id, 5, confirm, "k-stale", t0); code(err) != CodeHitVersion {
		t.Fatalf("stale version: %v", err)
	}
	if _, err := s.DecideHit(ctx, ids.New(), 1, confirm, "k-missing", t0); code(err) != CodeHitNotFound {
		t.Fatalf("unknown hit: %v", err)
	}
	got, err := s.DecideHit(ctx, id, 1, confirm, "k-confirm", t0)
	if err != nil || got.State != domain.HitConfirmed || got.DecidedBy != officer {
		t.Fatalf("confirm: %v %+v", err, got)
	}
	// The same key and request replays the first answer instead of failing on the decided hit.
	replay, err := s.DecideHit(ctx, id, 1, confirm, "k-confirm", t0)
	if err != nil || replay.State != domain.HitConfirmed || replay.Version != got.Version {
		t.Fatalf("replay: %v %+v", err, replay)
	}
	if _, err := s.DecideHit(ctx, id, 1, DecisionInput{Decision: domain.DecisionClearFalsePositive, Reason: "x"}, "k-confirm", t0); code(err) != CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	if _, err := s.DecideHit(ctx, id, 2, confirm, "k-second", t0); code(err) != domain.CodeHitState {
		t.Fatalf("second decision: %v", err)
	}
	// A confirmed hit keeps the document blocked and is not duplicated by a rescreen.
	again, err := s.Screen(ctx, doc, sdn, domain.DefaultScreeningThreshold, t0.Add(time.Hour))
	if err != nil || !again.Blocked || len(again.Hits) != 1 || again.Hits[0].HitID != id || again.Hits[0].State != domain.HitConfirmed {
		t.Fatalf("confirmed rescreen: %v %+v", err, again)
	}
}

func TestListHits_FiltersPagesAndIsolatesTenants(t *testing.T) {
	s, _ := migrated(t)
	tenantID := ids.New()
	ctx := as(tenantID, ids.New())
	for i := 0; i < 3; i++ {
		p := domain.Subject{Type: domain.SubjectParty, ID: ids.New(), Name: "Volga Maritime Holdings"}
		if _, err := s.Screen(ctx, p, sdn, domain.DefaultScreeningThreshold, t0.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ListHits(ctx, HitFilter{State: "OPEN", ListCode: "OFAC_SDN", SubjectType: "PARTY", Limit: 2})
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" || !first.Items[0].DetectedAt.After(first.Items[1].DetectedAt) {
		t.Fatalf("first page: %v %+v", err, first)
	}
	second, err := s.ListHits(ctx, HitFilter{State: "OPEN", Limit: 2, Cursor: first.NextCursor})
	if err != nil || len(second.Items) != 1 || second.NextCursor != "" {
		t.Fatalf("second page: %v %+v", err, second)
	}
	if none, _ := s.ListHits(ctx, HitFilter{State: "CONFIRMED"}); len(none.Items) != 0 {
		t.Fatalf("state filter: %+v", none)
	}
	if _, err := s.ListHits(ctx, HitFilter{Cursor: "bad"}); code(err) != "COM-0002" {
		t.Fatalf("bad cursor: %v", err)
	}
	other := as(ids.New(), ids.New())
	if page, _ := s.ListHits(other, HitFilter{}); len(page.Items) != 0 {
		t.Fatalf("another tenant sees %d hits", len(page.Items))
	}
	if _, err := s.GetHit(other, first.Items[0].HitID); code(err) != CodeHitNotFound {
		t.Fatalf("cross-tenant get: %v", err)
	}
	if got, err := s.GetHit(ctx, first.Items[0].HitID); err != nil || got.HitID != first.Items[0].HitID {
		t.Fatalf("get: %v %+v", err, got)
	}
}
