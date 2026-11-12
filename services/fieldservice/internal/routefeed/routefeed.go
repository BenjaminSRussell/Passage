// Package routefeed pushes "your route changed" to technicians' devices (#45): the store raises a Postgres NOTIFY in the same
// transaction as an assignment, publication or checklist change, every pod LISTENs, and the pod that holds a technician's
// open stream (SSE) wakes it. The device then reads its day again instead of polling every minute. A notification carries no
// data of the route, only whose it is, so nothing here needs the tenant's row-level security.
package routefeed

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Channel is the Postgres notification channel the store notifies on.
const Channel = "fieldservice_route"

// Payload is what the store sends: the tenant and the technician whose route changed.
func Payload(tenantID, technicianID string) string {
	return strings.ToLower(tenantID) + "|" + strings.ToLower(technicianID)
}

// Feed fans notifications out to the open streams of this pod.
type Feed struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

func New() *Feed { return &Feed{subs: map[string]map[chan struct{}]struct{}{}} }

// Subscribe returns a channel that receives a signal when the technician's route changed (signals coalesce: a slow reader gets
// one, not a backlog) and the function that ends the subscription.
func (f *Feed) Subscribe(tenantID, technicianID string) (<-chan struct{}, func()) {
	key := Payload(tenantID, technicianID)
	ch := make(chan struct{}, 1)
	f.mu.Lock()
	if f.subs[key] == nil {
		f.subs[key] = map[chan struct{}]struct{}{}
	}
	f.subs[key][ch] = struct{}{}
	f.mu.Unlock()
	return ch, func() {
		f.mu.Lock()
		delete(f.subs[key], ch)
		if len(f.subs[key]) == 0 {
			delete(f.subs, key)
		}
		f.mu.Unlock()
	}
}

// Publish wakes the open streams for a payload made by Payload.
func (f *Feed) Publish(payload string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for ch := range f.subs[strings.ToLower(payload)] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Subscribers counts the open streams, for tests and metrics.
func (f *Feed) Subscribers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.subs {
		n += len(s)
	}
	return n
}

const (
	reconnectMin = time.Second
	reconnectMax = 30 * time.Second
)

// Run listens on Channel until ctx ends and publishes what it hears. A lost connection is replaced with a backoff of one second
// doubling to thirty; devices that missed a notification meanwhile catch up with their slow poll.
func (f *Feed) Run(ctx context.Context, pool *pgxpool.Pool) {
	wait := reconnectMin
	for ctx.Err() == nil {
		started := time.Now()
		err := f.listen(ctx, pool)
		if ctx.Err() != nil {
			return
		}
		slog.WarnContext(ctx, "route feed listener stopped; reconnecting", "err", err, "in", wait)
		if time.Since(started) > reconnectMax {
			wait = reconnectMin
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait *= 2; wait > reconnectMax {
			wait = reconnectMax
		}
	}
}

func (f *Feed) listen(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "listen "+Channel); err != nil {
		return err
	}
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			// The connection may be broken: close it instead of returning it to the pool with a LISTEN on it.
			_ = conn.Conn().Close(context.WithoutCancel(ctx))
			return err
		}
		f.Publish(n.Payload)
	}
}
