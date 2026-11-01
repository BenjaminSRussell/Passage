package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const (
	tenantA = "00000000-0000-4000-8000-000000000001"
	tenantB = "00000000-0000-4000-8000-000000000002"
)

func TestNewActionSweep_ConfigurationIsValidated(t *testing.T) {
	apply := func(context.Context) (store.ApplyResult, error) { return store.ApplyResult{}, nil }
	if s, err := newActionSweep(envOf(nil), apply); s != nil || err != nil {
		t.Fatalf("no tenant ids must disable the sweep, got %v %v", s, err)
	}
	for name, env := range map[string]map[string]string{
		"bad tenant":   {"HR_ACTION_SWEEP_TENANT_IDS": tenantA + ",acme"},
		"nil tenant":   {"HR_ACTION_SWEEP_TENANT_IDS": "00000000-0000-0000-0000-000000000000"},
		"short":        {"HR_ACTION_SWEEP_TENANT_IDS": tenantA, "HR_ACTION_SWEEP_INTERVAL": "10s"},
		"not duration": {"HR_ACTION_SWEEP_TENANT_IDS": tenantA, "HR_ACTION_SWEEP_INTERVAL": "soon"},
	} {
		if _, err := newActionSweep(envOf(env), apply); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	s, err := newActionSweep(envOf(map[string]string{"HR_ACTION_SWEEP_TENANT_IDS": tenantA + " , " + tenantB + "," + tenantA, "HR_ACTION_SWEEP_INTERVAL": "2m"}), apply)
	if err != nil || len(s.tenants) != 2 || s.interval != 2*time.Minute {
		t.Fatalf("sweep = %+v, %v", s, err)
	}
}

func TestActionSweep_RunsEveryTenantWithAnAllEntityServicePrincipalAndSurvivesAFailure(t *testing.T) {
	var seen []string
	var scopes []string
	s := &actionSweep{tenants: []string{tenantA, tenantB}, apply: func(ctx context.Context) (store.ApplyResult, error) {
		p, ok := tenant.FromContext(ctx)
		if !ok {
			t.Fatal("no principal in the sweep context")
		}
		seen = append(seen, p.TenantID)
		scopes = append(scopes, dbx.LegalEntityScopes(p))
		if p.UserID != sweepActor {
			t.Errorf("user = %q, want %q", p.UserID, sweepActor)
		}
		if p.TenantID == tenantA {
			return store.ApplyResult{}, errors.New("database down")
		}
		return store.ApplyResult{Applied: 1}, nil
	}}
	s.once(context.Background())
	if strings.Join(seen, ",") != tenantA+","+tenantB {
		t.Fatalf("tenants swept = %v, want both despite the first failing", seen)
	}
	for _, scope := range scopes {
		if scope != "*" {
			t.Fatalf("scope = %q, want * so every legal entity's due actions are found", scope)
		}
	}
}

func TestActionSweep_LoopRunsImmediatelyThenStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runs := make(chan struct{}, 4)
	s := &actionSweep{tenants: []string{tenantA}, interval: time.Hour, apply: func(context.Context) (store.ApplyResult, error) {
		runs <- struct{}{}
		return store.ApplyResult{}, nil
	}}
	done := make(chan struct{})
	go func() { s.loop(ctx); close(done) }()
	select {
	case <-runs:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not sweep at start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not stop on cancel")
	}
}
