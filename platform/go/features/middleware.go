package features

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	CodeDisabled   = "FEAT-0001"
	DefaultTTL     = 30 * time.Second
	DefaultBackoff = 5 * time.Second
	MaxTenants     = 10_000
)

type Source interface {
	Enabled(ctx context.Context, tenantID string) (map[string]bool, error)
}

type cacheEntry struct {
	state   map[string]bool
	err     error
	expires time.Time
}

type lookup struct {
	done  chan struct{}
	state map[string]bool
	err   error
}

type Cache struct {
	source   Source
	ttl      time.Duration
	backoff  time.Duration
	now      func() time.Time
	mu       sync.Mutex
	entries  map[string]cacheEntry
	inflight map[string]*lookup
}

func NewCache(source Source, ttl time.Duration) *Cache {
	return &Cache{source: source, ttl: ttl, backoff: DefaultBackoff, now: time.Now, entries: map[string]cacheEntry{}, inflight: map[string]*lookup{}}
}

func (c *Cache) WithClock(now func() time.Time) *Cache {
	c.now = now
	return c
}

func (c *Cache) WithBackoff(d time.Duration) *Cache {
	c.backoff = d
	return c
}

func (c *Cache) Enabled(ctx context.Context, tenantID string) (state map[string]bool, err error) {
	c.mu.Lock()
	entry, cached := c.entries[tenantID]
	if cached && c.now().Before(entry.expires) {
		c.mu.Unlock()
		return entry.state, entry.err
	}
	if l, running := c.inflight[tenantID]; running {
		c.mu.Unlock()
		select {
		case <-l.done:
			return l.state, l.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	l := &lookup{done: make(chan struct{})}
	c.inflight[tenantID] = l
	c.mu.Unlock()

	// The leader finishes the lookup in a defer: if the source panics, the waiters still wake (with an error, which the
	// gate treats as a failed lookup and lets the request through) and the in-flight marker is dropped, so a later request
	// looks again instead of waiting forever on a lookup that will never end.
	err = errLookupPanicked
	defer func() {
		c.mu.Lock()
		switch {
		case err == nil:
			c.store(tenantID, cacheEntry{state: state, expires: c.now().Add(c.ttl)})
		case cached && entry.state != nil:
			slog.WarnContext(ctx, "feature lookup failed; using the last known state", "err", err)
			state, err = entry.state, nil
			c.store(tenantID, cacheEntry{state: state, expires: c.now().Add(c.backoff)})
		default:
			c.store(tenantID, cacheEntry{err: err, expires: c.now().Add(c.backoff)})
		}
		delete(c.inflight, tenantID)
		l.state, l.err = state, err
		c.mu.Unlock()
		close(l.done)
	}()
	state, err = c.source.Enabled(context.WithoutCancel(ctx), tenantID)
	return state, err
}

var errLookupPanicked = errors.New("feature lookup panicked")

func (c *Cache) store(tenantID string, e cacheEntry) {
	if _, present := c.entries[tenantID]; !present && len(c.entries) >= MaxTenants {
		oldest, first := "", true
		for k, v := range c.entries {
			if first || v.expires.Before(c.entries[oldest].expires) {
				oldest, first = k, false
			}
		}
		delete(c.entries, oldest)
	}
	c.entries[tenantID] = e
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
