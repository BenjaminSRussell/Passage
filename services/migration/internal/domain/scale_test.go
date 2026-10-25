package domain_test

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

const scaleBudget = 30 * time.Minute

func TestScale_FullSizeAXDatasetLoadsReconcilesAndRerunsWithoutChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("full-size AX dataset skipped under -short")
	}
	set := axDataset(50_000, 20_000, 100_000, 10_000)
	want := map[string]int{"customer": 50_000, "vendor": 20_000, "item": 100_000, "bom": 10_000}
	pipelines := newPipelines(set)

	start := time.Now()
	first := loadAll(t, pipelines)
	if elapsed := time.Since(start); elapsed >= scaleBudget {
		t.Fatalf("load took %s, budget %s", elapsed, scaleBudget)
	}
	for entity, rep := range first {
		if rep.Status != domain.StatusSucceeded || rep.Created != want[entity] || len(rep.Rejects) != 0 {
			t.Fatalf("%s: status=%s created=%d rejects=%d", entity, rep.Status, rep.Created, len(rep.Rejects))
		}
		if rep.Reconciliation == nil || !rep.Reconciliation.OK() || rep.Reconciliation.Source != want[entity] || rep.Reconciliation.Target != want[entity] {
			t.Fatalf("%s: reconciliation %+v", entity, rep.Reconciliation)
		}
	}

	for entity, rep := range loadAll(t, pipelines) {
		if rep.Created != 0 || rep.Updated != 0 || rep.Unchanged != want[entity] {
			t.Fatalf("%s: re-run created=%d updated=%d unchanged=%d", entity, rep.Created, rep.Updated, rep.Unchanged)
		}
	}
}
