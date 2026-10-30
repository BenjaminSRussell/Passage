package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/batch/internal/domain"
	"github.com/BenjaminSRussell/passage/services/batch/internal/store"
	"github.com/BenjaminSRussell/passage/services/batch/migrations"
)

func TestGrants_AppRoleWorkflowAndAppendOnlyTables(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	seedDefinition(t, s, ctx)
	inst, err := s.Submit(ctx, store.Submit{Entity: "purchaserequisition", RecordID: testkit.TenantID(31), LegalEntityID: testkit.TenantID(9), Assignees: []string{testkit.TenantID(6)}})
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.ListWorkItems(ctx, testkit.TenantID(6), domain.ItemPending)
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %v, %v", items, err)
	}
	if _, err := s.Act(ctx, items[0].ID, domain.Action{Kind: "approve", Actor: testkit.TenantID(6)}); err != nil {
		t.Fatalf("act updates the work item and instance as passage_app: %v", err)
	}
	if got, err := s.GetInstance(ctx, inst.ID); err != nil || got.Status == domain.InstanceSubmitted {
		t.Fatalf("instance = %+v, %v", got, err)
	}
	if _, err := s.SaveJob(ctx, store.Job{Code: "grants-job", Handler: "synthetic.rows"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveJob(ctx, store.Job{Code: "grants-job", Handler: "synthetic.rows", PartitionCount: 2}); err != nil {
		t.Fatalf("job upsert as passage_app: %v", err)
	}
	for _, stmt := range []string{
		`update outbox set type = type`,
		`delete from outbox`,
		`select count(*) from outbox`,
		`update workflow_definition set status = status`,
		`update batch_run set job_id = job_id`,
		`update batch_run set triggered_by = triggered_by`,
		`update batch_run set legal_entity_id = legal_entity_id`,
		`update batch_run set params = params`,
		`update batch_run set service_duties = service_duties`,
		`delete from batch_run_pages`,
		`delete from workflow_work_item`,
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
