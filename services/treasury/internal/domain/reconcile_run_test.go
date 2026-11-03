package domain

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

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

func reconcile(t *testing.T, lines []RecLine, items []Item, rules []Rule, th Thresholds) Outcome {
	t.Helper()
	out, err := ReconcileStatement(context.Background(), lines, items, rules, th)
	if err != nil {
		t.Fatal(err)
	}
	return out
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
	out := reconcile(t, plain, items, storedRules(), DefaultThresholds)
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
			out := reconcile(t, tc.lines, tc.items, storedRules(), DefaultThresholds)
			if len(out.Matches) != tc.want {
				t.Fatalf("matches = %+v; want %d", out.Matches, tc.want)
			}
		})
	}
}

func TestReconcileStatement_ReportsSuggestionsAndRule(t *testing.T) {
	item := Item{ID: "tx-1", Facts: facts("75.00", "2026-10-22", "1001", "Vendor A")}
	line := RecLine{1, facts("75.00", "2026-10-23", "1001", "x")}
	out := reconcile(t, []RecLine{line}, []Item{item}, storedRules(), DefaultThresholds)
	if len(out.Matches) != 0 || len(out.Suggestions) != 1 || out.Suggestions[0].ItemID != "tx-1" {
		t.Fatalf("outcome = %+v; want one suggestion for tx-1", out)
	}
	exact := RecLine{1, item.Facts}
	out = reconcile(t, []RecLine{exact}, []Item{item}, storedRules(), DefaultThresholds)
	if len(out.Matches) != 1 || out.Matches[0].Rule != "balanced" || !out.Matches[0].Difference.IsZero() {
		t.Fatalf("outcome = %+v; want a balanced match with no difference", out)
	}
}

func TestReconcileStatement_OrdersRulesByPriority(t *testing.T) {
	lo := Rule{Name: "z-first", Priority: 1, Weights: DefaultWeights, Tolerance: tol}
	hi := Rule{Name: "a-second", Priority: 2, Weights: DefaultWeights, Tolerance: tol}
	item := Item{ID: "tx-1", Facts: facts("10.00", "2026-10-22", "E2E00042", "Vendor")}
	out := reconcile(t, []RecLine{{1, item.Facts}}, []Item{item}, []Rule{hi, lo}, DefaultThresholds)
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

func largeRun(n, distinctAmounts int) ([]RecLine, []Item) {
	lines := make([]RecLine, n)
	items := make([]Item, n)
	for i := 0; i < n; i++ {
		f := Facts{
			Amount: decimal.New(int64(10000+(i%distinctAmounts)*100), -2), ValueDate: fmt.Sprintf("2026-10-%02d", 1+i%28),
			Reference: fmt.Sprintf("E2E%05d", i), Counterparty: fmt.Sprintf("Vendor %03d Holdings", i),
		}
		items[i] = Item{ID: fmt.Sprintf("tx-%d", i), Facts: f}
		lines[i] = RecLine{No: i + 1, Facts: f}
	}
	return lines, items
}

func unmatchableRun(n int) ([]RecLine, []Item) {
	lines, items := largeRun(n, 1)
	for i := range lines {
		lines[i].Reference = fmt.Sprintf("LINE%05d", i)
		lines[i].Counterparty = fmt.Sprintf("Payer %03d Ltd", i)
		lines[i].ValueDate = fmt.Sprintf("2026-10-%02d", 10+i%3)
		items[i].ValueDate = fmt.Sprintf("2026-10-%02d", 10+i%3)
	}
	return lines, items
}

func twentyRules() []Rule {
	rules := make([]Rule, 20)
	for i := range rules {
		rules[i] = Rule{Name: fmt.Sprintf("rule-%02d", i), Priority: i + 1, Weights: DefaultWeights, Tolerance: tol}
	}
	return rules
}

func TestReconcileStatement_TwoThousandByTwoThousandWithTwentyRulesFinishesQuickly(t *testing.T) {
	spread, spreadItems := largeRun(2000, 500)
	few, fewItems := largeRun(2000, 5)
	worst, worstItems := unmatchableRun(2000)
	for name, run := range map[string]struct {
		lines []RecLine
		items []Item
	}{"spread amounts": {spread, spreadItems}, "few distinct amounts": {few, fewItems}, "same amount, nothing matches": {worst, worstItems}} {
		lines, items := run.lines, run.items
		start := time.Now()
		out := reconcile(t, lines, items, twentyRules(), DefaultThresholds)
		elapsed := time.Since(start)
		t.Logf("%s: 2000 lines x 2000 items x 20 rules in %s, %d matches", name, elapsed, len(out.Matches))
		if elapsed > 5*time.Second {
			t.Errorf("%s: run took %s; want under 5s", name, elapsed)
		}
	}
}

func TestReconcileStatement_StopsWhenTheContextIsCancelled(t *testing.T) {
	lines, items := largeRun(200, 50)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReconcileStatement(ctx, lines, items, storedRules(), DefaultThresholds); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want context.Canceled", err)
	}
}

func TestMatchLine_PayerRuleDoesNotAutoMatchAContradictingReference(t *testing.T) {
	payer := []Rule{{Name: "payer", Priority: 1, Weights: Weights{Amount: 40, Date: 20, Counterparty: 40}, Tolerance: tol}}
	candidate := func(reference string) map[string]Facts {
		return map[string]Facts{"tx-1": facts("120.00", "2026-10-22", reference, "Acme Trading Ltd")}
	}
	tests := []struct {
		name          string
		lineRef       string
		altRef        string
		candidateRef  string
		wantAutoMatch bool
	}{
		{"line reference differs from the candidate", "INV-2026-0001", "", "INV-2026-0099", false},
		{"remittance text differs from the candidate", "NOTPROVIDED", "INV-2026-0001", "INV-2026-0099", false},
		{"line reference equals the candidate", "INV-2026-0001", "", "INV-2026-0001", true},
		{"remittance text contains the candidate reference", "NOTPROVIDED", "PAYMENT INV-2026-0001 THANKS", "INV-2026-0001", true},
		{"line reference is a placeholder", "NOTPROVIDED", "", "INV-2026-0099", true},
		{"line reference is too short to trust", "1234567", "", "INV-2026-0099", true},
		{"candidate has no reference", "INV-2026-0001", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			line := facts("120.00", "2026-10-22", tc.lineRef, "Acme Trading Ltd")
			line.AltReference = tc.altRef
			d := MatchLine(line, candidate(tc.candidateRef), payer, DefaultThresholds)
			wantStatus := StatusSuggested
			if tc.wantAutoMatch {
				wantStatus = StatusMatched
			}
			if d.Status != wantStatus {
				t.Fatalf("status = %s (best %+v); want %s", d.Status, d.Best, wantStatus)
			}
		})
	}
}

type ruleDecision struct {
	status Status
	rule   string
	item   string
}

func fullDecision(line Facts, items []Item, rules []Rule) ruleDecision {
	open := map[string]Facts{}
	for _, it := range items {
		open[it.ID] = it.Facts
	}
	ordered := append([]Rule(nil), rules...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority != ordered[j].Priority {
			return ordered[i].Priority < ordered[j].Priority
		}
		return ordered[i].Name < ordered[j].Name
	})
	d := MatchLine(line, open, ordered, DefaultThresholds)
	if d.Best == nil {
		return ruleDecision{status: d.Status}
	}
	got := ruleDecision{status: d.Status, item: d.Best.ID}
	if d.Status == StatusMatched {
		got.rule = d.Rule
	}
	return got
}

func reconciledDecision(t *testing.T, line Facts, items []Item, rules []Rule) ruleDecision {
	t.Helper()
	out := reconcile(t, []RecLine{{No: 1, Facts: line}}, items, rules, DefaultThresholds)
	switch {
	case len(out.Matches) == 1:
		return ruleDecision{status: StatusMatched, rule: out.Matches[0].Rule, item: out.Matches[0].ItemIDs[0]}
	case len(out.Suggestions) == 1:
		return ruleDecision{status: StatusSuggested, item: out.Suggestions[0].ItemID}
	}
	return ruleDecision{status: StatusUnmatched}
}

func TestReconcileStatement_ExcludedCandidateDoesNotDemoteALeaderOfAnEarlierRule(t *testing.T) {
	noDate := []Rule{{Name: "no-date", Priority: 1, Weights: Weights{Amount: 50, Reference: 25, Counterparty: 25}, Tolerance: tol}}
	leader := Item{ID: "tx-1", Facts: facts("120.00", "2026-10-22", "INV-2026-0001", "Acme Trading Ltd")}
	outside := Item{ID: "tx-2", Facts: facts("120.00", "2026-09-01", "OTHER-REF-77", "Acme Trading Ltd")}
	got := reconciledDecision(t, leader.Facts, []Item{leader, outside}, noDate)
	if want := fullDecision(leader.Facts, []Item{leader, outside}, noDate); got != want || got.status != StatusMatched || got.item != "tx-1" {
		t.Fatalf("decision = %+v; want the full-scan decision %+v (tx-1 matched)", got, want)
	}
}

func TestReconcileStatement_ReviewerProbeEqualsFullScan(t *testing.T) {
	rules := []Rule{
		{Name: "refcp", Priority: 1, Weights: Weights{Amount: 40, Date: 0, Reference: 30, Counterparty: 30}, Tolerance: Tolerance{Amount: decimal.RequireFromString("1.00"), DateWindowDays: 3}},
		{Name: "datecp", Priority: 2, Weights: Weights{Amount: 40, Date: 30, Reference: 0, Counterparty: 30}, Tolerance: Tolerance{Amount: decimal.RequireFromString("1.00"), DateWindowDays: 3}},
	}
	line := facts("100.00", "2026-10-10", "INV-AAAA1111", "Acme Corp")
	items := []Item{
		{ID: "X", Facts: facts("100.00", "2026-10-15", "INV-AAAA1111", "Acme Corp")},
		{ID: "Y", Facts: facts("100.00", "2026-10-10", "", "Acme Corp")},
		{ID: "Z", Facts: facts("5000.00", "2026-10-10", "", "Other")},
	}
	got, want := reconciledDecision(t, line, items, rules), fullDecision(line, items, rules)
	if got != want {
		t.Fatalf("decision = %+v; full scan gives %+v", got, want)
	}
}

func TestReconcileStatement_RandomStatementsEqualFullScanDecisions(t *testing.T) {
	rng := rand.New(rand.NewSource(36))
	w := func(a, d, r, c int) Weights { return Weights{Amount: a, Date: d, Reference: r, Counterparty: c} }
	wide := Tolerance{Amount: decimal.RequireFromString("1.00"), DateWindowDays: 3}
	chains := [][]Rule{
		{{Name: "refcp", Priority: 1, Weights: w(40, 0, 30, 30), Tolerance: wide}, {Name: "datecp", Priority: 2, Weights: w(40, 30, 0, 30), Tolerance: wide}},
		{{Name: "balanced", Priority: 1, Weights: DefaultWeights, Tolerance: tol}, {Name: "reference", Priority: 2, Weights: ReferenceWeights, Tolerance: tol, Strict: true}},
		{{Name: "nodate", Priority: 1, Weights: w(50, 0, 25, 25), Tolerance: tol}, {Name: "balanced", Priority: 2, Weights: DefaultWeights, Tolerance: wide}, {Name: "payer", Priority: 3, Weights: w(40, 20, 0, 40), Tolerance: Tolerance{DateWindowDays: 5}}},
		{{Name: "strict", Priority: 1, Weights: ReferenceWeights, Tolerance: wide, Strict: true}, {Name: "refcp", Priority: 2, Weights: w(30, 0, 40, 30), Tolerance: tol}},
	}
	amounts := []string{"100.00", "100.40", "99.60", "100.80", "250.00", "5000.00"}
	refs := []string{"", "NOTPROVIDED", "INV-AAAA1111", "INV-AAAA1112", "INV-BBBB2222", "PAY INV-AAAA1111 THX"}
	names := []string{"Acme Corp", "Acme Corporation", "Beta Ltd", "Gamma Holdings", ""}
	randomFacts := func() Facts {
		return facts(amounts[rng.Intn(len(amounts))], fmt.Sprintf("2026-10-%02d", 4+rng.Intn(14)), refs[rng.Intn(len(refs))], names[rng.Intn(len(names))])
	}
	for n := 0; n < 600; n++ {
		items := make([]Item, 1+rng.Intn(25))
		for i := range items {
			items[i] = Item{ID: fmt.Sprintf("it-%02d", i), Facts: randomFacts()}
		}
		line := randomFacts()
		if rng.Intn(3) == 0 {
			line.AltReference = refs[rng.Intn(len(refs))]
		}
		rules := chains[n%len(chains)]
		got, want := reconciledDecision(t, line, items, rules), fullDecision(line, items, rules)
		if got != want {
			t.Fatalf("statement %d chain %d: decision = %+v; full scan gives %+v\nline %+v\nitems %+v", n, n%len(chains), got, want, line, items)
		}
	}
}
