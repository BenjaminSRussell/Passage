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
	runDays []int
	calls   []tenant.Principal
	times   []time.Time
	fail    map[string]bool
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

func (f *fakeSweeper) SweepRuns(ctx context.Context, now time.Time, days int) ([]store.RunSweep, error) {
	p, _ := tenant.FromContext(ctx)
	f.runDays = append(f.runDays, days)
	if f.fail[p.TenantID+"/runs"] {
		return nil, errors.New("database down")
	}
	return []store.RunSweep{{DeletedRuns: 4, HeldRuns: 1}}, nil
}

// Every tenant is swept as the in-process system actor with the job's clock, and
// one failing tenant fails the job without skipping the rest.
func TestSweepTenants_RunsEachTenantAndFailsTheJobOnAnyError(t *testing.T) {
	at := time.Date(2026, 11, 8, 4, 0, 0, 0, time.UTC)
	f := &fakeSweeper{fail: map[string]bool{tenantA: true}}
	err := sweepTenants(context.Background(), f, func() time.Time { return at }, 90, []string{tenantA, tenantB})
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
	// The ad-hoc runs are swept for each tenant that sweeps, with the configured days; a failing run sweep fails the job.
	if len(f.runDays) != 1 || f.runDays[0] != 90 {
		t.Fatalf("run sweeps = %v, want one pass of 90 days (tenant A failed before it)", f.runDays)
	}
	g := &fakeSweeper{fail: map[string]bool{tenantB + "/runs": true}}
	if err := sweepTenants(context.Background(), g, time.Now, 30, []string{tenantB}); err == nil || len(g.runDays) != 1 || g.runDays[0] != 30 {
		t.Fatalf("failing run sweep = %v, days %v", err, g.runDays)
	}
	if err := sweepTenants(context.Background(), &fakeSweeper{}, time.Now, 90, []string{tenantB}); err != nil {
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
