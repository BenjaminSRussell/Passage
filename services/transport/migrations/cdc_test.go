package migrations_test

import (
	"context"
	"io/fs"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/transport/migrations"
)

func TestCDCMigrationsPublishTheOutboxAndRerunSafely(t *testing.T) {
	pool := testkit.DB(t)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1)})
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var published bool
	if err := pool.QueryRow(ctx, `select exists(select 1 from pg_publication_tables where pubname = 'passage_outbox_transport' and schemaname = current_schema() and tablename = 'outbox')`).Scan(&published); err != nil || !published {
		t.Fatalf("outbox published = %v, %v", published, err)
	}
	for _, name := range []string{"0011_cdc_publication.sql", "0012_cdc_role_grants.sql"} {
		sql, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, err := pool.Exec(ctx, string(sql)); err != nil {
				t.Fatalf("%s is not re-runnable: %v", name, err)
			}
		}
	}
}
