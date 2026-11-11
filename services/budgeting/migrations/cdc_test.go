package migrations_test

import (
	"context"
	"io/fs"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/budgeting/migrations"
)

func TestCDCMigrationsPublishTheOutboxAndRerunSafely(t *testing.T) {
	pool := testkit.DB(t)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1)})
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var published bool
	if err := pool.QueryRow(ctx, `select exists(select 1 from pg_publication_rel pr join pg_publication p on p.oid = pr.prpubid where p.pubname = 'passage_outbox_budgeting' and pr.prrelid = to_regclass(current_schema() || '.outbox'))`).Scan(&published); err != nil || !published {
		t.Fatalf("outbox published = %v, %v", published, err)
	}
	for _, name := range []string{"0008_cdc_publication.sql", "0009_cdc_role_grants.sql"} {
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
