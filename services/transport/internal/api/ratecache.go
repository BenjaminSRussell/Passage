package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
)

// Rate-shop cache (FR-34.4 "results cached 10 min per input hash", parameter
// rating_cache_min, default 10, 0 to 60). Results are kept per tenant and input
// hash. Saving a rate card drops the tenant's entries on this replica; another
// replica can serve a result up to the window old, which is the staleness the
// PRD accepts.
const (
	defaultRatingCacheMin = 10
	maxRatingCacheMin     = 60
	rateCacheMaxEntries   = 10_000
	headerRateCache       = "X-Passage-Cache"
)

type cachedQuotes struct {
	quotes  []domain.Quote
	expires time.Time
}

type rateCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	size    int
	tenants map[string]map[string]cachedQuotes
}

func newRateCache(ttl time.Duration) *rateCache {
	return &rateCache{ttl: ttl, tenants: map[string]map[string]cachedQuotes{}}
}

// get returns the cached quotes if they are still inside the window.
func (c *rateCache) get(tenantID, key string, now time.Time) ([]domain.Quote, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries := c.tenants[tenantID]
	e, ok := entries[key]
	if !ok {
		return nil, false
	}
	if !now.Before(e.expires) {
		delete(entries, key)
		c.size--
		return nil, false
	}
	return e.quotes, true
}

// put stores quotes for the window. When the cache is full it drops expired
// entries and, if it is still full, empties itself, so memory stays bounded.
func (c *rateCache) put(tenantID, key string, quotes []domain.Quote, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ttl <= 0 {
		return
	}
	if c.size >= rateCacheMaxEntries {
		for t, entries := range c.tenants {
			for k, e := range entries {
				if !now.Before(e.expires) {
					delete(entries, k)
					c.size--
				}
			}
			if len(entries) == 0 {
				delete(c.tenants, t)
			}
		}
	}
	if c.size >= rateCacheMaxEntries {
		c.tenants, c.size = map[string]map[string]cachedQuotes{}, 0
	}
	entries := c.tenants[tenantID]
	if entries == nil {
		entries = map[string]cachedQuotes{}
		c.tenants[tenantID] = entries
	}
	if _, ok := entries[key]; !ok {
		c.size++
	}
	entries[key] = cachedQuotes{quotes: quotes, expires: now.Add(c.ttl)}
}

// invalidate drops every entry of a tenant, so a new rate card is used at once.
func (c *rateCache) invalidate(tenantID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.size -= len(c.tenants[tenantID])
	delete(c.tenants, tenantID)
}

// rateCacheKey hashes the rating input. The shipment id is left out, so the same
// input for another shipment shares the result.
func rateCacheKey(req rateRequest) (string, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func clampInt(v, lo, hi int) int {
	return min(max(v, lo), hi)
}
