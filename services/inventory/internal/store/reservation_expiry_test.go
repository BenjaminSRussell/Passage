package store_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// FR-16.7 time to live: a reservation with ttlHours lapses once expires_at has passed; the
// expiry run releases only the due ones, returns their quantity to availability, writes
// one released event with reason EXPIRED each, and repeating it releases nothing more.
func TestExpiryRunReleasesOnlyDueReservations(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "1", dims()))
	in := func(qty, key string, ttl int) store.ReserveInput {
		return store.ReserveInput{LegalEntityID: legalEntity(ctx), ProductID: product, Qty: dec(qty), DemandType: domain.DemandSalesOrder, DemandID: "SO-1",
			Policy: domain.PolicyManual, Dims: dims(), RequiredDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), IdempotencyKey: key, TTLHours: ttl}
	}
	short, err := s.Reserve(ctx, in("2", "ttl-a", 1))
	if err != nil || short.Reservations[0].ExpiresAt == nil || !short.Reservations[0].ExpiresAt.After(time.Now()) {
		t.Fatalf("reserve with ttl: %v %+v", err, short.Reservations)
	}
	long, err := s.Reserve(ctx, in("3", "ttl-b", 48))
	if err != nil {
		t.Fatal(err)
	}
	forever, err := s.Reserve(ctx, in("1", "ttl-c", 0))
	if err != nil || forever.Reservations[0].ExpiresAt != nil {
		t.Fatalf("reserve without ttl: %v %+v", err, forever.Reservations)
	}
	if out, err := s.ExpireReservations(ctx, 10); err != nil || len(out.Released) != 0 || out.More {
		t.Fatalf("nothing due yet: %v %+v", err, out)
	}
	if _, err := admin.Exec(context.Background(), `update inventory_reservation set expires_at = now() - interval '1 minute' where id = $1`, short.Reservations[0].ID); err != nil {
		t.Fatal(err)
	}
	if avail, _ := availFor(t, s, ctx, product); !avail.Equal(dec("4")) {
		t.Fatalf("avail %s with 6 of 10 reserved, want 4", avail)
	}
	out, err := s.ExpireReservations(ctx, 10)
	if err != nil || len(out.Released) != 1 || out.Released[0].ID != short.Reservations[0].ID || out.Released[0].Status != domain.ReservationReleased {
		t.Fatalf("expire: %v %+v", err, out)
	}
	if avail, _ := availFor(t, s, ctx, product); !avail.Equal(dec("6")) {
		t.Fatalf("avail %s after the expiry, want 6 (the 48h and the open-ended reservations stay)", avail)
	}
	if again, err := s.ExpireReservations(ctx, 10); err != nil || len(again.Released) != 0 {
		t.Fatalf("repeat: %v %+v", err, again)
	}
	if n := outboxCount(t, admin, "dev.passage.inventory.reservation.released.v1"); n != 1 {
		t.Fatalf("released events %d, want 1", n)
	}
	var reason string
	if err := admin.QueryRow(context.Background(), `select payload->'data'->>'reason' from outbox where type = 'dev.passage.inventory.reservation.released.v1'`).Scan(&reason); err != nil || reason != "EXPIRED" {
		t.Fatalf("event reason %q err %v, want EXPIRED", reason, err)
	}
	// A manual release of a not-yet-due reservation is unaffected, and the limit pages the run.
	if _, err := admin.Exec(context.Background(), `update inventory_reservation set expires_at = now() - interval '1 minute' where id = any($1)`,
		[]string{long.Reservations[0].ID, forever.Reservations[0].ID}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ExpireReservations(ctx, 1)
	if err != nil || len(page.Released) != 1 || !page.More {
		t.Fatalf("limited run: %v %+v, want one released and more", err, page)
	}
}

// expireAll backdates every ACTIVE reservation with a time to live so the next run finds it due.
func expireAll(t *testing.T, admin *pgxpool.Pool) {
	t.Helper()
	if _, err := admin.Exec(context.Background(), `update inventory_reservation set expires_at = now() - interval '1 minute' where expires_at is not null`); err != nil {
		t.Fatal(err)
	}
}

// Two or more expiry runs at once release each due reservation exactly once: the release
// re-checks the row under its lock (SKIP LOCKED), so the bucket and the events are not doubled.
func TestConcurrentExpiryRunsReleaseEachReservationOnce(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "1", dims()))
	const n = 8
	for i := 0; i < n; i++ {
		if _, err := s.Reserve(ctx, store.ReserveInput{LegalEntityID: legalEntity(ctx), ProductID: product, Qty: dec("1"), DemandType: domain.DemandSalesOrder,
			DemandID: "SO-1", Policy: domain.PolicyManual, Dims: dims(), IdempotencyKey: ids.New(), TTLHours: 1}); err != nil {
			t.Fatal(err)
		}
	}
	expireAll(t, admin)
	var wg sync.WaitGroup
	var released atomic.Int64
	errs := make(chan error, 4)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := s.ExpireReservations(ctx, 50)
			if err != nil {
				errs <- err
				return
			}
			released.Add(int64(len(out.Released)))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := released.Load(); got != n {
		t.Fatalf("released %d across the runs, want %d", got, n)
	}
	if got := outboxCount(t, admin, "dev.passage.inventory.reservation.released.v1"); got != n {
		t.Fatalf("released events %d, want %d", got, n)
	}
	if avail, _ := availFor(t, s, ctx, product); !avail.Equal(dec("10")) {
		t.Fatalf("avail %s after every reservation expired, want 10", avail)
	}
}

// The run is the caller's: under RLS a principal scoped to one legal entity releases only
// that entity's due reservations; a tenant-wide principal (a scheduler) releases the rest.
func TestExpiryRunStaysInTheCallersLegalEntities(t *testing.T) {
	s, _, admin := setup(t)
	a, b := entityCtx(1, ids.New()), entityCtx(1, ids.New())
	product := ids.New()
	for _, ctx := range []context.Context{a, b} {
		post(t, ctx, s, domain.JournalOpeningBalance, line(product, "5", "1", dims()))
		if _, err := s.Reserve(ctx, store.ReserveInput{LegalEntityID: legalEntity(ctx), ProductID: product, Qty: dec("2"), DemandType: domain.DemandSalesOrder,
			DemandID: "SO-1", Policy: domain.PolicyManual, Dims: dims(), IdempotencyKey: ids.New(), TTLHours: 1}); err != nil {
			t.Fatal(err)
		}
	}
	expireAll(t, admin)
	out, err := s.ExpireReservations(a, 10)
	if err != nil || len(out.Released) != 1 || out.Released[0].LegalEntityID != legalEntity(a) || out.More {
		t.Fatalf("entity A run: %v %+v, want only A's reservation", err, out)
	}
	if avail, _ := availFor(t, s, b, product); !avail.Equal(dec("3")) {
		t.Fatalf("entity B avail %s, want 3 (its reservation untouched)", avail)
	}
	wide, err := s.ExpireReservations(tenantWideCtx(1), 10)
	if err != nil || len(wide.Released) != 1 || wide.Released[0].LegalEntityID != legalEntity(b) {
		t.Fatalf("tenant-wide run: %v %+v, want B's reservation", err, wide)
	}
	if other, err := s.ExpireReservations(tenantWideCtx(2), 10); err != nil || len(other.Released) != 0 {
		t.Fatalf("other tenant run: %v %+v", err, other)
	}
}

// ttlHours is part of the request an Idempotency-Key stands for: replaying it with other hours
// (or none) is a key reuse, not a silent replay of the first time to live.
func TestReservationReplayChecksTheTimeToLive(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "1", dims()))
	in := store.ReserveInput{LegalEntityID: legalEntity(ctx), ProductID: product, Qty: dec("2"), DemandType: domain.DemandSalesOrder,
		DemandID: "SO-1", Policy: domain.PolicyManual, Dims: dims(), IdempotencyKey: "ttl-replay", TTLHours: 24}
	if _, err := s.Reserve(ctx, in); err != nil {
		t.Fatal(err)
	}
	if again, err := s.Reserve(ctx, in); err != nil || !again.Replayed {
		t.Fatalf("same request: %v replayed %v", err, again.Replayed)
	}
	for _, ttl := range []int{0, 25} {
		in.TTLHours = ttl
		if _, err := s.Reserve(ctx, in); problemCode(err) != store.CodeIdempotencyReuse {
			t.Fatalf("ttl %d under the same key: %v, want %s", ttl, err, store.CodeIdempotencyReuse)
		}
	}
}
