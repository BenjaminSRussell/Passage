package store_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

func edited(v domain.BomVersion, site string, components ...string) domain.BomVersion {
	out := version(v.ProductID, domain.StatusDraft, releaseDay.AddDate(0, 1, 0), components...)
	out.ID, out.SiteID = v.ID, site
	return out
}

func TestPostgres_UpdateBomDraft_ReplacesLinesAndLocksOnVersion(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	draft := version("BIKE", domain.StatusDraft, releaseDay, "WHEEL", "FRAME")
	created, err := s.CreateBomVersion(ctx, draft, store.Idem{}, noCheck)
	if err != nil || created.Version != 1 {
		t.Fatalf("create = %+v, %v; want version 1", created, err)
	}
	next := edited(draft, "S1", "HUB")
	next.Lines[0].Quantity = decimal.RequireFromString("7")
	got, err := s.UpdateBomDraft(ctx, next, 1, store.Idem{Key: "edit-1", Hash: "h"}, noCheck)
	if err != nil || got.Version != 2 || got.SiteID != "S1" || len(got.Lines) != 1 {
		t.Fatalf("update = %+v, %v; want version 2, site S1, one line", got, err)
	}
	read, _ := s.GetBomVersion(ctx, draft.ID)
	if read.Version != 2 || len(read.Lines) != 1 || read.Lines[0].ComponentProductID != "HUB" || !read.Lines[0].Quantity.Equal(decimal.NewFromInt(7)) || !read.EffectiveFrom.Equal(next.EffectiveFrom) {
		t.Fatalf("stored = %+v; want the edited header and the single HUB line", read)
	}
	// A repeat of the same request replays: no second bump, no conflict.
	again, err := s.UpdateBomDraft(ctx, next, 1, store.Idem{Key: "edit-1", Hash: "h"}, noCheck)
	if err != nil || again.Version != 2 {
		t.Fatalf("replay = %+v, %v; want version 2 unchanged", again, err)
	}
	// Another editor still on version 1 loses and changes nothing.
	stale := edited(draft, "S2", "AXLE")
	if _, err := s.UpdateBomDraft(ctx, stale, 1, store.Idem{}, noCheck); !domain.HasCode(err, domain.CodeBomVersionConflict) {
		t.Fatalf("stale save = %v; want %s", err, domain.CodeBomVersionConflict)
	}
	if read, _ = s.GetBomVersion(ctx, draft.ID); read.SiteID != "S1" || read.Lines[0].ComponentProductID != "HUB" {
		t.Fatalf("a refused save changed the draft: %+v", read)
	}
	if len(outboxTypes(t, pool, ctx, draft.ID)) != 0 {
		t.Fatal("editing a draft emitted an event")
	}
	if _, err := s.UpdateBomDraft(ctx, edited(draft, "S1", "HUB"), 2, store.Idem{Key: "edit-1", Hash: "other"}, noCheck); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("reused key with another body = %v; want %s", err, store.CodeIdempotencyReuse)
	}
	// Cancelling bumps the counter too, and the response carries the bumped value.
	cancelled, err := s.CancelBomVersion(ctx, draft.ID)
	if err != nil || cancelled.Version != 3 {
		t.Fatalf("cancel = %+v, %v; want version 3", cancelled, err)
	}
	if read, _ = s.GetBomVersion(ctx, draft.ID); read.Version != 3 {
		t.Fatalf("stored version after cancel = %d; want 3", read.Version)
	}
}

func TestPostgres_UpdateBomDraft_RefusesNonDraftEcoDraftAndCheckFailure(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	active := version("BIKE", domain.StatusActive, releaseDay.AddDate(-1, 0, 0), "WHEEL")
	draft := version("BIKE", domain.StatusDraft, releaseDay, "WHEEL")
	for _, v := range []domain.BomVersion{active, draft} {
		if _, err := s.CreateBomVersion(ctx, v, store.Idem{}, noCheck); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.UpdateBomDraft(ctx, edited(active, "", "HUB"), 1, store.Idem{}, noCheck); !domain.HasCode(err, domain.CodeBomState) {
		t.Fatalf("edit an active version = %v; want %s", err, domain.CodeBomState)
	}
	if _, err := s.UpdateBomDraft(ctx, edited(draft, "", "HUB"), 1, store.Idem{}, func(store.Snapshot) error {
		return &domain.Error{Code: domain.CodeBomCycle, Detail: "cycle"}
	}); !domain.HasCode(err, domain.CodeBomCycle) {
		t.Fatalf("failing check = %v; want %s", err, domain.CodeBomCycle)
	}
	if got, _ := s.GetBomVersion(ctx, draft.ID); got.Version != 1 || got.Lines[0].ComponentProductID != "WHEEL" {
		t.Fatalf("a failed check changed the draft: %+v", got)
	}
	other := edited(draft, "", "HUB")
	other.ProductID = "TRIKE"
	if _, err := s.UpdateBomDraft(ctx, other, 1, store.Idem{}, noCheck); !domain.HasCode(err, domain.CodeBomState) {
		t.Fatalf("product change = %v; want %s", err, domain.CodeBomState)
	}
	eco := domain.Eco{ID: ids.New(), Number: "ECO-E", State: domain.EcoSubmitted, ReleaseDate: releaseDay, Replaces: []domain.Replacement{{OldVersionID: active.ID, NewVersionID: draft.ID}}}
	if _, err := s.CreateEco(ctx, eco, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateBomDraft(ctx, edited(draft, "", "HUB"), 1, store.Idem{}, noCheck); !domain.HasCode(err, domain.CodeBomInEco) {
		t.Fatalf("edit a draft in an open ECO = %v; want %s", err, domain.CodeBomInEco)
	}
	if _, err := pool.Exec(ctx, `update eco set state = 'rejected' where id = $1::uuid`, eco.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.UpdateBomDraft(ctx, edited(draft, "", "HUB"), 1, store.Idem{}, noCheck); err != nil || got.Version != 2 {
		t.Fatalf("edit after the ECO was rejected = %+v, %v", got, err)
	}
	if _, err := s.UpdateBomDraft(ctx, edited(draft, "", "HUB"), 1, store.Idem{}, noCheck); !domain.HasCode(err, domain.CodeBomVersionConflict) {
		t.Fatalf("stale = %v; want conflict", err)
	}
	if _, err := s.UpdateBomDraft(ctx, edited(domain.BomVersion{ID: ids.New(), ProductID: "X"}, "", "HUB"), 1, store.Idem{}, noCheck); problemCode(err) != store.CodeBomNotFound {
		t.Fatalf("unknown id = %v; want %s", err, store.CodeBomNotFound)
	}
}

func TestPostgres_UpdateBomDraft_RLSHidesAnotherTenantsDraft(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	draft := version("BIKE", domain.StatusDraft, releaseDay, "WHEEL")
	if _, err := s.CreateBomVersion(ctx, draft, store.Idem{}, noCheck); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateBomDraft(principalContext(2), edited(draft, "", "HUB"), 1, store.Idem{}, noCheck); problemCode(err) != store.CodeBomNotFound {
		t.Fatalf("another tenant's edit = %v; want %s", err, store.CodeBomNotFound)
	}
}

// A reader never sees the header of one save with the lines of another: two contents alternate, each
// with its own effective date and line count, while goroutines read through the plain get and
// through the idempotent replay of a save. Without the share lock on the header the lines can come
// from the next save.
func TestPostgres_ReadBomVersion_DuringEditsSeesOneSave(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	draft := version("BIKE", domain.StatusDraft, releaseDay, "WHEEL")
	if _, err := s.CreateBomVersion(ctx, draft, store.Idem{}, noCheck); err != nil {
		t.Fatal(err)
	}
	shapes := []domain.BomVersion{edited(draft, "S1", "HUB"), edited(draft, "S1", "HUB", "AXLE", "SPOKE")}
	shapes[1].EffectiveFrom = releaseDay.AddDate(0, 2, 0)
	if _, err := s.UpdateBomDraft(ctx, shapes[0], 1, store.Idem{Key: "seed", Hash: "h"}, noCheck); err != nil {
		t.Fatal(err)
	}
	check := func(got domain.BomVersion) error {
		two := got.EffectiveFrom.Equal(shapes[1].EffectiveFrom)
		if got.Version > 1 && two != (len(got.Lines) == 3) {
			return fmt.Errorf("mixed read at version %d: from %s with %d lines", got.Version, got.EffectiveFrom, len(got.Lines))
		}
		return nil
	}
	done := make(chan struct{})
	readers := []func() (domain.BomVersion, error){
		func() (domain.BomVersion, error) { return s.GetBomVersion(ctx, draft.ID) },
		func() (domain.BomVersion, error) {
			return s.UpdateBomDraft(ctx, shapes[0], 1, store.Idem{Key: "seed", Hash: "h"}, noCheck)
		},
	}
	errs := make(chan error, len(readers))
	for _, read := range readers {
		go func() {
			for {
				select {
				case <-done:
					errs <- nil
					return
				default:
				}
				got, err := read()
				if err == nil {
					err = check(got)
				}
				if err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	var saveErr error
	for i := 0; i < 30 && saveErr == nil; i++ {
		_, saveErr = s.UpdateBomDraft(ctx, shapes[(i+1)%2], i+2, store.Idem{}, noCheck)
	}
	close(done)
	for range readers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if saveErr != nil {
		t.Fatal(saveErr)
	}
}

// GetEco takes FOR SHARE on the headers of the ECO's products in id order. A release must write those
// headers in the same order: here the new version sorts before the old one, and a reader holds the
// new one while it waits for the old one. A release that expired the old header first would hold it
// while waiting for the new one, and Postgres would abort one side as a deadlock.
func TestPostgres_ReleaseEco_WritesHeadersInTheOrderReadersLockThem(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	lo, hi := ids.New(), ids.New()
	if hi < lo {
		lo, hi = hi, lo
	}
	old := version("BIKE", domain.StatusActive, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), "WHEEL")
	next := version("BIKE", domain.StatusDraft, releaseDay, "WHEEL", "FRAME")
	old.ID, next.ID = hi, lo
	for _, v := range []domain.BomVersion{old, next} {
		if _, err := s.CreateBomVersion(ctx, v, store.Idem{}, noCheck); err != nil {
			t.Fatal(err)
		}
	}
	eco := domain.Eco{ID: ids.New(), Number: "ECO-1", State: domain.EcoSubmitted, CreatedBy: "rita", ReleaseDate: releaseDay, Replaces: []domain.Replacement{{OldVersionID: old.ID, NewVersionID: next.ID}}}
	if _, err := s.CreateEco(ctx, eco, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	record, err := s.GetEco(ctx, eco.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded := record.Eco
	if err := loaded.Review(ctx, approverOf(true)); err != nil {
		t.Fatal(err)
	}
	plan, err := loaded.Release(record.Versions, releaseDay)
	if err != nil {
		t.Fatal(err)
	}
	exec, commit := inFlight(t, pool, ctx)
	if err := exec(`select 1 from bom_version where id = $1::uuid for share`, lo); err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	go func() { released <- s.ReleaseEco(ctx, loaded, domain.EcoSubmitted, plan) }()
	time.Sleep(300 * time.Millisecond)
	if err := exec(`select 1 from bom_version where id = $1::uuid for share`, hi); err != nil {
		t.Fatalf("reader's second share lock = %v; the release must not hold a header the reader locks later", err)
	}
	commit()
	if err := <-released; err != nil {
		t.Fatalf("release = %v", err)
	}
	if got, err := s.GetBomVersion(ctx, next.ID); err != nil || got.Status != domain.StatusActive {
		t.Fatalf("new version = %+v, %v; want active", got, err)
	}
}
