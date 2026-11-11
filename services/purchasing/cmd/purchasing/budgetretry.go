package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/budget"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

const (
	budgetRetryCommand = "budget-calls-retry"
	budgetRetryActor   = "system:budget-calls-retry"
	// maxRetryPasses bounds how many passes of up to 100 calls one run makes for a tenant.
	maxRetryPasses = 20
)

// retryQueue is what the job reads and writes of the call queue (the store).
type retryQueue interface {
	budget.Queue
}

// budgetCallsRetry is `purchasing budget-calls-retry TENANT_ID [TENANT_ID ...]` (FR-38.5): for each tenant it repeats the calls to
// budget control that failed, the same work as POST /v1/procurement/budget-calls:retry, so a conversion or release that budgeting
// missed while it was down is made without anybody pressing "try again". Every call has a stable idempotency key, so repeating is
// safe. The run fails when a tenant still has calls it could not make, which is the alert.
func budgetCallsRetry(ctx context.Context, getenv func(string) string, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: purchasing budget-calls-retry TENANT_ID [TENANT_ID ...]")
	}
	tenants := make([]string, len(args))
	for i, arg := range args {
		id, ok := ids.Canonical(arg)
		if !ok || id == ids.Nil {
			return fmt.Errorf("tenant id %q is not a UUID", arg)
		}
		tenants[i] = id
	}
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	url := getenv("BUDGETING_URL")
	if url == "" {
		return errors.New("BUDGETING_URL is required")
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return err
	}
	if signer == nil {
		return errors.New("BUDGETING_URL needs a service signing key (PASSAGE_SERVICE_SIGNING_KEY_FILE)")
	}
	pool, err := dbx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	return retryTenants(ctx, store.New(pool), budget.New(url, nil, signer), tenants)
}

func retryTenants(ctx context.Context, q retryQueue, port budget.Port, tenants []string) error {
	failed := 0
	for _, tenantID := range tenants {
		res, err := retryTenant(ctx, q, port, tenantID)
		slog.InfoContext(ctx, "budget calls repeated", "tenantId", tenantID, "tried", res.Tried, "done", res.Done, "failed", res.Failed)
		if err != nil {
			slog.ErrorContext(ctx, "budget calls could not be read", "tenantId", tenantID, "err", err)
			failed++
			continue
		}
		if res.Failed > 0 {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d tenants still have budget calls that budgeting did not accept", failed)
	}
	return nil
}

// retryTenant makes the pending calls of one tenant, oldest first, in passes of up to 100 until a pass finishes none. The queue is
// read as a tenant-wide system principal and each call is made as a principal of its own legal entity, because the token to budgeting
// names the one legal entity the call is for.
func retryTenant(ctx context.Context, q retryQueue, port budget.Port, tenantID string) (budget.RetryResult, error) {
	all := tenant.With(ctx, tenant.Principal{TenantID: tenantID, UserID: budgetRetryActor, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	var total budget.RetryResult
	// A call that failed in this run stays pending and sits at the head of an oldest-first list, so each pass asks for that many more
	// and skips them: calls that keep failing cannot starve the newer ones behind them.
	failed := map[string]bool{}
	for pass := 0; pass < maxRetryPasses; pass++ {
		pending, err := q.ListBudgetCalls(all, store.BudgetCallPending, min(100+len(failed), 1000))
		if err != nil {
			return total, err
		}
		tried := 0
		for _, c := range pending {
			if failed[c.ID] || tried == 100 {
				continue
			}
			tried++
			as := tenant.With(ctx, tenant.Principal{TenantID: tenantID, UserID: budgetRetryActor, LegalEntityID: c.LegalEntityID})
			total.Tried++
			if err := budget.Run(as, port, q, c); err != nil {
				failed[c.ID] = true
				continue
			}
			total.Done++
		}
		if tried == 0 {
			break
		}
	}
	total.Failed = len(failed)
	return total, nil
}
