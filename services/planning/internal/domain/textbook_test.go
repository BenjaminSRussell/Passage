package domain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"testing"

	"github.com/shopspring/decimal"
)

// The mrp-textbook fixtures (issue #22, AC1): APICS CPIM-style bucketed MRP
// records. The expected planned orders in expected.json come from
// testdata/mrp-textbook/reference_mrp.py, an independent bucketed MRP that also
// checks itself against the hand-derived plans in scenarios.json; nothing here
// derives an expected value from the engine.

type textbookItem struct {
	ID         string             `json:"id"`
	LT         int                `json:"lt"`
	SLT        int                `json:"slt"`
	OnHand     int64              `json:"onHand"`
	SS         int64              `json:"ss"`
	Lot        string             `json:"lot"`
	FOQ        int64              `json:"foq"`
	POQ        int                `json:"poq"`
	Min        int64              `json:"min"`
	Mult       int64              `json:"mult"`
	Max        int64              `json:"max"`
	ReorderMin int64              `json:"reorderMin"`
	ReorderMax int64              `json:"reorderMax"`
	Gross      map[string]int64   `json:"gross"`
	SR         map[string]int64   `json:"sr"`
	BOM        []textbookBOMEntry `json:"bom"`
}

type textbookBOMEntry struct {
	Item string `json:"item"`
	Per  int64  `json:"per"`
}

type textbookScenario struct {
	ID       string         `json:"id"`
	Category string         `json:"category"`
	Title    string         `json:"title"`
	Items    []textbookItem `json:"items"`
	PRDNote  string         `json:"prdNote"`
}

type textbookOrder struct {
	Item          string `json:"item"`
	Qty           string `json:"qty"`
	ReleasePeriod int    `json:"releasePeriod"`
	ReceiptPeriod int    `json:"receiptPeriod"`
	// FuturesPeriod and DelayPeriods are set for an order expected later than
	// its receipt (a past-due release, or a late component pegged to it).
	FuturesPeriod *int `json:"futuresPeriod"`
	DelayPeriods  int  `json:"delayPeriods"`
}

// textbookToday is period 1; period p is day 7p, so a lead time of n periods is
// 7n days and a release in period 0 or earlier is past due.
const textbookToday = 7

func textbookDay(period int) int { return 7 * period }

func loadTextbook(t *testing.T) ([]textbookScenario, map[string]map[string][]textbookOrder) {
	t.Helper()
	dir := filepath.Join("..", "..", "testdata", "mrp-textbook")
	var scenarios struct {
		Scenarios []textbookScenario `json:"scenarios"`
	}
	var expected struct {
		Scenarios map[string]map[string][]textbookOrder `json:"scenarios"`
	}
	for name, v := range map[string]any{"scenarios.json": &scenarios, "expected.json": &expected} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	return scenarios.Scenarios, expected.Scenarios
}

func textbookItems(t *testing.T, s textbookScenario) []Item {
	t.Helper()
	items := make([]Item, 0, len(s.Items))
	for _, ti := range s.Items {
		it := Item{ID: ti.ID, OrderType: OrderPurchase, LeadTimeDays: 7 * ti.LT, MarginDays: 7 * ti.SLT,
			OnHand: decimal.NewFromInt(ti.OnHand), SafetyStock: decimal.NewFromInt(ti.SS),
			MinOrderQty: decimal.NewFromInt(ti.Min), MultipleQty: decimal.NewFromInt(ti.Mult), MaxOrderQty: decimal.NewFromInt(ti.Max)}
		switch ti.Lot {
		case "L4L":
			it.Coverage = CoverageRequirement
		case "FOQ":
			it.Coverage = CoverageRequirement
			it.StandardQty = decimal.NewFromInt(ti.FOQ)
		case "POQ":
			it.Coverage, it.PeriodDays = CoveragePeriod, 7*ti.POQ
		case "MINMAX":
			it.Coverage = CoverageMinMax
			it.ReorderMin, it.ReorderMax = decimal.NewFromInt(ti.ReorderMin), decimal.NewFromInt(ti.ReorderMax)
		default:
			t.Fatalf("%s: lot rule %q", s.ID, ti.Lot)
		}
		for p, q := range ti.Gross {
			n, err := strconv.Atoi(p)
			if err != nil {
				t.Fatal(err)
			}
			it.Demand = append(it.Demand, Requirement{Ref: fmt.Sprintf("GR-%s-%02d", ti.ID, n), RefType: RefSales, Date: textbookDay(n), Qty: decimal.NewFromInt(q), Priority: PrioritySales})
		}
		for p, q := range ti.SR {
			n, err := strconv.Atoi(p)
			if err != nil {
				t.Fatal(err)
			}
			it.Receipts = append(it.Receipts, Requirement{Ref: fmt.Sprintf("SR-%s-%02d", ti.ID, n), RefType: "purchase-line", Date: textbookDay(n), Qty: decimal.NewFromInt(q)})
		}
		// Map iteration order is random; the engine must not care, but keep the
		// input stable so a failure reproduces.
		sort.Slice(it.Demand, func(a, b int) bool { return it.Demand[a].Ref < it.Demand[b].Ref })
		sort.Slice(it.Receipts, func(a, b int) bool { return it.Receipts[a].Ref < it.Receipts[b].Ref })
		for _, c := range ti.BOM {
			it.OrderType = OrderProduction
			it.Components = append(it.Components, BOMLine{ItemID: c.Item, Qty: decimal.NewFromInt(c.Per)})
		}
		items = append(items, it)
	}
	return items
}

// plannedRows renders orders as comparable rows "item qty order-day
// delivery-day futures-day delay-days", with "-" for no futures date.
func plannedRows(orders []PlannedOrder) []string {
	rows := make([]string, 0, len(orders))
	for _, o := range orders {
		futures := "-"
		if o.FuturesDate != nil {
			futures = strconv.Itoa(*o.FuturesDate)
		}
		rows = append(rows, fmt.Sprintf("%s %s %d %d %s %d", o.ItemID, o.Qty, o.OrderDate, o.DeliveryDate, futures, o.DelayDays))
	}
	slices.Sort(rows)
	return rows
}

func expectedRows(want []textbookOrder) []string {
	rows := make([]string, 0, len(want))
	for _, o := range want {
		futures := "-"
		if o.FuturesPeriod != nil {
			futures = strconv.Itoa(textbookDay(*o.FuturesPeriod))
		}
		rows = append(rows, fmt.Sprintf("%s %s %d %d %s %d", o.Item, o.Qty, textbookDay(o.ReleasePeriod), textbookDay(o.ReceiptPeriod), futures, 7*o.DelayPeriods))
	}
	slices.Sort(rows)
	return rows
}

// assertPeggingExplainsOrders checks AC3 on a result: the explain tree of every
// planned order has children whose quantities sum to the order quantity, and
// every bom-line child leads to a planned order of the run.
func assertPeggingExplainsOrders(t *testing.T, label string, r Result) {
	t.Helper()
	byRef := map[string][]PegLine{}
	for _, p := range r.Pegging {
		byRef[p.SupplyRef] = append(byRef[p.SupplyRef], PegLine{DemandRef: p.DemandRef, DemandRefType: p.DemandRefType, Qty: p.Qty})
	}
	numbers := make(map[string]bool, len(r.Orders))
	for _, o := range r.Orders {
		numbers[o.Number] = true
	}
	pegsOf := func(ref string) []PegLine { return byRef[ref] }
	for _, o := range r.Orders {
		tree := BuildTree(o.Number, 2, pegsOf)
		explained := decimal.Zero
		for _, c := range tree.Children {
			explained = explained.Add(c.Qty)
			if c.RefType == RefPlanned && !numbers[c.Ref] {
				t.Fatalf("%s: order %s pegs to a bom-line of unknown order %s", label, o.Number, c.Ref)
			}
		}
		if !explained.Equal(o.Qty) {
			t.Fatalf("%s: explain tree of %s (%s qty %s) covers %s", label, o.Number, o.ItemID, o.Qty, explained)
		}
	}
}

func TestRun_TextbookFixtures(t *testing.T) {
	scenarios, expected := loadTextbook(t)
	if len(scenarios) < 40 || len(expected) != len(scenarios) {
		t.Fatalf("fixture set has %d scenarios and %d expected plans; want at least 40 of each", len(scenarios), len(expected))
	}
	textbookExact := 0
	for _, s := range scenarios {
		t.Run(s.ID, func(t *testing.T) {
			items := textbookItems(t, s)
			r, err := Run(textbookToday, items)
			if err != nil {
				t.Fatal(err)
			}
			got := plannedRows(r.Orders)
			want := expectedRows(expected[s.ID]["prd"])
			if !slices.Equal(got, want) {
				t.Fatalf("%s %s (%s):\n got  %q\n want %q", s.ID, s.Title, s.Category, got, want)
			}
			if slices.Equal(got, expectedRows(expected[s.ID]["textbook"])) {
				textbookExact++
			} else if s.PRDNote == "" {
				t.Fatalf("%s differs from the textbook plan without a PRD rule", s.ID)
			}
			assertPeggingExplainsOrders(t, s.ID, r)
			slices.Reverse(items)
			again, err := Run(textbookToday, items)
			if err != nil || again.Hash != r.Hash {
				t.Fatalf("%s: plan hash changes with input order: %v", s.ID, err)
			}
		})
	}
	if !t.Failed() {
		t.Logf("%d of %d scenarios match the PRD plan; %d of them also match the plain textbook plan", len(scenarios), len(scenarios), textbookExact)
	}
}
