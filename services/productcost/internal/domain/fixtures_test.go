package domain

// Wave 39 proof fixtures (issue #39, "Proof (testable acceptance)"):
//
//   - testdata/rollup-50: 50 multi-level products and the reference workbook of
//     their unit cost per cost component, to the cent (AC1, AC-39.2).
//   - testdata/variance-200: 200 ended production orders and the reference
//     decomposition of each (AC3, AC-39.6).
//
// The expected values are produced by refRoll and refVariance below: an
// independent model of the costing rules in exact rational arithmetic
// (math/big), with its own rounding and its own minor-unit table, sharing no
// code with the engine. The worked example (FG-100, FR-39.4) and the AC-39.5
// order are in the fixtures, and their workbook rows are checked against the
// hand-derived literals of the spec, which anchors the reference model itself.
//
// Regenerate the fixture files with PRODUCTCOST_UPDATE_FIXTURES=1; the tests
// then rewrite testdata and fail so a regeneration is never silent.

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math/big"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

const (
	rollupDir   = "testdata/rollup-50"
	varianceDir = "testdata/variance-200"
	updateEnv   = "PRODUCTCOST_UPDATE_FIXTURES"
)

func updating() bool { return os.Getenv(updateEnv) == "1" }

// --- exact rational helpers -------------------------------------------------

func rat(v decimal.Decimal) *big.Rat {
	r, ok := new(big.Rat).SetString(v.String())
	if !ok {
		panic("bad decimal " + v.String())
	}
	return r
}

func ratInt(n int64) *big.Rat { return new(big.Rat).SetInt64(n) }

func radd(a, b *big.Rat) *big.Rat { return new(big.Rat).Add(a, b) }
func rsub(a, b *big.Rat) *big.Rat { return new(big.Rat).Sub(a, b) }
func rmul(a, b *big.Rat) *big.Rat { return new(big.Rat).Mul(a, b) }
func rdiv(a, b *big.Rat) *big.Rat { return new(big.Rat).Quo(a, b) }

// pct turns a percentage into the factor 1 + p/100.
func pct(p decimal.Decimal) *big.Rat { return radd(ratInt(1), rdiv(rat(p), ratInt(100))) }

// roundHalfAway formats r at scale decimals, rounding half away from zero
// (BR-39.1), without going through shopspring.
func roundHalfAway(r *big.Rat, scale int) string {
	pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	scaled := new(big.Rat).Mul(new(big.Rat).Abs(r), new(big.Rat).SetInt(pow))
	scaled.Add(scaled, big.NewRat(1, 2))
	q := new(big.Int).Quo(scaled.Num(), scaled.Denom())
	s := q.String()
	if scale > 0 {
		if len(s) <= scale {
			s = strings.Repeat("0", scale-len(s)+1) + s
		}
		s = s[:len(s)-scale] + "." + s[len(s)-scale:]
	}
	if r.Sign() < 0 && q.Sign() != 0 {
		s = "-" + s
	}
	return s
}

// --- reference roll-up (exact) ----------------------------------------------

type ratSplit map[string]*big.Rat

func (s ratSplit) add(c string, v *big.Rat) {
	if s[c] == nil {
		s[c] = new(big.Rat)
	}
	s[c] = radd(s[c], v)
}

func (s ratSplit) addScaled(o ratSplit, f *big.Rat) {
	for c, v := range o {
		s.add(c, rmul(v, f))
	}
}

func (s ratSplit) copy() ratSplit {
	out := ratSplit{}
	for c, v := range s {
		out[c] = new(big.Rat).Set(v)
	}
	return out
}

type refResult struct {
	own, rolled, direct ratSplit
	hours               *big.Rat
}

func (r refResult) total() ratSplit {
	out := r.own.copy()
	out.addScaled(r.rolled, ratInt(1))
	return out
}

// refRoll costs id under the Multilevel mode and the Legal view in exact
// arithmetic: the FR-39.4 algorithm as written in the README (BOM scrap, route
// scrap compounding backward over the material and run time, setup per lot,
// phantoms in place with the parent's sheet, sheet nodes in order).
func refRoll(cat Catalog, id string, memo map[string]refResult) refResult {
	if r, ok := memo[id]; ok {
		return r
	}
	it := cat[id]
	res := refResult{own: ratSplit{}, rolled: ratSplit{}, hours: new(big.Rat)}
	if it.Kind == KindPurchased {
		res.own.add("DM", rat(*it.Price))
		res.direct = res.own.copy()
		memo[id] = res
		return res
	}
	// Quantity processed at operation k per unit of output.
	opFactor := make([]*big.Rat, len(it.Operations))
	acc := ratInt(1)
	for k := len(it.Operations) - 1; k >= 0; k-- {
		acc = rmul(acc, pct(it.Operations[k].ScrapPct))
		opFactor[k] = acc
	}
	material := ratInt(1)
	if len(opFactor) > 0 {
		material = opFactor[0]
	}
	for _, ln := range it.BOM {
		q := rmul(rmul(rat(ln.Qty), pct(ln.ScrapPct)), material)
		child := cat[ln.ChildID]
		cr := refRoll(cat, ln.ChildID, memo)
		switch {
		case child.Kind == KindPurchased:
			res.own.addScaled(cr.total(), q)
		case ln.Phantom:
			res.own.addScaled(cr.direct, q)
			res.rolled.addScaled(cr.rolled, q)
			res.hours = radd(res.hours, rmul(cr.hours, q))
		default:
			res.rolled.addScaled(cr.total(), q)
		}
	}
	for k, op := range it.Operations {
		lot := op.LotSize
		if lot.IsZero() {
			lot = it.CostingLot
		}
		if lot.IsZero() {
			lot = decimal.NewFromInt(1)
		}
		comp := op.Component
		if comp == "" {
			comp = "DL"
		}
		run := rmul(rat(op.RunHours), opFactor[k])
		setup := rdiv(rat(op.SetupHours), rat(lot))
		res.own.add(comp, rmul(radd(run, setup), rat(op.Rate)))
		res.hours = radd(res.hours, radd(run, setup))
	}
	res.direct = res.own.copy()
	sub := map[string]*big.Rat{}
	basis := func(comps []string, rolled bool) *big.Rat {
		b := new(big.Rat)
		for _, c := range comps {
			if v := res.own[c]; v != nil {
				b = radd(b, v)
			}
			if v := res.rolled[c]; rolled && v != nil {
				b = radd(b, v)
			}
		}
		return b
	}
	for _, n := range it.Sheet {
		switch n.Type {
		case NodeBasis:
			sub[n.ID] = basis(n.Basis, n.IncludeRolled)
		case NodeTotal:
			t := new(big.Rat)
			for _, r := range n.Refs {
				t = radd(t, sub[r])
			}
			sub[n.ID] = t
		case NodeSurcharge:
			b := basis(n.Basis, n.IncludeRolled)
			if n.BasisRef != "" {
				b = sub[n.BasisRef]
			}
			res.own.add(n.Component, rdiv(rmul(b, rat(n.Percentage)), ratInt(100)))
		case NodeRate:
			driver := ratInt(1)
			if n.RateUnit == "Hour" {
				driver = res.hours
			}
			res.own.add(n.Component, rmul(rat(n.Rate), driver))
		}
	}
	memo[id] = res
	return res
}

// --- rollup-50 fixture generator --------------------------------------------

func fxAmount(r *rand.Rand, lo, hi int64, scale int32) decimal.Decimal {
	return decimal.New(lo+r.Int64N(hi-lo+1), -scale)
}

// genRollupCatalog builds the deterministic 50-product catalog: the FR-39.4
// worked example plus 49 generated finished goods over up to five
// manufactured levels with shared subassemblies, phantom lines, BOM and route
// scrap, operation lot sizes and all four sheet node types.
func genRollupCatalog() (Catalog, []string) {
	r := rand.New(rand.NewPCG(39, 2026))
	cat := workedCatalog()
	products := []string{"FG-100"}
	var raws []string
	for i := 1; i <= 40; i++ {
		id := fmt.Sprintf("RM-%03d", i)
		p := fxAmount(r, 50, 25000, 2)
		if i%7 == 0 {
			p = fxAmount(r, 5000, 250000, 4) // four-decimal purchase prices
		}
		cat[id] = Item{ID: id, Kind: KindPurchased, Price: &p}
		raws = append(raws, id)
	}
	lots := []string{"1", "3", "5", "7", "10", "12", "25"}
	sheets := []func() []SheetNode{
		func() []SheetNode {
			return []SheetNode{
				{ID: "OH-MAT", Type: NodeSurcharge, Component: CompIndMat, Basis: []string{CompDM}, Percentage: fxAmount(r, 0, 2500, 2)},
				{ID: "OH-LAB", Type: NodeSurcharge, Component: CompIndLab, Basis: []string{CompDL}, Percentage: fxAmount(r, 0, 9000, 2)},
			}
		},
		func() []SheetNode {
			return []SheetNode{{ID: "OH-ALL", Type: NodeSurcharge, Component: CompIndMat, Basis: []string{CompDM, CompDL}, Percentage: fxAmount(r, 100, 1500, 2), IncludeRolled: true}}
		},
		func() []SheetNode {
			return []SheetNode{
				{ID: "MACH", Type: NodeRate, Component: CompIndLab, Rate: fxAmount(r, 100, 6000, 2), RateUnit: "Hour"},
				{ID: "PACK", Type: NodeRate, Component: CompIndMat, Rate: fxAmount(r, 1, 500, 2)},
			}
		},
		func() []SheetNode {
			return []SheetNode{
				{ID: "B-MAT", Type: NodeBasis, Basis: []string{CompDM}, IncludeRolled: true},
				{ID: "OH-MAT", Type: NodeSurcharge, Component: CompIndMat, BasisRef: "B-MAT", Percentage: fxAmount(r, 0, 1200, 2)},
				{ID: "B-CONV", Type: NodeBasis, Basis: []string{CompDL, "SUB"}},
				{ID: "T", Type: NodeTotal, Refs: []string{"B-MAT", "B-CONV"}},
				{ID: "OH-ADM", Type: NodeSurcharge, Component: CompIndLab, BasisRef: "T", Percentage: fxAmount(r, 0, 800, 2)},
			}
		},
		func() []SheetNode { return nil },
	}
	ops := func() []Operation {
		n := 1 + r.IntN(3)
		out := make([]Operation, n)
		for k := range out {
			op := Operation{SetupHours: fxAmount(r, 0, 300, 2), RunHours: fxAmount(r, 1, 200, 2), Rate: fxAmount(r, 2000, 12000, 2)}
			if r.IntN(4) == 0 {
				op.ScrapPct = fxAmount(r, 1, 50, 1)
			}
			if r.IntN(6) == 0 {
				op.Component = "SUB"
			}
			if r.IntN(8) == 0 {
				op.LotSize = decimal.RequireFromString(lots[r.IntN(len(lots))])
			}
			out[k] = op
		}
		return out
	}
	line := func(child string, phantomOK bool) BOMLine {
		phantomOK = phantomOK && cat[child].Kind == KindManufactured
		ln := BOMLine{ChildID: child, Qty: fxAmount(r, 25, 400, 2)}
		if r.IntN(3) == 0 {
			ln.Qty = fxAmount(r, 100, 3000, 3)
		}
		if r.IntN(3) == 0 {
			ln.ScrapPct = fxAmount(r, 1, 150, 1)
		}
		ln.Phantom = phantomOK && r.IntN(6) == 0
		return ln
	}
	manufactured := func(id string, pools [][]string, minLower int) {
		it := Item{ID: id, Kind: KindManufactured, CostingLot: decimal.RequireFromString(lots[r.IntN(len(lots))]),
			Operations: ops(), Sheet: sheets[r.IntN(len(sheets))]()}
		used := map[string]bool{}
		for k := 0; k < 1+r.IntN(2); k++ {
			c := raws[r.IntN(len(raws))]
			if !used[c] {
				used[c] = true
				it.BOM = append(it.BOM, line(c, false))
			}
		}
		for k := 0; k < minLower+r.IntN(2); k++ {
			pool := pools[r.IntN(len(pools))]
			if k == 0 {
				pool = pools[len(pools)-1] // the level just below, so depth is guaranteed
			}
			c := pool[r.IntN(len(pool))]
			if !used[c] {
				used[c] = true
				it.BOM = append(it.BOM, line(c, true))
			}
		}
		cat[id] = it
	}
	var levels [][]string
	sizes := []int{20, 14, 9, 5}
	for l, size := range sizes {
		var ids []string
		for i := 1; i <= size; i++ {
			id := fmt.Sprintf("SA%d-%02d", l+1, i)
			if l == 0 {
				manufactured(id, [][]string{raws}, 0)
			} else {
				manufactured(id, levels, 1)
			}
			ids = append(ids, id)
		}
		levels = append(levels, ids)
	}
	for i := 1; i <= 49; i++ {
		id := fmt.Sprintf("FG-%03d", i)
		// FG-001..049 sit on top of a random subassembly level (2 to 5 levels deep).
		top := levels[:1+r.IntN(len(levels))]
		manufactured(id, top, 1+r.IntN(2))
		products = append(products, id)
	}
	return cat, products
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (regenerate with %s=1)", err, updateEnv)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

func writeCSV(t *testing.T, path string, rows [][]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := csv.NewWriter(f)
	if err := w.WriteAll(rows); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func readCSV(t *testing.T, path string) [][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("%v (regenerate with %s=1)", err, updateEnv)
	}
	defer func() { _ = f.Close() }()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows[1:] // header
}

type rollupFixture struct {
	Products []string `json:"products"`
	Catalog  Catalog  `json:"catalog"`
}

// rollupWorkbook is the reference workbook: one row per product and cost
// component (component "TOTAL" for the unit cost), to the cent.
func rollupWorkbook(fx rollupFixture) [][]string {
	rows := [][]string{{"product", "component", "unit_cost"}}
	memo := map[string]refResult{}
	for _, p := range fx.Products {
		tot := refRoll(fx.Catalog, p, memo).total()
		sum := new(big.Rat)
		comps := make([]string, 0, len(tot))
		for c := range tot {
			comps = append(comps, c)
		}
		sort.Strings(comps)
		for _, c := range comps {
			rows = append(rows, []string{p, c, roundHalfAway(tot[c], 2)})
			sum = radd(sum, tot[c])
		}
		rows = append(rows, []string{p, "TOTAL", roundHalfAway(sum, 2)})
	}
	return rows
}

func maybeRegenerateRollup(t *testing.T) {
	t.Helper()
	if !updating() {
		return
	}
	cat, products := genRollupCatalog()
	fx := rollupFixture{Products: products, Catalog: cat}
	writeJSON(t, filepath.Join(rollupDir, "catalog.json"), fx)
	writeCSV(t, filepath.Join(rollupDir, "workbook.csv"), rollupWorkbook(fx))
	t.Fatalf("rewrote %s; rerun without %s", rollupDir, updateEnv)
}

// maxDepth returns the number of manufactured levels under and including id.
func maxDepth(cat Catalog, id string) int {
	it := cat[id]
	if it.Kind == KindPurchased {
		return 0
	}
	deepest := 0
	for _, ln := range it.BOM {
		if d := maxDepth(cat, ln.ChildID); d > deepest {
			deepest = d
		}
	}
	return deepest + 1
}

// TestRollupFixtures_FiftyProductsMatchReferenceWorkbook is AC1 / AC-39.2: the
// mass roll of the 50 multi-level fixture products matches the reference
// workbook to the cent for every cost component and the unit cost.
func TestRollupFixtures_FiftyProductsMatchReferenceWorkbook(t *testing.T) {
	maybeRegenerateRollup(t)
	var fx rollupFixture
	readJSON(t, filepath.Join(rollupDir, "catalog.json"), &fx)
	if len(fx.Products) != 50 {
		t.Fatalf("fixture has %d products, want 50", len(fx.Products))
	}
	for _, p := range fx.Products {
		if d := maxDepth(fx.Catalog, p); d < 2 {
			t.Fatalf("%s has %d manufactured level(s); every fixture product must be multi-level", p, d)
		}
	}
	want := map[string]map[string]string{}
	for _, row := range readCSV(t, filepath.Join(rollupDir, "workbook.csv")) {
		if want[row[0]] == nil {
			want[row[0]] = map[string]string{}
		}
		want[row[0]][row[1]] = row[2]
	}
	run := MassRoll(fx.Catalog, RollOptions{})
	if run.ErrorCount != 0 {
		t.Fatalf("fixture roll has %d errors", run.ErrorCount)
	}
	got := map[string]Result{}
	for _, r := range run.Results {
		got[r.ItemID] = r
	}
	checked := 0
	for _, p := range fx.Products {
		r, ok := got[p]
		if !ok || want[p] == nil {
			t.Fatalf("%s missing from the roll or the workbook", p)
		}
		split := r.Combined()
		for c, w := range want[p] {
			g := r.Unit.StringFixed(2)
			if c != "TOTAL" {
				g = split[c].StringFixed(2)
			}
			if g != w {
				t.Errorf("%s %s = %s, workbook %s", p, c, g, w)
			}
			checked++
		}
		for c, v := range split {
			if _, ok := want[p][c]; !ok && !v.IsZero() {
				t.Errorf("%s has component %s = %s that the workbook does not list", p, c, v)
			}
		}
	}
	t.Logf("compared %d workbook cells for %d products", checked, len(fx.Products))
}

// TestRollupWorkbook_ReproducedByReferenceAndAnchoredToSpec: the committed
// workbook is exactly what the exact reference computes from the committed
// catalog (no hand edits drift), the reference reproduces the hand-derived
// FR-39.4 worked example (FG-100 = 104.32, DM 21.20 / DL 54.00 / IND-MAT 2.12
// / IND-LAB 27.00), and the engine's six-decimal storage stays within 0.001 of
// the exact value for every component.
func TestRollupWorkbook_ReproducedByReferenceAndAnchoredToSpec(t *testing.T) {
	var fx rollupFixture
	readJSON(t, filepath.Join(rollupDir, "catalog.json"), &fx)
	rows := readCSV(t, filepath.Join(rollupDir, "workbook.csv"))
	ref := rollupWorkbook(fx)[1:]
	if len(rows) != len(ref) {
		t.Fatalf("workbook has %d rows, reference %d", len(rows), len(ref))
	}
	for i := range rows {
		if strings.Join(rows[i], ",") != strings.Join(ref[i], ",") {
			t.Fatalf("workbook row %d = %v, reference %v", i+2, rows[i], ref[i])
		}
	}
	spec := map[string]string{"DM": "21.20", "DL": "54.00", "IND-MAT": "2.12", "IND-LAB": "27.00", "TOTAL": "104.32"}
	seen := 0
	for _, row := range rows {
		if row[0] == "FG-100" {
			if spec[row[1]] != row[2] {
				t.Errorf("workbook FG-100 %s = %s, spec %s", row[1], row[2], spec[row[1]])
			}
			seen++
		}
	}
	if seen != len(spec) {
		t.Errorf("workbook has %d FG-100 rows, want %d", seen, len(spec))
	}
	memo := map[string]refResult{}
	tolerance := big.NewRat(1, 1000)
	run := MassRoll(fx.Catalog, RollOptions{})
	for _, r := range run.Results {
		exact := refRoll(fx.Catalog, r.ItemID, memo).total()
		for c, v := range r.Combined() {
			e := exact[c]
			if e == nil {
				e = new(big.Rat)
			}
			if diff := new(big.Rat).Abs(rsub(rat(v), e)); diff.Cmp(tolerance) > 0 {
				t.Errorf("%s %s engine %s drifts %s from exact", r.ItemID, c, v, diff.FloatString(8))
			}
		}
	}
}

// --- variance-200 -----------------------------------------------------------

// refMinorUnits is the reference's own ISO 4217 table for the fixture currencies.
var refMinorUnits = map[string]int{"": 2, "USD": 2, "EUR": 2, "JPY": 0, "KWD": 3}

type refCell struct{ total, lot, qty, price, sub *big.Rat }

// refVariance decomposes an order per component in exact arithmetic (FR-39.8,
// with the service's LSV sign; see NOT-IMPLEMENTED "Lot size variance sign").
// The total is actual minus the standard cost of the reported-as-finished
// output; the four types are computed line by line.
func refVariance(o Order) map[string]*refCell {
	out := map[string]*refCell{}
	ratio := rdiv(rat(o.RAFQty), rat(o.PlannedQty))
	for _, ln := range o.Lines {
		c := out[ln.Component]
		if c == nil {
			c = &refCell{new(big.Rat), new(big.Rat), new(big.Rat), new(big.Rat), new(big.Rat)}
			out[ln.Component] = c
		}
		actual := rmul(rat(ln.ActualQty), rat(ln.ActualUnitCost))
		switch {
		case ln.Planned && ln.Fixed:
			absorbed := rmul(rat(ln.EstPerLot), ratio)
			standard := rmul(rat(ln.StdUnitCost), rat(o.RAFQty))
			c.lot = radd(c.lot, rsub(absorbed, standard))
			c.price = radd(c.price, rsub(actual, absorbed))
			c.total = radd(c.total, rsub(actual, standard))
		case ln.Planned:
			stdQty := rmul(rat(ln.PlannedQty), ratio)
			c.qty = radd(c.qty, rmul(rsub(rat(ln.ActualQty), stdQty), rat(ln.StdUnitCost)))
			c.price = radd(c.price, rmul(rat(ln.ActualQty), rsub(rat(ln.ActualUnitCost), rat(ln.StdUnitCost))))
			c.total = radd(c.total, rsub(actual, rmul(stdQty, rat(ln.StdUnitCost))))
		default:
			c.sub = radd(c.sub, actual)
			c.total = radd(c.total, actual)
		}
	}
	return out
}

type fixtureOrder struct {
	ID    string `json:"id"`
	Order Order  `json:"order"`
}

// genVarianceOrders builds 200 deterministic ended orders: AC-39.5 first, then
// short, exact and over-reported orders (including zero output) in USD, EUR,
// JPY and KWD with variable, per-lot and substituted lines.
func genVarianceOrders() []fixtureOrder {
	r := rand.New(rand.NewPCG(39, 200))
	out := []fixtureOrder{{ID: "PO-0001", Order: ac395Order()}}
	currencies := []string{"USD", "USD", "USD", "USD", "USD", "USD", "EUR", "JPY", "KWD", ""}
	comps := []string{CompDM, CompDL, CompIndMat, CompIndLab, "SUB"}
	for i := 2; i <= 200; i++ {
		cur := currencies[r.IntN(len(currencies))]
		planned := 1 + r.Int64N(1000)
		var raf int64
		switch r.IntN(10) {
		case 0:
			raf = 0
		case 1, 2:
			raf = planned + r.Int64N(planned/5+1)
		case 3, 4, 5:
			raf = planned
		default:
			raf = 1 + r.Int64N(planned)
		}
		o := Order{Currency: cur, PlannedQty: decimal.NewFromInt(planned), RAFQty: decimal.NewFromInt(raf)}
		for k := 0; k < 2+r.IntN(8); k++ {
			comp := comps[r.IntN(len(comps))]
			switch r.IntN(5) {
			case 0:
				o.Lines = append(o.Lines, OrderLine{Component: comp, Planned: true, Fixed: true,
					EstPerLot: fxAmount(r, 100, 500000, 2), StdUnitCost: fxAmount(r, 1, 50000, 4),
					ActualQty: decimal.NewFromInt(1 + r.Int64N(2)), ActualUnitCost: fxAmount(r, 100, 500000, 2)})
			case 1:
				o.Lines = append(o.Lines, OrderLine{Component: comp,
					ActualQty: fxAmount(r, 0, 50000, 3), ActualUnitCost: fxAmount(r, 1, 900000, 4)})
			default:
				std := fxAmount(r, 1, 900000, 4)
				ac := std
				if r.IntN(2) == 0 {
					ac = fxAmount(r, 1, 900000, 4)
				}
				o.Lines = append(o.Lines, OrderLine{Component: comp, Planned: true,
					PlannedQty: fxAmount(r, 1, 500000, 3), StdUnitCost: std,
					ActualQty: fxAmount(r, 0, 600000, 3), ActualUnitCost: ac})
			}
		}
		out = append(out, fixtureOrder{ID: fmt.Sprintf("PO-%04d", i), Order: o})
	}
	return out
}

func varianceExpected(orders []fixtureOrder) [][]string {
	rows := [][]string{{"order", "currency", "component", "total", "lot_size", "quantity", "price", "substitution"}}
	for _, fo := range orders {
		scale, ok := refMinorUnits[fo.Order.Currency]
		if !ok {
			panic("no minor unit for " + fo.Order.Currency)
		}
		cells := refVariance(fo.Order)
		comps := make([]string, 0, len(cells))
		for c := range cells {
			comps = append(comps, c)
		}
		sort.Strings(comps)
		for _, c := range comps {
			v := cells[c]
			rows = append(rows, []string{fo.ID, fo.Order.Currency, c,
				roundHalfAway(v.total, scale), roundHalfAway(v.lot, scale), roundHalfAway(v.qty, scale),
				roundHalfAway(v.price, scale), roundHalfAway(v.sub, scale)})
		}
	}
	return rows
}

// TestVarianceFixtures_TwoHundredEndedOrdersDecomposeToTotal is AC3 / AC-39.6:
// for each of the 200 ended fixture orders, per component and per order,
// LSV + QV + PV + SV equals the total variance; the total, LSV, PV and SV
// equal the reference to the minor unit; QV differs from the reference QV only
// by the rounding residual (at most two minor units, BR-39.9).
func TestVarianceFixtures_TwoHundredEndedOrdersDecomposeToTotal(t *testing.T) {
	path := filepath.Join(varianceDir, "orders.json")
	expPath := filepath.Join(varianceDir, "expected.csv")
	if updating() {
		orders := genVarianceOrders()
		writeJSON(t, path, orders)
		writeCSV(t, expPath, varianceExpected(orders))
		t.Fatalf("rewrote %s; rerun without %s", varianceDir, updateEnv)
	}
	var orders []fixtureOrder
	readJSON(t, path, &orders)
	if len(orders) != 200 {
		t.Fatalf("fixture has %d orders, want 200", len(orders))
	}
	rows := readCSV(t, expPath)
	if ref := varianceExpected(orders)[1:]; len(ref) != len(rows) {
		t.Fatalf("expected.csv has %d rows, reference %d", len(rows), len(ref))
	} else {
		for i := range ref {
			if strings.Join(ref[i], ",") != strings.Join(rows[i], ",") {
				t.Fatalf("expected.csv row %d = %v, reference %v", i+2, rows[i], ref[i])
			}
		}
	}
	want := map[string]map[string][]decimal.Decimal{}
	for _, row := range rows {
		if want[row[0]] == nil {
			want[row[0]] = map[string][]decimal.Decimal{}
		}
		vals := make([]decimal.Decimal, 5)
		for i := range vals {
			vals[i] = decimal.RequireFromString(row[3+i])
		}
		want[row[0]][row[2]] = vals
	}
	// AC-39.5 anchors the reference to the spec: DM QV 40.00 + SV 7.50 = 47.50,
	// DL (setup) LSV 0.00 + PV 4.00 = 4.00.
	if dm := want["PO-0001"][CompDM]; !dm[0].Equal(d("47.50")) || !dm[2].Equal(d("40.00")) || !dm[4].Equal(d("7.50")) {
		t.Errorf("reference PO-0001 DM = %v, spec total 47.50 QV 40.00 SV 7.50", dm)
	}
	if dl := want["PO-0001"][CompDL]; !dl[0].Equal(d("4.00")) || !dl[1].IsZero() || !dl[3].Equal(d("4.00")) {
		t.Errorf("reference PO-0001 DL = %v, spec total 4.00 LSV 0.00 PV 4.00", dl)
	}
	shapes := map[string]int{}
	for _, fo := range orders {
		o := fo.Order
		switch o.RAFQty.Cmp(o.PlannedQty) {
		case -1:
			shapes["short"]++
		case 0:
			shapes["exact"]++
		default:
			shapes["over"]++
		}
		got, err := Decompose(o)
		if err != nil {
			t.Fatalf("%s: %v", fo.ID, err)
		}
		if !got.Total.Equal(got.Types.Sum()) {
			t.Errorf("%s: total %s != LSV+QV+PV+SV %s", fo.ID, got.Total, got.Types.Sum())
		}
		exp := want[fo.ID]
		if len(got.ByComponent) != len(exp) {
			t.Fatalf("%s: %d components, reference %d", fo.ID, len(got.ByComponent), len(exp))
		}
		bound := decimal.New(2, -int32(refMinorUnits[o.Currency]))
		orderTotal := decimal.Zero
		for _, cv := range got.ByComponent {
			e, ok := exp[cv.Component]
			if !ok {
				t.Fatalf("%s: component %s not in the reference", fo.ID, cv.Component)
			}
			if !cv.Total.Equal(cv.Types.Sum()) {
				t.Errorf("%s %s: total %s != types %s", fo.ID, cv.Component, cv.Total, cv.Types.Sum())
			}
			if !cv.Total.Equal(e[0]) || !cv.Types.LotSize.Equal(e[1]) || !cv.Types.Price.Equal(e[3]) || !cv.Types.Substitution.Equal(e[4]) {
				t.Errorf("%s %s: total/LSV/PV/SV = %s/%s/%s/%s, reference %s/%s/%s/%s", fo.ID, cv.Component,
					cv.Total, cv.Types.LotSize, cv.Types.Price, cv.Types.Substitution, e[0], e[1], e[3], e[4])
			}
			if wantQV := e[0].Sub(e[1]).Sub(e[3]).Sub(e[4]); !cv.Types.Quantity.Equal(wantQV) {
				t.Errorf("%s %s: QV %s, reference total minus LSV, PV, SV = %s", fo.ID, cv.Component, cv.Types.Quantity, wantQV)
			}
			if cv.Types.Quantity.Sub(e[2]).Abs().GreaterThan(bound) {
				t.Errorf("%s %s: QV %s is more than the rounding bound from the reference %s", fo.ID, cv.Component, cv.Types.Quantity, e[2])
			}
			orderTotal = orderTotal.Add(e[0])
		}
		if !got.Total.Equal(orderTotal) {
			t.Errorf("%s: order total %s, reference %s", fo.ID, got.Total, orderTotal)
		}
	}
	if shapes["short"] == 0 || shapes["exact"] == 0 || shapes["over"] == 0 {
		t.Errorf("fixture lacks an order shape: %v", shapes)
	}
}
