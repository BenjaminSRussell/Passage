package domain

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
)

type demoJournal struct {
	period  int
	debit   string
	credit  string
	amount  decimal.Decimal
	closing bool
}

func cents(v int64) decimal.Decimal { return decimal.New(v, -2) }

func demoFY2026Journals() []demoJournal {
	js := []demoJournal{{0, "100100", "300100", cents(500000000), false}}
	revenue, expenses := decimal.Zero, decimal.Zero
	for p := 1; p <= 12; p++ {
		sale := cents(10000037 + int64(p)*111111)
		collect := cents(8000013 + int64(p)*50007)
		cogs := cents(4000041 + int64(p)*22222)
		opex := cents(1200005 + int64(p)*333)
		js = append(js,
			demoJournal{p, "110100", "400100", sale, false},
			demoJournal{p, "100100", "110100", collect, false},
			demoJournal{p, "500100", "200100", cogs, false},
			demoJournal{p, "200100", "100100", cents(3000029), false},
			demoJournal{p, "610100", "100100", opex, false})
		revenue, expenses = revenue.Add(sale), expenses.Add(cogs).Add(opex)
		switch p {
		case 3:
			js = append(js, demoJournal{p, "155100", "100100", cents(25000099), false})
		case 6:
			js = append(js, demoJournal{p, "100100", "230100", cents(10000000), false})
		}
	}
	cogsTotal, opexTotal := decimal.Zero, decimal.Zero
	for _, j := range js {
		switch j.debit {
		case "500100":
			cogsTotal = cogsTotal.Add(j.amount)
		case "610100":
			opexTotal = opexTotal.Add(j.amount)
		}
	}
	return append(js,
		demoJournal{12, "400100", "300900", revenue, true},
		demoJournal{12, "300900", "500100", cogsTotal, true},
		demoJournal{12, "300900", "610100", opexTotal, true})
}

func balancesFromJournals(js []demoJournal, upTo int) []Balance {
	type key struct {
		period  int
		account string
	}
	net, closing := map[key]decimal.Decimal{}, map[key]decimal.Decimal{}
	for _, j := range js {
		if j.period > upTo {
			continue
		}
		net[key{j.period, j.debit}] = net[key{j.period, j.debit}].Add(j.amount)
		net[key{j.period, j.credit}] = net[key{j.period, j.credit}].Sub(j.amount)
		if j.closing {
			closing[key{j.period, j.debit}] = closing[key{j.period, j.debit}].Add(j.amount)
			closing[key{j.period, j.credit}] = closing[key{j.period, j.credit}].Sub(j.amount)
		}
	}
	var out []Balance
	for k, v := range net {
		out = append(out, Balance{LedgerID: "L1", Period: k.period, Account: k.account, Combination: k.account,
			Dims: map[string]string{}, CurrencyType: "rpt", Net: v, ClosingNet: closing[k]})
	}
	return out
}

func demoCalendar(t *testing.T) Calendar {
	t.Helper()
	ps := []Period{{Year: 2026, No: 0, Quarter: 1}}
	for no := 1; no <= 12; no++ {
		ps = append(ps, Period{Year: 2026, No: no, Quarter: quarterOf(no)})
	}
	cal, err := NewCalendar(ps)
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

func journalSum(js []demoJournal, from, to int, account string, includeClosing bool) decimal.Decimal {
	sum := decimal.Zero
	for _, j := range js {
		if j.period < from || j.period > to || (j.closing && !includeClosing) {
			continue
		}
		if j.debit == account {
			sum = sum.Add(j.amount)
		}
		if j.credit == account {
			sum = sum.Sub(j.amount)
		}
	}
	return sum
}

func requireValue(t *testing.T, res Result, row, col string, want decimal.Decimal) {
	t.Helper()
	if c := cellAt(t, res, row, col); c.Blank || !c.Value.Equal(want) {
		t.Fatalf("%s/%s = %+v, want %s", row, col, c, want.StringFixed(2))
	}
}

// TestDemoDataset_StatementsTieToTrialBalanceAndCashFlowReconciles is AC1 on a
// deterministic closed fiscal year: 12 periods of cent-level postings with
// closing entries in period 12. In every period the trial balance totals 0.00,
// the balance sheet check is 0.00, the P&L result equals the journal result, and
// the cash flow net equals the change in cash taken from the journals.
func TestDemoDataset_StatementsTieToTrialBalanceAndCashFlowReconciles(t *testing.T) {
	js := demoFY2026Journals()
	cal := demoCalendar(t)
	for at := 1; at <= 12; at++ {
		t.Run(fmt.Sprintf("period_%02d", at), func(t *testing.T) {
			bal := balancesFromJournals(js, at)
			run := func(code string) (Input, Result) {
				in := Input{Definition: standardByCode(t, code), LedgerID: "L1", CurrencyType: "rpt", Calendar: cal, At: at, Balances: bal}
				res, err := Evaluate(in)
				if err != nil {
					t.Fatalf("%s: %v", code, err)
				}
				return in, res
			}
			_, tb := run(CodeTBStd)
			_, bs := run(CodeBSStd)
			_, pl := run(CodePLStd)
			cfIn, cf := run(CodeCFStd)

			requireValue(t, tb, "TOTAL", "CLOSE", decimal.Zero)
			requireValue(t, bs, "CHK", "CUR", decimal.Zero)

			result := journalSum(js, 1, at, "400100", false).Add(journalSum(js, 1, at, "500100", false)).
				Add(journalSum(js, 1, at, "610100", false)).Neg()
			requireValue(t, pl, "RES", "YTD", result)

			requireValue(t, cf, "NI", "YTD", result)
			requireValue(t, cf, "CASH", "YTD", journalSum(js, 1, at, "100100", true))
			requireValue(t, cf, "CASH", "P", journalSum(js, at, at, "100100", true))
			requireValue(t, cf, "CF_NET", "YTD", journalSum(js, 1, at, "100100", true))
			requireValue(t, cf, "CF_CHK", "YTD", decimal.Zero)
			requireValue(t, cf, "CF_CHK", "P", decimal.Zero)
			if err := CheckCashFlow(cfIn, cf); err != nil {
				t.Fatalf("cash flow does not reconcile: %v", err)
			}
		})
	}
}
