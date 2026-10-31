package store

import (
	"context"
	"strings"
	"sync"
	"time"

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
}

// snapshot holds the records bucketed for the engine; resolves share it read-only.
type snapshot struct {
	set     *domain.RecordSet
	expires time.Time
}

// NewCache wraps s with a snapshot cache of the given TTL. A TTL of zero or less disables
// the cache: every resolve reads the store.
func NewCache(s Store, ttl time.Duration) *Cache {
	return &Cache{Store: s, ttl: ttl, now: time.Now, entries: map[string]snapshot{}, loading: map[string]*loadCall{}}
}

func cacheKey(ctx context.Context, q ActiveQuery) string {
	p, _ := tenant.FromContext(ctx)
	return strings.Join([]string{p.TenantID, p.LegalEntityID, q.LegalEntityID, q.Currency, q.CustomerGroupID, dayStr(q.AsOf)}, "|")
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
		c.entries[key] = snapshot{set: call.set, expires: now.Add(c.ttl)}
	}
	c.mu.Unlock()
	close(call.done)
	return call.set, err
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
