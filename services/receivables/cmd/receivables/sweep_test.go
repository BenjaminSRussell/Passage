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

type fakeExpiringSweeper struct {
	fakeSweeper
	ttl     time.Duration
	expired []tenant.Principal
}

func (f *fakeExpiringSweeper) ExpireRMAReservations(ctx context.Context, _ time.Time, ttl time.Duration, _ int) (store.RMAExpiryResult, error) {
	p, _ := tenant.FromContext(ctx)
	f.expired = append(f.expired, p)
	f.ttl = ttl
	return store.RMAExpiryResult{Expired: 1}, nil
}

func TestNewRMASweep_ReservationTTLConfig(t *testing.T) {
	base := map[string]string{"RECEIVABLES_RMA_SWEEP_TENANT_IDS": "00000000-0000-0000-0000-000000000001"}
	s, err := newRMASweep(env(base), &fakeSweeper{})
	if err != nil || s.reservationTTL != 0 {
		t.Fatalf("expiry is off by default: %+v %v", s, err)
	}
	base["RECEIVABLES_RMA_RESERVATION_TTL"] = "30m"
	if _, err := newRMASweep(env(base), &fakeSweeper{}); err == nil {
		t.Fatal("a TTL below 1h must be refused")
	}
	base["RECEIVABLES_RMA_RESERVATION_TTL"] = "720h"
	if s, err = newRMASweep(env(base), &fakeSweeper{}); err != nil || s.reservationTTL != 720*time.Hour {
		t.Fatalf("ttl %+v %v", s, err)
	}
}

func TestRMASweep_ExpiresAbandonedCreditsPerLegalEntityWhenATTLIsSet(t *testing.T) {
	f := &fakeExpiringSweeper{fakeSweeper: fakeSweeper{entities: []string{"00000000-0000-0000-0000-0000000000a1"}}}
	s := &rmaSweep{tenants: []string{"00000000-0000-0000-0000-000000000001"}, store: f}
	s.once(context.Background())
	if len(f.expired) != 0 {
		t.Fatalf("no TTL, no expiry: %+v", f.expired)
	}
	s.reservationTTL = 48 * time.Hour
	s.once(context.Background())
	if len(f.expired) != 1 || f.ttl != 48*time.Hour || f.expired[0].LegalEntityID != f.entities[0] || f.expired[0].HasDuty(tenant.DutyAllScope) {
		t.Fatalf("expired %+v ttl %v", f.expired, f.ttl)
	}
}

type fakeJournalSweeper struct {
	fakeSweeper
	parked map[string]int64
}

func (f *fakeJournalSweeper) GLJournalLegalEntities(context.Context) ([]string, error) {
	return nil, nil
}

func (f *fakeJournalSweeper) PostDueJournals(context.Context, time.Time, int) (store.JournalSweepResult, error) {
	return store.JournalSweepResult{}, nil
}

func (f *fakeJournalSweeper) CountParkedJournals(ctx context.Context) (int64, error) {
	p, _ := tenant.FromContext(ctx)
	return f.parked[p.TenantID], nil
}

// The journal pass sets the parked-journal gauge to the parked journals of every tenant it sweeps.
func TestJournalPass_SetsTheParkedJournalGauge(t *testing.T) {
	f := &fakeJournalSweeper{parked: map[string]int64{"00000000-0000-0000-0000-000000000001": 2, "00000000-0000-0000-0000-000000000002": 3}}
	s := &rmaSweep{tenants: []string{"00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002"}, store: f}
	s.journalPass(context.Background())
	if got := store.ParkedJournals.Load(); got != 5 {
		t.Fatalf("parked gauge = %d, want 5", got)
	}
}
