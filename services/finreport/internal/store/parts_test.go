package store

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/migrations"
)

func revenueRows() domain.RowSet {
	return domain.RowSet{Code: "REV", Name: "Revenue", UpdatedBy: "u1", Rows: []domain.Row{
		{Code: "R", Kind: domain.RowAccountRange, Label: "Revenue", AccountFrom: "400000", AccountTo: "499999", NormalBalance: "credit"},
	}}
}

// FR-15.2: a row set is created without If-Match and replaced with the current
// revision (428 when missing, 412 when stale); listing is by code.
func TestRowSet_SaveReplaceAndRevisions(t *testing.T) {
	st, ctx := migrated(t)
	rs := revenueRows()
	if _, rev, err := st.SaveRowSet(ctx, rs, nil); err != nil || rev != 1 {
		t.Fatalf("create: rev %d, %v", rev, err)
	}
	rs.Name = "Revenue lines"
	if _, _, err := st.SaveRowSet(ctx, rs, nil); problemCode(err) != "precondition_required" {
		t.Fatalf("replace without If-Match = %v, want 428", err)
	}
	stale := 4
	if _, _, err := st.SaveRowSet(ctx, rs, &stale); problemCode(err) != CodeRevisionConflict {
		t.Fatalf("replace with a stale If-Match = %v, want %s", err, CodeRevisionConflict)
	}
	one := 1
	if _, rev, err := st.SaveRowSet(ctx, rs, &one); err != nil || rev != 2 {
		t.Fatalf("replace: rev %d, %v", rev, err)
	}
	got, rev, err := st.GetRowSet(ctx, "REV")
	if err != nil || rev != 2 || got.Name != "Revenue lines" || len(got.Rows) != 1 || got.Rows[0].NormalBalance != "credit" || got.UpdatedBy != "u1" {
		t.Fatalf("get: %+v rev %d, %v", got, rev, err)
	}
	first := revenueRows()
	first.Code = "AAA-FIRST"
	if _, _, err := st.SaveRowSet(ctx, first, nil); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListRowSets(ctx)
	if err != nil || len(list) != 2 || list[0].Code != "AAA-FIRST" || list[1].Code != "REV" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if _, _, err := st.GetRowSet(ctx, "MISSING"); problemCode(err) != domain.CodePartNotFound {
		t.Fatalf("missing row set = %v, want %s", err, domain.CodePartNotFound)
	}
}

// FR-15.3: column sets keep their CALC expressions; another tenant sees none.
func TestColumnSet_RoundTripAndTenantIsolation(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := testkit.Ctx(testkit.TenantID(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(app)
	cs := domain.ColumnTemplates()[1]
	cs.UpdatedBy = "u1"
	if _, rev, err := st.SaveColumnSet(ctx, cs, nil); err != nil || rev != 1 {
		t.Fatalf("create: rev %d, %v", rev, err)
	}
	got, rev, err := st.GetColumnSet(ctx, cs.Code)
	if err != nil || rev != 1 || len(got.Columns) != len(cs.Columns) || got.Columns[2].Expression != cs.Columns[2].Expression {
		t.Fatalf("get: %+v rev %d, %v", got, rev, err)
	}
	other := testkit.Ctx(testkit.TenantID(2))
	if list, err := st.ListColumnSets(other); err != nil || len(list) != 0 {
		t.Fatalf("other tenant lists %+v, %v; want nothing", list, err)
	}
	if _, _, err := st.GetColumnSet(other, cs.Code); problemCode(err) != domain.CodePartNotFound {
		t.Fatalf("other tenant get = %v, want %s", err, domain.CodePartNotFound)
	}
	if _, rev, err := st.SaveColumnSet(other, cs, nil); err != nil || rev != 1 {
		t.Fatalf("the same code in another tenant is a create: rev %d, %v", rev, err)
	}
	if _, _, err := st.SaveColumnSet(ctx, cs, nil); problemCode(err) != "precondition_required" {
		t.Fatalf("replace without If-Match = %v, want 428", err)
	}
	list, err := st.ListColumnSets(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

// PUT without If-Match only creates. When two requests create the same code at
// once, both find no row; the second insert waits on the first and then answers
// 409 conflict_version instead of overwriting it, and a later create is 428.
func TestRowSet_ConcurrentCreateOfOneCodeNeverOverwrites(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := testkit.Ctx(testkit.TenantID(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(app)
	first, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Rollback(ctx) }()
	if _, err := first.Exec(ctx, `insert into report_row_set(tenant_id, id, code, body, updated_by) values($1,$2,'REV','{"code":"REV","name":"First","rows":[]}','first')`,
		testkit.TenantID(1), ids.New()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := st.SaveRowSet(ctx, revenueRows(), nil)
		done <- err
	}()
	// Wait until the second create is blocked on the first one's uncommitted row.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := admin.QueryRow(ctx, `select count(*) from pg_stat_activity where wait_event_type = 'Lock' and query like 'insert into report_row_set%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the second create never waited on the first")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; problemCode(err) != CodeRevisionConflict {
		t.Fatalf("concurrent create = %v, want %s", err, CodeRevisionConflict)
	}
	got, rev, err := st.GetRowSet(ctx, "REV")
	if err != nil || rev != 1 || got.Name != "First" {
		t.Fatalf("after the race: %+v rev %d, %v; want the first create kept", got, rev, err)
	}
	if _, _, err := st.SaveRowSet(ctx, revenueRows(), nil); problemCode(err) != "precondition_required" {
		t.Fatalf("a later create of an existing code = %v, want 428", err)
	}
}
