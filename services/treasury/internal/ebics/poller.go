package ebics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	pollActor            = "system:treasury-ebics-poll"
	defaultPollInterval  = time.Hour
	minPollInterval      = time.Minute
	pollSubscriberBudget = 10 * time.Minute

	maxLocalFailures = 5
	CodeLocalFailure = "LOCAL-PROCESSING"
)

// PollableLister lists the subscribers of the principal's tenant that may be
// polled: READY, bank keys confirmed and not parked. TryLockSubscriber takes the
// cross-replica poll lock of one subscriber without waiting: unlock releases it,
// and acquired is false when another replica holds it.
type PollableLister interface {
	PollableSubscribers(ctx context.Context) ([]Subscriber, error)
	TryLockSubscriber(ctx context.Context, id string) (unlock func(), acquired bool, err error)
}

// PollConfig is the statement polling setup read from the environment.
type PollConfig struct {
	Tenants   []string
	Interval  time.Duration
	OrderType string
}

// LoadPollConfig reads TREASURY_EBICS_POLL_WORKERS (polling runs only when it is
// "true"), TREASURY_EBICS_POLL_TENANT_IDS (comma separated tenant UUIDs),
// TREASURY_EBICS_POLL_INTERVAL (default 1h, at least 1m), TREASURY_EBICS_POLL_ORDER
// (C53 or STA, default C53). It returns false when polling is off.
func LoadPollConfig(getenv func(string) string) (PollConfig, bool, error) {
	if getenv("TREASURY_EBICS_POLL_WORKERS") != "true" {
		return PollConfig{}, false, nil
	}
	cfg := PollConfig{Interval: defaultPollInterval, OrderType: "C53"}
	seen := map[string]bool{}
	for _, part := range strings.Split(getenv("TREASURY_EBICS_POLL_TENANT_IDS"), ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		id, ok := ids.Canonical(strings.TrimSpace(part))
		if !ok || id == ids.Nil {
			return PollConfig{}, false, fmt.Errorf("TREASURY_EBICS_POLL_TENANT_IDS entry %q is not a UUID", part)
		}
		if !seen[id] {
			seen[id] = true
			cfg.Tenants = append(cfg.Tenants, id)
		}
	}
	if len(cfg.Tenants) == 0 {
		return PollConfig{}, false, errors.New("TREASURY_EBICS_POLL_WORKERS needs TREASURY_EBICS_POLL_TENANT_IDS")
	}
	if v := getenv("TREASURY_EBICS_POLL_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < minPollInterval {
			return PollConfig{}, false, errors.New("TREASURY_EBICS_POLL_INTERVAL must be a duration of at least 1m, got " + v)
		}
		cfg.Interval = d
	}
	if v := getenv("TREASURY_EBICS_POLL_ORDER"); v != "" {
		if v != "C53" && v != "STA" {
			return PollConfig{}, false, errors.New("TREASURY_EBICS_POLL_ORDER must be C53 or STA, got " + v)
		}
		cfg.OrderType = v
	}
	return cfg, true, nil
}

// Poller downloads end-of-day statements for every pollable subscriber on an
// interval and hands them to the statement import. It asks without a date range,
// so the bank delivers everything it has not yet had a receipt for, however long
// a subscriber was parked or the service was down, for as long as the bank keeps
// the data (its retention applies); a fixed window would skip the days before it.
// Client.FetchStatements with a range is the explicit backfill. A subscriber the
// bank parked is not polled until a person unparks it; a back-off answer is
// retried by the client with growing delays and, when the retries run out, again
// at the next tick. Files are imported one by one and the bank is acknowledged
// only after all of them, so a delivery that fails here every time would be
// fetched again at every tick: after maxLocalFailures consecutive ticks that
// failed in processing (not at the bank), the subscriber is parked with
// CodeLocalFailure so a person looks. The count is kept in memory per replica.
type Poller struct {
	Client *Client
	Lister PollableLister
	Sink   StatementSink
	Config PollConfig

	mu       sync.Mutex
	failures map[string]int
}

// Loop polls once at start and then every Config.Interval until ctx ends.
func (p *Poller) Loop(ctx context.Context) {
	p.tick(ctx)
	ticker := time.NewTicker(p.Config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.tick(ctx)
		}
	}
}

func (p *Poller) tick(ctx context.Context) {
	files, err := p.Once(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "ebics statement poll finished with errors", "err", err, "files", files)
		return
	}
	if files > 0 {
		slog.InfoContext(ctx, "ebics statement poll finished", "files", files)
	}
}

// Once polls every pollable subscriber of every configured tenant. It returns
// how many statement files the import took and every failure joined.
func (p *Poller) Once(ctx context.Context) (int, error) {
	total := 0
	var errs []error
	for _, tenantID := range p.Config.Tenants {
		listCtx := tenant.With(ctx, tenant.Principal{TenantID: tenantID, UserID: pollActor, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
		subs, err := p.Lister.PollableSubscribers(listCtx)
		if err != nil {
			errs = append(errs, fmt.Errorf("tenant %s: list subscribers: %w", tenantID, err))
			continue
		}
		for _, sub := range subs {
			if ctx.Err() != nil {
				return total, errors.Join(append(errs, ctx.Err())...)
			}
			n, err := p.pollOne(ctx, tenantID, sub)
			total += n
			if err != nil {
				errs = append(errs, fmt.Errorf("subscriber %s: %w", sub.ID, err))
			}
		}
	}
	return total, errors.Join(errs...)
}

func (p *Poller) pollOne(ctx context.Context, tenantID string, sub Subscriber) (int, error) {
	unlock, acquired, err := p.Lister.TryLockSubscriber(ctx, sub.ID)
	if err != nil {
		return 0, fmt.Errorf("lock subscriber: %w", err)
	}
	if !acquired {
		slog.InfoContext(ctx, "ebics subscriber is being polled by another replica, skipped", "subscriber", sub.ID)
		return 0, nil
	}
	defer unlock()
	runCtx, cancel := context.WithTimeout(tenant.With(ctx, tenant.Principal{TenantID: tenantID, LegalEntityID: sub.LegalEntityID, UserID: pollActor}), pollSubscriberBudget)
	defer cancel()
	n, err := p.Client.FetchStatements(runCtx, sub.ID, p.Config.OrderType, "", "", p.Sink)
	switch {
	case err == nil || errors.Is(err, ErrNoData):
		p.clearFailures(sub.ID)
		return n, nil
	case errors.Is(err, ErrParked):
		slog.WarnContext(ctx, "ebics subscriber is parked, not polled", "subscriber", sub.ID)
		return n, nil
	case errors.Is(err, ErrLocal):
		if count := p.recordFailure(sub.ID); count >= maxLocalFailures {
			slog.ErrorContext(ctx, "ebics subscriber parked", "subscriber", sub.ID, "code", CodeLocalFailure, "failures", count)
			text := fmt.Sprintf("%d statement polls in a row failed in processing: %v", count, err)
			err = errors.Join(err, p.Client.park(ctx, sub.ID, &ReturnError{Code: CodeLocalFailure, Text: text, Disposition: Park}))
			p.clearFailures(sub.ID)
		}
	}
	return n, err
}

func (p *Poller) recordFailure(id string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failures == nil {
		p.failures = map[string]int{}
	}
	p.failures[id]++
	return p.failures[id]
}

func (p *Poller) clearFailures(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.failures, id)
}
