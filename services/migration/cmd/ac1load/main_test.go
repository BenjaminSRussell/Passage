package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func pools(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	owner := testkit.DB(t)
	appURL := os.Getenv(testkit.EnvAppURL)
	if appURL == "" {
		t.Fatalf("%s must be set", testkit.EnvAppURL)
	}
	app, err := pgxpool.New(context.Background(), appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return owner, app
}

func TestGuard_RefusesNonThrowawayNonEmptyDatabase(t *testing.T) {
	owner, app := pools(t)
	ctx := context.Background()
	var name string
	if err := owner.QueryRow(ctx, `select current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name == throwawayDatabase {
		t.Skipf("database is the throwaway %s", name)
	}
	if _, err := owner.Exec(ctx, `create schema ac1guard_test; create table ac1guard_test.keep (x int)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = owner.Exec(context.Background(), `drop schema ac1guard_test cascade`) })
	_, _, err := guard(ctx, owner, app)
	if err == nil || !strings.Contains(err.Error(), "refusing to drop schemas") {
		t.Fatalf("err = %v", err)
	}
}

func TestGuard_RefusesAppRoleThatBypassesRLS(t *testing.T) {
	owner, _ := pools(t)
	_, _, err := guard(context.Background(), owner, owner)
	if err == nil || !strings.Contains(err.Error(), "bypasses row-level security") {
		t.Fatalf("err = %v", err)
	}
}
