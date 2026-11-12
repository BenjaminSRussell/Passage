package main

import (
	"context"
	"os"
	"testing"
)

// TestPgInspectorAgainstPostgres runs the doctor's real Postgres queries when PASSAGE_TEST_DATABASE_URL is set.
func TestPgInspectorAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("PASSAGE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PASSAGE_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	db, err := RealDeps(fipsEnabled, fipsBuilt).OpenDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close(ctx) }()
	name, _, _, err := db.Role(ctx)
	if err != nil || name == "" {
		t.Fatalf("role = %q, %v", name, err)
	}
	if _, _, err := db.Migrations(ctx); err != nil {
		t.Fatalf("migrations: %v", err)
	}
}
