package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/migrations"
)

const insertRun = `insert into pack_run (tenant_id, run_id, pack, source_system, source_company, cutoff_ts, input_hash, status,
	records_in, records_loaded, records_quarantined) values ($1, $2, 'open_items', 'AX2012', 'USMF', now(), 'h', 'LOADED', 1, 1, 0)`

// insertRunWithHash is insertRun for probes whose insert takes a third argument: it binds that
// argument as the input hash so the parent statement uses every parameter.
const insertRunWithHash = `insert into pack_run (tenant_id, run_id, pack, source_system, source_company, cutoff_ts, input_hash, status,
	records_in, records_loaded, records_quarantined) values ($1, $2, 'open_items', 'AX2012', 'USMF', now(), $3::text, 'LOADED', 1, 1, 0)`

const insertPlan = `insert into cutover_plan (tenant_id, plan_id, t0, packs, entities, abort_offset_min)
	values ($1, $2, now(), '{open_items}', '{USMF}', 2160)`

func TestAppRole_CrossTenantRLS(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	testkit.AssertTenantRLS(t, admin)
	testkit.CrossTenant(t, app, []testkit.RLSCase{
		testkit.OutboxCase(),
		testkit.IdempotencyCase(),
		{
			Table:    "pack_run",
			Insert:   insertRun,
			Args:     func(tenantID, _ string) []any { return []any{tenantID, ids.New()} },
			NoUpdate: true,
		},
		{
			Table:  "control_total",
			Before: insertRunWithHash,
			Insert: `insert into control_total (tenant_id, run_id, total_id, seq, name, key_json, source_value, target_value, delta, status)
				values ($1, $2, $3, 1, 'open_items.count', '{}', 1, 1, 0, 'MATCH')`,
			Args:     func(tenantID, _ string) []any { return []any{tenantID, ids.New(), ids.New()} },
			NoUpdate: true,
		},
		{
			Table:  "pack_quarantine",
			Before: insertRun,
			Insert: `insert into pack_quarantine (tenant_id, run_id, seq, legacy_id, rule, reason, record)
				values ($1, $2, 1, 'OI-1', 'REFERENTIAL', 'party missing', '{}')`,
			Args:     func(tenantID, _ string) []any { return []any{tenantID, ids.New()} },
			NoUpdate: true,
		},
		{
			Table:  "legacy_xref",
			Before: insertRunWithHash,
			Insert: `insert into legacy_xref (tenant_id, run_id, xref_id, source_system, source_company, source_table, source_id, passage_entity, passage_id, amount, currency)
				values ($1, $2, $3::uuid, 'AX2012', 'USMF', 'open_items', $3::text, 'open_item', $3::uuid, 10, 'USD')`,
			Args:     func(tenantID, _ string) []any { return []any{tenantID, ids.New(), ids.New()} },
			NoUpdate: true,
		},
		{
			Table:  "cutover_plan",
			Insert: insertPlan,
			Args:   func(tenantID, _ string) []any { return []any{tenantID, ids.New()} },
		},
		{
			Table:  "cutover_step",
			Before: insertPlan,
			Insert: `insert into cutover_step (tenant_id, plan_id, seq, offset_minutes, name, owner, planned_duration_min, status)
				values ($1, $2, 1, 0, 'Extract transactional packs', 'Migration engineer', 180, 'PENDING')`,
			Args: func(tenantID, _ string) []any { return []any{tenantID, ids.New()} },
		},
	})
}

// legacy_xref is append-only (9.49.4): the application role cannot change or delete a row.
func TestAppRole_LegacyXrefIsAppendOnly(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tenantID, runID := ids.New(), ids.New()
	inTenant := func(fn func(tx pgx.Tx) error) error {
		return dbx.WithTenantTx(testkit.Ctx(tenantID), app, fn)
	}
	if err := inTenant(func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, insertRun, tenantID, runID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `insert into legacy_xref (tenant_id, run_id, xref_id, source_system, source_company, source_table, source_id, passage_entity, passage_id, amount, currency)
			values ($1, $2, $3, 'AX2012', 'USMF', 'open_items', 'OI-1', 'open_item', $3, 10, 'USD')`, tenantID, runID, ids.New())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{`update legacy_xref set amount = 0`, `delete from legacy_xref`, `update pack_run set status = 'RECONCILED'`} {
		err := inTenant(func(tx pgx.Tx) error { _, err := tx.Exec(ctx, stmt); return err })
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%s as passage_app: %v, want permission denied", stmt, err)
		}
	}
}
