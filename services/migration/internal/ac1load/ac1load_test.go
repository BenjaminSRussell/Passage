package ac1load

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

func TestRun_TinyDatasetLoadsReconcilesAndRerunsUnchanged(t *testing.T) {
	vols := Volumes{Customers: 20, Vendors: 8, Items: 40, BOMs: 4}
	got, err := Run(context.Background(), domain.NewMemXref(), vols)
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK() {
		t.Fatalf("load not ok: elapsed=%s entities=%d", got.Elapsed, len(got.Entities))
	}
	want := map[string]int{"customer": 20, "vendor": 8, "item": 40, "bom": 4}
	if len(got.Entities) != 4 {
		t.Fatalf("entities = %d", len(got.Entities))
	}
	for _, e := range got.Entities {
		n := want[e.Entity]
		if e.Report.Status != domain.StatusSucceeded || e.Report.Created != n || len(e.Report.Rejects) != 0 {
			t.Fatalf("%s: status=%s created=%d rejects=%d", e.Entity, e.Report.Status, e.Report.Created, len(e.Report.Rejects))
		}
		if e.Report.Reconciliation == nil || !e.Report.Reconciliation.OK() || e.Report.Reconciliation.Source != n || e.Report.Reconciliation.Target != n {
			t.Fatalf("%s: reconciliation %+v", e.Entity, e.Report.Reconciliation)
		}
		again := got.Rerun[e.Entity]
		if again.Created != 0 || again.Updated != 0 || again.Unchanged != n {
			t.Fatalf("%s: re-run created=%d updated=%d unchanged=%d", e.Entity, again.Created, again.Updated, again.Unchanged)
		}
	}
}

func TestDocument_FailsWhenReconciliationMissesOrBudgetExceeded(t *testing.T) {
	ok := Result{
		Volumes:  Volumes{Customers: 1},
		Elapsed:  time.Second,
		Entities: []EntityResult{{Entity: "customer", Report: domain.Report{Status: domain.StatusSucceeded, Created: 1, Reconciliation: &domain.ReconcileReport{Source: 1, Target: 1, Checks: []domain.Check{{Name: domain.CheckRowCount, OK: true}}}}}},
		Rerun:    map[string]domain.Report{"customer": {Unchanged: 1}},
	}
	doc := Document(ok)
	if !doc.OK || doc.Issue != 10 || doc.Criterion != "AC1" {
		t.Fatalf("ok document = %+v", doc)
	}
	miss := ok
	miss.Elapsed = Budget
	if Document(miss).OK {
		t.Fatal("elapsed at the budget must fail AC1")
	}
	broken := ok
	broken.Entities[0].Report.Reconciliation = &domain.ReconcileReport{Source: 1, Target: 0, Checks: []domain.Check{{Name: domain.CheckRowCount, OK: false}}}
	if Document(broken).OK {
		t.Fatal("failed reconciliation must fail AC1")
	}
}

func TestCommittedReport_FullSizeReconciledUnderBudget(t *testing.T) {
	raw, err := os.ReadFile("testdata/ac1-reconciliation.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc Doc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if !doc.OK || doc.Criterion != "AC1" || doc.Issue != 10 {
		t.Fatalf("committed report is not a passing AC1 run: %+v", doc)
	}
	if doc.Volumes != FullSize {
		t.Fatalf("volumes = %+v, want %+v", doc.Volumes, FullSize)
	}
	if doc.ElapsedMs >= Budget.Milliseconds() {
		t.Fatalf("elapsed %dms exceeds budget %s", doc.ElapsedMs, Budget)
	}
	want := map[string]int{"customer": FullSize.Customers, "vendor": FullSize.Vendors, "item": FullSize.Items, "bom": FullSize.BOMs}
	if len(doc.Entities) != 4 {
		t.Fatalf("entities = %d", len(doc.Entities))
	}
	for _, e := range doc.Entities {
		n := want[e.Entity]
		if e.Created != n || e.Rejected != 0 || e.RerunCreated != 0 || e.RerunUpdated != 0 || e.RerunUnchanged != n {
			t.Fatalf("%s: created=%d rejected=%d rerun created=%d updated=%d unchanged=%d", e.Entity, e.Created, e.Rejected, e.RerunCreated, e.RerunUpdated, e.RerunUnchanged)
		}
		if e.Reconciliation == nil || !e.Reconciliation.OK() || e.Reconciliation.Source != n || e.Reconciliation.Target != n {
			t.Fatalf("%s: reconciliation %+v", e.Entity, e.Reconciliation)
		}
	}
}
