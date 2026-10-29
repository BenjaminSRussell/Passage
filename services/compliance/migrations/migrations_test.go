package migrations

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

// TestMigrationsApplyWithRLS runs against PASSAGE_TEST_DATABASE_URL and is
// skipped without it: migrations apply on an empty schema, apply again as a
// no-op, and every tenant table enables and forces row-level security.
func TestMigrationsApplyWithRLS(t *testing.T) {
	pool := testkit.DB(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := dbx.Migrate(ctx, pool, FS); err != nil {
			t.Fatalf("migrate (run %d): %v", i+1, err)
		}
	}
	testkit.AssertTenantRLS(t, pool)
}
