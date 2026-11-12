package main

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

type fakeReplenishStore struct {
	entities []string
	calls    []tenant.Principal
	dates    []time.Time
}

func (f *fakeReplenishStore) TruckLegalEntities(ctx context.Context) ([]string, error) {
	p, _ := tenant.FromContext(ctx)
	if !p.HasDuty(tenant.DutyAllScope) || p.LegalEntityID != "" {
		return nil, context.Canceled
	}
	return f.entities, nil
}

func (f *fakeReplenishStore) ReplenishTrucks(ctx context.Context, le, site, wh string, shipDate time.Time) ([]domain.TransferOrder, error) {
	p, _ := tenant.FromContext(ctx)
	f.calls = append(f.calls, p)
	f.dates = append(f.dates, shipDate)
	if le != p.LegalEntityID || site != "" || wh != "" {
		return nil, context.Canceled
	}
	return []domain.TransferOrder{{}}, nil
}

func TestNewReplenishJob_Config(t *testing.T) {
	if j, err := newReplenishJob(env(nil), &fakeReplenishStore{}); j != nil || err != nil {
		t.Fatalf("unset tenants disable the job: %v %v", j, err)
	}
	if _, err := newReplenishJob(env(map[string]string{"INV_TRUCK_REPLENISH_TENANT_IDS": "nope"}), &fakeReplenishStore{}); err == nil {
		t.Fatal("a non-UUID tenant must be refused")
	}
	tenantID := "00000000-0000-0000-0000-000000000001"
	if _, err := newReplenishJob(env(map[string]string{"INV_TRUCK_REPLENISH_TENANT_IDS": tenantID, "INV_TRUCK_REPLENISH_AT": "02:30", "INV_TRUCK_REPLENISH_INTERVAL": "1h"}), &fakeReplenishStore{}); err == nil {
		t.Fatal("AT and INTERVAL together must be refused")
	}
	if _, err := newReplenishJob(env(map[string]string{"INV_TRUCK_REPLENISH_TENANT_IDS": tenantID, "INV_TRUCK_REPLENISH_AT": "25:00"}), &fakeReplenishStore{}); err == nil {
		t.Fatal("a bad time of day must be refused")
	}
	j, err := newReplenishJob(env(map[string]string{"INV_TRUCK_REPLENISH_TENANT_IDS": tenantID + ", " + tenantID, "INV_TRUCK_REPLENISH_AT": "02:30"}), &fakeReplenishStore{})
	if err != nil || len(j.tenants) != 1 || j.sched.at != 150 {
		t.Fatalf("job %+v %v", j, err)
	}
	j, err = newReplenishJob(env(map[string]string{"INV_TRUCK_REPLENISH_TENANT_IDS": tenantID}), &fakeReplenishStore{})
	if err != nil || j.sched.interval != 24*time.Hour || j.sched.at >= 0 {
		t.Fatalf("the default is a run every day: %+v %v", j, err)
	}
}

func TestReplenishJob_ReplenishesEachLegalEntityUnderItsOwnScopeForToday(t *testing.T) {
	f := &fakeReplenishStore{entities: []string{"00000000-0000-0000-0000-0000000000a1", "00000000-0000-0000-0000-0000000000a2"}}
	j := &replenishJob{tenants: []string{"00000000-0000-0000-0000-000000000001"}, store: f, now: func() time.Time { return time.Date(2026, 11, 11, 2, 30, 0, 0, time.UTC) }}
	j.once(context.Background())
	if len(f.calls) != 2 {
		t.Fatalf("calls %+v", f.calls)
	}
	for i, p := range f.calls {
		if p.TenantID != j.tenants[0] || p.LegalEntityID != f.entities[i] || p.HasDuty(tenant.DutyAllScope) || p.UserID != replenishActor {
			t.Fatalf("principal %d %+v", i, p)
		}
		if !f.dates[i].Equal(time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("ship date %v", f.dates[i])
		}
	}
}
