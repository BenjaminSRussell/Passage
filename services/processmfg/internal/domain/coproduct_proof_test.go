package domain

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// cobyCase is one case of pmf-coby-adv-23.json.
type cobyCase struct {
	Name       string `json:"name"`
	Currency   string `json:"currency"`
	BatchCost  string `json:"batchCost"`
	Method     string `json:"method"`
	Main       cobyProduct
	CoProducts []cobyProduct `json:"coProducts"`
	ByProducts []struct {
		CostType string `json:"costType"`
		Qty      string `json:"qty"`
		Rate     string `json:"rate"`
	} `json:"byProducts"`
	ExpectedCode string `json:"expectedCode"`
	Expected     *struct {
		Main       cobyShare   `json:"main"`
		CoProducts []cobyShare `json:"coProducts"`
		Burdens    []string    `json:"burdens"`
	} `json:"expected"`
}

type cobyProduct struct {
	StandardCost string `json:"standardCost"`
	RafQty       string `json:"rafQty"`
	Percent      string `json:"percent"`
}

type cobyShare struct {
	Amount  string `json:"amount"`
	Percent string `json:"percent"`
}

func (p cobyProduct) product(id string) Product {
	return Product{ItemID: id, StandardCost: dec(p.StandardCost), RafQty: dec(p.RafQty), Percent: dec(p.Percent)}
}

func readCobyCases(t *testing.T) []cobyCase {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/pmf-coby-adv-23.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []cobyCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 23 {
		t.Fatalf("pmf-coby-adv-23 has %d cases", len(cases))
	}
	return cases
}

// TestAllocateCost_AdversarialFixture runs pmf-coby-adv-23 (Wave 24 proof:
// co/by-product cost allocation sums to batch cost exactly across methods).
// Expected shares come from testdata/gen_pmf_coby_adv.py, an exact-rational
// reference with no code shared with the engine: thirds and sevenths, a
// weightless main product where rounding overshoots, burdens that take the
// whole cost, every burden type, JPY and KWD minor units, a ten-billion cost
// over five co-products, percent burdens totalling 100 % whose rounding alone
// would exceed the cost, and (in CLF, 4 minor units) many tiny burdens or
// co-products whose rounding overshoot is larger than any single share.
func TestAllocateCost_AdversarialFixture(t *testing.T) {
	for _, c := range readCobyCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			var co []Product
			for _, p := range c.CoProducts {
				co = append(co, p.product("co"))
			}
			var by []ByProduct
			for _, b := range c.ByProducts {
				by = append(by, ByProduct{ItemID: "by", CostType: b.CostType, Qty: dec(b.Qty), Rate: dec(b.Rate)})
			}
			cost := dec(c.BatchCost)
			res, err := AllocateCost(cost, AllocationMethod(c.Method), c.Main.product("main"), co, by)
			if c.ExpectedCode != "" {
				if de, ok := err.(*Error); !ok || de.Code != c.ExpectedCode {
					t.Fatalf("got %+v err %v, want %s", res, err, c.ExpectedCode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := c.Expected
			if !res.Main.Amount.Equal(dec(want.Main.Amount)) || !res.Main.Percent.Equal(dec(want.Main.Percent)) {
				t.Errorf("main %s / %s%%, want %s / %s%%", res.Main.Amount, res.Main.Percent, want.Main.Amount, want.Main.Percent)
			}
			if len(res.CoProducts) != len(want.CoProducts) || len(res.Burdens) != len(want.Burdens) {
				t.Fatalf("got %d co-products and %d burdens", len(res.CoProducts), len(res.Burdens))
			}
			sum := res.Main.Amount
			for i, w := range want.CoProducts {
				got := res.CoProducts[i]
				if !got.Amount.Equal(dec(w.Amount)) || !got.Percent.Equal(dec(w.Percent)) {
					t.Errorf("co-product %d %s / %s%%, want %s / %s%%", i, got.Amount, got.Percent, w.Amount, w.Percent)
				}
				sum = sum.Add(got.Amount)
			}
			for i, w := range want.Burdens {
				if !res.Burdens[i].Amount.Equal(dec(w)) {
					t.Errorf("burden %d %s, want %s", i, res.Burdens[i].Amount, w)
				}
				sum = sum.Add(res.Burdens[i].Amount)
			}
			if !sum.Equal(cost) || !res.Total.Equal(cost) {
				t.Errorf("parts sum to %s (total %s), batch cost %s", sum, res.Total, cost)
			}
		})
	}
}

// TestAllocateCost_ExactAndFairAcrossMethodsAndBurdens widens the slice-9
// property to every by-product cost type, including percent burdens up to a
// combined 100 %, and to costs from one ten-thousandth to ten billion. Whatever
// the method:
//   - the allocation is refused (PMF-0021) only when the burdens exceed the
//     cost both exactly and once rounded (weights are drawn non-zero);
//   - otherwise main + co-products + burdens = batch cost exactly, no share is
//     negative, and the percentages of main and co-products add up to 100;
//   - every product share is within n × 0.00005 (n = number of products plus
//     burdens) of its exact proportional share of the cost after burdens, so
//     the residual rule never moves more than rounding.
func TestAllocateCost_ExactAndFairAcrossMethodsAndBurdens(t *testing.T) {
	methods := []AllocationMethod{MethodNone, MethodManual, MethodTCA, MethodQuantity}
	types := []string{"percent", "per-series", "per-quantity", "recycled"}
	hundred := decimal.NewFromInt(100)
	rapid.Check(t, func(t *rapid.T) {
		cost := decimal.New(rapid.Int64Range(0, 100_000_000_000_000).Draw(t, "cost"), -4)
		method := rapid.SampledFrom(methods).Draw(t, "method")
		minMain := int64(1)
		n := rapid.IntRange(0, 5).Draw(t, "co")
		if n > 0 {
			minMain = 0
		}
		draw := func(label string, min int64) Product {
			return Product{ItemID: label,
				StandardCost: decimal.New(rapid.Int64Range(min, 1_000_000).Draw(t, label+"-std"), -2),
				RafQty:       decimal.New(rapid.Int64Range(1, 10_000_000).Draw(t, label+"-raf"), -3),
				Percent:      decimal.New(rapid.Int64Range(min, 1_000_000).Draw(t, label+"-pct"), -4)}
		}
		main := draw("main", minMain)
		var co []Product
		for i := 0; i < n; i++ {
			co = append(co, draw("co", 1))
		}
		var by []ByProduct
		pctLeft := int64(1_000_000) // 100 % in ten-thousandths
		exactBurden := decimal.Zero
		for i := rapid.IntRange(0, 4).Draw(t, "by"); i > 0; i-- {
			kind := rapid.SampledFrom(types).Draw(t, "type")
			if kind == "percent" {
				r := rapid.Int64Range(0, pctLeft).Draw(t, "rate-pct")
				pctLeft -= r
				rate := decimal.New(r, -4)
				by = append(by, ByProduct{ItemID: "by", CostType: kind, Rate: rate})
				exactBurden = exactBurden.Add(cost.Mul(rate).Div(hundred))
				continue
			}
			qty := decimal.New(rapid.Int64Range(0, 100_000).Draw(t, "bqty"), -3)
			rate := decimal.New(rapid.Int64Range(0, 1_000_000).Draw(t, "brate"), -4)
			by = append(by, ByProduct{ItemID: "by", CostType: kind, Qty: qty, Rate: rate})
			exactBurden = exactBurden.Add(qty.Mul(rate))
		}

		res, err := AllocateCost(cost, method, main, co, by)
		// Refused only when the burdens exceed the cost exactly and rounded: a
		// burden below the 4th decimal rounds away, and percent burdens totalling
		// 100 % must never be refused for their rounding.
		roundedBurden := decimal.Zero
		for _, b := range by {
			if b.CostType == "percent" {
				roundedBurden = roundedBurden.Add(cost.Mul(b.Rate).Div(hundred).Round(AmountPrecision))
			} else {
				roundedBurden = roundedBurden.Add(b.Qty.Mul(b.Rate).Round(AmountPrecision))
			}
		}
		if exactBurden.GreaterThan(cost) && roundedBurden.GreaterThan(cost) {
			if de, ok := err.(*Error); !ok || de.Code != CodeAllocation {
				t.Fatalf("burden %s above cost %s: err %v", exactBurden, cost, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("cost %s, exact burden %s: %v", cost, exactBurden, err)
		}
		sum, pct := res.Main.Amount, res.Main.Percent
		shares := append([]Allocated{res.Main}, res.CoProducts...)
		for _, c := range res.CoProducts {
			sum, pct = sum.Add(c.Amount), pct.Add(c.Percent)
		}
		burdens := decimal.Zero
		for _, b := range res.Burdens {
			if b.Amount.IsNegative() {
				t.Fatalf("negative burden %+v", b)
			}
			burdens = burdens.Add(b.Amount)
		}
		if !sum.Add(burdens).Equal(cost) || !res.Total.Equal(cost) {
			t.Fatalf("parts %s + burdens %s != cost %s", sum, burdens, cost)
		}
		if !pct.Equal(hundred) {
			t.Fatalf("percentages add to %s", pct)
		}
		weights := make([]decimal.Decimal, len(shares))
		total := decimal.Zero
		for i, p := range append([]Product{main}, co...) {
			switch method {
			case MethodNone:
				if i == 0 {
					weights[i] = decimal.NewFromInt(1)
				}
			case MethodManual:
				weights[i] = p.Percent
			case MethodTCA:
				weights[i] = p.StandardCost.Mul(p.RafQty)
			case MethodQuantity:
				weights[i] = p.RafQty
			}
			total = total.Add(weights[i])
		}
		tol := decimal.New(5, -5).Mul(decimal.NewFromInt(int64(len(shares) + len(by))))
		exactNet := cost.Sub(exactBurden)
		for i, s := range shares {
			if s.Amount.IsNegative() || s.Percent.IsNegative() || s.Percent.GreaterThan(hundred) {
				t.Fatalf("share out of range %+v", s)
			}
			exact := exactNet.Mul(weights[i]).DivRound(total, 20)
			if d := s.Amount.Sub(exact).Abs(); d.GreaterThan(tol) {
				t.Fatalf("share %d = %s, exact %s (off by %s > %s, method %s)", i, s.Amount, exact, d, tol, method)
			}
		}
	})
}

// Regression: percent burdens totalling 100 % on USD 0.01 were refused with
// PMF-0021 because 0.00505 and 0.00495 round to 0.0051 + 0.0050 = 0.0101. The
// overshoot now comes off the largest burden first (hand-derived: 0.0050 +
// 0.0050).
func TestAllocateCost_PercentBurdenRoundingIsNotRefused(t *testing.T) {
	by := []ByProduct{{ItemID: "P", CostType: "percent", Rate: dec("50.5")}, {ItemID: "Q", CostType: "percent", Rate: dec("49.5")}}
	res, err := AllocateCost(dec("0.01"), MethodQuantity, Product{ItemID: "A", RafQty: dec("1")}, nil, by)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Burdens[0].Amount.Equal(dec("0.005")) || !res.Burdens[1].Amount.Equal(dec("0.005")) || !res.Main.Amount.IsZero() || !res.Total.Equal(dec("0.01")) {
		t.Fatalf("burdens %+v main %s total %s", res.Burdens, res.Main.Amount, res.Total)
	}
	// Burdens that exceed the cost before rounding are still refused.
	by[1].Rate = dec("49.6")
	if _, err := AllocateCost(dec("100"), MethodQuantity, Product{ItemID: "A", RafQty: dec("1")}, nil, by); err == nil || err.(*Error).Code != CodeAllocation {
		t.Fatalf("100.1 %% of burdens: %v", err)
	}
}

// Property aimed at the corner the wide property above rarely draws: costs of
// a few ten-thousandths, up to 40 burdens of exactly half a ten-thousandth
// (each rounds up to 0.0001 while all of them still fit the cost exactly) and
// up to 12 co-products under a weightless main product. The rounding overshoot
// is then larger than any single share; no share may go negative, nothing may
// be refused, and the parts must still add up to the cost.
func TestAllocateCost_ManyTinySharesNeverGoNegative(t *testing.T) {
	half := decimal.New(5, -5)
	rapid.Check(t, func(t *rapid.T) {
		units := rapid.Int64Range(0, 20).Draw(t, "cost")
		cost := decimal.New(units, -4)
		var by []ByProduct
		for i := rapid.Int64Range(0, min(40, 2*units)).Draw(t, "by"); i > 0; i-- {
			// a percent of the cost that is exactly half a ten-thousandth, when one
			// exists (50 / units terminates); otherwise per-quantity 0.5 × 0.0001
			b := ByProduct{ItemID: "by", CostType: "per-quantity", Qty: dec("0.5"), Rate: dec("0.0001")}
			if rapid.Bool().Draw(t, "percent") && units > 0 {
				if rate := half.Shift(2).Div(cost); cost.Mul(rate).Shift(-2).Equal(half) {
					b = ByProduct{ItemID: "by", CostType: "percent", Rate: rate}
				}
			}
			by = append(by, b)
		}
		var co []Product
		for i := rapid.IntRange(1, 12).Draw(t, "co"); i > 0; i-- {
			co = append(co, Product{ItemID: "co", StandardCost: decimal.NewFromInt(rapid.Int64Range(1, 3).Draw(t, "std")), RafQty: dec("1")})
		}
		res, err := AllocateCost(cost, MethodTCA, Product{ItemID: "main", RafQty: dec("1")}, co, by)
		if err != nil {
			t.Fatalf("cost %s, %d burdens, %d co-products: %v", cost, len(by), len(co), err)
		}
		sum, pct := res.Main.Amount, res.Main.Percent
		for _, a := range append(append([]Allocated{res.Main}, res.CoProducts...), res.Burdens...) {
			if a.Amount.IsNegative() || a.Percent.IsNegative() {
				t.Fatalf("negative share %+v in %+v", a, res)
			}
		}
		for _, c := range res.CoProducts {
			sum, pct = sum.Add(c.Amount), pct.Add(c.Percent)
		}
		for _, b := range res.Burdens {
			sum = sum.Add(b.Amount)
		}
		if !sum.Equal(cost) || !res.Total.Equal(cost) || !pct.Equal(decimal.NewFromInt(100)) {
			t.Fatalf("parts %s (total %s), percent %s, cost %s", sum, res.Total, pct, cost)
		}
	})
}
