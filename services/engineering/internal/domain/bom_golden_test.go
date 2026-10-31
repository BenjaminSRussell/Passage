package domain_test

import (
	"slices"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/golden"
)

// p95Under runs explode 100 times and fails when the 95th percentile reaches
// limit (AC-9.1: p95 over 100 runs < 500 ms).
func p95Under(t *testing.T, limit time.Duration, explode func() error) time.Duration {
	t.Helper()
	runs := make([]time.Duration, 100)
	for i := range runs {
		start := time.Now()
		if err := explode(); err != nil {
			t.Fatal(err)
		}
		runs[i] = time.Since(start)
	}
	slices.Sort(runs)
	p95 := runs[94]
	if p95 >= limit {
		t.Fatalf("explosion p95 over 100 runs = %v, want < %v", p95, limit)
	}
	return p95
}

// The tree fixture's flat list is checked line by line against the CSV the
// independent oracle (testdata/gen_bom_golden.py) wrote, not only by digest.
func TestExplodeSevenLevel2000MatchesOracleCSV(t *testing.T) {
	want, err := golden.FlatCSV("../../testdata/bom-golden-7x2000.csv")
	if err != nil {
		t.Fatal(err)
	}
	cat := goldenCatalog()
	req := domain.ExplodeRequest{ProductID: "L0-0000", AsOf: date("2026-03-01"), Quantity: dec("10"), Levels: 7}
	got, err := domain.Explode(cat, req)
	if err != nil {
		t.Fatal(err)
	}
	if diffs := golden.FlatDiff(got.Flat, want); len(diffs) > 0 {
		t.Fatalf("%d flat lines differ from the oracle, first: %v", len(diffs), diffs[0])
	}
	p95 := p95Under(t, 500*time.Millisecond, func() error { _, err := domain.Explode(cat, req); return err })
	t.Logf("tree fixture: %d flat lines, p95 %v", len(got.Flat), p95)
}

func countTree(n *domain.Node) int {
	total := len(n.Children)
	for _, ch := range n.Children {
		total += countTree(ch)
	}
	return total
}

// The adversarial fixture: shared sub-assemblies, skip-level lines, phantoms,
// by- and co-products, line effectivity around the as-of date, scrap, series,
// round-up, decimal quantities and dated, site, draft and quantity-ranged
// versions with decoy lines that change the result if the wrong one is used.
func TestExplodeAdversarialDAGMatchesOracle(t *testing.T) {
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
	cat := catalogOf(versions...)
	req := domain.ExplodeRequest{ProductID: fx.Root, SiteID: fx.SiteID, AsOf: fx.AsOfDate(), Quantity: fx.Quantity, Levels: fx.Levels}
	got, err := domain.Explode(cat, req)
	if err != nil {
		t.Fatal(err)
	}
	if diffs := golden.FlatDiff(got.Flat, want); len(diffs) > 0 {
		t.Fatalf("%d flat lines differ from the oracle, first: %v", len(diffs), diffs[0])
	}
	total := decimal.Zero
	for _, f := range got.Flat {
		total = total.Add(f.Quantity)
	}
	if n := countTree(got.Root); n != fx.Expected.TreeNodes || len(got.Flat) != fx.Expected.FlatCount || !total.Equal(fx.Expected.TotalQuantity) {
		t.Fatalf("tree %d nodes, flat %d lines, total %s; oracle %d, %d, %s", n, len(got.Flat), total, fx.Expected.TreeNodes, fx.Expected.FlatCount, fx.Expected.TotalQuantity)
	}
	p95 := p95Under(t, 500*time.Millisecond, func() error { _, err := domain.Explode(cat, req); return err })
	t.Logf("dag fixture: %d tree nodes, %d flat lines, p95 %v", countTree(got.Root), len(got.Flat), p95)

	// The same catalog seen from another site must differ: site-specific
	// versions are what the S1 request resolved.
	other := req
	other.SiteID = "S2"
	if elsewhere, err := domain.Explode(cat, other); err != nil || len(golden.FlatDiff(elsewhere.Flat, want)) == 0 {
		t.Fatalf("explosion for site S2 = %v; want a different flat list than S1", err)
	}
}
