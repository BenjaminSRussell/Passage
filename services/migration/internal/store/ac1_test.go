package store_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/migration/internal/ac1load"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

func TestPostgres_AC1FullSizeLoadMeetsTimeBudgetAndReconciles(t *testing.T) {
	if testing.Short() {
		t.Skip("full-size AC1 Postgres load skipped under -short")
	}
	s := newStore(t)
	got, err := ac1load.Run(principalContext(1), s.Xref(), ac1load.FullSize)
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK() {
		t.Fatalf("AC1 load missed the budget or failed reconciliation: elapsed=%s", got.Elapsed)
	}
	want := map[string]int{"customer": 50_000, "vendor": 20_000, "item": 100_000, "bom": 10_000}
	for _, e := range got.Entities {
		n := want[e.Entity]
		if e.Report.Created != n || e.Report.Reconciliation == nil || !e.Report.Reconciliation.OK() {
			t.Fatalf("%s: created=%d recon=%+v", e.Entity, e.Report.Created, e.Report.Reconciliation)
		}
		again := got.Rerun[e.Entity]
		if again.Created != 0 || again.Updated != 0 || again.Unchanged != n {
			t.Fatalf("%s: re-run created=%d updated=%d unchanged=%d", e.Entity, again.Created, again.Updated, again.Unchanged)
		}
		if e.Report.Status != domain.StatusSucceeded {
			t.Fatalf("%s: status=%s", e.Entity, e.Report.Status)
		}
	}
}
