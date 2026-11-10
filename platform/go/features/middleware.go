package features

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	CodeDisabled = "FEAT-0001"
	DefaultTTL   = 30 * time.Second
	maxTenants   = 10_000
)

type Source interface {
	Enabled(ctx context.Context, tenantID string) (map[string]bool, error)
}

type cacheEntry struct {
	state   map[string]bool
	expires time.Time
}

type Cache struct {
	source  Source
	ttl     time.Duration
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]cacheEntry
}

func NewCache(source Source, ttl time.Duration) *Cache {
	return &Cache{source: source, ttl: ttl, now: time.Now, entries: map[string]cacheEntry{}}
}

func (c *Cache) WithClock(now func() time.Time) *Cache {
	c.now = now
	return c
}

func (c *Cache) Enabled(ctx context.Context, tenantID string) (map[string]bool, error) {
	c.mu.Lock()
	entry, ok := c.entries[tenantID]
	c.mu.Unlock()
	if ok && c.now().Before(entry.expires) {
		return entry.state, nil
	}
	state, err := c.source.Enabled(ctx, tenantID)
	if err != nil {
		if ok {
			slog.WarnContext(ctx, "feature lookup failed; using the last known state", "err", err)
			return entry.state, nil
		}
		return nil, err
	}
	c.mu.Lock()
	if len(c.entries) >= maxTenants {
		c.entries = map[string]cacheEntry{}
	}
	c.entries[tenantID] = cacheEntry{state: state, expires: c.now().Add(c.ttl)}
	c.mu.Unlock()
	return state, nil
}

func Require(source Source, key string) func(http.Handler) http.Handler {
	name := key
	if f, ok := Lookup(key); ok {
		name = f.Name
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := tenant.FromContext(r.Context())
			if !ok || r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
				next.ServeHTTP(w, r)
				return
			}
			state, err := source.Enabled(r.Context(), p.TenantID)
			if err != nil {
				slog.WarnContext(r.Context(), "feature lookup failed; allowing the request", "feature", key, "err", err, "correlationId", middleware.GetReqID(r.Context()))
				next.ServeHTTP(w, r)
				return
			}
			if on, known := state[key]; known && !on {
				problem := httpx.NewProblem(http.StatusForbidden, CodeDisabled, name+" is turned off for your organisation. A tenant administrator can turn it on under Settings, Features.")
				problem.Errors = map[string]string{"feature": key}
				httpx.Fail(w, r, problem)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
