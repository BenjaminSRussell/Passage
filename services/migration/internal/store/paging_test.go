package store_test

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/internal/testdb"
	"github.com/BenjaminSRussell/passage/services/migration/migrations"
)

// keysetContract pages every list one row at a time and checks the order matches the unpaged list, so the
// Postgres keyset predicates and the in-memory store agree.
func keysetContract(t *testing.T, s store.Store, ctx context.Context) {
	t.Helper()
	// Runs: newest first.
	var wantRuns []string
	for range 3 {
		r, err := s.SaveRun(ctx, sampleRun(), "")
		if err != nil {
			t.Fatal(err)
		}
		wantRuns = append([]string{r.ID}, wantRuns...)
	}
	var gotRuns []string
	var after *store.RunCursor
	for {
		page, err := s.ListRuns(ctx, store.RunFilter{After: after, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		gotRuns = append(gotRuns, page[0].ID)
		after = &store.RunCursor{CreatedAt: page[0].CreatedAt, ID: page[0].ID}
	}
	if len(gotRuns) != 3 || gotRuns[0] != wantRuns[0] || gotRuns[1] != wantRuns[1] || gotRuns[2] != wantRuns[2] {
		t.Fatalf("run pages = %v, want %v", gotRuns, wantRuns)
	}

	// Specs: entity ascending, version descending.
	for v := 1; v <= 3; v++ {
		sp := sampleSpec(v, "c"+strconv.Itoa(v))
		if _, _, err := s.SaveMappingSpec(ctx, sp, ""); err != nil {
			t.Fatal(err)
		}
	}
	vendor := sampleSpec(1, "v1")
	vendor.Entity = "vendor"
	if _, _, err := s.SaveMappingSpec(ctx, vendor, ""); err != nil {
		t.Fatal(err)
	}
	var specs []string
	var specAfter *store.SpecCursor
	for {
		page, err := s.ListMappingSpecs(ctx, "", specAfter, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		specs = append(specs, page[0].Entity+":"+strconv.Itoa(page[0].Version))
		specAfter = &store.SpecCursor{Entity: page[0].Entity, Version: page[0].Version}
	}
	if got := join(specs); got != "customer:3,customer:2,customer:1,vendor:1" {
		t.Fatalf("spec pages = %s", got)
	}

	// Templates: entity, name ascending, version descending.
	for _, nv := range [][2]string{{"Retail", "1"}, {"Retail", "2"}, {"Wholesale", "1"}} {
		v, _ := strconv.Atoi(nv[1])
		tm := sampleTemplate(v, "t"+nv[0]+nv[1])
		tm.Name = nv[0]
		if _, _, err := s.SaveDataTemplate(ctx, tm, ""); err != nil {
			t.Fatal(err)
		}
	}
	var tmpls []string
	var tmplAfter *store.TemplateCursor
	for {
		page, err := s.ListDataTemplates(ctx, "", tmplAfter, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		tmpls = append(tmpls, page[0].Name+":"+strconv.Itoa(page[0].Version))
		tmplAfter = &store.TemplateCursor{Entity: page[0].Entity, Name: page[0].Name, Version: page[0].Version}
	}
	if got := join(tmpls); got != "Retail:2,Retail:1,Wholesale:1" {
		t.Fatalf("template pages = %s", got)
	}

	// Ledger: source key ascending.
	rows, err := s.RunLedger(ctx, wantRuns[0], false, "C1", 10)
	if err != nil || len(rows) != 1 || rows[0].SourceKey != "C2" {
		t.Fatalf("ledger after C1 = %+v, %v", rows, err)
	}
}

func join(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out
}

func TestPostgres_KeysetPagingMatchesTheListOrder(t *testing.T) {
	keysetContract(t, newStore(t), principalContext(1))
}

func TestMem_KeysetPagingMatchesTheListOrder(t *testing.T) {
	keysetContract(t, store.NewMem(), context.Background())
}

// Runs that share a created_at are still paged without gaps or repeats (id breaks the tie), a filter holds
// across pages, and a cursor cannot reach another tenant's runs: the app role keeps RLS in force.
func TestPostgres_RunCursorBreaksCreatedAtTiesKeepsFiltersAndStaysInTheTenant(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	ctx := context.Background()
	testdb.Fresh(t, admin)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	one, two := principalContext(1), principalContext(2)
	var partial []string
	for i := range 5 {
		run := sampleRun()
		if i%2 == 1 {
			run.Report.Status = domain.StatusSucceeded
		}
		saved, err := s.SaveRun(one, run, "")
		if err != nil {
			t.Fatal(err)
		}
		if run.Report.Status == domain.StatusPartial {
			partial = append(partial, saved.ID)
		}
	}
	other, err := s.SaveRun(two, sampleRun(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "update migration.run set created_at = '2026-10-01T00:00:00Z'"); err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(partial, func(a, b string) int { return strings.Compare(b, a) })

	var got []string
	var after *store.RunCursor
	for range 10 {
		page, err := s.ListRuns(one, store.RunFilter{Status: domain.StatusPartial, After: after, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		got = append(got, page[0].ID)
		after = &store.RunCursor{CreatedAt: page[0].CreatedAt, ID: page[0].ID}
	}
	if strings.Join(got, ",") != strings.Join(partial, ",") {
		t.Fatalf("tied partial runs paged = %v, want id descending %v", got, partial)
	}

	// Tenant 2 replaying a tenant 1 position sees nothing of tenant 1; a position past every row gives its own run only.
	tied := store.RunCursor{CreatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), ID: partial[len(partial)-1]}
	if rows, err := s.ListRuns(two, store.RunFilter{After: &tied, Limit: 200}); err != nil || len(rows) != 0 {
		t.Fatalf("tenant 2 after a tenant 1 cursor = %+v, %v", rows, err)
	}
	future := store.RunCursor{CreatedAt: time.Now().Add(time.Hour), ID: partial[0]}
	if rows, err := s.ListRuns(two, store.RunFilter{After: &future, Limit: 200}); err != nil || len(rows) != 1 || rows[0].ID != other.ID {
		t.Fatalf("tenant 2 from the top = %+v, %v", rows, err)
	}
}
