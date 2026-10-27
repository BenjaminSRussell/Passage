package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// rollupWindow computes and stores the 15-minute buckets of [from, to) the way the API does.
func rollupWindow(t *testing.T, s *Postgres, ctx context.Context, resource string, from, to time.Time) OEERollupResult {
	t.Helper()
	streams, err := s.LoadOEEStreams(ctx, []string{resource}, from, to)
	if err != nil {
		t.Fatal(err)
	}
	r := OEERollup{ResourceIDs: []string{resource}, BucketMinutes: 15, From: from, To: to}
	if st, ok := streams[resource]; ok {
		buckets, err := domain.BuildBuckets(st, from, to, 15*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range buckets {
			r.Rows = append(r.Rows, OEERow{ResourceID: resource, IdealCycleSeconds: st.IdealCycleSeconds, Bucket: b})
		}
	}
	out, err := s.SaveOEEBuckets(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type storedBucket struct {
	start                  time.Time
	planned, run, down, a  string
	ideal                  string
	total, good, sourceEvt int64
}

func storedBuckets(t *testing.T, pool *pgxpool.Pool, tenantID string) []storedBucket {
	t.Helper()
	var out []storedBucket
	inTenant(t, pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `select bucket_start, planned_minutes::text, run_minutes::text, down_minutes::text,
			availability::text, ideal_cycle_s::text, total_count, good_count, source_events from oee_bucket order by bucket_start`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b storedBucket
			if err := rows.Scan(&b.start, &b.planned, &b.run, &b.down, &b.a, &b.ideal, &b.total, &b.good, &b.sourceEvt); err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out
}

// FR-25.10 OEE rollup: buckets are stored once per (resource, width, start) and
// publish oeebucket.updated; the same window again changes and publishes nothing;
// a new count rewrites only its bucket; a bucket whose downtime is later coded as
// planned has no planned time left and is removed with oeebucket.removed; another
// tenant sees none of it.
func TestOEE_RollupStoresOncePublishesChangesAndRemoves(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(32)
	ctx := testkit.Ctx(tenantID)
	resource := ids.New()
	mustMachine(t, s, ctx, "M-OEE", resource)

	t0 := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	countAt := func(seq int64, m int) MachineEventIn {
		return MachineEventIn{MachineCode: "M-OEE", Event: domain.MachineEvent{Seq: seq, Kind: "count", At: at(m), GoodCount: 3}}
	}
	// 08:00-08:15 RUNNING with a count every 3 minutes; DOWN from 08:15.
	if _, err := s.IngestMachineEvents(ctx, at(31), []MachineEventIn{
		floorEvent("M-OEE", 1, "state", at(0), domain.MachineRunning),
		countAt(2, 3), countAt(3, 6), countAt(4, 9), countAt(5, 12),
		floorEvent("M-OEE", 6, "state", at(15), domain.MachineDown),
	}); err != nil {
		t.Fatal(err)
	}

	res := rollupWindow(t, s, ctx, resource, t0, at(30))
	if res.Computed != 2 || res.Changed != 2 || res.Removed != 0 {
		t.Fatalf("first rollup %+v", res)
	}
	stored := storedBuckets(t, app, tenantID)
	if len(stored) != 2 || !stored[0].start.Equal(t0) || stored[0].planned != "15.0000" || stored[0].run != "15.0000" ||
		stored[0].down != "0.0000" || stored[0].a != "1.0000" || stored[0].ideal != "60.000" || stored[0].total != 12 || stored[0].sourceEvt != 5 {
		t.Fatalf("stored = %+v", stored)
	}
	if b := stored[1]; b.run != "0.0000" || b.down != "15.0000" || b.a != "0.0000" {
		t.Fatalf("down bucket = %+v", b)
	}
	if n := countEvents(t, app, tenantID, sfevents.OEEBucketUpdated); n != 2 {
		t.Fatalf("oeebucket.updated = %d, want 2", n)
	}
	var first sfevents.OEEBucketUpdatedData
	lastEventData(t, app, tenantID, sfevents.OEEBucketUpdated, &first)
	if first.ResourceID != resource || first.BucketMinutes != 15 || !first.PlannedMinutes.Equal(decimal.NewFromInt(15)) ||
		!first.PlannedMinutes.Equal(first.RunMinutes.Add(first.DownMinutes)) {
		t.Fatalf("event data %+v", first)
	}

	if res := rollupWindow(t, s, ctx, resource, t0, at(30)); res.Changed != 0 || res.Removed != 0 || res.Computed != 2 {
		t.Fatalf("repeated rollup %+v", res)
	}
	if n := countEvents(t, app, tenantID, sfevents.OEEBucketUpdated); n != 2 {
		t.Fatalf("a repeated rollup published: %d events", n)
	}

	// A late count in the first bucket rewrites that bucket only.
	if _, err := s.IngestMachineEvents(ctx, at(32), []MachineEventIn{countAt(7, 14)}); err != nil {
		t.Fatal(err)
	}
	if res := rollupWindow(t, s, ctx, resource, t0, at(30)); res.Changed != 1 || res.Removed != 0 {
		t.Fatalf("after a late count %+v", res)
	}
	if stored := storedBuckets(t, app, tenantID); stored[0].total != 15 || stored[1].total != 0 {
		t.Fatalf("after a late count stored = %+v", stored)
	}

	// Coding the downtime as planned leaves the second bucket without planned time.
	downs, err := s.ListDowntimes(ctx, DowntimeFilter{OpenOnly: true, Limit: 10})
	if err != nil || len(downs) != 1 {
		t.Fatalf("downtimes = %+v, %v", downs, err)
	}
	if _, err := s.CodeDowntime(ctx, downs[0].ID, "CHANGEOVER", "planned", Idem{}); err != nil {
		t.Fatal(err)
	}
	if res := rollupWindow(t, s, ctx, resource, t0, at(30)); res.Computed != 1 || res.Changed != 0 || res.Removed != 1 {
		t.Fatalf("after planned coding %+v", res)
	}
	if stored := storedBuckets(t, app, tenantID); len(stored) != 1 || !stored[0].start.Equal(t0) {
		t.Fatalf("after planned coding stored = %+v", stored)
	}
	var removed sfevents.OEEBucketRemovedData
	lastEventData(t, app, tenantID, sfevents.OEEBucketRemoved, &removed)
	if removed.ResourceID != resource || !removed.BucketStart.Equal(at(15)) || removed.BucketMinutes != 15 {
		t.Fatalf("removed event %+v", removed)
	}

	// Another tenant sees no stored bucket, and its rollup of the same resource id
	// finds no machine and removes nothing of this tenant's.
	other := testkit.TenantID(33)
	if stored := storedBuckets(t, app, other); len(stored) != 0 {
		t.Fatalf("buckets leak across tenants: %+v", stored)
	}
	if res := rollupWindow(t, s, testkit.Ctx(other), resource, t0, at(30)); res.Computed != 0 || res.Removed != 0 {
		t.Fatalf("other tenant rollup %+v", res)
	}
	if stored := storedBuckets(t, app, tenantID); len(stored) != 1 {
		t.Fatalf("other tenant's rollup touched this tenant: %+v", stored)
	}
}

// BR-25.6 through the store: the look-back finds the count before the window,
// so a window that starts mid-gap books the whole gap as idle, and the machine's
// own threshold applies.
func TestOEE_IdleLookBackAndMachineThreshold(t *testing.T) {
	s, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(34))
	short, long := ids.New(), ids.New()
	if _, err := s.RegisterMachine(ctx, Machine{Code: "M-SHORT", ResourceID: short, IdealCycleSeconds: decimal.NewFromInt(60), IdleThresholdSeconds: 60}, Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterMachine(ctx, Machine{Code: "M-LONG", ResourceID: long, IdealCycleSeconds: decimal.NewFromInt(60), IdleThresholdSeconds: 1800}, Idem{}); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	var events []MachineEventIn
	for _, code := range []string{"M-SHORT", "M-LONG"} {
		events = append(events,
			floorEvent(code, 1, "state", at(-20), domain.MachineRunning),
			MachineEventIn{MachineCode: code, Event: domain.MachineEvent{Seq: 2, Kind: "count", At: at(-2), GoodCount: 1}},
			MachineEventIn{MachineCode: code, Event: domain.MachineEvent{Seq: 3, Kind: "count", At: at(10), GoodCount: 1}},
			floorEvent(code, 4, "state", at(15), domain.MachineOff),
		)
	}
	if _, err := s.IngestMachineEvents(ctx, at(16), events); err != nil {
		t.Fatal(err)
	}
	streams, err := s.LoadOEEStreams(ctx, []string{short, long}, t0, at(15))
	if err != nil {
		t.Fatal(err)
	}
	// The look-back is the longest threshold read (30 min): RUNNING at -20 and the
	// count at -2 arrive as look-back events; OFF at 15 is the horizon.
	if st := streams[short]; st.IdleThreshold != time.Minute || !st.HistoryFrom.Equal(at(-30)) || st.Initial != "" ||
		len(st.Events) != 3 || !st.Events[1].At.Equal(at(-2)) || !st.Horizon.Equal(at(15)) {
		t.Fatalf("short stream %+v", st)
	}
	run := func(resource string) decimal.Decimal {
		b, err := domain.BuildBuckets(streams[resource], t0, at(15), 15*time.Minute)
		if err != nil || len(b) != 1 {
			t.Fatalf("buckets %+v, %v", b, err)
		}
		return b[0].RunMinutes
	}
	// Gap -2..10 (12 min): idle for a 60 s threshold, running for 1800 s; 10..15 is
	// 5 min to the OFF state: idle for 60 s, running for 1800 s.
	if got := run(short); !got.IsZero() {
		t.Fatalf("short threshold run %s, want 0", got)
	}
	if got := run(long); !got.Equal(decimal.NewFromInt(15)) {
		t.Fatalf("long threshold run %s, want 15", got)
	}
}
