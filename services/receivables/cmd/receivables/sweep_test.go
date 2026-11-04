package main

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

type fakeSweeper struct {
	entities []string
	swept    []tenant.Principal
}

func (f *fakeSweeper) RMASweepLegalEntities(ctx context.Context) ([]string, error) {
	p, _ := tenant.FromContext(ctx)
	if !p.HasDuty(tenant.DutyAllScope) || p.LegalEntityID != "" {
		return nil, context.Canceled
	}
	return f.entities, nil
}

func (f *fakeSweeper) SweepRMACredits(ctx context.Context, _ time.Time, limit int) (store.RMASweepResult, error) {
	p, _ := tenant.FromContext(ctx)
	f.swept = append(f.swept, p)
	return store.RMASweepResult{Posted: 1}, nil
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestNewRMASweep_Config(t *testing.T) {
	if s, err := newRMASweep(env(nil), &fakeSweeper{}); s != nil || err != nil {
		t.Fatalf("unset tenants disable the sweep: %v %v", s, err)
	}
	if _, err := newRMASweep(env(map[string]string{"RECEIVABLES_RMA_SWEEP_TENANT_IDS": "nope"}), &fakeSweeper{}); err == nil {
		t.Fatal("a non-UUID tenant must be refused")
	}
	if _, err := newRMASweep(env(map[string]string{"RECEIVABLES_RMA_SWEEP_TENANT_IDS": "00000000-0000-0000-0000-000000000001",
		"RECEIVABLES_RMA_SWEEP_INTERVAL": "10s"}), &fakeSweeper{}); err == nil {
		t.Fatal("an interval below 1m must be refused")
	}
	s, err := newRMASweep(env(map[string]string{"RECEIVABLES_RMA_SWEEP_TENANT_IDS": "00000000-0000-0000-0000-000000000001, 00000000-0000-0000-0000-000000000001"}), &fakeSweeper{})
	if err != nil || len(s.tenants) != 1 || s.interval != defaultRMASweepInterval {
		t.Fatalf("sweep %+v %v", s, err)
	}
}

func TestRMASweep_SweepsEachLegalEntityUnderItsOwnScope(t *testing.T) {
	f := &fakeSweeper{entities: []string{"00000000-0000-0000-0000-0000000000a1", "00000000-0000-0000-0000-0000000000a2"}}
	s := &rmaSweep{tenants: []string{"00000000-0000-0000-0000-000000000001"}, store: f}
	s.once(context.Background())
	if len(f.swept) != 2 {
		t.Fatalf("swept %+v", f.swept)
	}
	for i, p := range f.swept {
		if p.TenantID != s.tenants[0] || p.LegalEntityID != f.entities[i] || p.HasDuty(tenant.DutyAllScope) || p.UserID != rmaSweepActor {
			t.Fatalf("principal %d %+v", i, p)
		}
	}
}
