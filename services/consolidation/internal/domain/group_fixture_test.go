package domain

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/shopspring/decimal"
)

type fixtureRates struct {
	Closing, Average, Opening, Historical decimal.Decimal
}

type fixturePair struct {
	Unit               string
	SellerIsSubsidiary bool
	OwnershipPct       decimal.Decimal
	Rates              fixtureRates
	ICSales            decimal.Decimal
	ICCost             decimal.Decimal
	UnsoldShare        decimal.Decimal
	ICReceivable       decimal.Decimal
	PayableGroup       decimal.Decimal
	ShareCapital       decimal.Decimal
	PreAcqRE           decimal.Decimal
	InvestmentCost     decimal.Decimal
	NetIncome          decimal.Decimal
}

type fixtureLine struct {
	Rule, Account string
	Debit, Credit decimal.Decimal
}

type groupFixture struct {
	Currency string
	Pairs    []fixturePair
	Expected []fixtureLine
}

func loadGroupFixture(t *testing.T) groupFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/consol-group-3-interim.json")
	if err != nil {
		t.Fatal(err)
	}
	var f groupFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestEliminate_ThreeEntityGroupFixture checks a hand-computed (not yet
// accountant-reviewed) elimination set for an interim three-entity group.
func TestEliminate_ThreeEntityGroupFixture(t *testing.T) {
	f := loadGroupFixture(t)
	type key struct{ rule, account string }
	debits := map[key]decimal.Decimal{}
	credits := map[key]decimal.Decimal{}
	net := map[key]decimal.Decimal{}
	var order []key
	var all []Entry
	for _, p := range f.Pairs {
		entries, err := Eliminate(Pair{
			Currency:           f.Currency,
			OwnershipPct:       p.OwnershipPct,
			Rates:              Rates(p.Rates),
			ICSales:            p.ICSales,
			ICCost:             p.ICCost,
			UnsoldShare:        p.UnsoldShare,
			ICReceivable:       p.ICReceivable,
			PayableGroup:       p.PayableGroup,
			ShareCapital:       p.ShareCapital,
			PreAcqRE:           p.PreAcqRE,
			InvestmentCost:     p.InvestmentCost,
			NetIncome:          p.NetIncome,
			SellerIsSubsidiary: p.SellerIsSubsidiary,
		})
		if err != nil {
			t.Fatalf("%s: %v", p.Unit, err)
		}
		all = append(all, entries...)
		for _, e := range entries {
			k := key{e.RuleCode, e.Account}
			if _, seen := net[k]; !seen {
				order = append(order, k)
			}
			net[k] = net[k].Add(e.Debit.Sub(e.Credit))
			debits[k] = debits[k].Add(e.Debit)
			credits[k] = credits[k].Add(e.Credit)
		}
	}
	if err := CheckBalanced(all); err != nil {
		t.Fatal(err)
	}
	if len(order) != len(f.Expected) {
		t.Fatalf("got %d rule/account lines; want %d", len(order), len(f.Expected))
	}
	for _, w := range f.Expected {
		k := key{w.Rule, w.Account}
		if _, ok := net[k]; !ok {
			t.Fatalf("missing %s %s", w.Rule, w.Account)
		}
		if !debits[k].Equal(w.Debit) || !credits[k].Equal(w.Credit) {
			t.Fatalf("%s %s Dr %s Cr %s; want Dr %s Cr %s", w.Rule, w.Account,
				debits[k].StringFixed(2), credits[k].StringFixed(2), w.Debit.StringFixed(2), w.Credit.StringFixed(2))
		}
	}
}
