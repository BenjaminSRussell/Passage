// Package testkit gives integration tests a Postgres pool scoped to a fresh
// schema. Tests call DB(t) and are skipped when PASSAGE_TEST_DATABASE_URL is
// unset, so `go test ./...` always passes on a laptop without Postgres.
package testkit

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// EnvURL is the environment variable that enables database tests.
const EnvURL = "PASSAGE_TEST_DATABASE_URL"

// EnvAppURL is the application-role database URL used by RLS integration tests.
const EnvAppURL = "PASSAGE_TEST_APP_DATABASE_URL"

// DB returns a pool whose search_path is a throwaway schema, dropped on cleanup.
func DB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(EnvURL)
	if url == "" {
		t.Skipf("%s not set; skipping database test", EnvURL)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema := "t_" + strings.ReplaceAll(ids.New(), "-", "")
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	cfg, _ := pgxpool.ParseConfig(url)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "drop schema "+schema+" cascade")
		admin.Close()
	})
	return pool
}

// DBWithApp returns admin and application-role pools sharing a throwaway schema.
// The admin pool applies migrations; assertions and application queries use app.
func DBWithApp(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	admin := DB(t)
	appURL := os.Getenv(EnvAppURL)
	if appURL == "" {
		t.Fatalf("%s must be set for an RLS database test", EnvAppURL)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var schema string
	if err := admin.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	appConfig, err := pgxpool.ParseConfig(appURL)
	if err != nil {
		t.Fatal(err)
	}
	appConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	app, err := pgxpool.NewWithConfig(ctx, appConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		app.Close()
	})
	return admin, app
}

// Ctx returns a context carrying a test principal for tenantID with the given duties.
func Ctx(tenantID string, duties ...string) context.Context {
	p := tenant.Principal{TenantID: tenantID, LegalEntityID: "00000000-0000-0000-0000-0000000000e1", UserID: "user-test", Duties: map[string]struct{}{}}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	return tenant.With(context.Background(), p)
}

// ScopeDuties returns the duties for a test principal in legalEntity: the
// all-entity and tenant-admin duties when legalEntity is empty, none otherwise.
func ScopeDuties(legalEntity string) map[string]struct{} {
	d := map[string]struct{}{}
	if legalEntity == "" {
		d[tenant.DutyAllScope] = struct{}{}
		d[tenant.DutyTenantAdmin] = struct{}{}
	}
	return d
}

// TenantID returns a deterministic UUID for tests: 00000000-0000-0000-0000-00000000000N.
func TenantID(n int) string { return fmt.Sprintf("00000000-0000-0000-0000-%012d", n) }
