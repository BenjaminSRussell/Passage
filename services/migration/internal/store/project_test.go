package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/migrations"
)

const (
	leA = "00000000-0000-7000-8000-0000000000a1"
	leB = "00000000-0000-7000-8000-0000000000b1"
)

func problemStatus(t *testing.T, err error, status int, code string) {
	t.Helper()
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status != status || p.Code != code {
		t.Fatalf("want %d %s, got %v", status, code, err)
	}
}

func copyProject(name string) store.Project {
	return store.Project{Name: name, Type: domain.ProjectCopy, Status: domain.ProjectActive, SourceLegalEntityID: leA, TargetLegalEntityID: leB}
}

// exerciseProjects runs against both the Postgres store and Mem so the fake keeps matching the contract.
func exerciseProjects(t *testing.T, s store.Store, one, two context.Context, tenantAware bool) {
	saved, created, err := s.SaveProject(one, copyProject("USMF to CAMF"), "key-1")
	if err != nil || !created || saved.ID == "" || saved.Version != 1 || saved.Status != "active" || saved.SourceLegalEntityID != leA {
		t.Fatalf("save = %+v %v %v", saved, created, err)
	}
	if again, created, err := s.SaveProject(one, copyProject("USMF to CAMF"), "key-1"); err != nil || created || again.ID != saved.ID {
		t.Fatalf("same key must replay: %+v %v %v", again, created, err)
	}
	_, _, err = s.SaveProject(one, copyProject("Other"), "key-1")
	problemStatus(t, err, 422, "idempotency_key_reuse")
	_, _, err = s.SaveProject(one, copyProject("USMF to CAMF"), "key-2")
	problemStatus(t, err, 409, store.CodeProjectName)
	ax := store.Project{Name: "AX customers", Type: domain.ProjectMigrate, SourceSystem: "ax2012", Status: domain.ProjectActive, ScheduleCron: "0 2 * * *"}
	if _, _, err := s.SaveProject(one, ax, ""); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetProject(one, saved.ID)
	if err != nil || got.Name != "USMF to CAMF" {
		t.Fatalf("get = %+v %v", got, err)
	}
	if _, err := s.GetProject(one, "not-a-uuid"); err == nil {
		t.Fatal("bad id must be not found")
	}

	page, err := s.ListProjects(one, store.ProjectFilter{Limit: 1})
	if err != nil || len(page) != 1 || page[0].Name != "AX customers" {
		t.Fatalf("first page = %+v %v", page, err)
	}
	next, err := s.ListProjects(one, store.ProjectFilter{Limit: 5, After: &store.ProjectCursor{Name: page[0].Name, ID: page[0].ID}})
	if err != nil || len(next) != 1 || next[0].ID != saved.ID {
		t.Fatalf("second page = %+v %v", next, err)
	}
	if only, _ := s.ListProjects(one, store.ProjectFilter{Type: domain.ProjectMigrate}); len(only) != 1 || only[0].Name != "AX customers" {
		t.Fatalf("type filter = %+v", only)
	}

	renamed, err := s.UpdateProject(one, saved.ID, 1, func(p *store.Project) error { p.Name = "Renamed"; p.ScheduleCron = "0 3 * * *"; return nil })
	if err != nil || renamed.Version != 2 || renamed.Name != "Renamed" || renamed.ScheduleCron != "0 3 * * *" {
		t.Fatalf("update = %+v %v", renamed, err)
	}
	_, err = s.UpdateProject(one, saved.ID, 1, func(p *store.Project) error { return nil })
	problemStatus(t, err, 409, store.CodeVersionConflict)
	_, err = s.UpdateProject(one, saved.ID, 2, func(p *store.Project) error { p.Name = "AX customers"; return nil })
	problemStatus(t, err, 409, store.CodeProjectName)
	if _, err := s.UpdateProject(one, saved.ID, 2, func(p *store.Project) error { return errors.New("rejected") }); err == nil {
		t.Fatal("an edit error must abort the update")
	}
	if after, _ := s.GetProject(one, saved.ID); after.Version != 2 || after.Name != "Renamed" {
		t.Fatalf("failed updates must not change the project: %+v", after)
	}

	rules := []domain.KeyRewrite{{Field: domain.KeyField, From: "USMF", To: "CAMF"}}
	_, err = s.ReplaceRewriteRules(one, saved.ID, 1, rules)
	problemStatus(t, err, 409, store.CodeVersionConflict)
	if p, err := s.ReplaceRewriteRules(one, saved.ID, 2, rules); err != nil || p.Version != 3 {
		t.Fatalf("replace = %+v %v", p, err)
	}
	replacement := []domain.KeyRewrite{{Field: "dataAreaId", From: "US", To: "CA"}, {Field: domain.KeyField, From: "US", To: "CA"}}
	if _, err := s.ReplaceRewriteRules(one, saved.ID, 3, replacement); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.RewriteRules(one, saved.ID); len(got) != 2 || got[0] != replacement[0] || got[1] != replacement[1] {
		t.Fatalf("replace must drop the old set and keep the order: %+v", got)
	}
	if _, err := s.ReplaceRewriteRules(one, saved.ID, 4, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.RewriteRules(one, saved.ID); len(got) != 0 {
		t.Fatalf("an empty set clears the rules: %+v", got)
	}
	_, err = s.ReplaceRewriteRules(one, "00000000-0000-7000-8000-00000000ffff", 1, nil)
	problemStatus(t, err, 404, store.CodeProjectNotFound)

	// An archived project keeps its rules frozen until it is reactivated.
	if _, err := s.ReplaceRewriteRules(one, saved.ID, 5, rules); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateProject(one, saved.ID, 6, func(p *store.Project) error { p.Status = domain.ProjectArchived; return nil }); err != nil {
		t.Fatal(err)
	}
	_, err = s.ReplaceRewriteRules(one, saved.ID, 7, nil)
	problemStatus(t, err, 409, store.CodeProjectArchived)
	if got, _ := s.RewriteRules(one, saved.ID); len(got) != 1 {
		t.Fatalf("a refused replacement must keep the rules: %+v", got)
	}
	if after, _ := s.GetProject(one, saved.ID); after.Version != 7 {
		t.Fatalf("a refused replacement must not bump the version: %+v", after)
	}

	if !tenantAware {
		return
	}
	if _, err := s.GetProject(two, saved.ID); err == nil {
		t.Fatal("another tenant must not read the project")
	}
	if list, _ := s.ListProjects(two, store.ProjectFilter{}); len(list) != 0 {
		t.Fatalf("tenant 2 lists %d projects", len(list))
	}
	if _, err := s.UpdateProject(two, saved.ID, 7, func(*store.Project) error { return nil }); err == nil {
		t.Fatal("another tenant must not update the project")
	}
	if _, err := s.ReplaceRewriteRules(two, saved.ID, 7, rules); err == nil {
		t.Fatal("another tenant must not replace the rules")
	}
	// the same name is free in another tenant
	if _, _, err := s.SaveProject(two, copyProject("Renamed"), ""); err != nil {
		t.Fatal(err)
	}
}

func TestPostgres_ProjectsAndRewriteRulesUnderRLS(t *testing.T) {
	admin, pool := testkit.DBWithApp(t) // the app role proves the 0006 grants are enough for the store
	drop := func() { _, _ = admin.Exec(context.Background(), "drop schema if exists migration cascade") }
	drop()
	t.Cleanup(drop)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	exerciseProjects(t, store.New(pool), principalContext(1), principalContext(2), true)
}

// Projects are tenant-wide: a caller scoped to legal entities, even both of a copy project's, reads nothing.
func TestPostgres_ProjectsAreHiddenFromLegalEntityScopedCallers(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	drop := func() { _, _ = admin.Exec(context.Background(), "drop schema if exists migration cascade") }
	drop()
	t.Cleanup(drop)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	saved, _, err := s.SaveProject(principalContext(1), copyProject("USMF to CAMF"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceRewriteRules(principalContext(1), saved.ID, 1, []domain.KeyRewrite{{Field: domain.KeyField, From: "USMF", To: "CAMF"}}); err != nil {
		t.Fatal(err)
	}
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: leA + "," + leB,
		Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	if _, err := s.GetProject(scoped, saved.ID); err == nil {
		t.Fatal("a legal-entity-scoped caller must not read the project")
	}
	if list, _ := s.ListProjects(scoped, store.ProjectFilter{}); len(list) != 0 {
		t.Fatalf("a scoped caller lists %d projects", len(list))
	}
	if got, _ := s.RewriteRules(scoped, saved.ID); len(got) != 0 {
		t.Fatalf("a scoped caller reads %d rules", len(got))
	}
	if _, err := s.UpdateProject(scoped, saved.ID, 2, func(p *store.Project) error { p.Name = "x"; return nil }); err == nil {
		t.Fatal("a scoped caller must not update the project")
	}
	if _, _, err := s.SaveProject(scoped, copyProject("scoped"), ""); err == nil {
		t.Fatal("a scoped caller must not create a project")
	}
}

// Concurrent creates with one Idempotency-Key store one project and replay it to the others.
func TestPostgres_ConcurrentCreatesWithOneKeyStoreOneProject(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	drop := func() { _, _ = admin.Exec(context.Background(), "drop schema if exists migration cascade") }
	drop()
	t.Cleanup(drop)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	const n = 8
	type result struct {
		p       store.Project
		created bool
		err     error
	}
	results := make(chan result, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, created, err := s.SaveProject(principalContext(1), copyProject("USMF to CAMF"), "same-key")
			results <- result{p, created, err}
		}()
	}
	wg.Wait()
	close(results)
	created, id := 0, ""
	for r := range results {
		if r.err != nil {
			t.Fatalf("a replayed create must not fail: %v", r.err)
		}
		if id != "" && r.p.ID != id {
			t.Fatalf("two projects for one key: %s and %s", id, r.p.ID)
		}
		id = r.p.ID
		if r.created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created %d times, want once", created)
	}
}

func TestPostgres_ProjectTableRefusesInvalidCopyRows(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	drop := func() { _, _ = admin.Exec(context.Background(), "drop schema if exists migration cascade") }
	drop()
	t.Cleanup(drop)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	same := copyProject("same")
	same.TargetLegalEntityID = same.SourceLegalEntityID
	if _, _, err := s.SaveProject(principalContext(1), same, ""); err == nil {
		t.Fatal("the table must refuse a copy from an entity to itself")
	}
	if _, _, err := s.SaveProject(principalContext(1), store.Project{Name: "x", Type: "migrate", Status: "active"}, ""); err == nil {
		t.Fatal("the table must refuse a migrate project without a source system")
	}
}

func TestMem_ProjectContract(t *testing.T) {
	exerciseProjects(t, store.NewMem(), principalContext(1), principalContext(2), false)
}
