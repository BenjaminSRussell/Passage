package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/api"
)

const (
	subscriptionJobActor    = "system:billing-subscription-job"
	defaultSubscriptionJob  = time.Hour
	minSubscriptionJob      = time.Minute
	subscriptionJobTimeout  = 10 * time.Minute
	subscriptionJobPageSize = 500
)

// periodBiller is what the due-period job needs of the API.
type periodBiller interface {
	SubscriptionLegalEntities(ctx context.Context) ([]string, error)
	BillDueSubscriptions(ctx context.Context, legalEntityID string, asOf time.Time, limit int) (api.DueResult, error)
}

// subscriptionJob is the daily subscription billing job (FR-29.10, 29-5) run in-process: per configured tenant it lists the legal
// entities that hold subscriptions (under an all-scope read principal) and bills the periods due today for each, under a principal
// scoped to that legal entity. A period is invoiced once, so the interval only sets how soon a due period is picked up; a run
// another replica makes at the same time loses on the period's uniqueness and counts as a failure that the next tick repeats.
// It creates the invoice proposals; posting them stays with the billing workspace.
type subscriptionJob struct {
	interval time.Duration
	tenants  []string
	billing  periodBiller
	now      func() time.Time
}

func newSubscriptionJob(getenv func(string) string, billing periodBiller) (*subscriptionJob, error) {
	raw := getenv("BILLING_SUBSCRIPTION_JOB_TENANT_IDS")
	if strings.TrimSpace(raw) == "" {
		slog.Info("billing subscription job disabled; BILLING_SUBSCRIPTION_JOB_TENANT_IDS is not set")
		return nil, nil
	}
	var tenants []string
	for _, part := range strings.Split(raw, ",") {
		id, ok := ids.Canonical(strings.TrimSpace(part))
		if !ok || id == ids.Nil {
			return nil, fmt.Errorf("BILLING_SUBSCRIPTION_JOB_TENANT_IDS entry %q is not a UUID", part)
		}
		if !slices.Contains(tenants, id) {
			tenants = append(tenants, id)
		}
	}
	interval := defaultSubscriptionJob
	if v := getenv("BILLING_SUBSCRIPTION_JOB_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < minSubscriptionJob {
			return nil, errors.New("BILLING_SUBSCRIPTION_JOB_INTERVAL must be a duration of at least 1m, got " + v)
		}
		interval = d
	}
	return &subscriptionJob{interval: interval, tenants: tenants, billing: billing, now: time.Now}, nil
}

func (j *subscriptionJob) loop(ctx context.Context) {
	j.once(ctx)
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

func (j *subscriptionJob) once(ctx context.Context) {
	now := j.now().UTC()
	asOf := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for _, tenantID := range j.tenants {
		listCtx, cancel := context.WithTimeout(tenant.With(ctx, tenant.Principal{
			TenantID: tenantID, UserID: subscriptionJobActor, Duties: map[string]struct{}{tenant.DutyAllScope: {}},
		}), subscriptionJobTimeout)
		entities, err := j.billing.SubscriptionLegalEntities(listCtx)
		cancel()
		if err != nil {
			slog.Error("billing subscription job failed", "tenantId", tenantID, "err", err)
			continue
		}
		for _, le := range entities {
			runCtx, cancel := context.WithTimeout(tenant.With(ctx, tenant.Principal{TenantID: tenantID, LegalEntityID: le, UserID: subscriptionJobActor}), subscriptionJobTimeout)
			runCtx = context.WithValue(runCtx, middleware.RequestIDKey, "subscription-job-"+ids.New())
			res, err := j.billing.BillDueSubscriptions(runCtx, le, asOf, subscriptionJobPageSize)
			cancel()
			switch {
			case err != nil:
				slog.Error("billing subscription job failed", "tenantId", tenantID, "legalEntityId", le, "err", err)
			case res.Billed > 0 || res.Failed > 0:
				slog.Info("billing subscription job finished", "tenantId", tenantID, "legalEntityId", le, "considered", res.Considered, "billed", res.Billed, "failed", res.Failed)
			}
		}
	}
}
