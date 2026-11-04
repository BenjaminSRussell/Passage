package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/ac1load"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/internal/testdb"
	"github.com/BenjaminSRussell/passage/services/migration/migrations"
)

func ac1Target(t *testing.T) (*store.Postgres, *ac1load.PGTarget, func(string) error) {
	t.Helper()
	pool := testkit.DB(t)
	ctx := context.Background()
	testdb.Fresh(t, pool)
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := ac1load.SetupTarget(ctx, pool); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ac1load.DropTarget(context.Background(), pool) })
	exec := func(sql string) error { _, err := pool.Exec(ctx, sql); return err }
	return store.New(pool), ac1load.NewPGTarget(pool), exec
}

func TestPostgres_AC1FullSizeLoadMeetsTimeBudgetAndReconciles(t *testing.T) {
	if testing.Short() {
		t.Skip("full-size AC1 Postgres load skipped under -short")
	}
	s, target, _ := ac1Target(t)
	got, err := ac1load.Run(principalContext(1), s.Xref(), target, ac1load.FullSize)
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK() {
		t.Fatalf("AC1 load missed the budget or failed reconciliation: elapsed=%s", got.Elapsed)
	}
	for _, e := range got.Entities {
		n := ac1load.FullSize.Count(e.Entity)
		if e.Report.Created != n || e.Report.Reconciliation == nil || !e.Report.Reconciliation.OK() || e.SourceHash != e.TargetHash {
			t.Fatalf("%s: created=%d recon=%+v hashes %s/%s", e.Entity, e.Report.Created, e.Report.Reconciliation, e.SourceHash, e.TargetHash)
		}
		again := got.Rerun[e.Entity]
		if again.Created != 0 || again.Updated != 0 || again.Unchanged != n {
			t.Fatalf("%s: re-run created=%d updated=%d unchanged=%d", e.Entity, again.Created, again.Updated, again.Unchanged)
		}
		if e.Report.Status != domain.StatusSucceeded {
			t.Fatalf("%s: status=%s", e.Entity, e.Report.Status)
		}
	}
}

func TestPostgres_AC1ReconciliationFailsWhenTargetRowIsMutatedOrDropped(t *testing.T) {
	vols := ac1load.Volumes{Customers: 30, Vendors: 5, Items: 10, BOMs: 3}
	cases := map[string]string{
		"mutated": `update ac1.target set fields = jsonb_set(fields, '{name}', '"tampered"') where entity = 'customer' and source_key = 'C000007'`,
		"dropped": `delete from ac1.target where entity = 'customer' and source_key = 'C000007'`,
	}
	for name, statement := range cases {
		t.Run(name, func(t *testing.T) {
			s, target, exec := ac1Target(t)
			ctx := principalContext(1)
			if _, err := ac1load.Run(ctx, s.Xref(), target, vols); err != nil {
				t.Fatal(err)
			}
			if err := exec(statement); err != nil {
				t.Fatal(err)
			}
			m, err := ac1load.Mapping("customer")
			if err != nil {
				t.Fatal(err)
			}
			rep, srcHash, tgtHash, err := ac1load.Reconcile(ctx, "customer", m, ac1load.Dataset(vols)["customer"], target)
			if err != nil {
				t.Fatal(err)
			}
			if rep.OK() || srcHash == tgtHash {
				t.Fatalf("tampered target reconciled: %+v source=%s target=%s", rep, srcHash, tgtHash)
			}
		})
	}
}
