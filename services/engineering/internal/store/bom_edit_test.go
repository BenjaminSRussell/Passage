package store_test

import (
	"testing"

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
