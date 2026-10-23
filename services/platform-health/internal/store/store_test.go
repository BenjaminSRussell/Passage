package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/platform-health/internal/store"
	"github.com/BenjaminSRussell/passage/services/platform-health/migrations"
)

func TestPostgres_TenantIsolationAndIdempotency(t *testing.T) {
	pool := testkit.DB(t)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: testkit.TenantID(9)})
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)

	first, err := s.Create(ctx, "hello", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.Create(ctx, "different", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if replay.ID != first.ID || replay.Message != "hello" {
		t.Fatalf("idempotent replay must return the original entry, got %+v", replay)
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	own, err := s.List(ctx)
	if err != nil || len(own) != 1 {
		t.Fatalf("tenant 1 list = %v, %v; want 1 entry", own, err)
	}
	other, err := s.List(tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(2), LegalEntityID: testkit.TenantID(9)}))
	if err != nil || len(other) != 0 {
		t.Fatalf("tenant 2 must see no rows, got %v, %v", other, err)
	}
}
