package store_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

func TestPostgres_UpdateResource_LocksOnVersionAndEmitsChangedFields(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	id, _ := seedCapableMachine(t, s, ctx, "S1", releaseDay)
	rec, err := s.GetResource(ctx, id)
	if err != nil || rec.Resource.Version != 1 {
		t.Fatalf("seed = %+v, %v; want version 1", rec, err)
	}
	next := rec.Resource
	next.Name, next.Exclusive, next.CapacityPerDayH, next.Capabilities = "Renamed", true, decimal.RequireFromString("12.5"), nil
	got, err := s.UpdateResource(ctx, next, 1, store.Idem{Key: "res-edit", Hash: "h"})
	if err != nil || got.Resource.Version != 2 || got.Resource.Name != "Renamed" || !got.Resource.Exclusive || len(got.Resource.Capabilities) != 0 || got.Resource.Code != rec.Resource.Code {
		t.Fatalf("update = %+v, %v; want version 2, new name, exclusive, no capabilities", got, err)
	}
	if again, err := s.UpdateResource(ctx, next, 1, store.Idem{Key: "res-edit", Hash: "h"}); err != nil || again.Resource.Version != 2 {
		t.Fatalf("replay = %+v, %v; want version 2 unchanged", again, err)
	}
	if _, err := s.UpdateResource(ctx, next, 1, store.Idem{}); !domain.HasCode(err, domain.CodeResConflict) {
		t.Fatalf("stale save = %v; want %s", err, domain.CodeResConflict)
	}
	// A save that changes nothing writes nothing and emits nothing.
	if same, err := s.UpdateResource(ctx, got.Resource, 2, store.Idem{}); err != nil || same.Resource.Version != 2 {
		t.Fatalf("no-op save = %+v, %v; want version 2", same, err)
	}
	if types := outboxTypes(t, pool, ctx, id); !slices.Equal(types, []string{resourceChanged, resourceChanged}) {
		t.Fatalf("events = %v; want the create and the one edit", types)
	}
	if _, err := s.UpdateResource(ctx, domain.Resource{ID: ids.New(), Type: domain.TypeMachine}, 1, store.Idem{}); problemCode(err) != store.CodeResNotFound {
		t.Fatalf("unknown resource = %v; want %s", err, store.CodeResNotFound)
	}
}

func TestPostgres_DeleteResource_RefusedWhileAnActiveRouteRequiresIt(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	id, capabilityID := seedCapableMachine(t, s, ctx, "S1", releaseDay.AddDate(0, -1, 0))
	direct := newRoute("BIKE", "S1", 1, releaseDay, routeOp(10, 0, domain.OperationRequirement{Kind: domain.ReqResource, RefID: id, Quantity: 1}))
	if _, err := s.CreateRouteVersion(ctx, direct, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateRouteVersion(ctx, direct.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteResource(ctx, id); !domain.HasCode(err, domain.CodeResInUse) {
		t.Fatalf("delete = %v; want %s", err, domain.CodeResInUse)
	}
	if _, err := s.GetResource(ctx, id); err != nil {
		t.Fatalf("a refused delete removed the resource: %v", err)
	}
	// An active route that asks for a capability, and a draft that names the resource, do not block.
	free, _ := seedCapableMachine(t, s, ctx, "S1", releaseDay.AddDate(0, -1, 0))
	byCapability := newRoute("WHEEL", "S1", 1, releaseDay, routeOp(10, 0, domain.OperationRequirement{Kind: domain.ReqCapability, RefID: capabilityID, Level: 1, Quantity: 1}))
	draft := newRoute("FRAME", "S1", 1, releaseDay, routeOp(10, 0, domain.OperationRequirement{Kind: domain.ReqResource, RefID: free, Quantity: 1}))
	for _, v := range []domain.RouteVersion{byCapability, draft} {
		if _, err := s.CreateRouteVersion(ctx, v, store.Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ActivateRouteVersion(ctx, byCapability.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteResource(ctx, free); err != nil {
		t.Fatalf("delete of a resource no active route requires = %v", err)
	}
	if _, err := s.GetResource(ctx, free); problemCode(err) != store.CodeResNotFound {
		t.Fatalf("deleted resource read = %v; want %s", err, store.CodeResNotFound)
	}
	if n := count(t, pool, ctx, `select count(*) from resource_capability where resource_id = $1::uuid`, free); n != 0 {
		t.Fatalf("%d capability rows left behind", n)
	}
	if types := outboxTypes(t, pool, ctx, free); !slices.Equal(types, []string{resourceChanged, resourceChanged}) {
		t.Fatalf("events = %v; want the create and the delete", types)
	}
	if err := s.DeleteResource(ctx, free); problemCode(err) != store.CodeResNotFound {
		t.Fatalf("second delete = %v; want %s", err, store.CodeResNotFound)
	}
	// The draft still names the deleted resource; activating it says which resource is missing.
	if _, err := s.ActivateRouteVersion(ctx, draft.ID); !domain.HasCode(err, domain.CodeRouteUnsatisfied) || !strings.Contains(err.Error(), free) || !strings.Contains(err.Error(), "deleted") {
		t.Fatalf("activating a draft that requires a deleted resource = %v; want %s naming %s", err, domain.CodeRouteUnsatisfied, free)
	}
}

// inFlight opens a transaction on the admin pool and leaves it open, so a test can stop a competing
// writer half way through: it holds the locks its statements took until commit is called.
func inFlight(t *testing.T, pool *pgxpool.Pool, ctx context.Context) (exec func(sql string, args ...any) error, commit func()) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(ctx, `set local lock_timeout = '5s'`); err != nil {
		t.Fatal(err)
	}
	exec = func(sql string, args ...any) error { _, err := tx.Exec(ctx, sql, args...); return err }
	return exec, func() {
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

// waitBlocked runs call in the background and fails the test if it returns before the competing
// transaction is released; it then returns the call's result.
func waitBlocked(t *testing.T, call func() error, release func()) error {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- call() }()
	select {
	case err := <-result:
		t.Fatalf("returned %v while the competing transaction was still open; it must wait for it", err)
	case <-time.After(300 * time.Millisecond):
	}
	release()
	select {
	case err := <-result:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("still waiting after the competing transaction committed")
		return nil
	}
}

// A delete that has passed its in-use check but not committed makes an activation of a route that
// requires the resource wait, and the activation then finds the resource gone. Without the share
// lock the activation reads the resource as it was and commits an active route naming a deleted
// resource.
func TestPostgres_ActivateRouteVersion_WaitsForAResourceDeleteInFlight(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	id, _ := seedCapableMachine(t, s, ctx, "S1", releaseDay.AddDate(0, -1, 0))
	draft := newRoute("BIKE", "S1", 1, releaseDay, routeOp(10, 0, domain.OperationRequirement{Kind: domain.ReqResource, RefID: id, Quantity: 1}))
	if _, err := s.CreateRouteVersion(ctx, draft, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	exec, commit := inFlight(t, pool, ctx)
	for _, q := range []string{`select 1 from resource where id = $1::uuid for update`, `delete from resource_capability where resource_id = $1::uuid`, `delete from resource where id = $1::uuid`} {
		if err := exec(q, id); err != nil {
			t.Fatal(err)
		}
	}
	err := waitBlocked(t, func() error { _, err := s.ActivateRouteVersion(ctx, draft.ID); return err }, commit)
	if !domain.HasCode(err, domain.CodeRouteUnsatisfied) || !strings.Contains(err.Error(), id) {
		t.Fatalf("activation after the delete = %v; want %s naming resource %s", err, domain.CodeRouteUnsatisfied, id)
	}
	if got, err := s.GetRouteVersion(ctx, draft.ID); err != nil || got.Status != domain.StatusDraft {
		t.Fatalf("route = %+v, %v; want it still draft", got, err)
	}
}

// An activation that holds the resource and has not committed makes a delete wait, and the delete
// then sees the route active and is refused (BR-9.11).
func TestPostgres_DeleteResource_WaitsForAnActivationInFlight(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	id, _ := seedCapableMachine(t, s, ctx, "S1", releaseDay.AddDate(0, -1, 0))
	draft := newRoute("BIKE", "S1", 1, releaseDay, routeOp(10, 0, domain.OperationRequirement{Kind: domain.ReqResource, RefID: id, Quantity: 1}))
	if _, err := s.CreateRouteVersion(ctx, draft, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	exec, commit := inFlight(t, pool, ctx)
	if err := exec(`select 1 from resource where id = $1::uuid for share`, id); err != nil {
		t.Fatal(err)
	}
	if err := exec(`update route_version set status = 'active', version = version + 1 where id = $1::uuid`, draft.ID); err != nil {
		t.Fatal(err)
	}
	if err := waitBlocked(t, func() error { return s.DeleteResource(ctx, id) }, commit); !domain.HasCode(err, domain.CodeResInUse) {
		t.Fatalf("delete after the activation = %v; want %s", err, domain.CodeResInUse)
	}
	if _, err := s.GetResource(ctx, id); err != nil {
		t.Fatalf("a refused delete removed the resource: %v", err)
	}
}
