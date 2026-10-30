package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

// countingStore answers ActiveRecords from a slice and counts the loads. The embedded nil
// Store panics on anything the cache should not reach.
type countingStore struct {
	Store
	loads   int
	records []domain.Record
	err     error
	// during runs inside a load, to simulate a write committing while it is in flight.
	during func()
}

func (s *countingStore) ActiveRecords(_ context.Context, _ ActiveQuery) ([]domain.Record, error) {
	s.loads++
	if s.during != nil {
		s.during()
	}
	return append([]domain.Record(nil), s.records...), s.err
}

func (s *countingStore) ActivateRecord(context.Context, string, int, string) (Entry, error) {
	return Entry{}, errors.New("conflict")
}

func (s *countingStore) SetCampaignKilled(context.Context, string, int, bool, string) (domain.Campaign, error) {
	return domain.Campaign{}, nil
}

func cacheCtx(tenantID, scope string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: scope, Duties: testkit.ScopeDuties(scope)})
}

func query(le, group string) ActiveQuery {
	return ActiveQuery{LegalEntityID: le, Currency: "USD", CustomerGroupID: group, AsOf: time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)}
}

func TestCache_ServesTheSnapshotUntilTheTTL(t *testing.T) {
	inner := &countingStore{records: []domain.Record{{ID: "r1"}}}
	c := NewCache(inner, time.Minute)
	clock := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return clock }
	ctx := cacheCtx("t1", "")

	first, _ := c.ActiveRecords(ctx, query("le1", ""))
	first[0].ID = "changed by the engine"
	second, err := c.ActiveRecords(ctx, query("le1", ""))
	if err != nil || inner.loads != 1 || second[0].ID != "r1" {
		t.Fatalf("second resolve = %v, %v after %d loads; want the unchanged snapshot from one load", second, err, inner.loads)
	}
	clock = clock.Add(time.Minute)
	if _, _ = c.ActiveRecords(ctx, query("le1", "")); inner.loads != 2 {
		t.Fatalf("loads after the TTL = %d; want a reload", inner.loads)
	}
}

func TestCache_KeysSeparateTenantsScopesEntitiesAndGroups(t *testing.T) {
	inner := &countingStore{}
	c := NewCache(inner, time.Minute)
	calls := []struct {
		ctx context.Context
		q   ActiveQuery
	}{
		{cacheCtx("t1", ""), query("le1", "")},
		{cacheCtx("t2", ""), query("le1", "")},
		// A caller scoped to one legal entity must not read a snapshot loaded under '*'.
		{cacheCtx("t1", "le9"), query("le1", "")},
		{cacheCtx("t1", ""), query("le2", "")},
		{cacheCtx("t1", ""), query("le1", "G1")},
	}
	for i, call := range calls {
		if _, err := c.ActiveRecords(call.ctx, call.q); err != nil || inner.loads != i+1 {
			t.Fatalf("call %d: loads = %d, %v; want a separate snapshot", i+1, inner.loads, err)
		}
	}
}

func TestCache_WritesFlushEvenWhenTheyFail(t *testing.T) {
	inner := &countingStore{}
	c := NewCache(inner, time.Minute)
	ctx := cacheCtx("t1", "")
	_, _ = c.ActiveRecords(ctx, query("le1", ""))
	if _, err := c.ActivateRecord(ctx, "r1", 1, ""); err == nil {
		t.Fatal("the store error must reach the caller")
	}
	_, _ = c.ActiveRecords(ctx, query("le1", ""))
	if _, err := c.SetCampaignKilled(ctx, "c1", 1, true, ""); err != nil {
		t.Fatal(err)
	}
	_, _ = c.ActiveRecords(ctx, query("le1", ""))
	if inner.loads != 3 {
		t.Fatalf("loads = %d; want a reload after each write", inner.loads)
	}
}

func TestCache_DoesNotKeepALoadThatRacedAFlush(t *testing.T) {
	inner := &countingStore{}
	c := NewCache(inner, time.Minute)
	inner.during = func() { inner.during = nil; c.Flush() }
	ctx := cacheCtx("t1", "")
	_, _ = c.ActiveRecords(ctx, query("le1", ""))
	_, _ = c.ActiveRecords(ctx, query("le1", ""))
	if inner.loads != 2 {
		t.Fatalf("loads = %d; a snapshot loaded across a flush must not be served", inner.loads)
	}
}

func TestCache_ZeroTTLAndErrorsAreNotCached(t *testing.T) {
	inner := &countingStore{err: errors.New("db down")}
	c := NewCache(inner, time.Minute)
	ctx := cacheCtx("t1", "")
	for range 2 {
		if _, err := c.ActiveRecords(ctx, query("le1", "")); err == nil {
			t.Fatal("the load error must reach the caller")
		}
	}
	if inner.loads != 2 {
		t.Fatalf("loads = %d; a failed load must not be cached", inner.loads)
	}
	off := &countingStore{}
	disabled := NewCache(off, 0)
	_, _ = disabled.ActiveRecords(ctx, query("le1", ""))
	_, _ = disabled.ActiveRecords(ctx, query("le1", ""))
	if off.loads != 2 {
		t.Fatalf("loads with TTL 0 = %d; want every resolve to read the store", off.loads)
	}
}
