package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
	"github.com/BenjaminSRussell/passage/services/print/internal/jobs"
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
	return domain.Printer{Code: code, Name: "Dock", DeviceType: domain.DeviceZPL, Dpi: 203, MediaSize: domain.Media4x6In, Host: "10.0.4.20", Port: 9100, QueueCode: "DOCK", FailoverRank: rank}
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
	if other, err := s.ListPrinters(tenantCtx(2), false); err != nil || len(other) != 0 {
		t.Fatalf("tenant 2 saw %+v %v", other, err)
	}
}

// TestPDFJobs_BinaryDocumentAndMediaSize covers slice 4 (FR-32.13): media size is
// stored per device and the database refuses sheet media on a label printer; a PDF
// job keeps its binary document (NUL bytes included) for reprints, and the job log
// does not load it.
func TestPDFJobs_BinaryDocumentAndMediaSize(t *testing.T) {
	s, pool := migrated(t)
	ctx := tenantCtx(1)
	office := domain.Printer{Code: "OFFICE-1", Name: "Office", DeviceType: domain.DevicePDFIPP, Dpi: 300, MediaSize: domain.MediaLetter,
		Host: "10.0.7.1", Port: 631, QueueCode: "OFFICE", FailoverRank: 1}
	got, _, err := s.CreatePrinter(ctx, office, "office-1")
	if err != nil || got.MediaSize != domain.MediaLetter {
		t.Fatalf("create office printer: %+v %v", got, err)
	}
	a4 := office
	a4.MediaSize = domain.MediaA4
	if _, _, err := s.CreatePrinter(ctx, a4, "office-1"); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key with another media size: %v", err)
	}
	err = dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into print_device (tenant_id, id, code, name, device_type, dpi, media_size, host, port, queue_code, failover_rank, idempotency_key)
			values ($1, $2, 'LBL-9', 'Label', 'ZPL_II', 203, 'A4', '10.0.7.9', 9100, 'DOCK', 9, 'lbl-9')`, testkit.TenantID(1), ids.New())
		return err
	})
	if err == nil {
		t.Fatal("database accepted A4 media on a ZPL_II printer")
	}

	doc := "%PDF-1.7\n\x00\xff\xfe binary\n%%EOF"
	job := domain.Job{ID: ids.New(), TemplateCode: domain.PDFDocumentTemplate, QueueCode: "OFFICE", Format: domain.FormatPDF, Status: domain.JobQueued, ZPL: doc}
	created, inserted, err := s.CreateJob(ctx, store.NewJob{Job: job, RequestHash: "pdf-h", IdempotencyKey: "pdf-1"})
	if err != nil || !inserted || created.Format != domain.FormatPDF {
		t.Fatalf("create PDF job: %+v %v %v", created, inserted, err)
	}
	stored, err := s.GetJob(ctx, job.ID)
	if err != nil || stored.Format != domain.FormatPDF || stored.ZPL != doc {
		t.Fatalf("stored PDF job: format %q payload %q err %v", stored.Format, stored.ZPL, err)
	}
	log, err := s.ListJobs(ctx, store.JobFilter{QueueCode: "OFFICE", Limit: 10})
	if err != nil || len(log) != 1 || log[0].Format != domain.FormatPDF || log[0].ZPL != "" {
		t.Fatalf("job log: %+v %v", log, err)
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

// queuedJob inserts a QUEUED job on the DOCK queue under key.
func queuedJob(t *testing.T, s *store.Postgres, ctx context.Context, key string) domain.Job {
	t.Helper()
	j := domain.Job{ID: ids.New(), TemplateCode: domain.TemplateLP, QueueCode: "DOCK", Status: domain.JobQueued, ZPL: "^XA^XZ"}
	created, inserted, err := s.CreateJob(ctx, store.NewJob{Job: j, RequestHash: "h", IdempotencyKey: key})
	if err != nil || !inserted {
		t.Fatalf("create %s: %v %v", key, inserted, err)
	}
	return created
}

func TestPrinterStatuses_QueueDepthAndLastJob(t *testing.T) {
	s, _ := migrated(t)
	ctx := tenantCtx(1)
	p1, _, err := s.CreatePrinter(ctx, dock("DOCK-1", 1), "p-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreatePrinter(ctx, dock("DOCK-2", 2), "p-2"); err != nil {
		t.Fatal(err)
	}
	printed := queuedJob(t, s, ctx, "st-1")
	queuedJob(t, s, ctx, "st-2") // stays QUEUED

	statuses, err := s.PrinterStatuses(ctx)
	if err != nil || len(statuses) != 2 {
		t.Fatalf("statuses: %+v %v", statuses, err)
	}
	for _, st := range statuses {
		if st.QueueDepth != 2 || st.State != domain.StateIdle || st.LastJobAt != nil {
			t.Fatalf("before dispatch: %+v", st)
		}
	}

	if err := printed.Complete(domain.Outcome{Printer: p1, Attempts: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteJob(ctx, printed); err != nil {
		t.Fatal(err)
	}
	statuses, err = s.PrinterStatuses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st := statuses[0]; st.Code != "DOCK-1" || st.State != domain.StateReady || st.QueueDepth != 1 || st.LastJobAt == nil {
		t.Fatalf("DOCK-1 after print: %+v", st)
	}
	if st := statuses[1]; st.Code != "DOCK-2" || st.State != domain.StateIdle || st.QueueDepth != 1 {
		t.Fatalf("DOCK-2 after print: %+v", st)
	}

	failed := queuedJob(t, s, ctx, "st-3")
	if err := failed.Complete(domain.Outcome{Printer: p1, Attempts: 2}, &domain.Error{Code: domain.CodeUnreachable}); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteJob(ctx, failed); err != nil {
		t.Fatal(err)
	}
	statuses, err = s.PrinterStatuses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st := statuses[0]; st.State != domain.StateError || st.LastError != domain.CodeUnreachable {
		t.Fatalf("DOCK-1 after failure: %+v", st)
	}
	if other, err := s.PrinterStatuses(tenantCtx(2)); err != nil || len(other) != 0 {
		t.Fatalf("tenant 2 statuses: %+v %v", other, err)
	}
}

func TestPurgeJobs_KeepsQueuedRecentAndOtherTenants(t *testing.T) {
	s, pool := migrated(t)
	ctx := tenantCtx(1)
	done := queuedJob(t, s, ctx, "pg-1")
	queued := queuedJob(t, s, ctx, "pg-2")
	recent := queuedJob(t, s, ctx, "pg-3")
	otherTenant := queuedJob(t, s, tenantCtx(2), "pg-4")
	for _, j := range []struct {
		ctx context.Context
		job domain.Job
	}{{ctx, done}, {ctx, recent}, {tenantCtx(2), otherTenant}} {
		job := j.job
		if err := job.Complete(domain.Outcome{Printer: domain.Printer{ID: ids.New(), Code: "DOCK-1"}, Attempts: 1}, nil); err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteJob(j.ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	// Age every row but the recent one past the cutoff.
	if _, err := pool.Exec(ctx, `update print_job set created_at = now() - interval '40 days' where id <> $1`, recent.ID); err != nil {
		t.Fatal(err)
	}
	cutoff := domain.RetentionCutoff(time.Now(), domain.DefaultRetentionDays)
	deleted, err := s.PurgeJobs(ctx, cutoff)
	if err != nil || deleted != 1 {
		t.Fatalf("purge deleted %d err=%v, want 1", deleted, err)
	}
	left, err := s.ListJobs(ctx, store.JobFilter{Limit: 10})
	if err != nil || len(left) != 2 {
		t.Fatalf("remaining: %+v %v", left, err)
	}
	for _, j := range left {
		if j.ID == done.ID {
			t.Fatal("the old printed job survived the purge")
		}
	}
	if _, err := s.GetJob(ctx, queued.ID); err != nil {
		t.Fatalf("old QUEUED job was purged: %v", err)
	}
	if _, err := s.GetJob(tenantCtx(2), otherTenant.ID); err != nil {
		t.Fatalf("tenant 2's job was purged by tenant 1: %v", err)
	}
	if again, err := s.PurgeJobs(ctx, cutoff); err != nil || again != 0 {
		t.Fatalf("second purge deleted %d err=%v", again, err)
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
	printers, err := s.ListPrinters(other, false)
	if err != nil || len(printers) != 0 {
		t.Fatalf("tenant 2 printers: %+v %v", printers, err)
	}
	rules, err := s.ListRoutingRules(other)
	if err != nil || len(rules) != 0 {
		t.Fatalf("tenant 2 rules: %+v %v", rules, err)
	}
	done := queuedJob(t, s, ctx, "rls-job")
	if err := done.Complete(domain.Outcome{Printer: domain.Printer{ID: ids.New(), Code: "DOCK-1"}, Attempts: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteJob(ctx, done); err != nil {
		t.Fatal(err)
	}
	if statuses, err := s.PrinterStatuses(other); err != nil || len(statuses) != 0 {
		t.Fatalf("tenant 2 printer statuses: %+v %v", statuses, err)
	}
	if deleted, err := s.PurgeJobs(other, time.Now().Add(time.Hour)); err != nil || deleted != 0 {
		t.Fatalf("tenant 2 purge removed %d rows of tenant 1 (err %v)", deleted, err)
	}
	if _, err := s.GetJob(ctx, done.ID); err != nil {
		t.Fatalf("tenant 1 job gone after tenant 2 purge: %v", err)
	}
	// The same code is free in another tenant: uniqueness is per tenant.
	if _, _, err := s.CreatePrinter(other, dock("DOCK-1", 1), "k-1"); err != nil {
		t.Fatalf("tenant 2 register: %v", err)
	}
}

func TestScheduledPurge_RunsForEachListedTenant(t *testing.T) {
	s, pool := migrated(t)
	for n, key := range map[int]string{1: "sp-1", 2: "sp-2", 3: "sp-3"} {
		ctx := tenantCtx(n)
		job := queuedJob(t, s, ctx, key)
		if err := job.Complete(domain.Outcome{Printer: domain.Printer{ID: ids.New(), Code: "DOCK-1"}, Attempts: 1}, nil); err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(tenantCtx(1), `update print_job set created_at = now() - interval '40 days'`); err != nil {
		t.Fatal(err)
	}
	// Tenant 3 is not listed, so the schedule leaves its job for an on-demand purge.
	deleted, err := jobs.PurgeTenants(context.Background(), s, []string{testkit.TenantID(1), testkit.TenantID(2)}, time.Now(), domain.DefaultRetentionDays)
	if err != nil || deleted != 2 {
		t.Fatalf("scheduled purge deleted %d err=%v, want 2", deleted, err)
	}
	left, err := s.ListJobs(tenantCtx(3), store.JobFilter{Limit: 10})
	if err != nil || len(left) != 1 {
		t.Fatalf("tenant 3 jobs after the scheduled purge: %+v %v, want 1", left, err)
	}
	for _, n := range []int{1, 2} {
		if left, err := s.ListJobs(tenantCtx(n), store.JobFilter{Limit: 10}); err != nil || len(left) != 0 {
			t.Fatalf("tenant %d jobs after the scheduled purge: %+v %v, want none", n, left, err)
		}
	}
}
