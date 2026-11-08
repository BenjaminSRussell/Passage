package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

type fakeSweeper struct {
	calls []tenant.Principal
	times []time.Time
	fail  map[string]bool
}

func (f *fakeSweeper) SweepLibrary(ctx context.Context, now time.Time) ([]store.Sweep, error) {
	p, _ := tenant.FromContext(ctx)
	f.calls = append(f.calls, p)
	f.times = append(f.times, now)
	if f.fail[p.TenantID] {
		return nil, errors.New("database down")
	}
	return []store.Sweep{{DeletedCount: 2, HeldCount: 1}}, nil
}

// Every tenant is swept as the in-process system actor with the job's clock, and
// one failing tenant fails the job without skipping the rest.
func TestSweepTenants_RunsEachTenantAndFailsTheJobOnAnyError(t *testing.T) {
	at := time.Date(2026, 11, 8, 4, 0, 0, 0, time.UTC)
	f := &fakeSweeper{fail: map[string]bool{tenantA: true}}
	err := sweepTenants(context.Background(), f, func() time.Time { return at }, []string{tenantA, tenantB})
	if err == nil || !strings.Contains(err.Error(), "1 of 2") {
		t.Fatalf("err = %v, want one of two failed", err)
	}
	if len(f.calls) != 2 || f.calls[1].TenantID != tenantB || !f.times[1].Equal(at) {
		t.Fatalf("calls = %+v", f.calls)
	}
	p := f.calls[0]
	if p.UserID != store.SweepActor || !p.HasDuty(tenant.DutyAllScope) || len(p.Duties) != 1 {
		t.Fatalf("principal = %+v, want the system actor with all-scope only", p)
	}
	if err := sweepTenants(context.Background(), &fakeSweeper{}, time.Now, []string{tenantB}); err != nil {
		t.Fatalf("healthy pass = %v", err)
	}
}

func TestSweepLibrary_RefusesBadArguments(t *testing.T) {
	env := func(string) string { return "" }
	for _, args := range [][]string{nil, {"not-a-uuid"}} {
		if err := sweepLibrary(context.Background(), env, args); err == nil || !strings.Contains(err.Error(), sweepLibraryCommand+" TENANT_ID") && !strings.Contains(err.Error(), "not a UUID") {
			t.Fatalf("sweep-library %v = %v", args, err)
		}
	}
	if err := sweepLibrary(context.Background(), env, []string{tenantA}); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("without DATABASE_URL = %v", err)
	}
}
