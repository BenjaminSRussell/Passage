package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/internal/testdb"
	"github.com/BenjaminSRussell/passage/services/migration/migrations"
)

// newAppStore migrates as the admin role and returns a store on the NOSUPERUSER NOBYPASSRLS app role, so the
// 0004 grants and row-level security are what the template tests exercise.
func newAppStore(t *testing.T) (*store.Postgres, *pgxpool.Pool) {
	admin, pool := testkit.DBWithApp(t)
	testdb.Fresh(t, admin)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return store.New(pool), pool
}

// visibleTemplates counts data_template rows without a tenant filter, so only RLS hides other tenants' rows.
func visibleTemplates(t *testing.T, pool *pgxpool.Pool, ctx context.Context) int {
	var n int
	if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "select count(*) from migration.data_template").Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func sampleTemplate(version int, checksum string) store.DataTemplate {
	return store.DataTemplate{Entity: "customer", Name: "Retail", Version: version, Checksum: checksum, Defaults: map[string]string{"segment": "retail", "terms": "NET30"}}
}

func TestPostgres_DataTemplatesAreImmutableIdempotentAndTenantScoped(t *testing.T) {
	s, pool := newAppStore(t)
	ctx := principalContext(1)
	first, created, err := s.SaveDataTemplate(ctx, sampleTemplate(1, "c1"), "k1")
	if err != nil || !created || first.ID == "" || first.DefaultCount != 2 || first.Defaults["terms"] != "NET30" {
		t.Fatalf("first save = %+v, %v, %v", first, created, err)
	}
	same, created, err := s.SaveDataTemplate(ctx, sampleTemplate(1, "c1"), "")
	if err != nil || created || same.ID != first.ID {
		t.Fatalf("same checksum must return the stored version: %+v, %v, %v", same, created, err)
	}
	replay, created, err := s.SaveDataTemplate(ctx, sampleTemplate(1, "c1"), "k1")
	if err != nil || created || replay.ID != first.ID {
		t.Fatalf("key replay = %+v, %v, %v", replay, created, err)
	}
	var problem *httpx.Problem
	if _, _, err := s.SaveDataTemplate(ctx, sampleTemplate(1, "other"), ""); !errors.As(err, &problem) || problem.Code != store.CodeTemplateVersionUsed {
		t.Fatalf("changed checksum must conflict, got %v", err)
	}
	if _, _, err := s.SaveDataTemplate(ctx, sampleTemplate(2, "c2"), "k1"); !errors.As(err, &problem) || problem.Code != store.CodeSpecKeyReuse {
		t.Fatalf("key reuse must be rejected, got %v", err)
	}
	if _, _, err := s.SaveDataTemplate(ctx, sampleTemplate(2, "c2"), ""); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListDataTemplates(ctx, "customer", nil, 10)
	if err != nil || len(list) != 2 || list[0].Version != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if none, _ := s.ListDataTemplates(ctx, "vendor", nil, 10); len(none) != 0 {
		t.Fatalf("entity filter leaked %v", none)
	}
	if _, err := s.GetDataTemplate(principalContext(2), first.ID); !errors.As(err, &problem) || problem.Code != store.CodeTemplateNotFound {
		t.Fatalf("other tenant must not read the template, got %v", err)
	}
	if other, _ := s.ListDataTemplates(principalContext(2), "", nil, 10); len(other) != 0 {
		t.Fatalf("tenant 2 sees %d templates", len(other))
	}
	if own, other := visibleTemplates(t, pool, ctx), visibleTemplates(t, pool, principalContext(2)); own != 2 || other != 0 {
		t.Fatalf("row-level security: tenant 1 sees %d rows, tenant 2 sees %d", own, other)
	}
	if _, err := s.GetDataTemplate(ctx, "bad"); err == nil {
		t.Fatal("malformed id must error")
	}
}

func TestPostgres_RunPinsTemplateAndLedgerKeepsTemplateLineage(t *testing.T) {
	s, _ := newAppStore(t)
	ctx := principalContext(1)
	tmpl, _, err := s.SaveDataTemplate(ctx, sampleTemplate(1, "c1"), "")
	if err != nil {
		t.Fatal(err)
	}
	run := sampleRun()
	run.DataTemplateID = tmpl.ID
	// A mapping target may hold a comma; the lineage must keep it as one target.
	run.Report.Ledger[0].TemplateFields = []string{"segment", "terms,net"}
	saved, err := s.SaveRun(ctx, run, "")
	if err != nil || saved.DataTemplateID != tmpl.ID {
		t.Fatalf("saved = %+v, %v", saved, err)
	}
	all, err := s.RunLedger(ctx, saved.ID, false, "", 10)
	if err != nil || len(all) != 2 || all[0].SourceKey != "C1" || len(all[0].TemplateFields) != 2 || all[1].TemplateFields == nil || len(all[1].TemplateFields) != 0 {
		t.Fatalf("ledger = %+v, %v", all, err)
	}
	only, err := s.RunLedger(ctx, saved.ID, true, "", 10)
	if err != nil || len(only) != 1 || only[0].SourceKey != "C1" || len(only[0].TemplateFields) != 2 || only[0].TemplateFields[1] != "terms,net" {
		t.Fatalf("templated ledger = %+v, %v", only, err)
	}
	var problem *httpx.Problem
	if _, err := s.RunLedger(principalContext(2), saved.ID, false, "", 10); !errors.As(err, &problem) || problem.Status != 404 {
		t.Fatalf("other tenant must get 404, got %v", err)
	}
	if _, err := s.RunLedger(ctx, "bad", false, "", 10); err == nil {
		t.Fatal("malformed id must error")
	}
}

func TestMem_TemplateAndLedgerContract(t *testing.T) {
	m := store.NewMem()
	ctx := context.Background()
	first, created, _ := m.SaveDataTemplate(ctx, sampleTemplate(1, "c1"), "k")
	again, created2, _ := m.SaveDataTemplate(ctx, sampleTemplate(1, "c1"), "k")
	if !created || created2 || again.ID != first.ID {
		t.Fatal("same template must replay")
	}
	if _, _, err := m.SaveDataTemplate(ctx, sampleTemplate(1, "x"), ""); err == nil {
		t.Fatal("changed checksum must conflict")
	}
	if _, _, err := m.SaveDataTemplate(ctx, sampleTemplate(3, "x"), "k"); err == nil {
		t.Fatal("key reuse must fail")
	}
	if _, _, err := m.SaveDataTemplate(ctx, sampleTemplate(2, "c2"), ""); err != nil {
		t.Fatal(err)
	}
	if l, _ := m.ListDataTemplates(ctx, "customer", nil, 0); len(l) != 2 || l[0].Version != 2 {
		t.Fatalf("list must put the newest version first like Postgres: %+v", l)
	}
	if _, err := m.GetDataTemplate(ctx, "x"); err == nil {
		t.Fatal("missing must error")
	}
	run := sampleRun()
	run.Report.Ledger[1].TemplateFields = []string{"segment"}
	saved, _ := m.SaveRun(ctx, run, "")
	if rows, _ := m.RunLedger(ctx, saved.ID, true, "", 10); len(rows) != 1 || rows[0].SourceKey != "C2" {
		t.Fatalf("templated ledger = %+v", rows)
	}
	if rows, _ := m.RunLedger(ctx, saved.ID, false, "", 10); len(rows) != 2 || rows[0].TemplateFields == nil {
		t.Fatalf("ledger = %+v", rows)
	}
	if _, err := m.RunLedger(ctx, "missing", false, "", 10); err == nil {
		t.Fatal("unknown run must error")
	}
}
