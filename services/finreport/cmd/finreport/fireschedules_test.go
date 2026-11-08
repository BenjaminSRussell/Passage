package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/firing"
)

type fakeFirer struct {
	calls []tenant.Principal
	fail  map[string]bool
}

func (f *fakeFirer) Run(ctx context.Context) (firing.Result, error) {
	p, _ := tenant.FromContext(ctx)
	f.calls = append(f.calls, p)
	if f.fail[p.TenantID] {
		return firing.Result{}, errors.New("database down")
	}
	return firing.Result{Due: 1, Completed: 1}, nil
}

// Every tenant is fired as the in-process system actor, and one failing tenant
// fails the job without skipping the rest.
func TestFireTenants_RunsEachTenantAndFailsTheJobOnAnyError(t *testing.T) {
	f := &fakeFirer{fail: map[string]bool{tenantA: true}}
	err := fireTenants(context.Background(), f, []string{tenantA, tenantB})
	if err == nil || !strings.Contains(err.Error(), "1 of 2") {
		t.Fatalf("err = %v, want one of two failed", err)
	}
	if len(f.calls) != 2 || f.calls[1].TenantID != tenantB {
		t.Fatalf("calls = %+v", f.calls)
	}
	p := f.calls[0]
	if p.UserID != firing.Actor || !p.HasDuty(tenant.DutyAllScope) || len(p.Duties) != 1 {
		t.Fatalf("principal = %+v, want the system actor with all-scope only", p)
	}
	if err := fireTenants(context.Background(), &fakeFirer{}, []string{tenantB}); err != nil {
		t.Fatalf("healthy pass = %v", err)
	}
}

func TestFireSchedules_RefusesBadArguments(t *testing.T) {
	env := func(string) string { return "" }
	for _, args := range [][]string{nil, {"not-a-uuid"}} {
		if err := fireSchedules(context.Background(), env, args); err == nil || !strings.Contains(err.Error(), fireSchedulesCommand+" TENANT_ID") && !strings.Contains(err.Error(), "not a UUID") {
			t.Fatalf("fire-schedules %v = %v", args, err)
		}
	}
	if err := fireSchedules(context.Background(), env, []string{tenantA}); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("without DATABASE_URL = %v", err)
	}
}
