package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
)

// CacheConsumerName is the consumer every replica runs to keep its caches in step with the other
// replicas. It acts as the service principal of that name, whose only duty is the flag
// DutyCacheInvalidate: its handlers touch this process's memory and read only the tenant's own
// parameters row, never a route.
const CacheConsumerName = "pricing-cache"

// DutyCacheInvalidate is the flag duty of the pricing-cache service principal (no route).
const DutyCacheInvalidate = "pricing.cache.invalidate"

// CacheTopics are the topics the consumer follows (outbox aggregates of the pricing relay).
var CacheTopics = []string{"passage.pricing.campaign", "passage.pricing.parameters"}

// staleEventMargin is how far before a replica's start an event may be stamped and still be
// applied: the clock skew allowed between the producing replica and this one. Anything older
// happened before this replica's caches held anything, so applying it would only flush.
const staleEventMargin = time.Minute

// InvalidationHandlers are the handlers of the replica's cache consumer (FR-26.14 event
// invalidation, FR-26.10 kill switch):
//
//   - campaign.killed and campaign.restored empty the snapshots when the tenant's
//     campaign_kill_switch_effect is IMMEDIATE_CACHE_FLUSH (or cannot be read: flushing is
//     always safe); with NEXT_RESOLVE the replica that took the kill has flushed and the others
//     wait for their snapshot lifetime.
//   - parameters.changed drops the tenant's cached parameters.
//   - tenantparameter.changed drops them too and empties the snapshots, since the lifetime or
//     the kill switch effect may have changed.
//
// started is when this replica's caches began; an event stamped more than staleEventMargin
// before it is skipped, so a new replica whose group reads its topics from the start does not
// replay a storm of flushes. A handler never fails on a cache matter, so nothing reaches the
// dead-letter topic but a principal without DutyCacheInvalidate. Every handler is idempotent
// (the consumer runs with an in-memory inbox).
func (c *Cache) InvalidationHandlers(started time.Time) map[string]eventing.Handler {
	guard := func(apply func(ctx context.Context, evt events.Event)) eventing.Handler {
		return eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			if actor, ok := tenant.FromContext(ctx); !ok || !actor.HasDuty("pricing.cache.invalidate") {
				return eventing.Permanent(errors.New("cache consumer principal lacks " + DutyCacheInvalidate))
			}
			if !evt.Time.IsZero() && evt.Time.Before(started.Add(-staleEventMargin)) {
				return nil
			}
			apply(ctx, evt)
			return nil
		})
	}
	flushOnKill := guard(func(ctx context.Context, _ events.Event) {
		if tp, err := c.GetTenantParameters(ctx); err != nil || tp.CampaignKillSwitchEffect == domain.KillEffectFlush {
			c.Flush()
		}
	})
	return map[string]eventing.Handler{
		pevents.CampaignKilled:   flushOnKill,
		pevents.CampaignRestored: flushOnKill,
		pevents.ParametersChanged: guard(func(_ context.Context, evt events.Event) {
			c.InvalidateParameters(evt.TenantID)
		}),
		pevents.TenantParametersChanged: guard(func(_ context.Context, evt events.Event) {
			c.InvalidateParameters(evt.TenantID)
			c.Flush()
		}),
	}
}

// MemoryInbox is the consumer inbox of an invalidation consumer: it remembers nothing, so every
// replica applies every event it reads (an invalidation is idempotent, and a database inbox
// would let only one replica apply it).
type MemoryInbox struct{}

// Once runs the effect; the tx is nil.
func (MemoryInbox) Once(_ context.Context, _, _ string, effect func(pgx.Tx) error) (bool, error) {
	return true, effect(nil)
}

// Seen reports nothing as seen.
func (MemoryInbox) Seen(context.Context, string, string) (bool, error) { return false, nil }

// Record records nothing.
func (MemoryInbox) Record(context.Context, string, string) error { return nil }
