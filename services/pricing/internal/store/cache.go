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
}

type snapshot struct {
	records []domain.Record
	expires time.Time
}

// NewCache wraps s with a snapshot cache of the given TTL. A TTL of zero or less disables
// the cache: every resolve reads the store.
func NewCache(s Store, ttl time.Duration) *Cache {
	return &Cache{Store: s, ttl: ttl, now: time.Now, entries: map[string]snapshot{}}
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
	key := cacheKey(ctx, q)
	c.mu.Lock()
	hit, ok := c.entries[key]
	gen := c.gen
	now := c.now()
	c.mu.Unlock()
	if ok && now.Before(hit.expires) {
		return append([]domain.Record(nil), hit.records...), nil
	}
	records, err := c.Store.ActiveRecords(ctx, q)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	// Keep the snapshot only when no change was flushed while it loaded: a load that began
	// before a commit may hold the old ACTIVE set.
	if c.gen == gen {
		if len(c.entries) >= maxCacheEntries {
			c.entries = map[string]snapshot{}
		}
		c.entries[key] = snapshot{records: append([]domain.Record(nil), records...), expires: now.Add(c.ttl)}
	}
	c.mu.Unlock()
	return records, nil
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
