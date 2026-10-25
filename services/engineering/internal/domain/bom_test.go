package domain_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

type memCatalog map[string][]domain.BomVersion

func (c memCatalog) VersionsFor(p string) []domain.BomVersion { return c[p] }

func (c memCatalog) VersionsUsing(comp string) []domain.BomVersion {
	var out []domain.BomVersion
	for _, vs := range c {
		for _, v := range vs {
			for _, l := range v.Lines {
				if l.ComponentProductID == comp {
					out = append(out, v)
					break
				}
			}
		}
	}
	return out
}

func date(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func line(no int, comp string, qty int64) domain.BomLine {
	return domain.BomLine{LineNo: no, ComponentProductID: comp, Quantity: decimal.NewFromInt(qty), Series: decimal.NewFromInt(1), LineType: domain.LineItem}
}

func version(id, product, from, to string, lines ...domain.BomLine) domain.BomVersion {
	v := domain.BomVersion{ID: id, ProductID: product, EffectiveFrom: date(from), Status: domain.StatusActive, Lines: lines}
	if to != "" {
		v.EffectiveTo = date(to)
	}
	return v
}

func catalogOf(vs ...domain.BomVersion) memCatalog {
	c := memCatalog{}
	for _, v := range vs {
		c[v.ProductID] = append(c[v.ProductID], v)
	}
	return c
}

func request(product string, qty int64, levels int) domain.ExplodeRequest {
	return domain.ExplodeRequest{ProductID: product, AsOf: date("2026-03-01"), Quantity: decimal.NewFromInt(qty), Levels: levels}
}

func goldenCatalog() memCatalog {
	sizes := []int{1, 4, 12, 30, 60, 150, 300, 1444}
	pid := func(l, i int) string { return fmt.Sprintf("L%d-%04d", l, i) }
	lines := map[string][]domain.BomLine{}
	for l := 1; l < len(sizes); l++ {
		for i := 0; i < sizes[l]; i++ {
			ln := line(i, pid(l, i), int64(i%5+1))
			if i%3 == 0 {
				ln.ScrapVarPct, ln.RoundUp = decimal.NewFromInt(5), domain.RoundUpQuantity
			}
			if l == 4 && i%10 == 0 {
				ln.LineType = domain.LinePhantom
			}
			parent := pid(l-1, i%sizes[l-1])
			lines[parent] = append(lines[parent], ln)
		}
	}
	c := memCatalog{}
	for parent, ls := range lines {
		c[parent] = []domain.BomVersion{version("v-"+parent, parent, "2025-01-01", "", ls...)}
	}
	return c
}

func countNodes(n *domain.Node) int {
	total := len(n.Children)
	for _, ch := range n.Children {
		total += countNodes(ch)
	}
	return total
}

func TestExplodeSevenLevel2000Golden(t *testing.T) {
	var golden struct {
		BomLines, TreeNodes, FlatCount int
		RequestQty, AsOf               string
		TotalQuantity, Sha256          string
	}
	raw, err := os.ReadFile("../../testdata/bom-golden-7x2000.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	cat := goldenCatalog()
	req := domain.ExplodeRequest{ProductID: "L0-0000", AsOf: date(golden.AsOf), Quantity: dec(golden.RequestQty), Levels: 7}

	start := time.Now()
	got, err := domain.Explode(cat, req)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("explosion took %v, want < 500ms", elapsed)
	}

	total, digest := decimal.Zero, sha256.New()
	for _, f := range got.Flat {
		total = total.Add(f.Quantity)
		fmt.Fprintf(digest, "%s,%s\n", f.ProductID, f.Quantity)
	}
	if n := countNodes(got.Root); n != golden.TreeNodes {
		t.Errorf("tree nodes = %d, want %d", n, golden.TreeNodes)
	}
	if len(got.Flat) != golden.FlatCount {
		t.Errorf("flat lines = %d, want %d", len(got.Flat), golden.FlatCount)
	}
	if total.String() != golden.TotalQuantity {
		t.Errorf("total quantity = %s, want %s", total, golden.TotalQuantity)
	}
	if h := hex.EncodeToString(digest.Sum(nil)); h != golden.Sha256 {
		t.Errorf("flat list digest = %s, want %s", h, golden.Sha256)
	}
	bomLines := 0
	for _, vs := range cat {
		bomLines += len(vs[0].Lines)
	}
	if bomLines != golden.BomLines {
		t.Errorf("generated BOM lines = %d, want %d", bomLines, golden.BomLines)
	}
}

func TestResolveAsOf(t *testing.T) {
	q499, q500 := dec("499"), dec("500")
	v1 := version("V1", "P", "2025-01-01", "2026-05-31")
	v1.ToQty = &q499
	v2 := version("V2", "P", "2026-06-01", "")
	v2.ToQty = &q499
	v3 := version("V3", "P", "2026-01-01", "2026-12-31")
	v3.FromQty = &q500
	draft := version("D", "P", "2025-01-01", "")
	draft.Status = domain.StatusDraft
	siteSpecific := version("S", "P", "2026-06-01", "")
	siteSpecific.SiteID = "site-1"
	all := []domain.BomVersion{v1, v2, v3, draft}

	tests := []struct {
		name    string
		vs      []domain.BomVersion
		site    string
		asOf    string
		qty     string
		want    string
		wantErr string
	}{
		{"before the split picks V1", all, "", "2026-01-01", "1", "V1", ""},
		{"split day picks V2", all, "", "2026-06-01", "1", "V2", ""},
		{"last day of V1 with large qty picks V3", all, "", "2026-05-31", "1000", "V3", ""},
		{"expired version still resolves for history", all, "", "2025-03-01", "1", "V1", ""},
		{"before every version", all, "", "2024-12-31", "1", "", domain.CodeBomNoVersion},
		{"draft never resolves", []domain.BomVersion{draft}, "", "2026-01-01", "1", "", domain.CodeBomNoVersion},
		{"site specific beats all sites", append([]domain.BomVersion{siteSpecific}, all...), "site-1", "2026-06-01", "1", "S", ""},
		{"other site falls back to all sites", append([]domain.BomVersion{siteSpecific}, all...), "site-2", "2026-06-01", "1", "V2", ""},
		{"two equally specific versions", []domain.BomVersion{version("V2a", "P", "2026-06-01", ""), version("V2b", "P", "2026-06-01", "")}, "", "2026-06-02", "1", "", domain.CodeBomOverlap},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := domain.ResolveVersion(tt.vs, "P", tt.site, date(tt.asOf), dec(tt.qty))
			if tt.wantErr != "" {
				if !domain.HasCode(err, tt.wantErr) {
					t.Fatalf("err = %v, want %s", err, tt.wantErr)
				}
				return
			}
			if err != nil || got.ID != tt.want {
				t.Fatalf("got %q err %v, want %s", got.ID, err, tt.want)
			}
		})
	}
}

func TestExplodeRules(t *testing.T) {
	scrapped := line(10, "C", 3)
	scrapped.Series, scrapped.ScrapVarPct, scrapped.ScrapConst, scrapped.RoundUp = decimal.NewFromInt(2), decimal.NewFromInt(5), decimal.NewFromInt(1), domain.RoundUpQuantity
	phantom := line(10, "X", 2)
	phantom.LineType = domain.LinePhantom
	byProduct := line(20, "W", 1)
	byProduct.LineType = domain.LineByProduct
	expiredLine := line(30, "OLD", 1)
	expiredLine.EffectiveTo = date("2026-02-28")
	badScrap := line(10, "C", 1)
	badScrap.ScrapVarPct = decimal.NewFromInt(101)

	tests := []struct {
		name     string
		cat      memCatalog
		req      domain.ExplodeRequest
		wantFlat map[string]string
		wantErr  string
	}{
		{"scrap series and round up", catalogOf(version("a", "A", "2025-01-01", "", scrapped)), request("A", 7, 5), map[string]string{"C": "13"}, ""},
		{"phantom replaced by its own BOM", catalogOf(version("a", "A", "2025-01-01", "", phantom, line(20, "Y", 1)), version("x", "X", "2025-01-01", "", line(10, "Z", 5))), request("A", 3, 5), map[string]string{"Y": "3", "Z": "30"}, ""},
		{"phantom without BOM", catalogOf(version("a", "A", "2025-01-01", "", phantom)), request("A", 1, 5), nil, domain.CodeBomPhantomNoBom},
		{"by-products and inactive lines are not consumed", catalogOf(version("a", "A", "2025-01-01", "", line(10, "C", 1), byProduct, expiredLine)), request("A", 2, 5), map[string]string{"C": "2"}, ""},
		{"levels cap makes sub-assembly a leaf", catalogOf(version("a", "A", "2025-01-01", "", line(10, "B", 1)), version("b", "B", "2025-01-01", "", line(10, "C", 1))), request("A", 1, 1), map[string]string{"B": "1"}, ""},
		{"cycle", catalogOf(version("a", "A", "2025-01-01", "", line(10, "B", 1)), version("b", "B", "2025-01-01", "", line(10, "A", 1))), request("A", 1, 5), nil, domain.CodeBomCycle},
		{"empty catalog", memCatalog{}, request("A", 1, 5), nil, domain.CodeBomNoVersion},
		{"levels zero", catalogOf(version("a", "A", "2025-01-01", "")), request("A", 1, 0), nil, domain.CodeBomLevels},
		{"zero quantity", catalogOf(version("a", "A", "2025-01-01", "")), request("A", 0, 5), nil, domain.CodeBomQty},
		{"scrap above 100 percent", catalogOf(version("a", "A", "2025-01-01", "", badScrap)), request("A", 1, 5), nil, domain.CodeBomLine},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := domain.Explode(tt.cat, tt.req)
			if tt.wantErr != "" {
				if !domain.HasCode(err, tt.wantErr) {
					t.Fatalf("err = %v, want %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			flat := map[string]string{}
			for _, f := range got.Flat {
				flat[f.ProductID] = f.Quantity.String()
			}
			if fmt.Sprint(flat) != fmt.Sprint(tt.wantFlat) {
				t.Fatalf("flat = %v, want %v", flat, tt.wantFlat)
			}
		})
	}
}

func TestCheckCycle(t *testing.T) {
	cat := catalogOf(version("a", "A", "2025-01-01", "", line(10, "B", 1)), version("b", "B", "2025-01-01", ""))
	asOf := date("2026-03-01")
	if err := domain.CheckCycle(cat, "B", "A", asOf); !domain.HasCode(err, domain.CodeBomCycle) {
		t.Errorf("adding A under B: err = %v, want cycle", err)
	}
	if err := domain.CheckCycle(cat, "A", "C", asOf); err != nil {
		t.Errorf("unrelated component: err = %v, want nil", err)
	}
}

func TestCheckNoOverlap(t *testing.T) {
	q500 := dec("500")
	big := version("big", "P", "2026-01-01", "")
	big.FromQty = &q500
	otherSite := version("os", "P", "2026-01-01", "")
	otherSite.SiteID = "site-1"
	existing := version("e", "P", "2026-01-01", "2026-06-30")
	tests := []struct {
		name string
		cand domain.BomVersion
		bad  bool
	}{
		{"overlapping dates", version("n", "P", "2026-06-30", ""), true},
		{"adjacent dates", version("n", "P", "2026-07-01", ""), false},
		{"other site", otherSite, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := domain.CheckNoOverlap([]domain.BomVersion{existing}, tt.cand)
			if tt.bad != domain.HasCode(err, domain.CodeBomOverlap) {
				t.Fatalf("err = %v, want overlap=%v", err, tt.bad)
			}
		})
	}
	q499 := dec("499")
	low := version("low", "P", "2026-01-01", "")
	low.ToQty = &q499
	if err := domain.CheckNoOverlap([]domain.BomVersion{low}, big); err != nil {
		t.Errorf("disjoint quantity ranges: err = %v, want nil", err)
	}
}

func TestWhereUsed(t *testing.T) {
	cat := catalogOf(
		version("s", "SUB", "2025-01-01", "", line(10, "C", 1)),
		version("p1", "P1", "2025-01-01", "", line(10, "SUB", 1)),
		version("p2", "P2", "2025-01-01", "", line(10, "C", 2)),
		version("old", "P3", "2025-01-01", "2025-12-31", line(10, "C", 1)),
	)
	asOf := date("2026-03-01")

	direct, err := domain.WhereUsed(cat, "C", asOf, 1)
	if err != nil || len(direct) != 2 || direct[0].ProductID != "P2" || direct[1].ProductID != "SUB" {
		t.Fatalf("direct where-used = %v err %v, want P2 and SUB", direct, err)
	}
	if _, err := domain.WhereUsed(cat, "C", asOf, 0); !domain.HasCode(err, domain.CodeBomLevels) {
		t.Errorf("levels 0: err = %v, want %s", err, domain.CodeBomLevels)
	}
	cyc := catalogOf(version("a", "A", "2025-01-01", "", line(10, "B", 1)), version("b", "B", "2025-01-01", "", line(10, "A", 1)))
	if _, err := domain.WhereUsed(cyc, "A", asOf, 25); !domain.HasCode(err, domain.CodeBomCycle) {
		t.Errorf("cyclic data: err = %v, want %s", err, domain.CodeBomCycle)
	}
}

func collectProducts(tree []*domain.UsedBy, into map[string]bool) {
	for _, n := range tree {
		into[n.ProductID] = true
		collectProducts(n.Parents, into)
	}
}

func TestExplosionImplosionInverse(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(2, 9).Draw(t, "products")
		name := func(i int) string { return fmt.Sprintf("P%d", i) }
		cat := memCatalog{}
		for i := 0; i < n-1; i++ {
			var lines []domain.BomLine
			for j := i + 1; j < n; j++ {
				if rapid.Bool().Draw(t, fmt.Sprintf("edge-%d-%d", i, j)) {
					lines = append(lines, line(j, name(j), int64(rapid.IntRange(1, 4).Draw(t, "qty"))))
				}
			}
			cat[name(i)] = []domain.BomVersion{version("v"+name(i), name(i), "2025-01-01", "", lines...)}
		}
		asOf := date("2026-03-01")
		for i := 0; i < n-1; i++ {
			exp, err := domain.Explode(cat, domain.ExplodeRequest{ProductID: name(i), AsOf: asOf, Quantity: decimal.NewFromInt(1), Levels: 25})
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range exp.Flat {
				used, err := domain.WhereUsed(cat, f.ProductID, asOf, 25)
				if err != nil {
					t.Fatal(err)
				}
				seen := map[string]bool{}
				collectProducts(used, seen)
				if !seen[name(i)] {
					t.Fatalf("%s explodes to %s but where-used of %s misses it", name(i), f.ProductID, f.ProductID)
				}
			}
		}
	})
}

func TestRollupCost(t *testing.T) {
	flat := []domain.FlatLine{{ProductID: "A", Quantity: dec("3")}, {ProductID: "B", Quantity: dec("2")}, {ProductID: "C", Quantity: dec("1")}}
	costs := map[string]money.Amount{"A": money.Must("1.50", "USD"), "B": money.Must("2.00", "USD")}
	total, missing, err := domain.RollupCost(flat, costs, "USD")
	if err != nil || total.Value.String() != "8.5" || fmt.Sprint(missing) != "[C]" {
		t.Fatalf("total %s missing %v err %v, want 8.5 and [C]", total, missing, err)
	}
}
