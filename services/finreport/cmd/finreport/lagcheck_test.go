package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/config"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

type fakeChecker struct {
	calls []tenant.Principal
	args  [2]int
	fail  map[string]bool
}

func (f *fakeChecker) CheckLag(ctx context.Context, maxLag, sustain int) (store.LagCheck, error) {
	p, _ := tenant.FromContext(ctx)
	f.calls = append(f.calls, p)
	f.args = [2]int{maxLag, sustain}
	if f.fail[p.TenantID] {
		return store.LagCheck{}, errors.New("database down")
	}
	return store.LagCheck{Checked: 1, Raised: []store.LagAlert{}}, nil
}

const (
	tenantA = "0190f0a0-0000-7000-8000-00000000000a"
	tenantB = "0190f0a0-0000-7000-8000-00000000000b"
)

// Every tenant is checked as the in-process system actor with the configured
// gate and window, and one failing tenant fails the job without skipping the rest.
func TestCheckTenants_RunsEachTenantAndFailsTheJobOnAnyError(t *testing.T) {
	f := &fakeChecker{fail: map[string]bool{tenantA: true}}
	err := checkTenants(context.Background(), f, config.Reporting{MaxLagSeconds: 5, LagAlertSeconds: 7}, []string{tenantA, tenantB})
	if err == nil || !strings.Contains(err.Error(), "1 of 2") {
		t.Fatalf("err = %v, want one of two failed", err)
	}
	if len(f.calls) != 2 || f.calls[1].TenantID != tenantB || f.args != [2]int{5, 7} {
		t.Fatalf("calls = %+v args %v", f.calls, f.args)
	}
	p := f.calls[0]
	if p.UserID != lagCheckActor || !p.HasDuty(tenant.DutyAllScope) || len(p.Duties) != 1 || p.LegalEntityID != "" {
		t.Fatalf("principal = %+v, want the system actor with all-scope only", p)
	}
	if err := checkTenants(context.Background(), &fakeChecker{}, config.Reporting{}, []string{tenantB}); err != nil {
		t.Fatalf("healthy check = %v", err)
	}
}

func TestLagCheck_RefusesBadArguments(t *testing.T) {
	env := func(string) string { return "" }
	for _, args := range [][]string{nil, {"not-a-uuid"}, {"00000000-0000-0000-0000-000000000000"}} {
		if err := lagCheck(context.Background(), env, args); err == nil {
			t.Fatalf("lag-check %v must fail", args)
		}
	}
	if err := lagCheck(context.Background(), env, []string{tenantA}); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("lag-check without DATABASE_URL = %v", err)
	}
}
