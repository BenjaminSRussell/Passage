package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
	"github.com/BenjaminSRussell/passage/services/org/migrations"
)

func principalContext(n int) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n)})
}

func newStore(t *testing.T, n int) (*store.Postgres, context.Context) {
	pool := testkit.DB(t)
	ctx := principalContext(n)
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return store.New(pool), ctx
}

func TestPostgres_TenantIsolationIdempotencyAndDuplicateCode(t *testing.T) {
	s, ctx := newStore(t, 1)
	in := store.LegalEntity{Code: "US01", Name: "Passage US", Country: "US", Currency: "USD"}

	first, err := s.CreateLegalEntity(ctx, in, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateLegalEntity(ctx, in, "key-1")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("idempotent replay must return the original entity, got %+v, %v", replay, err)
	}
	var problem *httpx.Problem
	if _, err := s.CreateLegalEntity(ctx, store.LegalEntity{Code: "US02", Name: "Other", Country: "US", Currency: "USD"}, "key-1"); !errors.As(err, &problem) || problem.Code != store.CodeIdempotencyReuse {
		t.Fatalf("reused key with different body must be %s, got %v", store.CodeIdempotencyReuse, err)
	}
	if _, err := s.CreateLegalEntity(ctx, in, "key-2"); !errors.As(err, &problem) || problem.Code != store.CodeLegalEntityCode {
		t.Fatalf("duplicate code must be %s, got %v", store.CodeLegalEntityCode, err)
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	own, err := s.ListLegalEntities(ctx, "", store.MaxPageSize)
	if err != nil || len(own.Items) != 1 {
		t.Fatalf("tenant 1 list = %v, %v; want 1 entity", own, err)
	}
	other, err := s.ListLegalEntities(principalContext(2), "", store.MaxPageSize)
	if err != nil || len(other.Items) != 0 {
		t.Fatalf("tenant 2 must see no rows, got %v, %v", other, err)
	}
}

func TestPostgres_ListPagesPastFirstPage(t *testing.T) {
	s, ctx := newStore(t, 3)
	for _, code := range []string{"E1", "E2", "E3"} {
		if _, err := s.CreateLegalEntity(ctx, store.LegalEntity{Code: code, Name: code, Country: "US", Currency: "USD"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ListLegalEntities(ctx, "", 2)
	if err != nil || len(first.Items) != 2 || first.NextCode != "E2" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	second, err := s.ListLegalEntities(ctx, first.NextCode, 2)
	if err != nil || len(second.Items) != 1 || second.Items[0].Code != "E3" || second.NextCode != "" {
		t.Fatalf("second page = %+v, %v", second, err)
	}
}
