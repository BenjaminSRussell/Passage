package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
	"github.com/BenjaminSRussell/passage/services/tax/internal/store"
)

const (
	settlementActor        = "system:tax-settlement"
	recommitActor          = "system:tax-recommit"
	defaultSettlementDay   = 5
	defaultSettlementAt    = "04:00"
	defaultRecommitEvery   = 5 * time.Minute
	minRecommitEvery       = time.Minute
	jobTimeout             = 10 * time.Minute
	scheduledRecommitBatch = 20
)

// scheduledAPI is what the jobs need of the API.
type scheduledAPI interface {
	RunSettlement(ctx context.Context, period domain.Period) (store.Run, error)
	RunRecommit(ctx context.Context, limit int) (store.RecommitSummary, error)
}

// entities lists the legal entities a job has work for in a tenant.
type entities interface {
	LedgerConfigEntities(ctx context.Context) ([]string, error)
	RecommitEntities(ctx context.Context) ([]string, error)
}

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

// forEachEntity runs pass once per legal entity of each tenant: it lists the entities under an all-scope read principal, then runs the
// pass under a principal scoped to each one.
func forEachEntity(ctx context.Context, actor string, tenants []string, list func(context.Context) ([]string, error), pass func(context.Context, string, string) error) {
	for _, tenantID := range tenants {
		listCtx, cancel := context.WithTimeout(tenant.With(ctx, tenant.Principal{TenantID: tenantID, UserID: actor, Duties: map[string]struct{}{tenant.DutyAllScope: {}}}), jobTimeout)
		les, err := list(listCtx)
		cancel()
		if err != nil {
			slog.Error("tax job failed", "job", actor, "tenantId", tenantID, "err", err)
			continue
		}
		for _, le := range les {
			runCtx, cancel := context.WithTimeout(tenant.With(ctx, tenant.Principal{TenantID: tenantID, LegalEntityID: le, UserID: actor}), jobTimeout)
			if err := pass(runCtx, tenantID, le); err != nil {
				slog.Error("tax job failed", "job", actor, "tenantId", tenantID, "legalEntityId", le, "err", err)
			}
			cancel()
		}
	}
}

// settlementJob settles the previous calendar month of every legal entity with a ledger configuration, once a month on day
// TAX_SETTLEMENT_DAY (1 to 28, default 5) at TAX_SETTLEMENT_AT (UTC "HH:MM", default 04:00): the same reconciliation to the general
// ledger as POST /v1/tax/settlements:run, so a month that differs from the GL tax accounts is refused (TAX-0008) and logged, never
// settled. A month with nothing open (TAX-0009) is normal and logged quietly. Off until TAX_SETTLEMENT_TENANT_IDS names tenants. A
// replica that runs it twice settles nothing twice: transactions are locked for the run and a later run settles only the delta.
type settlementJob struct {
	tenants []string
	day     int
	at      int
	api     scheduledAPI
	lister  entities
	now     func() time.Time
}

func newSettlementJob(getenv func(string) string, api scheduledAPI, lister entities) (*settlementJob, error) {
	tenants, err := parseTenants("TAX_SETTLEMENT_TENANT_IDS", getenv("TAX_SETTLEMENT_TENANT_IDS"))
	if err != nil {
		return nil, err
	}
	if len(tenants) == 0 {
		slog.Info("tax settlement job disabled; TAX_SETTLEMENT_TENANT_IDS is not set")
		return nil, nil
	}
	day := defaultSettlementDay
	if v := getenv("TAX_SETTLEMENT_DAY"); v != "" {
		if day, err = strconv.Atoi(v); err != nil || day < 1 || day > 28 {
			return nil, errors.New("TAX_SETTLEMENT_DAY must be a day of the month from 1 to 28, got " + v)
		}
	}
	atText := defaultSettlementAt
	if v := getenv("TAX_SETTLEMENT_AT"); v != "" {
		atText = v
	}
	t, err := time.Parse("15:04", atText)
	if err != nil {
		return nil, errors.New("TAX_SETTLEMENT_AT must be a UTC time of day as HH:MM, got " + atText)
	}
	return &settlementJob{tenants: tenants, day: day, at: t.Hour()*60 + t.Minute(), api: api, lister: lister, now: time.Now}, nil
}

// nextMonthly is the first moment after now that is day of a month at minutes after midnight UTC.
func nextMonthly(now time.Time, day, at int) time.Time {
	now = now.UTC()
	next := time.Date(now.Year(), now.Month(), day, at/60, at%60, 0, 0, time.UTC)
	if !next.After(now) {
		next = time.Date(now.Year(), now.Month()+1, day, at/60, at%60, 0, 0, time.UTC)
	}
	return next
}

// previousMonth is the whole calendar month before the one now is in.
func previousMonth(now time.Time) domain.Period {
	now = now.UTC()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return domain.Period{From: domain.Date{Time: first.AddDate(0, -1, 0)}, To: domain.Date{Time: first.AddDate(0, 0, -1)}}
}

func (j *settlementJob) loop(ctx context.Context) {
	for {
		timer := time.NewTimer(nextMonthly(j.now(), j.day, j.at).Sub(j.now()))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			j.once(ctx)
		}
	}
}

func (j *settlementJob) once(ctx context.Context) {
	period := previousMonth(j.now())
	forEachEntity(ctx, settlementActor, j.tenants, j.lister.LedgerConfigEntities, func(ctx context.Context, tenantID, le string) error {
		run, err := j.api.RunSettlement(ctx, period)
		var de *domain.Error
		switch {
		case err == nil:
			slog.Info("tax settlement run", "tenantId", tenantID, "legalEntityId", le, "periodFrom", period.From.Format("2006-01-02"), "periodTo", period.To.Format("2006-01-02"), "run", run.ID, "version", run.Version)
			return nil
		case errors.As(err, &de) && de.Code == domain.CodeNothingToSettle:
			return nil
		}
		return err
	})
}

// recommitJob drains the recommit queue of each tenant's legal entities every TAX_RECOMMIT_INTERVAL (default 5m, at least 1m), so a
// document calculated by the internal engine while a provider was down is recalculated on the provider once it is back. Off until
// TAX_RECOMMIT_TENANT_IDS names tenants.
type recommitJob struct {
	tenants  []string
	interval time.Duration
	api      scheduledAPI
	lister   entities
}

func newRecommitJob(getenv func(string) string, api scheduledAPI, lister entities) (*recommitJob, error) {
	tenants, err := parseTenants("TAX_RECOMMIT_TENANT_IDS", getenv("TAX_RECOMMIT_TENANT_IDS"))
	if err != nil {
		return nil, err
	}
	if len(tenants) == 0 {
		slog.Info("tax recommit job disabled; TAX_RECOMMIT_TENANT_IDS is not set")
		return nil, nil
	}
	interval := defaultRecommitEvery
	if v := getenv("TAX_RECOMMIT_INTERVAL"); v != "" {
		if interval, err = time.ParseDuration(v); err != nil || interval < minRecommitEvery {
			return nil, errors.New("TAX_RECOMMIT_INTERVAL must be a duration of at least 1m, got " + v)
		}
	}
	return &recommitJob{tenants: tenants, interval: interval, api: api, lister: lister}, nil
}

func (j *recommitJob) loop(ctx context.Context) {
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			j.once(ctx)
		}
	}
}

func (j *recommitJob) once(ctx context.Context) {
	forEachEntity(ctx, recommitActor, j.tenants, j.lister.RecommitEntities, func(ctx context.Context, tenantID, le string) error {
		sum, err := j.api.RunRecommit(ctx, scheduledRecommitBatch)
		if err != nil {
			return err
		}
		if sum.Completed > 0 || sum.Failed > 0 {
			slog.Info("tax recommit run", "tenantId", tenantID, "legalEntityId", le, "completed", sum.Completed, "failed", sum.Failed)
		}
		return nil
	})
}
