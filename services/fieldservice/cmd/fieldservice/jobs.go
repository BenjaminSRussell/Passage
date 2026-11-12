package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	jobMinInterval = time.Minute
	jobTimeout     = 10 * time.Minute
)

// schedule says when a job runs: every interval, or once a day at a UTC time of day.
type schedule struct {
	interval time.Duration
	// at is minutes after midnight UTC of a daily run; negative means tick by interval.
	at  int
	now func() time.Time
}

// parseSchedule reads <prefix>_INTERVAL and <prefix>_AT (HH:MM, UTC). Setting both is an error; neither takes the default interval.
func parseSchedule(getenv func(string) string, prefix string, defaultInterval time.Duration) (schedule, error) {
	s := schedule{interval: defaultInterval, at: -1, now: time.Now}
	if v := getenv(prefix + "_AT"); v != "" {
		if getenv(prefix+"_INTERVAL") != "" {
			return s, fmt.Errorf("set %s_AT or %s_INTERVAL, not both", prefix, prefix)
		}
		t, err := time.Parse("15:04", v)
		if err != nil {
			return s, fmt.Errorf("%s_AT must be a UTC time of day as HH:MM, got %s", prefix, v)
		}
		s.at = t.Hour()*60 + t.Minute()
	}
	if v := getenv(prefix + "_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < jobMinInterval {
			return s, fmt.Errorf("%s_INTERVAL must be a duration of at least 1m, got %s", prefix, v)
		}
		s.interval = d
	}
	return s, nil
}

// nextDaily is the next moment at minutes after midnight UTC that is after now.
func nextDaily(now time.Time, at int) time.Time {
	now = now.UTC()
	next := time.Date(now.Year(), now.Month(), now.Day(), at/60, at%60, 0, 0, time.UTC)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// run calls fn on the schedule until ctx ends: at start and then every interval, or only at the daily time.
func (s schedule) run(ctx context.Context, fn func(context.Context)) {
	if s.at >= 0 {
		for {
			timer := time.NewTimer(nextDaily(s.now(), s.at).Sub(s.now()))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				fn(ctx)
			}
		}
	}
	fn(ctx)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fn(ctx)
		}
	}
}

// parseTenants reads a comma separated list of tenant UUIDs from name; an empty value returns none.
func parseTenants(name, raw string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	for _, part := range strings.Split(raw, ",") {
		id, ok := ids.Canonical(strings.TrimSpace(part))
		if !ok || id == ids.Nil {
			return nil, fmt.Errorf("%s entry %q is not a UUID", name, part)
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// forEachEntity runs pass once per legal entity of every tenant: it lists the entities under an all-scope read principal, then
// runs the pass under a principal scoped to each, which is what a store call that posts or settles needs.
func forEachEntity(ctx context.Context, actor string, tenants []string, list func(context.Context) ([]string, error), pass func(ctx context.Context, tenantID, legalEntityID string) error) {
	for _, tenantID := range tenants {
		listCtx, cancel := context.WithTimeout(tenant.With(ctx, tenant.Principal{
			TenantID: tenantID, UserID: actor, Duties: map[string]struct{}{tenant.DutyAllScope: {}},
		}), jobTimeout)
		entities, err := list(listCtx)
		cancel()
		if err != nil {
			slog.Error("fieldservice job failed", "job", actor, "tenantId", tenantID, "err", err)
			continue
		}
		for _, le := range entities {
			runCtx, cancel := context.WithTimeout(tenant.With(ctx, tenant.Principal{TenantID: tenantID, LegalEntityID: le, UserID: actor}), jobTimeout)
			err := pass(runCtx, tenantID, le)
			cancel()
			if err != nil {
				slog.Error("fieldservice job failed", "job", actor, "tenantId", tenantID, "legalEntityId", le, "err", err)
			}
		}
	}
}
