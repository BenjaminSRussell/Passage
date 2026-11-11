package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// SweepInterval is how often stale QUEUED jobs are looked for (§6.32.16: every 15 s).
const SweepInterval = 15 * time.Second

// Sweeper re-dispatches the stale QUEUED jobs of the tenant in ctx and returns how many it
// claimed. api.API satisfies it through SweepClaimed.
type Sweeper interface {
	SweepClaimed(ctx context.Context) (int, error)
}

// SweepTenants runs one sweep for each tenant. A failing tenant does not stop the others.
func SweepTenants(ctx context.Context, s Sweeper, tenants []string) (int, error) {
	total := 0
	var errs []error
	for _, id := range tenants {
		n, err := s.SweepClaimed(tenant.With(ctx, tenant.Principal{TenantID: id, UserID: "system:print-sweep"}))
		total += n
		if err != nil {
			errs = append(errs, fmt.Errorf("sweep tenant %s: %w", id, err))
		}
	}
	return total, errors.Join(errs...)
}

// RunSweep calls SweepTenants every interval until ctx is cancelled. onRun, when set, receives
// each run's outcome for logging.
func RunSweep(ctx context.Context, s Sweeper, tenants []string, every time.Duration, onRun func(claimed int, err error)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := SweepTenants(ctx, s, tenants)
			if onRun != nil && (n > 0 || err != nil) {
				onRun(n, err)
			}
		}
	}
}
