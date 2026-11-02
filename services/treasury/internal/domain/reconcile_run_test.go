package domain

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

type reconLine struct {
	line  RecLine
	want  []string
	group string
}

func storedRules() []Rule {
	return []Rule{
		{Name: "reference", Priority: 2, Weights: ReferenceWeights, Tolerance: tol, Strict: true},
		{Name: "balanced", Priority: 1, Weights: DefaultWeights, Tolerance: tol},
	}
}

// recon1000 is the PRD fixture of AC-36.2: 1,000 statement lines, 60% exact,
// 25% reference-only (garbled counterparty, value date one day late), 10% N:1
// batch receipts and 5% bank fees that match their fee accrual.
func recon1000() ([]Item, []reconLine) {
	var items []Item
	var lines []reconLine
	no := 0
	add := func(it Item, line Facts, group string, want ...string) {
		no++
		items = append(items, it)
		lines = append(lines, reconLine{RecLine{No: no, Facts: line}, want, group})
	}
	garble := func(f Facts, n int) Facts {
		day, _ := Day(f.ValueDate)
		f.ValueDate = day.AddDate(0, 0, 1).Format("2006-01-02")
		f.Counterparty = fmt.Sprintf("ZQX%d unknown payer", n)
		return f
	}
	for i := 0; i < 850; i++ {
		f := Facts{
			Amount: decimal.New(int64(10000+(i%53)*100), -2), ValueDate: fmt.Sprintf("2026-10-%02d", 1+i%28),
			Reference: fmt.Sprintf("E2E%05d", i), Counterparty: fmt.Sprintf("Vendor %03d Holdings", i),
		}
		id := fmt.Sprintf("tx-%d", i)
		if i < 600 {
			add(Item{ID: id, Facts: f}, f, "exact", id)
		} else {
			add(Item{ID: id, Facts: f}, garble(f, i), "reference-only", id)
		}
	}
	for b := 0; b < 100; b++ {
		batch := fmt.Sprintf("RMT%06d", b)
		sum := decimal.Zero
		var want []string
		for k := 0; k < 2+b%3; k++ {
			id := fmt.Sprintf("inv-%d-%d", b, k)
			f := Facts{
				Amount: decimal.New(int64(5000+b*37+k*211), -2), ValueDate: fmt.Sprintf("2026-10-%02d", 10+b%10),
				Reference: fmt.Sprintf("INV%03d%02d", b, k), Counterparty: "Remitter " + batch,
			}
			sum = sum.Add(f.Amount)
			want = append(want, id)
			items = append(items, Item{ID: id, Batch: batch, Facts: f})
		}
		no++
		lines = append(lines, reconLine{RecLine{No: no, Facts: Facts{
			Amount: sum, ValueDate: fmt.Sprintf("2026-10-%02d", 11+b%10), Reference: batch, Counterparty: "ZQX bulk payer",
		}}, want, "batch"})
	}
	for i := 0; i < 50; i++ {
		f := Facts{
			Amount: decimal.RequireFromString("-12.50"), ValueDate: fmt.Sprintf("2026-10-%02d", 5+i%20),
			Reference: fmt.Sprintf("FEE-%04d", i), Counterparty: "Example Bank AG",
		}
		id := fmt.Sprintf("fee-%d", i)
		add(Item{ID: id, Facts: f}, f, "fee", id)
	}
	return items, lines
}

func TestReconcileStatement_Recon1000AutoMatchesAtLeast95PercentWithoutFalseMatches(t *testing.T) {
	items, lines := recon1000()
	if len(lines) != 1000 {
		t.Fatalf("fixture has %d lines", len(lines))
	}
	plain := make([]RecLine, len(lines))
	want := map[int]string{}
	group := map[int]string{}
	for i, l := range lines {
		plain[i] = l.line
		want[l.line.No] = strings.Join(l.want, ",")
		group[l.line.No] = l.group
	}
	out := ReconcileStatement(plain, items, storedRules(), DefaultThresholds)
	correct := map[string]int{}
	total := 0
	for _, m := range out.Matches {
		if len(m.LineNos) != 1 {
			t.Fatalf("auto-match with %d lines", len(m.LineNos))
		}
		got := strings.Join(m.ItemIDs, ",")
		if got != want[m.LineNos[0]] {
			t.Errorf("line %d matched to %s; want %s", m.LineNos[0], got, want[m.LineNos[0]])
			continue
		}
		correct[group[m.LineNos[0]]]++
		total++
	}
	t.Logf("auto-matched correctly %d of %d lines (%.1f%%); by group %v", total, len(lines), 100*float64(total)/float64(len(lines)), correct)
	if t.Failed() {
		t.FailNow()
	}
	if total < 950 {
		t.Fatalf("auto-matched correctly %d lines; AC-36.2 needs at least 950", total)
	}
	for g, n := range map[string]int{"exact": 600, "reference-only": 250, "batch": 100, "fee": 50} {
		if correct[g] != n {
			t.Errorf("group %s: %d auto-matched correctly; want %d", g, correct[g], n)
		}
	}
}

func TestReconcileStatement_NeverAutoMatchesContestedOrUnbalancedClaims(t *testing.T) {
	item := Item{ID: "tx-1", Facts: facts("120.00", "2026-10-22", "E2E00042", "Vendor 042 Holdings")}
	batchItem := func(id, amount string) Item {
		return Item{ID: id, Batch: "RMT000001", Facts: facts(amount, "2026-10-20", "INV"+id, "Remitter")}
	}
	tests := []struct {
		name  string
		lines []RecLine
		items []Item
		want  int
	}{
		{"two identical lines claim one item", []RecLine{{1, item.Facts}, {2, item.Facts}}, []Item{item}, 0},
		{"one line, one item", []RecLine{{1, item.Facts}}, []Item{item}, 1},
		{"batch sum short by a cent", []RecLine{{1, facts("299.99", "2026-10-21", "RMT000001", "x")}}, []Item{batchItem("a", "100.00"), batchItem("b", "200.00")}, 0},
		{"batch sum exact", []RecLine{{1, facts("300.00", "2026-10-21", "RMT000001", "x")}}, []Item{batchItem("a", "100.00"), batchItem("b", "200.00")}, 1},
		{"two lines for one batch", []RecLine{{1, facts("300.00", "2026-10-21", "RMT000001", "x")}, {2, facts("300.00", "2026-10-21", "RMT000001", "y")}}, []Item{batchItem("a", "100.00"), batchItem("b", "200.00")}, 0},
		{"batch item outside the window", []RecLine{{1, facts("300.00", "2026-11-30", "RMT000001", "x")}}, []Item{batchItem("a", "100.00"), batchItem("b", "200.00")}, 0},
		{"single item batch is not a batch", []RecLine{{1, facts("100.00", "2026-10-21", "RMT000001", "x")}}, []Item{batchItem("a", "100.00")}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := ReconcileStatement(tc.lines, tc.items, storedRules(), DefaultThresholds)
			if len(out.Matches) != tc.want {
				t.Fatalf("matches = %+v; want %d", out.Matches, tc.want)
			}
		})
	}
}

func TestReconcileStatement_ReportsSuggestionsAndRule(t *testing.T) {
	item := Item{ID: "tx-1", Facts: facts("75.00", "2026-10-22", "1001", "Vendor A")}
	line := RecLine{1, facts("75.00", "2026-10-23", "1001", "x")}
	out := ReconcileStatement([]RecLine{line}, []Item{item}, storedRules(), DefaultThresholds)
	if len(out.Matches) != 0 || len(out.Suggestions) != 1 || out.Suggestions[0].ItemID != "tx-1" {
		t.Fatalf("outcome = %+v; want one suggestion for tx-1", out)
	}
	exact := RecLine{1, item.Facts}
	out = ReconcileStatement([]RecLine{exact}, []Item{item}, storedRules(), DefaultThresholds)
	if len(out.Matches) != 1 || out.Matches[0].Rule != "balanced" || !out.Matches[0].Difference.IsZero() {
		t.Fatalf("outcome = %+v; want a balanced match with no difference", out)
	}
}

func TestReconcileStatement_OrdersRulesByPriority(t *testing.T) {
	lo := Rule{Name: "z-first", Priority: 1, Weights: DefaultWeights, Tolerance: tol}
	hi := Rule{Name: "a-second", Priority: 2, Weights: DefaultWeights, Tolerance: tol}
	item := Item{ID: "tx-1", Facts: facts("10.00", "2026-10-22", "E2E00042", "Vendor")}
	out := ReconcileStatement([]RecLine{{1, item.Facts}}, []Item{item}, []Rule{hi, lo}, DefaultThresholds)
	if len(out.Matches) != 1 || out.Matches[0].Rule != "z-first" {
		t.Fatalf("matches = %+v; want the priority 1 rule to decide", out.Matches)
	}
}

func TestRule_Validate(t *testing.T) {
	ok := Rule{Name: "r", Priority: 1, Weights: DefaultWeights, Tolerance: tol}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*Rule){
		"weights not 100":       func(r *Rule) { r.Weights.Date = 21 },
		"amount weight too low": func(r *Rule) { r.Weights = Weights{Amount: 10, Date: 40, Reference: 25, Counterparty: 25} },
		"negative weight":       func(r *Rule) { r.Weights = Weights{Amount: 60, Date: -5, Reference: 25, Counterparty: 20} },
		"negative tolerance":    func(r *Rule) { r.Tolerance.Amount = decimal.NewFromInt(-1) },
		"window too wide":       func(r *Rule) { r.Tolerance.DateWindowDays = 31 },
		"priority zero":         func(r *Rule) { r.Priority = 0 },
		"blank name":            func(r *Rule) { r.Name = "  " },
	}
	names := make([]string, 0, len(mutations))
	for n := range mutations {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		r := ok
		mutations[n](&r)
		if r.Validate() == nil {
			t.Errorf("%s must be rejected", n)
		}
	}
}
