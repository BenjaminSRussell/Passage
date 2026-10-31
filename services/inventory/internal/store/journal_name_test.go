package store_test

import (
	"context"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

func newName(ctx context.Context, s *store.Postgres, key, code string, typ domain.JournalType, approval bool, offset string) (domain.JournalName, bool, error) {
	return s.CreateJournalName(ctx, key, domain.JournalName{LegalEntityID: legalEntity(ctx), Code: code, Type: typ, RequiresApproval: approval, OffsetAccountID: offset})
}

func TestJournalNameCreateReplayAndUniqueness(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	n, replayed, err := newName(ctx, s, "k1", "adj-a", domain.JournalAdjustment, true, "")
	if err != nil || replayed || n.Code != "ADJ-A" || !n.Active || n.Version != 1 || !n.RequiresApproval {
		t.Fatalf("create: %v replayed=%v %+v", err, replayed, n)
	}
	if again, replayed, err := newName(ctx, s, "k1", "adj-a", domain.JournalAdjustment, true, ""); err != nil || !replayed || again.ID != n.ID {
		t.Fatalf("replay: %v %v", err, replayed)
	}
	if _, _, err := newName(ctx, s, "k1", "adj-a", domain.JournalAdjustment, false, ""); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key, other body: %v", err)
	}
	if _, _, err := newName(ctx, s, "k2", "ADJ-A", domain.JournalAdjustment, false, ""); problemCode(err) != domain.CodeJournalNameDup {
		t.Fatalf("duplicate code: %v", err)
	}
	if _, _, err := newName(ctx, s, "k3", "MOVE", domain.JournalMovement, false, "bad account"); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("bad offset: %v", err)
	}
	var events int
	_ = admin.QueryRow(context.Background(), `select count(*) from outbox where tenant_id = $1 and type = $2`, testkit.TenantID(1), "dev.passage.inventory.journalname.created.v1").Scan(&events)
	if events != 1 {
		t.Fatalf("created events %d, want 1 (a replay writes none)", events)
	}
	got, err := s.GetJournalName(ctx, n.ID)
	if err != nil || got.ID != n.ID {
		t.Fatalf("get: %v", err)
	}
	list, err := s.ListJournalNames(ctx, store.JournalNameFilter{})
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %d", err, len(list))
	}
}

func TestJournalNameIsolatedByTenantAndLegalEntity(t *testing.T) {
	s, _, _ := setup(t)
	le1, le2 := ids.New(), ids.New()
	ctx1, ctx2 := entityCtx(1, le1), entityCtx(1, le2)
	n, _, err := newName(ctx1, s, "k1", "ADJ", domain.JournalAdjustment, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetJournalName(ctx2, n.ID); problemCode(err) != domain.CodeJournalNameNotFound {
		t.Fatalf("other legal entity: %v", err)
	}
	if _, err := s.GetJournalName(entityCtx(2, le1), n.ID); problemCode(err) != domain.CodeJournalNameNotFound {
		t.Fatalf("other tenant: %v", err)
	}
	if list, _ := s.ListJournalNames(ctx2, store.JournalNameFilter{}); len(list) != 0 {
		t.Fatalf("other legal entity lists %d names", len(list))
	}
}

func TestJournalNameUpdateGuardsVersionAndEmitsEvent(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	n, _, _ := newName(ctx, s, "k1", "ADJ", domain.JournalAdjustment, false, "")
	on, off := true, false
	u, err := s.UpdateJournalName(ctx, n.ID, 1, store.JournalNamePatch{RequiresApproval: &on})
	if err != nil || u.Version != 2 || !u.RequiresApproval {
		t.Fatalf("update: %v %+v", err, u)
	}
	if _, err := s.UpdateJournalName(ctx, n.ID, 1, store.JournalNamePatch{Active: &off}); problemCode(err) != store.CodeConflictVersion {
		t.Fatalf("stale version: %v", err)
	}
	desc := "x"
	offset := "5000"
	if _, err := s.UpdateJournalName(ctx, n.ID, 2, store.JournalNamePatch{OffsetAccountID: &offset, Description: &desc}); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("offset on an ADJUSTMENT name: %v", err)
	}
	var events int
	_ = admin.QueryRow(context.Background(), `select count(*) from outbox where tenant_id = $1 and type = $2`, testkit.TenantID(1), "dev.passage.inventory.journalname.updated.v1").Scan(&events)
	if events != 1 {
		t.Fatalf("updated events %d, want 1", events)
	}
}

func TestJournalUnderNameFollowsItsRules(t *testing.T) {
	s, _, _ := setup(t)
	le := ids.New()
	ctx := entityCtx(1, le)
	product := ids.New()
	adj, _, _ := newName(ctx, s, "n1", "ADJ", domain.JournalAdjustment, true, "")
	mov, _, _ := newName(ctx, s, "n2", "MOVE", domain.JournalMovement, false, "500100")

	mk := func(typ domain.JournalType, nameID string) (domain.Journal, error) {
		h := header(ctx, typ, "USD")
		h.OffsetAccountID, h.JournalNameID = "", nameID
		j, _, err := s.CreateJournal(ctx, h, ids.New())
		return j, err
	}
	// The type, legal entity and active flag are checked at creation.
	if _, err := mk(domain.JournalMovement, adj.ID); problemCode(err) != domain.CodeJournalNameUnusable {
		t.Fatalf("name of another type: %v", err)
	}
	if _, err := mk(domain.JournalAdjustment, ids.New()); problemCode(err) != domain.CodeJournalNameNotFound {
		t.Fatalf("unknown name: %v", err)
	}
	other := entityCtx(1, ids.New())
	h := header(other, domain.JournalAdjustment, "USD")
	h.JournalNameID = adj.ID
	if _, _, err := s.CreateJournal(other, h, ids.New()); problemCode(err) != domain.CodeJournalNameNotFound {
		t.Fatalf("name of another legal entity is invisible: %v", err)
	}

	// MOVEMENT takes the default offset account of its name.
	m, err := mk(domain.JournalMovement, mov.ID)
	if err != nil || m.OffsetAccountID != "500100" || m.JournalNameID != mov.ID {
		t.Fatalf("movement under name: %v %+v", err, m)
	}

	// A name that requires approval refuses a post from DRAFT, then lets the approved journal post.
	j, err := mk(domain.JournalAdjustment, adj.ID)
	if err != nil {
		t.Fatal(err)
	}
	j, err = s.AddJournalLines(ctx, j.ID, []domain.JournalLine{line(product, "5", "2", dims())})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostJournal(ctx, j.ID, j.Version, postParams()); problemCode(err) != domain.CodeApprovalRequired {
		t.Fatalf("post from DRAFT: %v", err)
	}
	if j, err = s.TransitionJournal(ctx, j.ID, j.Version, domain.JournalSubmitted); err != nil {
		t.Fatal(err)
	}
	if j, err = s.TransitionJournal(ctx, j.ID, j.Version, domain.JournalApproved); err != nil {
		t.Fatal(err)
	}
	posted, err := s.PostJournal(ctx, j.ID, j.Version, postParams())
	if err != nil || posted.Status != domain.JournalPosted {
		t.Fatalf("post approved: %v %+v", err, posted)
	}

	// An inactive name refuses new journals, but a replay of an earlier create still answers.
	h = header(ctx, domain.JournalAdjustment, "USD")
	h.JournalNameID = adj.ID
	first, _, err := s.CreateJournal(ctx, h, "replay-key")
	if err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := s.UpdateJournalName(ctx, adj.ID, adj.Version, store.JournalNamePatch{Active: &off}); err != nil {
		t.Fatal(err)
	}
	if again, inserted, err := s.CreateJournal(ctx, h, "replay-key"); err != nil || inserted || again.ID != first.ID {
		t.Fatalf("replay after deactivation: %v inserted=%v", err, inserted)
	}
	if _, err := mk(domain.JournalAdjustment, adj.ID); problemCode(err) != domain.CodeJournalNameUnusable {
		t.Fatalf("inactive name: %v", err)
	}
	h.JournalNameID = ""
	if _, _, err := s.CreateJournal(ctx, h, "replay-key"); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key without the name: %v", err)
	}
}

// Concurrent retries of one create (a double click, a client retry) all answer
// with the same journal instead of failing on the unique key.
func TestConcurrentJournalCreatesUnderOneKeyReplay(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	mov, _, err := newName(ctx, s, "n1", "MOVE", domain.JournalMovement, false, "500100")
	if err != nil {
		t.Fatal(err)
	}
	h := header(ctx, domain.JournalMovement, "USD")
	h.OffsetAccountID, h.JournalNameID = "", mov.ID
	const n = 8
	got := make([]domain.Journal, n)
	inserted := make([]bool, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], inserted[i], errs[i] = s.CreateJournal(ctx, h, "same-key")
		}()
	}
	wg.Wait()
	created := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("create %d: %v", i, errs[i])
		}
		if got[i].ID != got[0].ID {
			t.Fatalf("create %d made journal %s, want %s", i, got[i].ID, got[0].ID)
		}
		if inserted[i] {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("%d creates inserted, want 1", created)
	}
}

// The database itself refuses a journal that points at a name of another legal
// entity, whatever path writes it.
func TestJournalNameForeignKeyHoldsTheLegalEntity(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	adj, _, err := newName(ctx, s, "n1", "ADJ", domain.JournalAdjustment, false, "")
	if err != nil {
		t.Fatal(err)
	}
	j, _, err := s.CreateJournal(ctx, header(ctx, domain.JournalAdjustment, "USD"), ids.New())
	if err != nil {
		t.Fatal(err)
	}
	other := entityCtx(1, ids.New())
	foreign, _, err := newName(other, s, "n2", "ADJ", domain.JournalAdjustment, false, "")
	if err != nil {
		t.Fatal(err)
	}
	set := `update inventory_journal set journal_name_id = $3 where tenant_id = $1 and id = $2`
	if _, err := admin.Exec(context.Background(), set, testkit.TenantID(1), j.ID, foreign.ID); err == nil {
		t.Fatal("journal accepted a journal name of another legal entity")
	}
	if _, err := admin.Exec(context.Background(), set, testkit.TenantID(1), j.ID, adj.ID); err != nil {
		t.Fatalf("journal name of its own legal entity: %v", err)
	}
}
