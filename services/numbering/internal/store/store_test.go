package store_test

import (
	"context"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/numbering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/numbering/internal/store"
	"github.com/BenjaminSRussell/passage/services/numbering/migrations"
)

func principal(n int) tenant.Principal {
	return tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: testkit.TenantID(9), UserID: "user-test"}
}

func migrated(t *testing.T) (*store.Postgres, context.Context) {
	t.Helper()
	pool := testkit.DB(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return store.New(pool), ctx
}

func TestPostgresCreateAllocateIdempotencyAndTenantIsolation(t *testing.T) {
	s, ctx := migrated(t)
	in := store.Sequence{Code: "INV", Name: "Invoices", Format: "INV-{######}", LegalEntityID: testkit.TenantID(9), Mode: domain.ModeGapFree, StartFrom: 1}
	first, err := s.Create(ctx, in, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.Create(ctx, in, "key-1")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("idempotent create: %+v, %v", replay, err)
	}
	got, err := s.Allocate(ctx, "INV", "alloc-1")
	if err != nil || got.Value != 1 || got.Formatted != "INV-000001" {
		t.Fatalf("first allocate = %+v, %v", got, err)
	}
	again, err := s.Allocate(ctx, "INV", "alloc-1")
	if err != nil || again.Value != 1 {
		t.Fatalf("idempotent allocate = %+v, %v", again, err)
	}
	second, err := s.Allocate(ctx, "INV", "alloc-2")
	if err != nil || second.Value != 2 {
		t.Fatalf("second allocate = %+v, %v", second, err)
	}
	other := tenant.With(context.Background(), principal(2))
	if _, err := s.Get(other, "INV"); err == nil {
		t.Fatal("tenant 2 must not see tenant 1 sequence")
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresGapFreeTenThousandConcurrent(t *testing.T) {
	s, ctx := migrated(t)
	in := store.Sequence{Code: "INV", Name: "Invoices", Format: "INV-{######}", LegalEntityID: testkit.TenantID(9), Mode: domain.ModeGapFree, StartFrom: 1}
	if _, err := s.Create(ctx, in, ""); err != nil {
		t.Fatal(err)
	}
	const n = 10_000
	values := make([]int64, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			got, err := s.Allocate(ctx, "INV", "")
			if err != nil {
				t.Errorf("allocate %d: %v", i, err)
				return
			}
			values[i] = got.Value
		}(i)
	}
	wg.Wait()
	seen := make([]bool, n+1)
	for _, v := range values {
		if v < 1 || v > n || seen[v] {
			t.Fatalf("bad or duplicate value %d", v)
		}
		seen[v] = true
	}
	for i := 1; i <= n; i++ {
		if !seen[i] {
			t.Fatalf("gap at %d", i)
		}
	}
}

func TestMigrationsApplyWithRLS(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var currentUser string
	var roleSuper, roleBypass bool
	metadataErr := pool.QueryRow(ctx, `select current_user, r.rolsuper, r.rolbypassrls from pg_roles r where r.rolname = current_user`).Scan(&currentUser, &roleSuper, &roleBypass)
	if metadataErr != nil || currentUser != "passage_app" || roleSuper || roleBypass {
		t.Fatalf("RLS test must use non-bypass passage_app role: user=%q super=%v bypass=%v err=%v", currentUser, roleSuper, roleBypass, metadataErr)
	}
	for _, table := range []string{"number_sequence", "number_sequence_state", "number_allocation", "outbox"} {
		var enabled, forced bool
		err := pool.QueryRow(ctx, `select relrowsecurity, relforcerowsecurity from pg_class where relname = $1 and relnamespace = current_schema()::regnamespace`, table).Scan(&enabled, &forced)
		if err != nil || !enabled || !forced {
			t.Errorf("%s: rls enabled=%v forced=%v err=%v", table, enabled, forced, err)
		}
	}
	app := store.New(pool)
	in := store.Sequence{Code: "INV", Name: "Invoices", Format: "INV-{######}", LegalEntityID: testkit.TenantID(9), Mode: domain.ModeGapFree, StartFrom: 1}
	if _, err := app.Create(ctx, in, "rls-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Allocate(ctx, "INV", "rls-alloc"); err != nil {
		t.Fatal(err)
	}
	other := tenant.With(context.Background(), principal(2))
	if _, err := app.Get(other, "INV"); err == nil {
		t.Fatal("tenant 2 must not see tenant 1 sequence through the app role")
	}
}
