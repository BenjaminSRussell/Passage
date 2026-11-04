package ac1load

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

type memTarget map[string]*domain.MemLoader

func newMemTarget() memTarget {
	t := memTarget{}
	for _, e := range entities {
		t[e] = domain.NewMemLoader()
	}
	return t
}

func (t memTarget) LoadBatch(ctx context.Context, entity string, items []domain.LoadItem) ([]domain.LoadResult, error) {
	return t[entity].LoadBatch(ctx, entity, items)
}

func (t memTarget) Read(ctx context.Context, entity string) ([]domain.Record, error) {
	return t[entity].Read(ctx, entity)
}

var tiny = Volumes{Customers: 20, Vendors: 8, Items: 40, BOMs: 4}

func TestRun_TinyDatasetLoadsReconcilesAndRerunsUnchanged(t *testing.T) {
	got, err := Run(context.Background(), domain.NewMemXref(), newMemTarget(), tiny)
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK() {
		t.Fatalf("load not ok: elapsed=%s entities=%d", got.Elapsed, len(got.Entities))
	}
	if len(got.Entities) != 4 {
		t.Fatalf("entities = %d", len(got.Entities))
	}
	for _, e := range got.Entities {
		n := tiny.Count(e.Entity)
		if e.Report.Status != domain.StatusSucceeded || e.Report.Created != n || len(e.Report.Rejects) != 0 {
			t.Fatalf("%s: status=%s created=%d rejects=%d", e.Entity, e.Report.Status, e.Report.Created, len(e.Report.Rejects))
		}
		if e.Report.Reconciliation == nil || !e.Report.Reconciliation.OK() || e.Report.Reconciliation.Source != n || e.Report.Reconciliation.Target != n {
			t.Fatalf("%s: reconciliation %+v", e.Entity, e.Report.Reconciliation)
		}
		if e.SourceHash == "" || e.SourceHash != e.TargetHash {
			t.Fatalf("%s: hash totals source=%q target=%q", e.Entity, e.SourceHash, e.TargetHash)
		}
		again := got.Rerun[e.Entity]
		if again.Created != 0 || again.Updated != 0 || again.Unchanged != n {
			t.Fatalf("%s: re-run created=%d updated=%d unchanged=%d", e.Entity, again.Created, again.Updated, again.Unchanged)
		}
	}
}

func TestRun_ReconciliationFailsWhenATargetRowIsMutatedOrDropped(t *testing.T) {
	cases := map[string]func(rows map[string]domain.LoadItem){
		"mutated": func(rows map[string]domain.LoadItem) {
			for id, it := range rows {
				it.Fields["name"] = "tampered"
				rows[id] = it
				return
			}
		},
		"dropped": func(rows map[string]domain.LoadItem) {
			for id := range rows {
				delete(rows, id)
				return
			}
		},
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			target := newMemTarget()
			if _, err := Run(context.Background(), domain.NewMemXref(), target, tiny); err != nil {
				t.Fatal(err)
			}
			tamper(target["customer"].Rows)
			m, err := Mapping("customer")
			if err != nil {
				t.Fatal(err)
			}
			rep, srcHash, tgtHash, err := Reconcile(context.Background(), "customer", m, Dataset(tiny)["customer"], target["customer"])
			if err != nil {
				t.Fatal(err)
			}
			if rep.OK() || srcHash == tgtHash {
				t.Fatalf("tampered target reconciled: %+v source=%s target=%s", rep, srcHash, tgtHash)
			}
		})
	}
}

func TestDocument_FailsWhenReconciliationMissesOrBudgetExceeded(t *testing.T) {
	ok := Result{
		Volumes:  Volumes{Customers: 1},
		Elapsed:  time.Second,
		Entities: []EntityResult{{Entity: "customer", Report: domain.Report{Status: domain.StatusSucceeded, Created: 1, Reconciliation: &domain.ReconcileReport{Source: 1, Target: 1, Checks: []domain.Check{{Name: domain.CheckRowCount, OK: true}}}}}},
		Rerun:    map[string]domain.Report{"customer": {Unchanged: 1}},
	}
	doc := Document(ok, Env{})
	if !doc.OK || doc.Issue != 10 || doc.Criterion != "AC1" {
		t.Fatalf("ok document = %+v", doc)
	}
	miss := ok
	miss.Elapsed = Budget
	if Document(miss, Env{}).OK {
		t.Fatal("elapsed at the budget must fail AC1")
	}
	broken := ok
	broken.Entities = []EntityResult{{Entity: "customer", Report: domain.Report{Status: domain.StatusSucceeded, Created: 1, Reconciliation: &domain.ReconcileReport{Source: 1, Target: 0, Checks: []domain.Check{{Name: domain.CheckRowCount, OK: false}}}}}}
	if Document(broken, Env{}).OK {
		t.Fatal("failed reconciliation must fail AC1")
	}
}
