package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
	"github.com/BenjaminSRussell/passage/services/print/internal/store"
	"github.com/BenjaminSRussell/passage/services/print/migrations"
)

func TestGrants_AppRoleCannotDeletePrintersOrRewriteOutbox(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := tenantCtx(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`delete from print_device`,
		`select count(*) from outbox`,
		`update outbox set type = type`,
	} {
		err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%q = %v; want insufficient_privilege 42501", stmt, err)
		}
	}
}

func TestGrants_JobLifecycleAndPurgeWorkAsAppRole(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := tenantCtx(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	job := queuedJob(t, s, ctx, "grants-job")
	if err := job.Complete(domain.Outcome{Printer: domain.Printer{ID: ids.New(), Code: "DOCK-1"}, Attempts: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteJob(ctx, job); err != nil {
		t.Fatalf("complete as app role: %v", err)
	}
	if deleted, err := s.PurgeJobs(ctx, time.Now().Add(time.Hour)); err != nil || deleted != 1 {
		t.Fatalf("purge as app role = %d, %v; want 1", deleted, err)
	}
}
