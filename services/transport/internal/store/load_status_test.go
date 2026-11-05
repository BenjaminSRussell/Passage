package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	tmsevents "github.com/BenjaminSRussell/passage/services/transport/internal/events"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
	"github.com/BenjaminSRussell/passage/services/transport/migrations"
)

// TestLoadStatus_FollowsReleaseAndTender walks a load through its tender
// statuses against Postgres: the release moves it to TENDERED under If-Match,
// the workflow's tender start keeps it there, an acceptance makes it ACCEPTED
// and a tender that fails (by declines or by time) puts it back to PLANNED so it
// can be released again. Refused commands leave the load as it was.
func TestLoadStatus_FollowsReleaseAndTender(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	ctx := scopedCtx(testkit.TenantID(1), ids.New())
	carrier1, carrier2 := ids.New(), ids.New()
	state := func(load string) (string, int) {
		t.Helper()
		l, err := s.GetLoad(ctx, load)
		if err != nil {
			t.Fatal(err)
		}
		return l.Status, l.Version
	}
	want := func(load, status string, version int) {
		t.Helper()
		if got, v := state(load); got != status || v != version {
			t.Fatalf("load = %s v%d, want %s v%d", got, v, status, version)
		}
	}
	released := func() int { return len(eventsOf(t, admin, tmsevents.LoadReleased)) }

	t.Run("release, workflow start, decline, accept", func(t *testing.T) {
		load := seedLoad(t, s, ctx)
		want(load, domain.LoadPlanned, 1)
		before := released()
		if _, err := s.ReleaseLoad(ctx, releaseOf(load, carrier1, carrier2), 2, store.Idem{}, t0); problemCode(t, err) != store.CodeVersionConflict {
			t.Fatalf("release with a stale If-Match = %v, want %s", err, store.CodeVersionConflict)
		}
		want(load, domain.LoadPlanned, 1)
		r, err := s.ReleaseLoad(ctx, releaseOf(load, carrier1, carrier2), 1, store.Idem{Key: "status-release"}, t0)
		if err != nil || r.LoadStatus != domain.LoadTendered || r.LoadVersion != 2 {
			t.Fatalf("release = %+v, %v", r, err)
		}
		want(load, domain.LoadTendered, 2)
		// A released load is not released twice: that would start a second cascade.
		if _, err := s.ReleaseLoad(ctx, releaseOf(load, carrier1), 2, store.Idem{}, t0); problemCode(t, err) != domain.CodeLoadState {
			t.Fatalf("second release = %v, want %s", err, domain.CodeLoadState)
		}
		if n := released() - before; n != 1 {
			t.Fatalf("load.released events = %d, want 1", n)
		}
		// The workflow starts the tender of the released load; the status stays.
		tender, err := s.CreateTender(ctx, sentTender(load, time.Hour, carrier1, carrier2), store.Idem{Key: r.ID}, t0)
		if err != nil {
			t.Fatal(err)
		}
		want(load, domain.LoadTendered, 2)
		if _, err := s.RespondTender(ctx, tender.ID, carrier1, false, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		// One decline passes the load to the next carrier; it is still tendered.
		want(load, domain.LoadTendered, 2)
		if _, err := s.RespondTender(ctx, tender.ID, carrier2, true, t0.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
		want(load, domain.LoadAccepted, 3)
		// Repeating the acceptance changes nothing.
		if _, err := s.RespondTender(ctx, tender.ID, carrier2, true, t0.Add(3*time.Minute)); err != nil {
			t.Fatal(err)
		}
		want(load, domain.LoadAccepted, 3)
		if _, err := s.ReleaseLoad(ctx, releaseOf(load, carrier1), 3, store.Idem{}, t0); problemCode(t, err) != domain.CodeTenderOpen {
			t.Fatalf("release of an accepted load = %v, want %s", err, domain.CodeTenderOpen)
		}
		// The replay of the first release shows the load as it is now.
		again, err := s.ReleaseLoad(ctx, releaseOf(load, carrier1, carrier2), 1, store.Idem{Key: "status-release"}, t0)
		if err != nil || again.ID != r.ID || again.LoadStatus != domain.LoadAccepted || again.LoadVersion != 3 {
			t.Fatalf("replay = %+v, %v", again, err)
		}
	})

	t.Run("every carrier declines: back to PLANNED, released again", func(t *testing.T) {
		load := seedLoad(t, s, ctx)
		tender, err := s.CreateTender(ctx, sentTender(load, time.Hour, carrier1, carrier2), store.Idem{}, t0)
		if err != nil {
			t.Fatal(err)
		}
		// A tender started by a planner, without a release, also tenders the load.
		want(load, domain.LoadTendered, 2)
		for i, c := range []string{carrier1, carrier2} {
			if _, err := s.RespondTender(ctx, tender.ID, c, false, t0.Add(time.Duration(i+1)*time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := s.GetTender(ctx, tender.ID); err != nil || got.Status != domain.TenderFailed {
			t.Fatalf("tender = %+v, %v", got, err)
		}
		want(load, domain.LoadPlanned, 3)
		if r, err := s.ReleaseLoad(ctx, releaseOf(load, carrier2), 3, store.Idem{}, t0.Add(time.Hour)); err != nil || r.LoadStatus != domain.LoadTendered || r.LoadVersion != 4 {
			t.Fatalf("release after the failed tender = %+v, %v", r, err)
		}
	})

	t.Run("the expiry sweep fails the tender: back to PLANNED", func(t *testing.T) {
		load := seedLoad(t, s, ctx)
		if _, err := s.CreateTender(ctx, sentTender(load, time.Hour, carrier1), store.Idem{}, t0); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ExpireTenders(ctx, t0.Add(2*time.Hour)); err != nil {
			t.Fatal(err)
		}
		want(load, domain.LoadPlanned, 3)
	})

	t.Run("refused commands leave the load", func(t *testing.T) {
		held := seedLoad(t, s, ctx)
		if _, err := s.RecordDGCheck(ctx, domain.DGCheck{LoadID: held, Regulation: domain.RegADR, Result: domain.DGFail, ADRPoints: dec("0"), Findings: []domain.DGFinding{}, ShippingPapers: domain.ShippingPapers(domain.DGRequest{}), CheckedAt: t0}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateTender(ctx, sentTender(held, time.Hour, carrier1), store.Idem{}, t0); problemCode(t, err) != domain.CodeDGSegregation {
			t.Fatalf("tender after a failed DG check = %v", err)
		}
		if _, err := s.ReleaseLoad(ctx, releaseOf(held, carrier1), 1, store.Idem{}, t0); problemCode(t, err) != domain.CodeDGSegregation {
			t.Fatalf("release after a failed DG check = %v", err)
		}
		want(held, domain.LoadPlanned, 1)

		// A load moved on by a later step is neither released nor tendered.
		moved := seedLoad(t, s, ctx)
		if _, err := admin.Exec(context.Background(), `update load set status = 'DISPATCHED' where id = $1`, moved); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateTender(ctx, sentTender(moved, time.Hour, carrier1), store.Idem{}, t0); problemCode(t, err) != domain.CodeLoadState {
			t.Fatalf("tender of a dispatched load = %v, want %s", err, domain.CodeLoadState)
		}
		if _, err := s.ReleaseLoad(ctx, releaseOf(moved, carrier1), 1, store.Idem{}, t0); problemCode(t, err) != domain.CodeLoadState {
			t.Fatalf("release of a dispatched load = %v, want %s", err, domain.CodeLoadState)
		}
		if n := countAs(t, app, ctx, `select count(*) from tender where load_id = $1`, moved); n != 0 {
			t.Fatalf("tenders stored for the dispatched load = %d", n)
		}
		want(moved, domain.LoadDispatched, 1)
	})
}
