package domain

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func n64(n int64) decimal.Decimal { return decimal.NewFromInt(n) }

// fiveLines is the AC-27.12 shape: five open lines for FG-100 with mixed priority.
func fiveLines() []BackorderLine {
	return []BackorderLine{
		{ID: "L1", Priority: 3, RequestedDate: "2027-03-02", OrderValue: n64(900), Demand: n64(20), Confirmed: n64(20)},
		{ID: "L2", Priority: 1, RequestedDate: "2027-03-05", OrderValue: n64(100), Demand: n64(10), Confirmed: n64(0)},
		{ID: "L3", Priority: 1, RequestedDate: "2027-03-03", OrderValue: n64(50), Demand: n64(15), Confirmed: n64(0)},
		{ID: "L4", Priority: 2, RequestedDate: "2027-03-01", OrderValue: n64(300), Demand: n64(12), Confirmed: n64(0)},
		{ID: "L5", Priority: 9, RequestedDate: "2027-02-28", OrderValue: n64(20), Demand: n64(8), Confirmed: n64(8)},
	}
}

func afterOf(p BackorderPlan, id string) decimal.Decimal {
	for _, l := range p.Lines {
		if l.ID == id {
			return l.After
		}
	}
	panic("missing line " + id)
}

func TestPlanBackorder_Scenarios(t *testing.T) {
	cases := []struct {
		name      string
		strategy  string
		available int64
		threshold int
		want      map[string]int64
		deconf    int
	}{
		// Base order: L3 (p1 03-03), L2 (p1 03-05), L4 (p2), L1 (p3), L5 (p9).
		// REDISTRIBUTE ignores confirmation: L1 and L5 lose their confirmed quantity.
		{"REDISTRIBUTE serves priority class 1 first", StrategyRedistribute, 40, 3,
			map[string]int64{"L3": 15, "L2": 10, "L4": 12, "L1": 3, "L5": 0}, 2},
		// WIN keeps the 20 confirmed on L1 and 8 on L5 before any open demand.
		{"WIN protects confirmed quantity", StrategyWin, 40, 3,
			map[string]int64{"L1": 20, "L5": 8, "L3": 12, "L2": 0, "L4": 0}, 0},
		// LOSE at threshold 4 protects L1 (p3) and L3/L2/L4 but may deconfirm L5 (p9).
		{"LOSE deconfirms only priority >= threshold", StrategyLose, 25, 4,
			map[string]int64{"L1": 20, "L3": 5, "L2": 0, "L4": 0, "L5": 0}, 1},
		// GAIN keeps L1's 20 and L5's 8, then serves the smallest open demands:
		// L2 (10), L4 (2 of 12); L3 (15 open) gets none.
		{"GAIN keeps confirmations and favours small open demands", StrategyGain, 40, 3,
			map[string]int64{"L1": 20, "L5": 8, "L2": 10, "L4": 2, "L3": 0}, 0},
		// FILL serves by requested date: L5 (02-28), L4 (03-01), L1 (03-02), L3 (03-03), L2 (03-05).
		{"FILL ignores priority", StrategyFill, 40, 3,
			map[string]int64{"L5": 8, "L4": 12, "L1": 20, "L3": 0, "L2": 0}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := PlanBackorder(c.strategy, n64(c.available), c.threshold, fiveLines())
			if err != nil {
				t.Fatal(err)
			}
			for id, want := range c.want {
				if got := afterOf(p, id); !got.Equal(n64(want)) {
					t.Errorf("%s: after = %s, want %d", id, got, want)
				}
			}
			if p.DeconfirmedCount != c.deconf {
				t.Errorf("deconfirmed = %d, want %d (%+v)", p.DeconfirmedCount, c.deconf, p.Lines)
			}
		})
	}
}

func TestPlanBackorder_Errors(t *testing.T) {
	ok := fiveLines()
	badConfirmed := fiveLines()
	badConfirmed[0].Confirmed = n64(99)
	badDate := fiveLines()
	badDate[0].RequestedDate = "03/02/2027"
	cases := []struct {
		name string
		run  func() error
	}{
		{"unknown strategy", func() error { _, err := PlanBackorder("BOGUS", n64(1), 3, ok); return err }},
		{"negative supply", func() error { _, err := PlanBackorder(StrategyWin, n64(-1), 3, ok); return err }},
		{"threshold 0", func() error { _, err := PlanBackorder(StrategyWin, n64(1), 0, ok); return err }},
		{"duplicate line", func() error {
			_, err := PlanBackorder(StrategyWin, n64(1), 3, append(fiveLines(), ok[0]))
			return err
		}},
		{"confirmed above demand", func() error { _, err := PlanBackorder(StrategyWin, n64(1), 3, badConfirmed); return err }},
		{"bad requested date", func() error { _, err := PlanBackorder(StrategyWin, n64(1), 3, badDate); return err }},
		{"LOSE refuses to deconfirm a protected line", func() error {
			protected := []BackorderLine{{ID: "P", Priority: 1, RequestedDate: "2027-03-01", OrderValue: n64(1), Demand: n64(1), Confirmed: n64(1)}}
			_, err := PlanBackorder(StrategyLose, n64(0), 2, protected)
			return err
		}},
		// BR-27.11: WIN never reduces a confirmed quantity, so 27 of 28 confirmed is refused.
		{"WIN refuses when supply is below the confirmed quantity", func() error { _, err := PlanBackorder(StrategyWin, n64(27), 3, ok); return err }},
		{"GAIN refuses when supply is below the confirmed quantity", func() error { _, err := PlanBackorder(StrategyGain, n64(27), 3, ok); return err }},
	}
	for _, c := range cases {
		if err := c.run(); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
}

// genLines draws 1 to 8 lines with integer quantities so the properties are exact.
func genLines(t *rapid.T) []BackorderLine {
	n := rapid.IntRange(1, 8).Draw(t, "n")
	out := make([]BackorderLine, 0, n)
	for i := 0; i < n; i++ {
		demand := rapid.IntRange(1, 50).Draw(t, "demand")
		conf := rapid.IntRange(0, demand).Draw(t, "confirmed")
		out = append(out, BackorderLine{
			ID:            string(rune('A' + i)),
			Priority:      rapid.IntRange(1, 9).Draw(t, "priority"),
			RequestedDate: "2027-03-" + twoDigits(rapid.IntRange(1, 28).Draw(t, "day")),
			OrderValue:    n64(int64(rapid.IntRange(0, 1000).Draw(t, "value"))),
			Demand:        n64(int64(demand)),
			Confirmed:     n64(int64(conf)),
		})
	}
	return out
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func TestPlanBackorder_Properties(t *testing.T) {
	strategies := []string{StrategyWin, StrategyGain, StrategyRedistribute, StrategyFill, StrategyLose}
	rapid.Check(t, func(t *rapid.T) {
		lines := genLines(t)
		avail := int64(rapid.IntRange(0, 200).Draw(t, "available"))
		threshold := rapid.IntRange(1, 9).Draw(t, "threshold")
		strategy := strategies[rapid.IntRange(0, len(strategies)-1).Draw(t, "strategy")]
		p, err := PlanBackorder(strategy, n64(avail), threshold, lines)
		var protectedConf decimal.Decimal
		for _, l := range lines {
			if protects(strategy, threshold, l) {
				protectedConf = protectedConf.Add(l.Confirmed)
			}
		}
		if err != nil {
			if strings.Contains(err.Error(), "supply is short") && protectedConf.GreaterThan(n64(avail)) {
				return // an infeasible run is refused, never partially committed
			}
			t.Fatal(err)
		}
		again, _ := PlanBackorder(strategy, n64(avail), threshold, lines)
		if len(again.Lines) != len(p.Lines) || !again.Allocated.Equal(p.Allocated) {
			t.Fatal("plan is not deterministic")
		}
		if p.Allocated.GreaterThan(n64(avail)) {
			t.Fatalf("allocated %s exceeds supply %d", p.Allocated, avail)
		}
		demand := map[string]decimal.Decimal{}
		conf := map[string]decimal.Decimal{}
		prio := map[string]int{}
		for _, l := range lines {
			demand[l.ID], conf[l.ID], prio[l.ID] = l.Demand, l.Confirmed, l.Priority
		}
		for _, l := range p.Lines {
			if l.After.IsNegative() || l.After.GreaterThan(demand[l.ID]) {
				t.Fatalf("%s after %s outside [0, demand %s]", l.ID, l.After, demand[l.ID])
			}
			if l.Deconfirmed != l.After.LessThan(conf[l.ID]) {
				t.Fatalf("%s deconfirmed flag inconsistent", l.ID)
			}
			if strategy == StrategyLose && l.Deconfirmed && prio[l.ID] < threshold {
				t.Fatalf("LOSE deconfirmed %s with priority %d below threshold %d", l.ID, prio[l.ID], threshold)
			}
			if (strategy == StrategyWin || strategy == StrategyGain) && l.Deconfirmed {
				t.Fatalf("%s deconfirmed %s", strategy, l.ID)
			}
		}
		// No supply is left idle while some line still has open demand.
		var sumDemand decimal.Decimal
		for _, l := range lines {
			sumDemand = sumDemand.Add(l.Demand)
		}
		if want := decimal.Min(sumDemand, n64(avail)); !p.Allocated.Equal(want) {
			t.Fatalf("allocated %s, want min(demand %s, supply %d)", p.Allocated, sumDemand, avail)
		}
	})
}
