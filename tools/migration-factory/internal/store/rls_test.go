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

const insertParallel = `insert into parallel_run (tenant_id, parallel_run_id, period, source_system, documents_replayed, revenue, explained_max_pct,
	input_hash, state, unexplained_count, unexplained_amount, explained_amount) values ($1, $2, '2026-11', 'AX2012', 1, 100, 0.1, 'h', 'REVIEW', 1, 5, 0)`

// insertParallelWithHash is insertParallel, for another period, for probes whose insert takes a third argument.
const insertParallelWithHash = `insert into parallel_run (tenant_id, parallel_run_id, period, source_system, documents_replayed, revenue, explained_max_pct,
	input_hash, state, unexplained_count, unexplained_amount, explained_amount) values ($1, $2, '2026-10', 'AX2012', 1, 100, 0.1, $3::text, 'REVIEW', 1, 5, 0)`

// insertXrefForLine creates the run and the xref row an opening line references; $3 is the xref id.
const insertXrefForLine = `with r as (insert into pack_run (tenant_id, run_id, pack, source_system, source_company, cutoff_ts, input_hash, status,
	records_in, records_loaded, records_quarantined) values ($1, $2, 'project_wip', 'AX2012', 'USMF', now(), 'h', 'LOADED', 1, 1, 0) returning run_id)
	insert into legacy_xref (tenant_id, run_id, xref_id, source_system, source_company, source_table, source_id, passage_entity, passage_id, amount, currency)
	select $1, run_id, $3::uuid, 'AX2012', 'USMF', 'project_wip', $3::text, 'project_opening_line', $3::uuid, 10, 'USD' from r`

// insertXrefForBalance creates the gl_balances run and the xref row an opening balance references; $3 is the xref id.
const insertXrefForBalance = `with r as (insert into pack_run (tenant_id, run_id, pack, source_system, source_company, cutoff_ts, input_hash, status,
	records_in, records_loaded, records_quarantined) values ($1, $2, 'gl_balances', 'AX2012', 'USMF', now(), 'h', 'LOADED', 1, 1, 0) returning run_id)
	insert into legacy_xref (tenant_id, run_id, xref_id, source_system, source_company, source_table, source_id, passage_entity, passage_id, amount, currency)
	select $1, run_id, $3::uuid, 'AX2012', 'USMF', 'gl_balances', $3::text, 'gl_opening_balance', $3::uuid, 10, 'USD' from r`

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
			Table:  "pack_run",
			Insert: insertRun,
			Args:   func(tenantID, _ string) []any { return []any{tenantID, ids.New()} },
		},
		{
			Table:  "control_total",
			Before: insertRunWithHash,
			Insert: `insert into control_total (tenant_id, run_id, total_id, seq, name, key_json, source_value, target_value, delta, status)
				values ($1, $2, $3, 1, 'open_items.count', '{}', 1, 1, 0, 'MATCH')`,
			Args: func(tenantID, _ string) []any { return []any{tenantID, ids.New(), ids.New()} },
		},
		{
			Table:  "pack_quarantine",
			Before: insertRun,
			Insert: `insert into pack_quarantine (tenant_id, run_id, seq, legacy_id, rule, reason, record)
				values ($1, $2, 1, 'OI-1', 'REFERENTIAL', 'party missing', '{}')`,
			Args: func(tenantID, _ string) []any { return []any{tenantID, ids.New()} },
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
			Table:  "parallel_run",
			Insert: insertParallel,
			Args:   func(tenantID, _ string) []any { return []any{tenantID, ids.New()} },
		},
		{
			Table:  "variance",
			Before: insertParallelWithHash,
			Insert: `insert into variance (tenant_id, parallel_run_id, variance_id, seq, area, key_text, legacy_value, passage_value, delta, class)
				values ($1, $2, $3, 1, 'TB', 'USMF|1100|USD|2026-11', 10, 15, 5, 'UNEXPLAINED')`,
			Args: func(tenantID, _ string) []any { return []any{tenantID, ids.New(), ids.New()} },
		},
		{
			Table:  "project_opening_line",
			Before: insertXrefForLine,
			Insert: `insert into project_opening_line (tenant_id, run_id, legacy_xref_id, line_id, source_system, source_company, legacy_id, project_id, category,
				transaction_date, qty, cost_amount, sales_amount, currency, billing_status, wip_cost, wip_sales)
				values ($1, $2, $3::uuid, $3::uuid, 'AX2012', 'USMF', 'PT-1', 'PRJ-1', 'HOUR', '2026-12-01', 8, 400, 800, 'USD', 'CHARGEABLE_UNBILLED', 400, 800)`,
			Args:     func(tenantID, _ string) []any { return []any{tenantID, ids.New(), ids.New()} },
			NoUpdate: true,
		},
		{
			Table:  "gl_opening_balance",
			Before: insertXrefForBalance,
			Insert: `insert into gl_opening_balance (tenant_id, run_id, legacy_xref_id, balance_id, source_system, source_company, legacy_id, account,
				dimensions, currency, balance)
				values ($1, $2, $3::uuid, $3::uuid, 'AX2012', 'USMF', 'GL-1', '1100', 'USMF-100', 'USD', 10)`,
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
		{
			Table:  "rekey_line",
			Before: insertPlan,
			Insert: `insert into rekey_line (tenant_id, plan_id, kind, seq, entity, document_ref, line, posted_at, account, currency, amount)
				values ($1, $2, 'JOURNAL', 1, 'USMF', 'JV-1', 1, now(), '1100', 'USD', 10)`,
			Args:     func(tenantID, _ string) []any { return []any{tenantID, ids.New()} },
			NoUpdate: true,
		},
		{
			Table:  "cutover_decision",
			Before: insertPlan,
			Insert: `insert into cutover_decision (tenant_id, plan_id, seq, decision, approvers, reason, checklist, plan_version, decided_by)
				values ($1, $2, 1, 'NO_GO', '{cfo}', 'not ready', '[]', 2, 'cfo')`,
			Args:     func(tenantID, _ string) []any { return []any{tenantID, ids.New()} },
			NoUpdate: true,
		},
		{
			Table:  "cutover_go_approval",
			Before: insertPlan,
			Insert: `insert into cutover_go_approval (tenant_id, plan_id, round, approver, attestations, plan_version)
				values ($1, $2, 1, 'cfo', '[]', 2)`,
			Args:     func(tenantID, _ string) []any { return []any{tenantID, ids.New()} },
			NoUpdate: true,
		},
		{
			Table:  "cutover_rollback_approval",
			Before: insertPlan,
			Insert: `insert into cutover_rollback_approval (tenant_id, plan_id, round, approver, plan_version)
				values ($1, $2, 1, 'alice', 2)`,
			Args:     func(tenantID, _ string) []any { return []any{tenantID, ids.New()} },
			NoUpdate: true,
		},
	})
}

// legacy_xref, project opening lines, GL opening balances and re-keying lines are append-only
// (9.49.4, FR-49.12, FR-49.1, FR-49.9): the application role cannot change or delete a row, and it deletes nothing else
// either.
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
	for _, stmt := range []string{`update legacy_xref set amount = 0`, `delete from legacy_xref`, `delete from pack_run`,
		`update project_opening_line set cost_amount = 0`, `delete from project_opening_line`, `delete from variance`, `delete from parallel_run`,
		`delete from control_total`, `delete from pack_quarantine`, `update rekey_line set amount = 0`, `delete from rekey_line`, `update cutover_decision set reason = 'x'`, `delete from cutover_decision`,
		`update cutover_go_approval set approver = 'x'`, `delete from cutover_go_approval`,
		`update cutover_rollback_approval set approver = 'x'`, `delete from cutover_rollback_approval`,
		`update gl_opening_balance set balance = 0`, `delete from gl_opening_balance`} {
		err := inTenant(func(tx pgx.Tx) error { _, err := tx.Exec(ctx, stmt); return err })
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%s as passage_app: %v, want permission denied", stmt, err)
		}
	}
}
