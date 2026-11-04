package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
)

func TestCache_TenantTTLReplacesTheDeploymentTTL(t *testing.T) {
	inner := &countingStore{records: []domain.Record{{ID: "r1"}}, tenantParams: TenantParameters{CacheTTLSeconds: 20}}
	c := NewCache(inner, time.Minute)
	clock := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return clock }
	ctx := cacheCtx("t1", "")
	_, _ = c.ActiveRecords(ctx, query("le1", ""))
	clock = clock.Add(19 * time.Second)
	_, _ = c.ActiveRecords(ctx, query("le1", ""))
	if inner.loads != 1 {
		t.Fatalf("loads inside the tenant TTL of 20 s = %d; want 1", inner.loads)
	}
	clock = clock.Add(2 * time.Second)
	_, _ = c.ActiveRecords(ctx, query("le1", ""))
	if inner.loads != 2 {
		t.Fatalf("loads after the tenant TTL = %d; want a reload at 21 s although the deployment's is 60 s", inner.loads)
	}
	// Another tenant, with nothing set, follows the deployment.
	inner.tenantParams = TenantParameters{}
	c.InvalidateParameters("t2")
	other := cacheCtx("t2", "")
	before := inner.loads
	_, _ = c.ActiveRecords(other, query("le1", ""))
	clock = clock.Add(30 * time.Second)
	_, _ = c.ActiveRecords(other, query("le1", ""))
	if inner.loads != before+1 {
		t.Fatalf("loads of a tenant on the deployment TTL = %d; want 1 within 30 s", inner.loads-before)
	}
}

func TestCache_ParametersAreServedUntilInvalidated(t *testing.T) {
	inner := &countingStore{}
	c := NewCache(inner, time.Minute)
	clock := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return clock }
	ctx := cacheCtx("t1", "")
	for i := 0; i < 3; i++ {
		if _, err := c.GetParameters(ctx, "le1"); err != nil {
			t.Fatal(err)
		}
	}
	if inner.legalReads != 1 {
		t.Fatalf("reads = %d; want 1 for three resolves", inner.legalReads)
	}
	// A caller of another scope, or another legal entity, reads for itself.
	_, _ = c.GetParameters(cacheCtx("t1", "le9"), "le1")
	_, _ = c.GetParameters(ctx, "le2")
	if inner.legalReads != 3 {
		t.Fatalf("reads = %d; want separate copies per scope and entity", inner.legalReads)
	}
	// The event of another replica's change drops the tenant's copies; another tenant keeps its own.
	_, _ = c.GetParameters(cacheCtx("t2", ""), "le1")
	c.InvalidateParameters("t1")
	_, _ = c.GetParameters(ctx, "le1")
	_, _ = c.GetParameters(cacheCtx("t2", ""), "le1")
	if inner.legalReads != 5 {
		t.Fatalf("reads = %d; want t1 re-read and t2 kept", inner.legalReads)
	}
	// This replica's own write drops them, and so does the lifetime bound.
	if _, err := c.SetParameters(ctx, Parameters{LegalEntityID: "le1"}, 0, ""); err != nil {
		t.Fatal(err)
	}
	_, _ = c.GetParameters(ctx, "le1")
	if inner.legalReads != 6 {
		t.Fatalf("reads after a write = %d; want a re-read", inner.legalReads)
	}
	clock = clock.Add(paramTTL + time.Second)
	_, _ = c.GetParameters(ctx, "le1")
	if inner.legalReads != 7 {
		t.Fatalf("reads after the lifetime = %d; want a re-read", inner.legalReads)
	}
}

func TestCache_ParameterCacheOffWithTheSnapshotCache(t *testing.T) {
	inner := &countingStore{}
	c := NewCache(inner, 0)
	ctx := cacheCtx("t1", "")
	_, _ = c.GetParameters(ctx, "le1")
	_, _ = c.GetParameters(ctx, "le1")
	_, _ = c.GetTenantParameters(ctx)
	_, _ = c.GetTenantParameters(ctx)
	if inner.legalReads != 2 || inner.tenantReads != 2 {
		t.Fatalf("reads = %d and %d; want every call to reach the store with the cache off", inner.legalReads, inner.tenantReads)
	}
}

func TestCache_TenantParametersWriteFlushesSnapshots(t *testing.T) {
	inner := &countingStore{}
	c := NewCache(inner, time.Minute)
	ctx := cacheCtx("t1", "")
	_, _ = c.ActiveRecords(ctx, query("le1", ""))
	if _, err := c.SetTenantParameters(ctx, TenantParameters{CacheTTLSeconds: 15}, 0, ""); err != nil {
		t.Fatal(err)
	}
	_, _ = c.ActiveRecords(ctx, query("le1", ""))
	if inner.loads != 2 {
		t.Fatalf("loads = %d; want a reload after the tenant parameters changed", inner.loads)
	}
	if got, _ := c.GetTenantParameters(ctx); got.CacheTTLSeconds != 15 {
		t.Fatalf("tenant parameters = %+v; want the stored value, not a stale copy", got)
	}
}

// consumerCtx is the context the platform consumer hands a handler: the pricing-cache service
// principal of the event's tenant, holding only its flag duty.
func consumerCtx(ctx context.Context) context.Context {
	p, _ := tenant.FromContext(ctx)
	return tenant.With(context.Background(), tenant.Principal{TenantID: p.TenantID, UserID: tenant.ServiceActor(CacheConsumerName),
		Duties: map[string]struct{}{DutyCacheInvalidate: {}}})
}

func TestCache_CallersWithNoLegalEntityClaimShareACopyOnlyUnderTheSameRowScope(t *testing.T) {
	inner := &countingStore{}
	c := NewCache(inner, time.Minute)
	allScope := cacheCtx("t1", "")                                                 // no claim, holds the all-scope duty: row security '*'
	noScope := tenant.With(context.Background(), tenant.Principal{TenantID: "t1"}) // no claim, no duty: sees no legal entity
	_, _ = c.GetParameters(allScope, "le1")
	_, _ = c.GetParameters(noScope, "le1")
	_, _ = c.ActiveRecords(allScope, query("le1", ""))
	_, _ = c.ActiveRecords(noScope, query("le1", ""))
	if inner.legalReads != 2 || inner.loads != 2 {
		t.Fatalf("reads = %d, loads = %d; a caller row security restricts must never be served the all-scope caller's copy", inner.legalReads, inner.loads)
	}
	// Two spellings of one claim are one row scope and share.
	le := "6f1c1a7e-3c1d-4f3a-9b52-0d3c4a1b2c3d"
	_, _ = c.GetParameters(cacheCtx("t1", le), le)
	_, _ = c.GetParameters(cacheCtx("t1", " "+le), le)
	if inner.legalReads != 3 {
		t.Fatalf("reads = %d; want one read for one row scope", inner.legalReads)
	}
}

func TestInvalidationHandlers_RefuseAPrincipalWithoutTheFlagDuty(t *testing.T) {
	c := NewCache(&countingStore{}, time.Minute)
	h := c.InvalidationHandlers(time.Time{})[pevents.ParametersChanged]
	err := h.Handle(cacheCtx("t1", ""), nil, events.Event{Type: pevents.ParametersChanged, TenantID: "t1"})
	if !eventing.IsPermanent(err) {
		t.Fatalf("err = %v; want a permanent refusal for a principal without %s", err, DutyCacheInvalidate)
	}
}

func TestInvalidationHandlers_SkipEventsFromBeforeTheReplicaStarted(t *testing.T) {
	inner := &countingStore{tenantParams: TenantParameters{CampaignKillSwitchEffect: domain.KillEffectFlush}}
	c := NewCache(inner, time.Minute)
	ctx := cacheCtx("t1", "")
	started := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	handlers := c.InvalidationHandlers(started)
	_, _ = c.ActiveRecords(ctx, query("le1", ""))
	// A kill from last week (a new group reads its topics from the start) is skipped.
	old := events.Event{Type: pevents.CampaignKilled, TenantID: "t1", Time: started.Add(-7 * 24 * time.Hour)}
	if err := handlers[pevents.CampaignKilled].Handle(consumerCtx(ctx), nil, old); err != nil {
		t.Fatal(err)
	}
	_, _ = c.ActiveRecords(ctx, query("le1", ""))
	if inner.loads != 1 || inner.tenantReads != 1 {
		t.Fatalf("loads = %d, tenant reads = %d; want an old event neither read nor flushed", inner.loads, inner.tenantReads)
	}
	// One stamped within the clock skew margin of the start is applied.
	recent := events.Event{Type: pevents.CampaignKilled, TenantID: "t1", Time: started.Add(-10 * time.Second)}
	if err := handlers[pevents.CampaignKilled].Handle(consumerCtx(ctx), nil, recent); err != nil {
		t.Fatal(err)
	}
	_, _ = c.ActiveRecords(ctx, query("le1", ""))
	if inner.loads != 2 {
		t.Fatalf("loads = %d; want the recent kill to flush", inner.loads)
	}
}

func TestInvalidationHandlers_KillFlushesWhenTheEffectCannotBeRead(t *testing.T) {
	inner := &countingStore{tenantErr: errors.New("database down")}
	c := NewCache(inner, time.Minute)
	ctx := cacheCtx("t1", "")
	_, _ = c.ActiveRecords(ctx, query("le1", ""))
	handle(t, c, ctx, pevents.CampaignKilled)
	_, _ = c.ActiveRecords(ctx, query("le1", ""))
	if inner.loads != 2 {
		t.Fatalf("loads = %d; want a flush (always safe) instead of a retried, dead-lettered event", inner.loads)
	}
}

func handle(t *testing.T, c *Cache, ctx context.Context, typ string) {
	t.Helper()
	h, ok := c.InvalidationHandlers(time.Time{})[typ]
	if !ok {
		t.Fatalf("no handler for %s", typ)
	}
	if err := h.Handle(consumerCtx(ctx), nil, events.Event{Type: typ, TenantID: "t1"}); err != nil {
		t.Fatalf("%s: %v", typ, err)
	}
}

func TestInvalidationHandlers_KillSwitchEffect(t *testing.T) {
	inner := &countingStore{}
	c := NewCache(inner, time.Minute)
	ctx := cacheCtx("t1", "")
	warm := func() int {
		_, _ = c.ActiveRecords(ctx, query("le1", ""))
		return inner.loads
	}
	warm()
	// NEXT_RESOLVE: another replica's kill does not flush this one.
	handle(t, c, ctx, pevents.CampaignKilled)
	if got := warm(); got != 1 {
		t.Fatalf("loads after campaign.killed under NEXT_RESOLVE = %d; want the snapshot kept", got)
	}
	// IMMEDIATE_CACHE_FLUSH: both events flush.
	inner.tenantParams = TenantParameters{CampaignKillSwitchEffect: domain.KillEffectFlush}
	handle(t, c, ctx, pevents.TenantParametersChanged) // drops the copy of the effect and the snapshots
	warm()
	before := inner.loads
	handle(t, c, ctx, pevents.CampaignKilled)
	if got := warm(); got != before+1 {
		t.Fatalf("loads after campaign.killed under IMMEDIATE_CACHE_FLUSH = %d; want a reload", got)
	}
	before = inner.loads
	handle(t, c, ctx, pevents.CampaignRestored)
	if got := warm(); got != before+1 {
		t.Fatalf("loads after campaign.restored under IMMEDIATE_CACHE_FLUSH = %d; want a reload", got)
	}
}

func TestInvalidationHandlers_ParametersChangedDropsTheTenantCopies(t *testing.T) {
	inner := &countingStore{}
	c := NewCache(inner, time.Minute)
	ctx := cacheCtx("t1", "")
	_, _ = c.GetParameters(ctx, "le1")
	handle(t, c, ctx, pevents.ParametersChanged)
	_, _ = c.GetParameters(ctx, "le1")
	if inner.legalReads != 2 {
		t.Fatalf("reads = %d; want a re-read after parameters.changed", inner.legalReads)
	}
	// parameters.changed keeps the snapshots.
	_, _ = c.ActiveRecords(ctx, query("le1", ""))
	handle(t, c, ctx, pevents.ParametersChanged)
	_, _ = c.ActiveRecords(ctx, query("le1", ""))
	if inner.loads != 1 {
		t.Fatalf("loads = %d; want the snapshot kept", inner.loads)
	}
}

func TestMemoryInbox_AppliesEveryEvent(t *testing.T) {
	var in MemoryInbox
	n := 0
	for i := 0; i < 2; i++ {
		applied, err := in.Once(context.Background(), CacheConsumerName, "e1", func(pgx.Tx) error { n++; return nil })
		if err != nil || !applied {
			t.Fatalf("once = %v, %v", applied, err)
		}
	}
	if seen, _ := in.Seen(context.Background(), CacheConsumerName, "e1"); seen || n != 2 {
		t.Fatalf("seen = %v after %d applications; want every replica to apply every event", seen, n)
	}
}
