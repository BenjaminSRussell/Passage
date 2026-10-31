package domain

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// budgetFixtures mirrors testdata/budget-check-fixtures.json, the Wave 38 proof
// set for "POs over budget are blocked/warned per policy; encumbrance
// liquidation on invoice is exact". The expected values are hand-derived
// literals in the file, never recomputed here.
type budgetFixtures struct {
	Policies map[string]struct {
		WarnPct  string `json:"warnPct"`
		BlockPct string `json:"blockPct"`
	} `json:"policies"`
	Balances map[string]struct {
		Budget          string `json:"budget"`
		Actuals         string `json:"actuals"`
		Encumbrances    string `json:"encumbrances"`
		PreEncumbrances string `json:"preEncumbrances"`
		Reservations    string `json:"reservations"`
		Available       string `json:"available"`
	} `json:"balances"`
	Checks []struct {
		ID       string `json:"id"`
		Policy   string `json:"policy"`
		Balance  string `json:"balance"`
		Amount   string `json:"amount"`
		Prior    string `json:"prior"`
		Relieved string `json:"relieved"`
		Override bool   `json:"override"`
		Want     struct {
			Result  Result `json:"result"`
			Net     string `json:"net"`
			OverPct string `json:"overPct"`
			Reserve string `json:"reserve"`
		} `json:"want"`
	} `json:"checks"`
	Liquidations []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Reserve string `json:"reserve"`
		Steps   []struct {
			Op           string `json:"op"`
			Amount       string `json:"amount"`
			MatchedShare string `json:"matchedShare"`
			Reason       string `json:"reason"`
			Want         struct {
				Relief    string `json:"relief"`
				Excess    string `json:"excess"`
				Released  string `json:"released"`
				Remaining string `json:"remaining"`
				Status    string `json:"status"`
			} `json:"want"`
		} `json:"steps"`
	} `json:"liquidations"`
}

func loadBudgetFixtures(t *testing.T) budgetFixtures {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/budget-check-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var f budgetFixtures
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Checks) == 0 || len(f.Liquidations) == 0 {
		t.Fatal("fixture file has no cases")
	}
	return f
}

// dz parses a fixture amount; an omitted amount is zero.
func dz(s string) decimal.Decimal {
	if s == "" {
		return decimal.Zero
	}
	return decimal.RequireFromString(s)
}

// Wave 38 proof, AC "budget check fixtures": every fixture line is blocked,
// warned or passed per its policy, with the hand-derived net, over-percentage
// and reservation.
func TestBudgetCheckFixtures_OutcomePerPolicy(t *testing.T) {
	f := loadBudgetFixtures(t)
	seen := map[Result]int{}
	for _, c := range f.Checks {
		t.Run(c.ID, func(t *testing.T) {
			p, ok := f.Policies[c.Policy]
			b, okb := f.Balances[c.Balance]
			if !ok || !okb {
				t.Fatalf("unknown policy %q or balance %q", c.Policy, c.Balance)
			}
			bal := Balance{Budget: dz(b.Budget), Actuals: dz(b.Actuals), Encumbrances: dz(b.Encumbrances),
				PreEncumbrances: dz(b.PreEncumbrances), Reservations: dz(b.Reservations)}
			got := Check(bal, Thresholds{WarnPct: dz(p.WarnPct), BlockPct: dz(p.BlockPct)},
				CheckInput{Amount: dz(c.Amount), Prior: dz(c.Prior), Relieved: dz(c.Relieved), Override: c.Override})
			if got.Result != c.Want.Result {
				t.Fatalf("result = %s, want %s (trace %v)", got.Result, c.Want.Result, got.Trace)
			}
			if !got.Available.Equal(dz(b.Available)) {
				t.Errorf("available = %s, want %s", got.Available, b.Available)
			}
			if !got.Net.Equal(dz(c.Want.Net)) {
				t.Errorf("net = %s, want %s", got.Net, c.Want.Net)
			}
			if c.Want.OverPct != "" && !got.OverPct.Equal(dz(c.Want.OverPct)) {
				t.Errorf("overPct = %s, want %s", got.OverPct, c.Want.OverPct)
			}
			if !got.Reserve.Equal(dz(c.Want.Reserve)) {
				t.Errorf("reserve = %s, want %s", got.Reserve, c.Want.Reserve)
			}
			seen[got.Result]++
		})
	}
	// The set must exercise every outcome, so a policy change cannot slip by.
	if seen[Pass] == 0 || seen[Warn] == 0 || seen[Fail] == 0 {
		t.Fatalf("outcomes covered: %v, want pass, warn and fail", seen)
	}
}

// Wave 38 proof, AC "encumbrance liquidation on invoice is exact": each
// liquidation sequence relieves, releases and reports excess to the hand-derived
// cent, and at every step relieved + released + remaining = reserved.
func TestBudgetCheckFixtures_LiquidationIsExact(t *testing.T) {
	f := loadBudgetFixtures(t)
	for _, l := range f.Liquidations {
		t.Run(l.ID, func(t *testing.T) {
			reserved, relieved, released := dz(l.Reserve), decimal.Zero, decimal.Zero
			for i, s := range l.Steps {
				switch s.Op {
				case "invoice":
					// The store relieves against what a release has not given back.
					relief := Relief(reserved.Sub(released), relieved, dz(s.MatchedShare))
					if !relief.Equal(dz(s.Want.Relief)) {
						t.Fatalf("step %d: relief = %s, want %s", i, relief, s.Want.Relief)
					}
					if got := Excess(dz(s.Amount), relief); !got.Equal(dz(s.Want.Excess)) {
						t.Fatalf("step %d: excess = %s, want %s", i, got, s.Want.Excess)
					}
					relieved = relieved.Add(relief)
				case "release":
					rel := Release(reserved, relieved, released)
					if !rel.Equal(dz(s.Want.Released)) {
						t.Fatalf("step %d: released = %s, want %s", i, rel, s.Want.Released)
					}
					released = released.Add(rel)
				default:
					t.Fatalf("step %d: unknown op %q", i, s.Op)
				}
				remaining := Release(reserved, relieved, released)
				if !remaining.Equal(dz(s.Want.Remaining)) {
					t.Fatalf("step %d: remaining = %s, want %s", i, remaining, s.Want.Remaining)
				}
				if got := EncumbranceStatus(reserved, relieved, released); got != s.Want.Status {
					t.Fatalf("step %d: status = %s, want %s", i, got, s.Want.Status)
				}
				if !relieved.Add(released).Add(remaining).Equal(reserved) {
					t.Fatalf("step %d: relieved %s + released %s + remaining %s != reserved %s", i, relieved, released, remaining, reserved)
				}
			}
		})
	}
}

// oracleCheck is an independent statement of FR-38.4 steps 4-8 in integer cents
// and basis points. It never divides: "over is at most p% of available" is
// tested by cross-multiplying, over x 10000 <= p_bp x available.
func oracleCheck(budget, actuals, enc, pre, res, amount, prior, relieved, warnBp, blockBp int64, override bool) (Result, int64) {
	avail := budget - actuals - enc - pre - res
	net := max(amount, relieved) - prior
	var r Result
	switch over := net - avail; {
	case net <= 0 || over <= 0:
		r = Pass
	case avail <= 0:
		r = Fail
	case over*10000 <= warnBp*avail:
		r = Pass
	case over*10000 <= blockBp*avail:
		r = Warn
	default:
		r = Fail
	}
	if r == Fail && override {
		r = Warn
	}
	if r == Fail {
		return r, 0
	}
	return r, net
}

// Property: Check agrees with the integer oracle on the outcome and the
// reservation for random balances, revisions, thresholds and overrides.
func TestCheck_MatchesIntegerOracle(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cents := func(label string, hi int64) int64 { return rapid.Int64Range(0, hi).Draw(t, label) }
		budget := cents("budget", 1_000_000_000)
		actuals, enc := cents("actuals", budget), cents("enc", budget)
		pre, res := cents("pre", budget/4), cents("res", budget/4)
		prior := cents("prior", enc)
		relieved := cents("relieved", prior)
		amount := cents("amount", 2*budget+1)
		if rapid.Bool().Draw(t, "nearBoundary") {
			// Land on or one cent beside the block threshold to probe the boundary.
			blockBp := rapid.Int64Range(0, 2000).Draw(t, "boundaryBp")
			if avail := budget - actuals - enc - pre - res; avail > 0 && blockBp*avail%10000 == 0 {
				amount = prior + avail + blockBp*avail/10000 + rapid.Int64Range(-1, 1).Draw(t, "nudge")
				if amount < 0 {
					amount = 0
				}
			}
		}
		warnBp := rapid.Int64Range(0, 2000).Draw(t, "warnBp")
		blockBp := warnBp + rapid.Int64Range(0, 2000).Draw(t, "spreadBp")
		override := rapid.Bool().Draw(t, "override")

		c := func(v int64) decimal.Decimal { return decimal.New(v, -2) }
		got := Check(Balance{Budget: c(budget), Actuals: c(actuals), Encumbrances: c(enc), PreEncumbrances: c(pre), Reservations: c(res)},
			Thresholds{WarnPct: decimal.New(warnBp, -2), BlockPct: decimal.New(blockBp, -2)},
			CheckInput{Amount: c(amount), Prior: c(prior), Relieved: c(relieved), Override: override})
		want, reserve := oracleCheck(budget, actuals, enc, pre, res, amount, prior, relieved, warnBp, blockBp, override)
		if got.Result != want {
			t.Fatalf("result = %s, oracle %s (available %s, net %s, overPct %s)", got.Result, want, got.Available, got.Net, got.OverPct)
		}
		if !got.Reserve.Equal(c(reserve)) {
			t.Fatalf("reserve = %s, oracle %s", got.Reserve, c(reserve))
		}
	})
}
