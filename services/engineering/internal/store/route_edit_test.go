package store_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

func editedRoute(v domain.RouteVersion, ops ...domain.VersionOperation) domain.RouteVersion {
	out := newRoute(v.ProductID, v.SiteID, v.VersionNo, releaseDay.AddDate(0, 1, 0), ops...)
	out.ID = v.ID
	return out
}

func TestPostgres_UpdateRouteDraft_ReplacesOperationsAndLocksOnVersion(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	req := domain.OperationRequirement{Kind: domain.ReqType, RefID: domain.TypeMachine, Quantity: 1}
	draft := newRoute("BIKE", "S1", 1, releaseDay, routeOp(10, 20, req), routeOp(20, 0))
	created, err := s.CreateRouteVersion(ctx, draft, store.Idem{})
	if err != nil || created.Version != 1 {
		t.Fatalf("create = %+v, %v; want version 1", created, err)
	}
	next := editedRoute(draft, routeOp(10, 0))
	next.Operations[0].RunH = decimal.RequireFromString("3.5")
	next.Operations[0].Requirements = []domain.OperationRequirement{{Kind: domain.ReqCapability, RefID: "weld", Level: 2, Quantity: 2}}
	got, err := s.UpdateRouteDraft(ctx, next, 1, store.Idem{Key: "edit-1", Hash: "h"})
	if err != nil || got.Version != 2 || len(got.Operations) != 1 {
		t.Fatalf("update = %+v, %v; want version 2 and one operation", got, err)
	}
	read, _ := s.GetRouteVersion(ctx, draft.ID)
	if read.Version != 2 || len(read.Operations) != 1 || !read.Operations[0].RunH.Equal(decimal.RequireFromString("3.5")) || !read.EffectiveFrom.Equal(next.EffectiveFrom) ||
		len(read.Operations[0].Requirements) != 1 || read.Operations[0].Requirements[0].RefID != "weld" || read.VersionNo != 1 || read.ProductID != "BIKE" {
		t.Fatalf("stored = %+v; want the edited header and the single weld operation", read)
	}
	again, err := s.UpdateRouteDraft(ctx, next, 1, store.Idem{Key: "edit-1", Hash: "h"})
	if err != nil || again.Version != 2 {
		t.Fatalf("replay = %+v, %v; want version 2 unchanged", again, err)
	}
	if _, err := s.UpdateRouteDraft(ctx, editedRoute(draft, routeOp(10, 0)), 1, store.Idem{}); !domain.HasCode(err, domain.CodeRouteConflict) {
		t.Fatalf("stale save = %v; want %s", err, domain.CodeRouteConflict)
	}
	if _, err := s.UpdateRouteDraft(ctx, next, 2, store.Idem{Key: "edit-1", Hash: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("reused key with another body = %v; want %s", err, store.CodeIdempotencyReuse)
	}
	if len(outboxTypes(t, pool, ctx, draft.ID)) != 0 {
		t.Fatal("editing a draft emitted an event")
	}
	cancelled, err := s.CancelRouteVersion(ctx, draft.ID)
	if err != nil || cancelled.Version != 3 {
		t.Fatalf("cancel = %+v, %v; want version 3", cancelled, err)
	}
	if _, err := s.UpdateRouteDraft(ctx, editedRoute(draft, routeOp(10, 0)), 3, store.Idem{}); !domain.HasCode(err, domain.CodeRouteState) {
		t.Fatalf("edit a cancelled version = %v; want %s", err, domain.CodeRouteState)
	}
}

func TestPostgres_UpdateRouteDraft_RefusesActiveInvalidAndUnknown(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	req := domain.OperationRequirement{Kind: domain.ReqType, RefID: domain.TypeMachine, Quantity: 1}
	_, _ = seedCapableMachine(t, s, ctx, "S1", releaseDay.AddDate(0, -1, 0))
	live := newRoute("BIKE", "S1", 1, releaseDay.AddDate(-1, 0, 0), routeOp(10, 0, req))
	draft := newRoute("BIKE", "S1", 2, releaseDay, routeOp(10, 0))
	for _, v := range []domain.RouteVersion{live, draft} {
		if _, err := s.CreateRouteVersion(ctx, v, store.Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	activated, err := s.ActivateRouteVersion(ctx, live.ID)
	if err != nil || activated.Version != 2 {
		t.Fatalf("activate = %+v, %v; want version 2", activated, err)
	}
	if _, err := s.UpdateRouteDraft(ctx, editedRoute(live, routeOp(10, 0)), 2, store.Idem{}); !domain.HasCode(err, domain.CodeRouteState) {
		t.Fatalf("edit an active version = %v; want %s", err, domain.CodeRouteState)
	}
	if _, err := s.UpdateRouteDraft(ctx, editedRoute(draft, routeOp(10, 99)), 1, store.Idem{}); !domain.HasCode(err, domain.CodeRouteNext) {
		t.Fatalf("dangling next = %v; want %s", err, domain.CodeRouteNext)
	}
	if got, _ := s.GetRouteVersion(ctx, draft.ID); got.Version != 1 || !got.EffectiveFrom.Equal(releaseDay) {
		t.Fatalf("a refused save changed the draft: %+v", got)
	}
	if _, err := s.UpdateRouteDraft(ctx, editedRoute(domain.RouteVersion{ID: ids.New(), ProductID: "X", SiteID: "S", VersionNo: 1}, routeOp(10, 0)), 1, store.Idem{}); problemCode(err) != store.CodeRouteNotFound {
		t.Fatalf("unknown id = %v; want %s", err, store.CodeRouteNotFound)
	}
}

// Two editors who loaded the same version save at once: one save wins, the other is refused with the
// conflict code, and the stored draft is exactly the winner's content.
func TestPostgres_UpdateRouteDraft_ConcurrentSavesOneWins(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	draft := newRoute("BIKE", "S1", 1, releaseDay, routeOp(10, 0))
	if _, err := s.CreateRouteVersion(ctx, draft, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	edits := []domain.RouteVersion{editedRoute(draft, routeOp(10, 20), routeOp(20, 0)), editedRoute(draft, routeOp(10, 30), routeOp(30, 0))}
	results := make([]error, len(edits))
	var wg sync.WaitGroup
	for i := range edits {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = s.UpdateRouteDraft(ctx, edits[i], 1, store.Idem{})
		}(i)
	}
	wg.Wait()
	winner := -1
	for i, err := range results {
		switch {
		case err == nil && winner == -1:
			winner = i
		case !domain.HasCode(err, domain.CodeRouteConflict):
			t.Fatalf("save %d = %v; want one success and one %s", i, err, domain.CodeRouteConflict)
		}
	}
	if winner == -1 {
		t.Fatalf("both saves refused: %v", results)
	}
	got, err := s.GetRouteVersion(ctx, draft.ID)
	if err != nil || got.Version != 2 || len(got.Operations) != 2 || got.Operations[1].No != edits[winner].Operations[1].No {
		t.Fatalf("stored = %+v, %v; want version 2 with the winning save's operations", got, err)
	}
}

// A reader never sees the header of one save with the operations or requirements of another: two
// contents alternate, each with its own operation numbers, while another goroutine reads.
func TestPostgres_GetRouteVersion_DuringEditsSeesOneSave(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	req := domain.OperationRequirement{Kind: domain.ReqType, RefID: domain.TypeMachine, Quantity: 1}
	draft := newRoute("BIKE", "S1", 1, releaseDay, routeOp(10, 0, req))
	if _, err := s.CreateRouteVersion(ctx, draft, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	shapes := []domain.RouteVersion{editedRoute(draft, routeOp(20, 0, req)), editedRoute(draft, routeOp(30, 40, req), routeOp(40, 0, req))}
	shapes[1].EffectiveFrom = releaseDay.AddDate(0, 2, 0)
	done := make(chan struct{})
	readErr := make(chan error, 1)
	go func() {
		defer close(readErr)
		for {
			select {
			case <-done:
				return
			default:
			}
			got, err := s.GetRouteVersion(ctx, draft.ID)
			if err != nil {
				readErr <- err
				return
			}
			two := got.EffectiveFrom.Equal(shapes[1].EffectiveFrom)
			if got.Version > 1 && (two != (len(got.Operations) == 2) || len(got.Operations[0].Requirements) != 1) {
				readErr <- fmt.Errorf("mixed read at version %d: from %s with %d operations", got.Version, got.EffectiveFrom, len(got.Operations))
				return
			}
		}
	}()
	for i := 0; i < 30; i++ {
		if _, err := s.UpdateRouteDraft(ctx, shapes[i%2], i+1, store.Idem{}); err != nil {
			close(done)
			t.Fatalf("edit %d: %v", i, err)
		}
	}
	close(done)
	if err := <-readErr; err != nil {
		t.Fatal(err)
	}
}
