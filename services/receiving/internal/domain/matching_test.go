package domain_test

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

const (
	M = domain.Matched
	P = domain.PriceVariance
	Q = domain.QtyVariance
)

var (
	scenarioQtys   = []string{"50", "100", "105", "106", "200"}
	scenarioPrices = []string{"100", "101.5", "60", "125", "126"}
	ppvPerUnit     = map[string]string{"101.5": "1.5", "60": "-40", "125": "25"}
	upperOnlyGrid  = [5][5]domain.Status{{M, M, M, M, P}, {M, M, M, M, P}, {M, M, M, M, P}, {Q, Q, Q, Q, P}, {Q, Q, Q, Q, P}}
	bothGrid       = [5][5]domain.Status{{M, M, P, M, P}, {M, M, P, M, P}, {M, M, P, M, P}, {Q, Q, P, Q, P}, {Q, Q, P, Q, P}}
)

func scenario(level domain.MatchLevel, receipt domain.ReceiptState, qty, price string) (domain.Policy, domain.Invoice) {
	p := domain.DefaultPolicy()
	p.MatchLevel = level
	line := domain.InvoiceLine{
		Qty: dec(qty), UnitPrice: dec(price), POUnitPrice: dec("100"),
		ReceiptState: receipt, ReceivedUninvoiced: dec("100"), POQtyRemaining: dec("100"),
	}
	return p, domain.Invoice{Currency: "USD", HeaderTotal: dec(qty).Mul(dec(price)), Lines: []domain.InvoiceLine{line}}
}

func TestEvaluate_Match200(t *testing.T) {
	type mode struct {
		name    string
		level   domain.MatchLevel
		receipt domain.ReceiptState
		both    bool
		want    func(row, col int, both bool) domain.Status
	}
	grid := func(row, col int, both bool) domain.Status {
		if both {
			return bothGrid[row][col]
		}
		return upperOnlyGrid[row][col]
	}
	constant := func(s domain.Status) func(int, int, bool) domain.Status {
		return func(int, int, bool) domain.Status { return s }
	}
	modes := []mode{
		{"three-way posted", domain.MatchThreeWay, domain.Posted, false, grid},
		{"two-way", domain.MatchTwoWay, "", false, grid},
		{"three-way no receipt", domain.MatchThreeWay, "", false, constant(domain.NoReceipt)},
		{"three-way waiting", domain.MatchThreeWay, domain.Registered, false, constant(domain.WaitingReceipt)},
	}
	count := 0
	for _, both := range []bool{false, true} {
		for _, m := range modes {
			for qi, qty := range scenarioQtys {
				for pi, price := range scenarioPrices {
					count++
					t.Run(fmt.Sprintf("%s both=%v qty=%s price=%s", m.name, both, qty, price), func(t *testing.T) {
						p, in := scenario(m.level, m.receipt, qty, price)
						if both {
							p.PriceTolMode = domain.Both
						}
						res := domain.Evaluate(p, in)
						want := m.want(qi, pi, both)
						if res.Status != want {
							t.Fatalf("status %s, want %s", res.Status, want)
						}
						if want != domain.Matched {
							if len(res.Postings) != 0 {
								t.Fatalf("variance status must not post, got %+v", res.Postings)
							}
							return
						}
						unit, hasVariance := ppvPerUnit[price]
						if !hasVariance {
							if len(res.Postings) != 0 {
								t.Fatalf("exact price must not post, got %+v", res.Postings)
							}
							return
						}
						wantAmount := dec(unit).Mul(dec(qty))
						if len(res.Postings) != 1 || res.Postings[0].Kind != domain.PostingPPV || !res.Postings[0].Amount.Value.Equal(wantAmount) {
							t.Fatalf("postings %+v, want one PPV of %s", res.Postings, wantAmount)
						}
					})
				}
			}
		}
	}
	if count != 200 {
		t.Fatalf("scenario count %d, want 200", count)
	}
}

func TestEvaluate_HeaderAndPolicyEdges(t *testing.T) {
	tests := []struct {
		name         string
		tweakPolicy  func(*domain.Policy)
		tweakInvoice func(*domain.Invoice)
		want         domain.Status
		wantPosting  string
	}{
		{"total variance above tolerance", nil, func(i *domain.Invoice) { i.HeaderTotal = dec("10101") }, domain.TotalVariance, ""},
		{"small difference posts and matches", nil, func(i *domain.Invoice) { i.HeaderTotal = dec("10000.40") }, M, domain.PostingSmallDif},
		{"difference above small but within total tolerance matches without small-diff posting", nil, func(i *domain.Invoice) { i.HeaderTotal = dec("10020") }, M, ""},
		{"charges and tax are excluded from variance", nil, func(i *domain.Invoice) {
			i.Charges, i.Tax, i.HeaderTotal = dec("30"), dec("70"), dec("10100")
		}, M, ""},
		{"currency mismatch is an exception", nil, func(i *domain.Invoice) { i.Lines[0].POCurrency = "EUR" }, P, ""},
		{"matching switched off", func(p *domain.Policy) { p.MatchLevel = domain.MatchNone }, nil, domain.NotMatched, ""},
		{"price within percent but above absolute", func(p *domain.Policy) { p.PriceTolAbs = dec("0") }, func(i *domain.Invoice) {
			i.Lines[0].UnitPrice, i.Lines[0].Qty, i.HeaderTotal = dec("101.99"), dec("1"), dec("101.99")
		}, M, domain.PostingPPV},
		{"price just above percent with zero absolute", func(p *domain.Policy) { p.PriceTolAbs = dec("0") }, func(i *domain.Invoice) {
			i.Lines[0].UnitPrice, i.Lines[0].Qty, i.HeaderTotal = dec("102.01"), dec("1"), dec("102.01")
		}, P, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, in := scenario(domain.MatchThreeWay, domain.Posted, "100", "100")
			if tc.tweakPolicy != nil {
				tc.tweakPolicy(&p)
			}
			if tc.tweakInvoice != nil {
				tc.tweakInvoice(&in)
			}
			res := domain.Evaluate(p, in)
			if res.Status != tc.want {
				t.Fatalf("status %s, want %s", res.Status, tc.want)
			}
			if tc.wantPosting != "" && (len(res.Postings) == 0 || res.Postings[len(res.Postings)-1].Kind != tc.wantPosting) {
				t.Fatalf("postings %+v, want last kind %s", res.Postings, tc.wantPosting)
			}
			if tc.wantPosting == "" && len(res.Postings) != 0 {
				t.Fatalf("unexpected postings %+v", res.Postings)
			}
		})
	}
}

func TestEvaluate_ToleranceMonotonic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		low := rapid.IntRange(10000, 20000).Draw(t, "low")
		high := rapid.IntRange(low, 20000).Draw(t, "high")
		statusAt := func(cents int) domain.Status {
			price := decimal.New(int64(cents), -2).String()
			p, in := scenario(domain.MatchThreeWay, domain.Posted, "10", price)
			return domain.Evaluate(p, in).Status
		}
		if statusAt(low) != domain.Matched && statusAt(high) == domain.Matched {
			t.Fatalf("price %d is a variance but higher price %d matched", low, high)
		}
	})
}
