package store_test

import (
	"slices"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/golden"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

// AC-9.1 through Postgres: the adversarial 7-level fixture is stored version by
// version, loaded back through the recursive catalog query (IX-ENG-01/02) and
// exploded; the flat list must equal the independent oracle's CSV and catalog
// load plus explosion must stay under 500 ms at p95 over 100 runs.
func TestPostgres_GoldenDAG_CatalogLoadAndExplodeMatchOracle(t *testing.T) {
	s, _, ctx := newStore(t, 7)
	fx, err := golden.Load("../../testdata/bom-dag-7x2000.json")
	if err != nil {
		t.Fatal(err)
	}
	want, err := golden.FlatCSV("../../testdata/bom-dag-7x2000.csv")
	if err != nil {
		t.Fatal(err)
	}
	versions, err := fx.BomVersions()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range versions {
		v.ID = ids.New()
		if _, err := s.CreateBomVersion(ctx, v, store.Idem{}, noCheck); err != nil {
			t.Fatalf("store %s: %v", v.ProductID, err)
		}
	}
	req := domain.ExplodeRequest{ProductID: fx.Root, SiteID: fx.SiteID, AsOf: fx.AsOfDate(), Quantity: fx.Quantity, Levels: fx.Levels}
	explode := func() (domain.Explosion, error) {
		snapshot, err := s.Catalog(ctx, []string{fx.Root}, false)
		if err != nil {
			return domain.Explosion{}, err
		}
		return domain.Explode(snapshot, req)
	}
	got, err := explode()
	if err != nil {
		t.Fatal(err)
	}
	if diffs := golden.FlatDiff(got.Flat, want); len(diffs) > 0 {
		t.Fatalf("%d flat lines differ from the oracle after a Postgres round trip, first: %v", len(diffs), diffs[0])
	}
	runs := make([]time.Duration, 100)
	for i := range runs {
		start := time.Now()
		if _, err := explode(); err != nil {
			t.Fatal(err)
		}
		runs[i] = time.Since(start)
	}
	slices.Sort(runs)
	if runs[94] >= 500*time.Millisecond {
		t.Fatalf("catalog load + explosion p95 over 100 runs = %v, want < 500ms", runs[94])
	}
	t.Logf("catalog load + explosion p95 %v (max %v)", runs[94], runs[99])
}
