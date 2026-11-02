package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/internal/testdb"
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

const (
	runA = "00000000-0000-7000-8000-0000000000c1"
	runB = "00000000-0000-7000-8000-0000000000c2"
)

func exerciseProjectRuns(t *testing.T, s store.Store, one, two context.Context, tenantAware bool) {
	ax, _, err := s.SaveProject(one, store.Project{Name: "AX run", Type: domain.ProjectMigrate, SourceSystem: "ax2012", Status: domain.ProjectActive}, "")
	if err != nil {
		t.Fatal(err)
	}
	pr, err := s.BeginProjectRun(one, ax.ID, runA)
	if err != nil || pr.ID != ax.ID {
		t.Fatalf("begin = %+v %v", pr, err)
	}
	_, err = s.BeginProjectRun(one, ax.ID, runB)
	problemStatus(t, err, 409, store.CodeRunActive)
	var p *httpx.Problem
	if errors.As(err, &p); !strings.Contains(p.Detail, runA) {
		t.Fatalf("the 409 must name the active run %s: %q", runA, p.Detail)
	}
	if tenantAware {
		_, err := s.BeginProjectRun(two, ax.ID, runB)
		problemStatus(t, err, 404, store.CodeProjectNotFound)
	}
	// a run that does not hold the claim cannot release it
	if err := s.EndProjectRun(one, ax.ID, runB); err != nil {
		t.Fatal(err)
	}
	_, err = s.BeginProjectRun(one, ax.ID, runB)
	problemStatus(t, err, 409, store.CodeRunActive)
	run, err := s.SaveRun(one, store.Run{ID: runA, SourceSystem: "ax2012", Mode: "rehearsal", Entity: "customer", MappingVersion: 1, Mapping: []byte(`{}`), ProjectID: ax.ID, Report: domain.Report{Status: domain.StatusSucceeded}}, "")
	if err != nil || run.ID != runA || run.ProjectID != ax.ID {
		t.Fatalf("save under the claimed id = %+v %v", run, err)
	}
	if err := s.EndProjectRun(one, ax.ID, runA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginProjectRun(one, ax.ID, runB); err != nil {
		t.Fatalf("a released project must be claimable: %v", err)
	}
	if err := s.EndProjectRun(one, ax.ID, runB); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveRun(one, store.Run{SourceSystem: "ax2012", Mode: "rehearsal", Entity: "customer", MappingVersion: 1, Mapping: []byte(`{}`), Report: domain.Report{Status: domain.StatusSucceeded}}, ""); err != nil {
		t.Fatal(err)
	}
	listed, err := s.ListRuns(one, store.RunFilter{ProjectID: ax.ID})
	if err != nil || len(listed) != 1 || listed[0].ID != run.ID {
		t.Fatalf("project filter = %+v %v", listed, err)
	}
	if all, _ := s.ListRuns(one, store.RunFilter{}); len(all) != 2 {
		t.Fatalf("unfiltered runs = %d", len(all))
	}
	_, err = s.BeginProjectRun(one, "00000000-0000-7000-8000-00000000ffff", runB)
	problemStatus(t, err, 404, store.CodeProjectNotFound)

	// of many concurrent starts exactly one takes the claim
	const starts = 8
	var wg sync.WaitGroup
	results := make(chan error, starts)
	for i := range starts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.BeginProjectRun(one, ax.ID, fmt.Sprintf("00000000-0000-7000-8000-0000000001%02d", i))
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		if err == nil {
			won++
		} else if !errors.As(err, &p) || p.Code != store.CodeRunActive {
			t.Fatalf("concurrent start: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("concurrent starts that took the claim = %d, want 1", won)
	}

	archived := ax.Version
	if _, err := s.UpdateProject(one, ax.ID, archived, func(p *store.Project) error { p.Status = domain.ProjectArchived; return nil }); err != nil {
		t.Fatal(err)
	}
	_, err = s.BeginProjectRun(one, ax.ID, runB)
	problemStatus(t, err, 409, store.CodeProjectArchived)
}

func TestPostgres_ProjectRunsClaimAndFilter(t *testing.T) {
	admin, pool := testkit.DBWithApp(t) // the app role proves the claim needs no grant beyond 0006
	testdb.Fresh(t, admin)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	exerciseProjectRuns(t, store.New(pool), principalContext(1), principalContext(2), true)
}

// A claim whose run died without releasing it lapses after the abandonment window, judged on the database clock,
// and the run that held it can no longer clear the claim the next run took.
func TestPostgres_AbandonedProjectClaimLapses(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	testdb.Fresh(t, admin)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s, one := store.New(pool), principalContext(1)
	ax, _, err := s.SaveProject(one, store.Project{Name: "AX stale", Type: domain.ProjectMigrate, SourceSystem: "ax2012", Status: domain.ProjectActive}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginProjectRun(one, ax.ID, runA); err != nil {
		t.Fatal(err)
	}
	age := func(by string) {
		t.Helper()
		if _, err := admin.Exec(ctx, `update migration.project set active_run_started_at = now() - $2::interval where id = $1`, ax.ID, by); err != nil {
			t.Fatal(err)
		}
	}
	age("39 minutes")
	_, err = s.BeginProjectRun(one, ax.ID, runB)
	problemStatus(t, err, 409, store.CodeRunActive)
	age("41 minutes")
	if _, err := s.BeginProjectRun(one, ax.ID, runB); err != nil {
		t.Fatalf("an abandoned claim must lapse: %v", err)
	}
	if err := s.EndProjectRun(one, ax.ID, runA); err != nil {
		t.Fatal(err)
	}
	_, err = s.BeginProjectRun(one, ax.ID, "00000000-0000-7000-8000-0000000000c3")
	problemStatus(t, err, 409, store.CodeRunActive)
}

func TestMem_ProjectRunsClaimAndFilter(t *testing.T) {
	exerciseProjectRuns(t, store.NewMem(), principalContext(1), principalContext(2), false)
}

func TestPostgres_ProjectsAndRewriteRulesUnderRLS(t *testing.T) {
	admin, pool := testkit.DBWithApp(t) // the app role proves the 0006 grants are enough for the store
	testdb.Fresh(t, admin)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	exerciseProjects(t, store.New(pool), principalContext(1), principalContext(2), true)
}

// Projects are tenant-wide: a caller scoped to legal entities, even both of a copy project's, reads nothing.
func TestPostgres_ProjectsAreHiddenFromLegalEntityScopedCallers(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	testdb.Fresh(t, admin)
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
	testdb.Fresh(t, admin)
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
	testdb.Fresh(t, admin)
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
