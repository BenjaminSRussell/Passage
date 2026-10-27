package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
	"github.com/BenjaminSRussell/passage/services/print/internal/store"
	"github.com/BenjaminSRussell/passage/services/print/migrations"
)

func tenantCtx(n int) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), UserID: "worker-7"})
}

func migrated(t *testing.T) (*store.Postgres, *pgxpool.Pool) {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(tenantCtx(1), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return store.New(pool), pool
}

func dock(code string, rank int) domain.Printer {
	return domain.Printer{Code: code, Name: "Dock", DeviceType: domain.DeviceZPL, Dpi: 203, Host: "10.0.4.20", Port: 9100, QueueCode: "DOCK", FailoverRank: rank}
}

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

func TestPrinterRegistry_IdempotencyAndConflicts(t *testing.T) {
	s, _ := migrated(t)
	ctx := tenantCtx(1)
	first, inserted, err := s.CreatePrinter(ctx, dock("DOCK-2", 2), "k-2")
	if err != nil || !inserted || !ids.Valid(first.ID) {
		t.Fatalf("create: %+v %v %v", first, inserted, err)
	}
	again, inserted, err := s.CreatePrinter(ctx, dock("DOCK-2", 2), "k-2")
	if err != nil || inserted || again.ID != first.ID {
		t.Fatalf("replay: %+v %v %v", again, inserted, err)
	}
	renamed := dock("DOCK-2", 2)
	renamed.Name = "Another name"
	if _, _, err := s.CreatePrinter(ctx, renamed, "k-2"); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("reused key with another body: %v", err)
	}
	if _, _, err := s.CreatePrinter(ctx, dock("DOCK-2", 3), "k-dup"); problemCode(err) != domain.CodeDuplicatePrinter {
		t.Fatalf("duplicate code: %v", err)
	}
	if _, _, err := s.CreatePrinter(ctx, dock("DOCK-9", 2), "k-rank"); problemCode(err) != domain.CodeRankTaken {
		t.Fatalf("rank taken: %v", err)
	}
	if _, _, err := s.CreatePrinter(ctx, dock("DOCK-1", 1), "k-1"); err != nil {
		t.Fatal(err)
	}
	queue, err := s.QueueDevices(ctx, "DOCK")
	if err != nil || len(queue) != 2 || queue[0].Code != "DOCK-1" || queue[1].Code != "DOCK-2" {
		t.Fatalf("queue order: %+v %v", queue, err)
	}
	if other, err := s.ListPrinters(tenantCtx(2)); err != nil || len(other) != 0 {
		t.Fatalf("tenant 2 saw %+v %v", other, err)
	}
}

func TestJobs_CompleteWritesOutboxInSameTransaction(t *testing.T) {
	s, pool := migrated(t)
	ctx := tenantCtx(1)
	job := domain.Job{ID: ids.New(), TemplateCode: domain.TemplateLP, QueueCode: "DOCK", Status: domain.JobQueued, CreatedBy: "worker-7", ZPL: "^XA^XZ"}
	created, inserted, err := s.CreateJob(ctx, store.NewJob{Job: job, RequestHash: "h1", IdempotencyKey: "job-1"})
	if err != nil || !inserted || created.Status != domain.JobQueued || created.ZPL != "^XA^XZ" {
		t.Fatalf("create: %+v %v %v", created, inserted, err)
	}
	replay, inserted, err := s.CreateJob(ctx, store.NewJob{Job: domain.Job{ID: ids.New(), TemplateCode: domain.TemplateLP, QueueCode: "DOCK", ZPL: "x"}, RequestHash: "h1", IdempotencyKey: "job-1"})
	if err != nil || inserted || replay.ID != job.ID {
		t.Fatalf("replay: %+v %v %v", replay, inserted, err)
	}
	if _, _, err := s.CreateJob(ctx, store.NewJob{Job: domain.Job{ID: ids.New(), TemplateCode: domain.TemplateLP, QueueCode: "DOCK", ZPL: "x"}, RequestHash: "h2", IdempotencyKey: "job-1"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("reused key: %v", err)
	}

	if err := created.Complete(domain.Outcome{Printer: domain.Printer{ID: ids.New(), Code: "DOCK-2"}, Attempts: 3, Failover: true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteJob(ctx, created); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteJob(ctx, created); !errors.Is(err, domain.ErrTerminal) {
		t.Fatalf("second completion: %v", err)
	}
	got, err := s.GetJob(ctx, job.ID)
	if err != nil || got.Status != domain.JobPrinted || got.PrinterCode != "DOCK-2" || got.Attempts != 3 {
		t.Fatalf("stored job: %+v %v", got, err)
	}

	var n int
	var typ, status string
	if err := pool.QueryRow(ctx, `select count(*), min(type), min(payload->'data'->>'status') from outbox where aggregate_id = $1`, job.ID).Scan(&n, &typ, &status); err != nil {
		t.Fatal(err)
	}
	if n != 1 || typ != "dev.passage.print.job.completed.v1" || status != domain.JobPrinted {
		t.Fatalf("outbox rows=%d type=%s status=%s", n, typ, status)
	}

	if _, err := s.GetJob(tenantCtx(2), job.ID); problemCode(err) != store.CodeJobNotFound {
		t.Fatalf("tenant 2 read the job: %v", err)
	}
}

func TestJobs_LogNewestFirstWithFilters(t *testing.T) {
	s, _ := migrated(t)
	ctx := tenantCtx(1)
	var made []domain.Job
	for i, outcome := range []error{nil, &domain.Error{Code: domain.CodeUnreachable}, nil} {
		j := domain.Job{ID: ids.New(), TemplateCode: domain.TemplateLP, QueueCode: "DOCK", Status: domain.JobQueued, ZPL: "^XA^XZ"}
		created, _, err := s.CreateJob(ctx, store.NewJob{Job: j, RequestHash: "h", IdempotencyKey: "log-" + string(rune('a'+i))})
		if err != nil {
			t.Fatal(err)
		}
		if err := created.Complete(domain.Outcome{Printer: domain.Printer{ID: ids.New(), Code: "DOCK-1"}, Attempts: 1}, outcome); err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteJob(ctx, created); err != nil {
			t.Fatal(err)
		}
		made = append(made, created)
	}
	all, err := s.ListJobs(ctx, store.JobFilter{Limit: 10})
	if err != nil || len(all) != 3 || all[0].ID != made[2].ID || all[2].ID != made[0].ID {
		t.Fatalf("log: %+v %v", all, err)
	}
	failed, err := s.ListJobs(ctx, store.JobFilter{Status: domain.JobFailed, Limit: 10})
	if err != nil || len(failed) != 1 || failed[0].ID != made[1].ID || failed[0].LastError != domain.CodeUnreachable {
		t.Fatalf("failed filter: %+v %v", failed, err)
	}
	if limited, err := s.ListJobs(ctx, store.JobFilter{Limit: 2}); err != nil || len(limited) != 2 {
		t.Fatalf("limit: %d %v", len(limited), err)
	}
	if other, err := s.ListJobs(tenantCtx(2), store.JobFilter{Limit: 10}); err != nil || len(other) != 0 {
		t.Fatalf("tenant 2 log: %+v %v", other, err)
	}
}

func TestRoutingRules_StoreReplayAndOrder(t *testing.T) {
	s, _ := migrated(t)
	ctx := tenantCtx(1)
	rule := domain.RoutingRule{DocumentType: "PICK_LIST", Zone: "COLD", QueueCode: "COLD", Sequence: 5}
	first, inserted, err := s.CreateRoutingRule(ctx, rule, "r-1")
	if err != nil || !inserted || first.Zone != "COLD" || first.UserID != "" {
		t.Fatalf("create: %+v %v %v", first, inserted, err)
	}
	if again, inserted, err := s.CreateRoutingRule(ctx, rule, "r-1"); err != nil || inserted || again.ID != first.ID {
		t.Fatalf("replay: %+v %v %v", again, inserted, err)
	}
	changed := rule
	changed.QueueCode = "OTHER"
	if _, _, err := s.CreateRoutingRule(ctx, changed, "r-1"); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("reused key: %v", err)
	}
	if _, _, err := s.CreateRoutingRule(ctx, domain.RoutingRule{DocumentType: "PICK_LIST", QueueCode: "OFFICE", Sequence: 1}, "r-2"); err != nil {
		t.Fatal(err)
	}
	rules, err := s.ListRoutingRules(ctx)
	if err != nil || len(rules) != 2 || rules[0].QueueCode != "OFFICE" {
		t.Fatalf("rules: %+v %v", rules, err)
	}
	if got, ok := domain.ResolveRoute(rules, domain.Document{Type: "PICK_LIST", Zone: "COLD"}); !ok || got.QueueCode != "COLD" {
		t.Fatalf("resolve over stored rules: %+v %v", got, ok)
	}
}

// Runs the service queries as the non-superuser application role, so FORCE RLS
// is what keeps tenants apart (PASSAGE_TEST_APP_DATABASE_URL).
func TestMigrationsApplyWithForcedRLS(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := tenantCtx(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := admin.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"grant usage on schema " + schema + " to passage_app",
		"grant select, insert, update, delete on all tables in schema " + schema + " to passage_app",
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	var super, bypass bool
	if err := app.QueryRow(ctx, `select rolsuper, rolbypassrls from pg_roles where rolname = current_user`).Scan(&super, &bypass); err != nil || super || bypass {
		t.Fatalf("app role must not bypass RLS: super=%v bypass=%v err=%v", super, bypass, err)
	}
	for _, table := range []string{"print_device", "print_job", "print_routing_rule", "outbox"} {
		var enabled, forced bool
		err := app.QueryRow(ctx, `select relrowsecurity, relforcerowsecurity from pg_class where relname = $1 and relnamespace = current_schema()::regnamespace`, table).Scan(&enabled, &forced)
		if err != nil || !enabled || !forced {
			t.Errorf("%s: rls enabled=%v forced=%v err=%v", table, enabled, forced, err)
		}
	}
	s := store.New(app)
	if _, _, err := s.CreatePrinter(ctx, dock("DOCK-1", 1), "k-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateRoutingRule(ctx, domain.RoutingRule{DocumentType: "LP_LABEL", QueueCode: "DOCK", Sequence: 1}, "r-1"); err != nil {
		t.Fatal(err)
	}
	other := tenantCtx(2)
	printers, err := s.ListPrinters(other)
	if err != nil || len(printers) != 0 {
		t.Fatalf("tenant 2 printers: %+v %v", printers, err)
	}
	rules, err := s.ListRoutingRules(other)
	if err != nil || len(rules) != 0 {
		t.Fatalf("tenant 2 rules: %+v %v", rules, err)
	}
	// The same code is free in another tenant: uniqueness is per tenant.
	if _, _, err := s.CreatePrinter(other, dock("DOCK-1", 1), "k-1"); err != nil {
		t.Fatalf("tenant 2 register: %v", err)
	}
}
