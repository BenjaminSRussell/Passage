package store_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

func importRow(line int, unitType, code, name, entity string) store.OperatingUnitImportRow {
	return store.OperatingUnitImportRow{Line: line, Type: unitType, Code: code, Name: name, LegalEntityCode: entity}
}

func TestPostgres_ImportOperatingUnits(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	a := mustCreate(t, s, ctx, newEntity("US01"))
	b := mustCreate(t, s, ctx, newEntity("US02"))
	inactive := "inactive"
	if _, err := s.UpdateLegalEntity(ctx, b.ID, 1, store.LegalEntityPatch{Status: &inactive}); err != nil {
		t.Fatal(err)
	}
	existing := unit("department", "D1")
	existing.DefaultLegalEntityID = &a.ID
	mustCreateUnit(t, s, ctx, existing)
	before := len(unitEvents(t, ctx, pool, "dev.passage.org.operatingunit.created.v1"))

	rows := []store.OperatingUnitImportRow{
		importRow(1, "department", "D1", "Unit D1", "US01"),
		importRow(2, "department", "D2", "Unit D2", ""),
		importRow(3, "cost_center", "D2", "Cost D2", "US01"),
	}
	t.Run("dry run writes nothing", func(t *testing.T) {
		res, err := s.ImportOperatingUnits(ctx, rows, true)
		if err != nil || len(res) != 3 || res[0].Status != store.ImportUnchanged || res[1].Status != store.ImportCreated {
			t.Fatalf("res %+v err %v", res, err)
		}
		if page, _ := s.ListOperatingUnits(ctx, store.OperatingUnitFilter{}, store.OperatingUnitKey{}, store.MaxPageSize); len(page.Items) != 1 {
			t.Fatalf("units after dry run = %d", len(page.Items))
		}
	})
	t.Run("creates the missing units with one event each", func(t *testing.T) {
		res, err := s.ImportOperatingUnits(ctx, rows, false)
		if err != nil || res[1].Status != store.ImportCreated || res[2].Status != store.ImportCreated || res[2].ID == "" {
			t.Fatalf("res %+v err %v", res, err)
		}
		created, err := s.GetOperatingUnit(ctx, res[2].ID)
		if err != nil || created.DefaultLegalEntityID == nil || *created.DefaultLegalEntityID != a.ID || created.Version != 1 {
			t.Fatalf("created %+v err %v", created, err)
		}
		if got := len(unitEvents(t, ctx, pool, "dev.passage.org.operatingunit.created.v1")) - before; got != 2 {
			t.Fatalf("events written = %d; want 2", got)
		}
	})
	t.Run("running it again writes nothing", func(t *testing.T) {
		res, err := s.ImportOperatingUnits(ctx, rows, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range res {
			if r.Status != store.ImportUnchanged {
				t.Fatalf("res %+v", res)
			}
		}
		if got := len(unitEvents(t, ctx, pool, "dev.passage.org.operatingunit.created.v1")) - before; got != 2 {
			t.Fatalf("events after rerun = %d; want 2", got)
		}
	})
	t.Run("a failed row stops the whole file", func(t *testing.T) {
		res, err := s.ImportOperatingUnits(ctx, []store.OperatingUnitImportRow{
			importRow(1, "department", "D9", "Nine", ""),
			importRow(2, "department", "D1", "Renamed", "US01"),
			importRow(3, "department", "D8", "Eight", "US02"),
			importRow(4, "department", "D8", "Eight again", ""),
			importRow(5, "department", "D7", "Seven", "NOPE"),
		}, false)
		if err != nil {
			t.Fatal(err)
		}
		for i, field := range []string{"", "code", "legalEntity", "code", "legalEntity"} {
			if (field == "") != (len(res[i].Errors) == 0) || (field != "" && res[i].Errors[field] == "") {
				t.Fatalf("row %d = %+v; want error on %q", i+1, res[i], field)
			}
		}
		if res[0].Status != store.ImportCreated {
			t.Fatalf("valid row = %+v", res[0])
		}
		if page, _ := s.ListOperatingUnits(ctx, store.OperatingUnitFilter{}, store.OperatingUnitKey{}, store.MaxPageSize); len(page.Items) != 3 {
			t.Fatalf("units = %d; D9 must not be written", len(page.Items))
		}
	})
	t.Run("another tenant and a scoped caller see their own data", func(t *testing.T) {
		other := principalContext(2)
		res, err := s.ImportOperatingUnits(other, []store.OperatingUnitImportRow{importRow(1, "department", "D1", "Other tenant", ""), importRow(2, "department", "D5", "x", "US01")}, false)
		if err != nil || res[0].Status != store.ImportCreated || res[1].Status != store.ImportFailed {
			t.Fatalf("res %+v err %v", res, err)
		}
		c := mustCreate(t, s, ctx, newEntity("US03"))
		scoped := tenant.With(ctx, tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: a.ID})
		res, err = s.ImportOperatingUnits(scoped, []store.OperatingUnitImportRow{importRow(1, "department", "D6", "x", "US03")}, true)
		if err != nil || res[0].Status != store.ImportFailed {
			t.Fatalf("scoped res %+v err %v (entity %s)", res, err, c.ID)
		}
	})
}

// A unit inserted by another transaction after the import read the tenant's units makes the
// import's insert hit the unique index: the import reports ORG-OU-CODE-DUPLICATE and writes nothing.
func TestPostgres_ImportOperatingUnits_ConcurrentCreateIsConflict(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	inserted, release, otherDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		otherDone <- dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `insert into operating_unit (tenant_id, id, type, code, name) values ($1, $2, 'department', 'D50', 'Other')`,
				testkit.TenantID(1), ids.New()); err != nil {
				close(inserted)
				return err
			}
			close(inserted)
			<-release
			return nil
		})
	}()
	<-inserted
	importDone := make(chan error, 1)
	go func() {
		_, err := s.ImportOperatingUnits(ctx, []store.OperatingUnitImportRow{importRow(1, "department", "D49", "Before", ""), importRow(2, "department", "D50", "Mine", "")}, false)
		importDone <- err
	}()
	// Wait until the import blocks on the other transaction's uncommitted row, then commit it.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var waiting int
		if err := pool.QueryRow(ctx, `select count(*) from pg_stat_activity where wait_event_type = 'Lock' and query like 'insert into operating_unit%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the import never waited on the concurrent insert")
		}
	}
	close(release)
	if err := <-otherDone; err != nil {
		t.Fatal(err)
	}
	if code := problemCode(t, <-importDone); code != store.CodeOUCodeDuplicate {
		t.Fatalf("code = %s", code)
	}
	page, err := s.ListOperatingUnits(ctx, store.OperatingUnitFilter{}, store.OperatingUnitKey{}, store.MaxPageSize)
	if err != nil || len(page.Items) != 1 || page.Items[0].Code != "D50" {
		t.Fatalf("units %+v err %v; D49 must not be written", page.Items, err)
	}
}
