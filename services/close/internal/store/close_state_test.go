package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
	"github.com/BenjaminSRussell/passage/services/close/internal/store"
	"github.com/BenjaminSRussell/passage/services/close/migrations"
)

const (
	entityB    = "00000000-0000-0000-0000-0000000000c2"
	templateID = "00000000-0000-0000-0000-0000000000d1"
	runID      = "00000000-0000-0000-0000-0000000000e1"
)

type seedRow struct {
	table, insert string
	args          []int
}

var closeStateRows = []seedRow{
	{"close_template", `insert into close_template(tenant_id, id, name, updated_by) values($1, $2, 'month-end', 'u')`, []int{1, 2}},
	{"close_run", `insert into close_run(tenant_id, id, template_id, template_version, fiscal_period_id, period_end, legal_entity_ids, status, created_by) values($1, $2, $3, 1, $4, '2026-01-31', array[$5::uuid], 'in_progress', 'u')`, []int{1, 6, 3, 4, 5}},
	{"close_template_task", `insert into close_template_task(tenant_id, template_id, task_key, name, module, assignee_role, due_offset_days, position) values($1, $3, $2::text, 'n', 'gl', 'controller', 1, 1)`, []int{1, 2, 3}},
	{"close_task", `insert into close_task(tenant_id, id, run_id, legal_entity_id, task_key, module, name, assignee_role, due_at, status) values($1, $2, $3, $4, 'k', 'gl', 'n', 'controller', now(), 'not_started')`, []int{1, 2, 6, 5}},
	{"period_status_change", `insert into period_status_change(tenant_id, id, legal_entity_id, ledger_id, fiscal_period_id, module, from_status, to_status, actor) values($1, $2, $3, $4, $5, 'gl', 'open', 'closed', 'u')`, []int{1, 2, 5, 7, 4}},
	{"period_reopen_request", `insert into period_reopen_request(tenant_id, id, legal_entity_id, ledger_id, fiscal_period_id, module, reason, requested_by, required_approvals, status, expires_days) values($1, $2, $3, $4, $5, 'gl', 'ten chars!!', 'u', 2, 'pending', 3)`, []int{1, 2, 5, 7, 4}},
	{"outbox", `insert into outbox(id, tenant_id, aggregate_type, aggregate_id, type, payload) values($2::text, $1, 'close_run', 'a', 'dev.passage.close.run.created.v1', '{}')`, []int{1, 2}},
	{"idempotency_keys", `insert into idempotency_keys(tenant_id, key, request_hash) values($1, $2::text, 'h')`, []int{1, 2}},
}

func insertRow(ctx context.Context, exec func(context.Context, string, ...any) error, row seedRow, tenantID, id, entity string) error {
	all := []any{nil, tenantID, id, templateID, periodID, entity, runID, ledgerID}
	args := make([]any, len(row.args))
	for i, n := range row.args {
		args[i] = all[n]
	}
	return exec(ctx, row.insert, args...)
}

func adminExec(pool *pgxpool.Pool) func(context.Context, string, ...any) error {
	return func(ctx context.Context, sql string, args ...any) error {
		_, err := pool.Exec(ctx, sql, args...)
		return err
	}
}

func migrated(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return admin, app
}

func visibleRows(t *testing.T, ctx context.Context, app *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from `+table).Scan(&n)
	})
	if err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestMigration0002_TenantIsolation(t *testing.T) {
	admin, app := migrated(t)
	for _, row := range closeStateRows {
		if err := insertRow(context.Background(), adminExec(admin), row, testkit.TenantID(1), templateID, entityID); err != nil {
			t.Fatalf("seed %s: %v", row.table, err)
		}
	}
	for _, row := range closeStateRows {
		t.Run(row.table, func(t *testing.T) {
			if got := visibleRows(t, principal(1), app, row.table); got != 1 {
				t.Fatalf("owner tenant sees %d rows, want 1", got)
			}
			if got := visibleRows(t, principal(2), app, row.table); got != 0 {
				t.Fatalf("second tenant sees %d rows, want 0", got)
			}
			err := dbx.WithTenantTx(principal(2), app, func(tx pgx.Tx) error {
				exec := func(ctx context.Context, sql string, args ...any) error {
					_, err := tx.Exec(ctx, sql, args...)
					return err
				}
				return insertRow(context.Background(), exec, row, testkit.TenantID(1), "00000000-0000-0000-0000-0000000000ff", entityID)
			})
			if err == nil || !strings.Contains(err.Error(), "row-level security") {
				t.Fatalf("second tenant writing tenant 1 rows: got %v, want row-level security violation", err)
			}
		})
	}
}

func TestMigration0002_LegalEntityScope(t *testing.T) {
	admin, app := migrated(t)
	for _, row := range closeStateRows {
		if err := insertRow(context.Background(), adminExec(admin), row, testkit.TenantID(1), templateID, entityID); err != nil {
			t.Fatalf("seed %s: %v", row.table, err)
		}
	}
	if _, err := admin.Exec(context.Background(), `update period_reopen_request set status = 'approved'`); err != nil {
		t.Fatal(err)
	}
	for _, row := range closeStateRows {
		if row.table == "close_task" || row.table == "period_status_change" || row.table == "period_reopen_request" {
			if err := insertRow(context.Background(), adminExec(admin), row, testkit.TenantID(1), "00000000-0000-0000-0000-0000000000f2", entityB); err != nil {
				t.Fatalf("seed second entity %s: %v", row.table, err)
			}
		}
	}
	scoped := func(entity string) context.Context {
		return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entity})
	}
	for _, table := range []string{"close_task", "period_status_change", "period_reopen_request"} {
		t.Run(table, func(t *testing.T) {
			if got := visibleRows(t, scoped(entityID), app, table); got != 1 {
				t.Fatalf("entity A principal sees %d rows, want 1", got)
			}
			if got := visibleRows(t, scoped(entityB), app, table); got != 1 {
				t.Fatalf("entity B principal sees %d rows, want 1", got)
			}
			if got := visibleRows(t, principal(1), app, table); got != 2 {
				t.Fatalf("unscoped principal sees %d rows, want 2", got)
			}
		})
	}
}

func TestMigration0002_CheckConstraints(t *testing.T) {
	admin, _ := migrated(t)
	ctx := context.Background()
	if err := insertRow(ctx, adminExec(admin), closeStateRows[0], testkit.TenantID(1), templateID, entityID); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{-11, 31} {
		_, err := admin.Exec(ctx, `insert into close_template_task(tenant_id, template_id, task_key, name, module, assignee_role, due_offset_days, position) values($1, $2, 'k', 'n', 'gl', 'r', $3, 1)`, testkit.TenantID(1), templateID, offset)
		if err == nil {
			t.Fatalf("due_offset_days %d must be rejected", offset)
		}
	}
	if _, err := admin.Exec(ctx, `insert into close_template(tenant_id, id, name, updated_by) values($1, $2, 'month-end', 'u')`, testkit.TenantID(1), runID); err == nil {
		t.Fatal("template names are unique per tenant")
	}
	if _, err := admin.Exec(ctx, `insert into close_template(tenant_id, id, name, updated_by) values($1, $2, 'month-end', 'u')`, testkit.TenantID(2), runID); err != nil {
		t.Fatalf("the same name in another tenant is allowed: %v", err)
	}
	pending := closeStateRows[5]
	if err := insertRow(ctx, adminExec(admin), pending, testkit.TenantID(1), templateID, entityID); err != nil {
		t.Fatal(err)
	}
	if err := insertRow(ctx, adminExec(admin), pending, testkit.TenantID(1), runID, entityID); err == nil {
		t.Fatal("a second pending reopen request for the same row must be rejected")
	}
}

func TestMigration0002_StatusChangeIsAppendOnly(t *testing.T) {
	admin, app := migrated(t)
	ctx := principal(1)
	if err := insertRow(context.Background(), adminExec(admin), closeStateRows[4], testkit.TenantID(1), templateID, entityID); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{`update period_status_change set actor = 'x'`, `delete from period_status_change`} {
		err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("%s: got %v, want permission denied", stmt, err)
		}
	}
}

func TestReopenDueTenants(t *testing.T) {
	admin, app := migrated(t)
	ctx := context.Background()
	for _, row := range []struct {
		tenant, status, expiry string
	}{
		{testkit.TenantID(1), "open", "now() - interval '1 hour'"},
		{testkit.TenantID(2), "open", "now() + interval '1 day'"},
		{testkit.TenantID(3), "closed", "now() - interval '1 hour'"},
	} {
		if _, err := admin.Exec(ctx,
			`insert into period_status(tenant_id, legal_entity_id, ledger_id, fiscal_period_id, module, status, reopen_expires_at, changed_by) values($1,$2,$3,$4,'gl',$5,`+row.expiry+`,'t')`,
			row.tenant, entityID, ledgerID, periodID, row.status); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := app.Query(ctx, `select * from close_reopen_due_tenants()`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var due []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		due = append(due, id)
	}
	if len(due) != 1 || due[0] != testkit.TenantID(1) {
		t.Fatalf("due tenants = %v, want only tenant 1", due)
	}
	var visible int
	if err := app.QueryRow(ctx, `select count(*) from period_status`).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("app role without a tenant context must see no period_status rows: %d, %v", visible, err)
	}
}

func TestDefinerFunctionsIgnoreHostileOverloads(t *testing.T) {
	admin, app := migrated(t)
	ctx := context.Background()
	for _, stmt := range []string{
		`create function now(variadic anyarray default null) returns timestamptz language sql as $$ select '-infinity'::timestamptz $$`,
		`create function now() returns timestamptz language sql as $$ select '-infinity'::timestamptz $$`,
		`create function cardinality(anyelement) returns int language sql as $$ select 0 $$`,
		`create function "any"(anyelement) returns bool language sql as $$ select false $$`,
		`create function pg_temp.now() returns timestamptz language sql as $$ select '-infinity'::timestamptz $$`,
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	for i, row := range []struct{ status, expiry string }{
		{"open", "now() - interval '1 hour'"},
		{"closed", "now() - interval '1 hour'"},
		{"on_hold", "now() + interval '1 day'"},
	} {
		if _, err := admin.Exec(ctx,
			`insert into period_status(tenant_id, legal_entity_id, ledger_id, fiscal_period_id, module, status, reopen_expires_at, changed_by) values($1,$2,$3,$4,'gl',$5,`+row.expiry+`,'t')`,
			testkit.TenantID(i+1), entityID, ledgerID, periodID, row.status); err != nil {
			t.Fatal(err)
		}
	}
	var tracked int
	if err := admin.QueryRow(ctx, `select count(*) from reopen_expiry`).Scan(&tracked); err != nil || tracked != 2 {
		t.Fatalf("trigger must track only open/on_hold rows: %d, %v", tracked, err)
	}
	var due []string
	rows, err := app.Query(ctx, `select * from close_reopen_due_tenants()`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		due = append(due, id)
	}
	if len(due) != 1 || due[0] != testkit.TenantID(1) {
		t.Fatalf("due tenants = %v, want only tenant 1 despite hostile now()", due)
	}
}

func TestDefinerFunctionsPinSearchPath(t *testing.T) {
	admin, _ := migrated(t)
	rows, err := admin.Query(context.Background(), `select proname, proconfig[1] from pg_proc
		where proname in ('close_reopen_due_tenants', 'period_status_track_reopen_expiry') and prosecdef
		  and pronamespace = current_schema()::regnamespace`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var name, cfg string
		if err := rows.Scan(&name, &cfg); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(cfg, "search_path=pg_catalog, ") || !strings.HasSuffix(cfg, ", pg_temp") {
			t.Fatalf("%s search_path = %q, want pg_catalog first and pg_temp last", name, cfg)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("found %d definer functions, want 2", n)
	}
}

func idempotentStore(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool, *store.Postgres) {
	admin, app := migrated(t)
	return admin, app, store.New(app)
}

func writeOutbox(id string) func(tx pgx.Tx) (int, any, error) {
	return func(tx pgx.Tx) (int, any, error) {
		_, err := tx.Exec(context.Background(),
			`insert into outbox(id, tenant_id, aggregate_type, aggregate_id, type, payload) values($1, current_setting('passage.tenant_id', true)::uuid, 'close_run', 'a', 'dev.passage.close.run.created.v1', '{}')`, id)
		return 201, map[string]string{"id": id}, err
	}
}

func countOutbox(t *testing.T, admin *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIdempotent_ReplayCreatesNothing(t *testing.T) {
	admin, _, s := idempotentStore(t)
	ctx := principal(1)
	hash := store.HashRequest("POST", "/v1/close/runs", []byte(`{"a":1}`))
	first, err := s.Idempotent(ctx, "key-1", hash, writeOutbox("evt-1"))
	if err != nil || first.Replayed || first.Status != 201 {
		t.Fatalf("first call: %+v, %v", first, err)
	}
	var ran atomic.Bool
	second, err := s.Idempotent(ctx, "key-1", hash, func(pgx.Tx) (int, any, error) {
		ran.Store(true)
		return 500, nil, nil
	})
	if err != nil || !second.Replayed || second.Status != 201 || strings.ReplaceAll(string(second.Body), " ", "") != `{"id":"evt-1"}` {
		t.Fatalf("replay: %+v, %v", second, err)
	}
	if ran.Load() || countOutbox(t, admin) != 1 {
		t.Fatalf("replay must not run the write: ran=%v outbox=%d", ran.Load(), countOutbox(t, admin))
	}
}

func TestIdempotent_DifferentHashIsReuse(t *testing.T) {
	admin, _, s := idempotentStore(t)
	ctx := principal(1)
	if _, err := s.Idempotent(ctx, "key-1", store.HashRequest("POST", "/p", []byte(`{"a":1}`)), writeOutbox("evt-1")); err != nil {
		t.Fatal(err)
	}
	_, err := s.Idempotent(ctx, "key-1", store.HashRequest("POST", "/p", []byte(`{"a":2}`)), writeOutbox("evt-2"))
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeIdempotencyReuse {
		t.Fatalf("got %v, want %s", err, domain.CodeIdempotencyReuse)
	}
	if countOutbox(t, admin) != 1 {
		t.Fatal("a rejected reuse must write nothing")
	}
	for _, c := range []struct{ key, hash string }{{"", "h"}, {"k", ""}} {
		_, err := s.Idempotent(ctx, c.key, c.hash, writeOutbox("evt"))
		if !errors.As(err, &de) || de.Code != domain.CodeValidation {
			t.Fatalf("key=%q hash=%q: got %v, want %s", c.key, c.hash, err, domain.CodeValidation)
		}
	}
	other, err := s.Idempotent(principal(2), "key-1", store.HashRequest("POST", "/p", []byte(`{"a":1}`)), writeOutbox("evt-3"))
	if err != nil || other.Replayed {
		t.Fatalf("tenant 2 must not replay tenant 1's key: %+v, %v", other, err)
	}
}

func TestIdempotent_FailedWriteLeavesNoKey(t *testing.T) {
	admin, _, s := idempotentStore(t)
	ctx := principal(1)
	hash := store.HashRequest("POST", "/p", nil)
	boom := errors.New("boom")
	_, err := s.Idempotent(ctx, "key-1", hash, func(tx pgx.Tx) (int, any, error) {
		if _, _, err := writeOutbox("evt-1")(tx); err != nil {
			return 0, nil, err
		}
		return 0, nil, boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want boom", err)
	}
	var keys int
	if err := admin.QueryRow(context.Background(), `select count(*) from idempotency_keys`).Scan(&keys); err != nil || keys != 0 || countOutbox(t, admin) != 0 {
		t.Fatalf("failed write must roll back key and rows: keys=%d outbox=%d err=%v", keys, countOutbox(t, admin), err)
	}
	if out, err := s.Idempotent(ctx, "key-1", hash, writeOutbox("evt-1")); err != nil || out.Replayed {
		t.Fatalf("retry after failure must run: %+v, %v", out, err)
	}
}

func TestIdempotent_LockedAndExpiredKeys(t *testing.T) {
	admin, _, s := idempotentStore(t)
	ctx := principal(1)
	hash := store.HashRequest("POST", "/p", nil)
	seed := func(key, locked, expires string) {
		t.Helper()
		if _, err := admin.Exec(context.Background(),
			`insert into idempotency_keys(tenant_id, key, request_hash, locked_until, expires_at) values($1,$2,$3,`+locked+`,`+expires+`)`,
			testkit.TenantID(1), key, hash); err != nil {
			t.Fatal(err)
		}
	}
	seed("in-flight", "now() + interval '20 seconds'", "now() + interval '1 hour'")
	_, err := s.Idempotent(ctx, "in-flight", hash, writeOutbox("evt-1"))
	var inFlight *store.ErrInFlight
	if !errors.As(err, &inFlight) || inFlight.RetryAfterSeconds < 1 {
		t.Fatalf("got %v, want ErrInFlight with a retry delay", err)
	}
	seed("abandoned", "now() - interval '1 minute'", "now() + interval '1 hour'")
	if out, err := s.Idempotent(ctx, "abandoned", hash, writeOutbox("evt-2")); err != nil || out.Replayed {
		t.Fatalf("an abandoned lock is reclaimed: %+v, %v", out, err)
	}
	seed("expired", "null", "now() - interval '1 minute'")
	if out, err := s.Idempotent(ctx, "expired", store.HashRequest("POST", "/other", nil), writeOutbox("evt-3")); err != nil || out.Replayed {
		t.Fatalf("an expired key is treated as absent: %+v, %v", out, err)
	}
}

func TestIdempotent_ConcurrentDuplicatesRunOnce(t *testing.T) {
	admin, _, s := idempotentStore(t)
	hash := store.HashRequest("POST", "/p", nil)
	var runs atomic.Int32
	var wg sync.WaitGroup
	results := make([]store.Outcome, 4)
	errs := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = s.Idempotent(principal(1), "dup", hash, func(tx pgx.Tx) (int, any, error) {
				runs.Add(1)
				return writeOutbox("evt-1")(tx)
			})
		}()
	}
	wg.Wait()
	replayed := 0
	for i := range results {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if results[i].Replayed {
			replayed++
		}
	}
	if runs.Load() != 1 || replayed != 3 || countOutbox(t, admin) != 1 {
		t.Fatalf("runs=%d replayed=%d outbox=%d, want 1, 3, 1", runs.Load(), replayed, countOutbox(t, admin))
	}
}
