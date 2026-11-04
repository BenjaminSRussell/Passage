package store

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

// maxCacheEntries bounds the snapshot cache; it is emptied when full.
const maxCacheEntries = 1024

// Cache wraps a Store with the in-memory record snapshot of FR-26.14 (§5.26.4: "cache
// snapshot per (entity, pricing_date), 60 s TTL plus event invalidation"). A snapshot is
// the ACTIVE record set one resolve loads, keyed by tenant, the caller's legal-entity scope
// (RLS decides what a caller may read, so callers of different scopes never share a
// snapshot), legal entity, currency, customer group (campaign targeting) and pricing day.
// It lives for the TTL. Every change this process makes to the ACTIVE set (activate,
// withdraw, expiry, journal post and reverse, campaign kill and restore) empties the cache once the
// change has committed; other replicas see it within the TTL, which bounds staleness until
// the conditionrecord.activated consumer exists (NOT-IMPLEMENTED.md).
type Cache struct {
	Store
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	gen     uint64
	entries map[string]snapshot
	loading map[string]*loadCall

	// params holds the parameters the store served, per tenant: a legal entity's row (key
	// tenant|scope|legal entity) and the tenant's own row (key tenant). pgen counts the
	// invalidations, so a read that began before one is not kept.
	params map[string]paramEntry
	pgen   uint64
}

// paramTTL bounds how long a replica keeps parameters it read. A change made on this replica
// drops them at once, and one made on another replica is announced by parameters.changed
// (InvalidateParameters); without the event the bound is this lifetime, the shortest a
// snapshot may live (10 s, PRD 5.26.13 cache_ttl_seconds).
const paramTTL = 10 * time.Second

type paramEntry struct {
	tenantID string
	legal    Parameters
	tenant   TenantParameters
	expires  time.Time
}

// snapshot holds the records bucketed for the engine; resolves share it read-only.
type snapshot struct {
	set     *domain.RecordSet
	expires time.Time
}

// NewCache wraps s with a snapshot cache of the given TTL. A TTL of zero or less disables
// the cache: every resolve reads the store.
func NewCache(s Store, ttl time.Duration) *Cache {
	return &Cache{Store: s, ttl: ttl, now: time.Now, entries: map[string]snapshot{}, loading: map[string]*loadCall{},
		params: map[string]paramEntry{}}
}

// callerScope is the legal-entity scope row security applies to the caller: exactly the
// passage.legal_entity_scopes a store transaction sets (dbx.LegalEntityScopes). Keying on the raw
// claim would let two callers with no claim share a copy although only the one holding the
// all-scope duty may read every legal entity's rows ('*' against the empty list).
func callerScope(p tenant.Principal) string { return dbx.LegalEntityScopes(p) }

func cacheKey(ctx context.Context, q ActiveQuery) string {
	p, _ := tenant.FromContext(ctx)
	return strings.Join([]string{p.TenantID, callerScope(p), q.LegalEntityID, q.Currency, q.CustomerGroupID, dayStr(q.AsOf)}, "|")
}

// ActiveRecords serves a fresh snapshot and loads one otherwise. Callers get their own
// copy of the slice, so the engine cannot change a snapshot.
func (c *Cache) ActiveRecords(ctx context.Context, q ActiveQuery) ([]domain.Record, error) {
	if c.ttl <= 0 {
		return c.Store.ActiveRecords(ctx, q)
	}
	set, err := c.ActiveRecordSet(ctx, q)
	if err != nil {
		return nil, err
	}
	return set.Records(), nil
}

// ActiveRecordSet serves the snapshot as the shared, pre-bucketed domain.RecordSet itself:
// a hit neither copies nor re-indexes the price list, so a resolve costs what its own
// lines' accesses read (FR-26.14). The engine never changes a RecordSet. Concurrent misses
// of one key wait for a single load instead of each reading the whole price list (a cold
// or expired snapshot under load); a load that began before a flush is neither joined nor
// kept, since it may hold the old ACTIVE set. Without the cache (TTL zero) every call
// loads and buckets afresh.
func (c *Cache) ActiveRecordSet(ctx context.Context, q ActiveQuery) (*domain.RecordSet, error) {
	if c.ttl <= 0 {
		records, err := c.Store.ActiveRecords(ctx, q)
		if err != nil {
			return nil, err
		}
		return domain.NewRecordSet(records), nil
	}
	key := cacheKey(ctx, q)
	ttl := c.snapshotTTL(ctx)
	c.mu.Lock()
	now := c.now()
	if hit, ok := c.entries[key]; ok && now.Before(hit.expires) {
		c.mu.Unlock()
		return hit.set, nil
	}
	if call, ok := c.loading[key]; ok && call.gen == c.gen {
		c.mu.Unlock()
		select {
		case <-call.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if call.err == nil {
			return call.set, nil
		}
		// The shared load failed (perhaps its caller went away): load for this caller.
		records, err := c.Store.ActiveRecords(ctx, q)
		if err != nil {
			return nil, err
		}
		return domain.NewRecordSet(records), nil
	}
	call := &loadCall{gen: c.gen, done: make(chan struct{})}
	c.loading[key] = call
	c.mu.Unlock()

	records, err := c.Store.ActiveRecords(ctx, q)
	if err == nil {
		call.set = domain.NewRecordSet(records)
	}
	call.err = err
	c.mu.Lock()
	if c.loading[key] == call {
		delete(c.loading, key)
	}
	if err == nil && c.gen == call.gen {
		if len(c.entries) >= maxCacheEntries {
			c.entries = map[string]snapshot{}
		}
		c.entries[key] = snapshot{set: call.set, expires: now.Add(ttl)}
	}
	c.mu.Unlock()
	close(call.done)
	return call.set, err
}

// snapshotTTL is the snapshot lifetime of the caller's tenant: its cache_ttl_seconds when it set
// one, else the deployment's. A tenant whose parameters cannot be read gets the deployment's.
func (c *Cache) snapshotTTL(ctx context.Context) time.Duration {
	if tp, err := c.GetTenantParameters(ctx); err == nil && tp.CacheTTLSeconds > 0 {
		return time.Duration(tp.CacheTTLSeconds) * time.Second
	}
	return c.ttl
}

// loadCall is one snapshot load that concurrent misses of the same key wait for.
type loadCall struct {
	gen  uint64
	done chan struct{}
	set  *domain.RecordSet
	err  error
}

// Flush drops every snapshot (the runbook's cache rebuild: the next resolve reloads).
func (c *Cache) Flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.entries = map[string]snapshot{}
}

// ActivateRecord flushes after the store call: the record may have joined the ACTIVE set.
func (c *Cache) ActivateRecord(ctx context.Context, id string, version int, idempotencyKey string) (Entry, error) {
	defer c.Flush()
	return c.Store.ActivateRecord(ctx, id, version, idempotencyKey)
}

// WithdrawRecord flushes after the store call: the record may have left the ACTIVE set.
func (c *Cache) WithdrawRecord(ctx context.Context, id string, version int, reason, idempotencyKey string) (Entry, error) {
	defer c.Flush()
	return c.Store.WithdrawRecord(ctx, id, version, reason, idempotencyKey)
}

// ExpireRecords flushes after the store call: expired records left the ACTIVE set.
func (c *Cache) ExpireRecords(ctx context.Context, legalEntityID string, asOf time.Time) ([]Entry, error) {
	defer c.Flush()
	return c.Store.ExpireRecords(ctx, legalEntityID, asOf)
}

// PostJournal flushes after the store call: a posted journal activates and withdraws records.
func (c *Cache) PostJournal(ctx context.Context, id, idempotencyKey string) (Journal, error) {
	defer c.Flush()
	return c.Store.PostJournal(ctx, id, idempotencyKey)
}

// ReverseJournal flushes after the store call: a reversal restores the records it replaced.
func (c *Cache) ReverseJournal(ctx context.Context, id, idempotencyKey string) (Journal, error) {
	defer c.Flush()
	return c.Store.ReverseJournal(ctx, id, idempotencyKey)
}

// SetCampaignKilled flushes after the store call: kill and restore change which campaign
// records a resolve keeps (FR-26.10).
func (c *Cache) SetCampaignKilled(ctx context.Context, id string, version int, killed bool, idempotencyKey string) (domain.Campaign, error) {
	defer c.Flush()
	return c.Store.SetCampaignKilled(ctx, id, version, killed, idempotencyKey)
}

// ReviseCampaign flushes after the store call: validity and target group change which
// campaign records a resolve keeps (FR-26.10).
func (c *Cache) ReviseCampaign(ctx context.Context, rev domain.CampaignRevision, idempotencyKey string) (domain.Campaign, error) {
	defer c.Flush()
	return c.Store.ReviseCampaign(ctx, rev, idempotencyKey)
}

func paramKey(ctx context.Context, legalEntityID string) string {
	p, _ := tenant.FromContext(ctx)
	return strings.Join([]string{p.TenantID, callerScope(p), legalEntityID}, "|")
}

// GetParameters serves a legal entity's parameters from this replica's copy while it is fresh:
// a resolve reads them on every call (the date basis, the total discount basis, the
// threshold). Callers of different legal-entity scopes never share a copy, since row
// security decides what a caller reads.
func (c *Cache) GetParameters(ctx context.Context, legalEntityID string) (Parameters, error) {
	if c.ttl <= 0 {
		return c.Store.GetParameters(ctx, legalEntityID)
	}
	key := paramKey(ctx, legalEntityID)
	c.mu.Lock()
	if hit, ok := c.params[key]; ok && c.now().Before(hit.expires) {
		c.mu.Unlock()
		return hit.legal, nil
	}
	gen := c.pgen
	c.mu.Unlock()
	got, err := c.Store.GetParameters(ctx, legalEntityID)
	if err == nil {
		p, _ := tenant.FromContext(ctx)
		c.keepParams(key, gen, paramEntry{tenantID: p.TenantID, legal: got})
	}
	return got, err
}

// GetTenantParameters serves the tenant's parameters like GetParameters.
func (c *Cache) GetTenantParameters(ctx context.Context) (TenantParameters, error) {
	if c.ttl <= 0 {
		return c.Store.GetTenantParameters(ctx)
	}
	p, _ := tenant.FromContext(ctx)
	c.mu.Lock()
	if hit, ok := c.params[p.TenantID]; ok && c.now().Before(hit.expires) {
		c.mu.Unlock()
		return hit.tenant, nil
	}
	gen := c.pgen
	c.mu.Unlock()
	got, err := c.Store.GetTenantParameters(ctx)
	if err == nil {
		c.keepParams(p.TenantID, gen, paramEntry{tenantID: p.TenantID, tenant: got})
	}
	return got, err
}

func (c *Cache) keepParams(key string, gen uint64, e paramEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pgen != gen {
		return
	}
	if len(c.params) >= maxCacheEntries {
		c.params = map[string]paramEntry{}
	}
	e.expires = c.now().Add(paramTTL)
	c.params[key] = e
}

// InvalidateParameters drops the parameters this replica holds for a tenant (all its legal
// entities, and its own row). It is the handler of parameters.changed.
func (c *Cache) InvalidateParameters(tenantID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pgen++
	for k, e := range c.params {
		if e.tenantID == tenantID {
			delete(c.params, k)
		}
	}
}

// SetParameters drops the tenant's copies after the store call, whether or not it succeeded:
// a lost race may mean the stored version moved.
func (c *Cache) SetParameters(ctx context.Context, in Parameters, version int, idempotencyKey string) (Parameters, error) {
	p, _ := tenant.FromContext(ctx)
	defer c.InvalidateParameters(p.TenantID)
	return c.Store.SetParameters(ctx, in, version, idempotencyKey)
}

// SetTenantParameters drops the tenant's copies and its snapshots after the store call: a new
// snapshot lifetime applies to the snapshots loaded from now on.
func (c *Cache) SetTenantParameters(ctx context.Context, in TenantParameters, version int, idempotencyKey string) (TenantParameters, error) {
	p, _ := tenant.FromContext(ctx)
	defer func() {
		c.InvalidateParameters(p.TenantID)
		c.Flush()
	}()
	return c.Store.SetTenantParameters(ctx, in, version, idempotencyKey)
}
