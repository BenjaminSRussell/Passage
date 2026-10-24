package domain

import (
	"errors"
	"math/rand/v2"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func res(factors ...string) Resolution {
	out := Resolution{Path: PathDirect}
	for _, f := range factors {
		out.Factors = append(out.Factors, dec(f))
	}
	return out
}

func eurToUSD(rate string) RateSet {
	return RateSet{
		Acct: Target{Currency: "USD", Resolutions: map[string]Resolution{"EUR": res(rate)}},
		Rpt:  Target{Currency: "EUR", Resolutions: map[string]Resolution{}},
	}
}

func TestConvertSameCurrencyAppliesNoRate(t *testing.T) {
	a := money.Must("123.45", "USD")
	got, err := Convert(a, "USD", res("9.99"))
	if err != nil || got.Currency != a.Currency || !got.Value.Equal(a.Value) {
		t.Fatalf("got %v err %v, want %v unchanged", got, err, a)
	}
}

func TestConvertMissingResolution(t *testing.T) {
	if _, err := Convert(money.Must("1", "EUR"), "USD", Resolution{}); !errors.Is(err, ErrRateMissing) {
		t.Fatalf("err = %v", err)
	}
}

func TestTriangulationRoundsOnce(t *testing.T) {
	b := testBook(t)
	for _, amt := range []string{"1000.00", "0.07", "1234567.89", "-999.99"} {
		a := money.Must(amt, "GBP")
		r, err := b.Resolve("SPOT", "GBP", "CHF", date("2026-03-15"), "EUR")
		if err != nil || r.Path != PathTriangulated {
			t.Fatalf("resolve: %v %v", r.Path, err)
		}
		got, err := Convert(a, "CHF", r)
		if err != nil {
			t.Fatal(err)
		}
		want := money.Amount{Value: a.Value, Currency: "CHF"}.Mul(dec("1.17").Mul(dec("0.95")))
		if got.Currency != want.Currency || !got.Value.Equal(want.Value) {
			t.Fatalf("%s: got %v want %v", amt, got, want)
		}
	}
}

func TestConvertHalfEvenVersusHalfAway(t *testing.T) {
	a := money.Amount{Value: dec("2.5"), Currency: "USD"}
	away, _ := convert(a, "JPY", res("1"), HalfAwayFromZero)
	even, _ := convert(a, "JPY", res("1"), HalfEven)
	if !away.Value.Equal(dec("3")) || !even.Value.Equal(dec("2")) {
		t.Fatalf("away %v even %v", away, even)
	}
}

func TestConvertEntryResidualOnLargestSide(t *testing.T) {
	lines := []Line{
		{1, money.Must("33.33", "EUR")},
		{2, money.Must("33.33", "EUR")},
		{3, money.Must("-66.66", "EUR")},
	}
	out, err := ConvertEntry(lines, eurToUSD("1.0850000000"), HalfAwayFromZero)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"36.16", "36.16", "-72.32"}
	sum := decimal.Zero
	adjusted := 0
	for i, l := range out {
		if !l.Acct.Value.Equal(dec(want[i])) {
			t.Fatalf("line %d acct %v want %s", l.No, l.Acct, want[i])
		}
		if l.AcctAdjusted {
			adjusted++
		}
		sum = sum.Add(l.Acct.Value)
		if l.Rpt.Currency != l.Txn.Currency || !l.Rpt.Value.Equal(l.Txn.Value) {
			t.Fatalf("reporting equals transaction currency, got %v for %v", l.Rpt, l.Txn)
		}
	}
	if !sum.IsZero() || adjusted != 1 || !out[2].AcctAdjusted {
		t.Fatalf("sum %s adjusted %d", sum, adjusted)
	}
}

func TestConvertEntryTieGoesToLowestLineNumber(t *testing.T) {
	lines := []Line{
		{7, money.Must("33.33", "EUR")},
		{3, money.Must("33.33", "EUR")},
		{9, money.Must("-66.66", "EUR")},
		{4, money.Must("0", "EUR")},
	}
	rounded := []decimal.Decimal{dec("36.17"), dec("36.17"), dec("-72.32"), dec("0")}
	_, adjusted, err := allocateResidual(lines, rounded, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if !adjusted[1] || adjusted[0] || adjusted[2] {
		t.Fatalf("adjusted = %v, want only line number 3", adjusted)
	}
}

func TestConvertEntryFailures(t *testing.T) {
	cases := []struct {
		name  string
		lines []Line
		rates RateSet
		err   error
	}{
		{"empty", nil, eurToUSD("1.1"), ErrUnbalanced},
		{"transaction column off", []Line{{1, money.Must("10", "EUR")}, {2, money.Must("-9", "EUR")}}, eurToUSD("1.1"), ErrUnbalanced},
		{"balanced per currency only", []Line{{1, money.Must("10", "EUR")}, {2, money.Must("-10", "USD")}}, eurToUSD("1.1"), ErrUnbalanced},
		{"missing currency", []Line{{1, money.Amount{Value: dec("1")}}, {2, money.Amount{Value: dec("-1")}}}, eurToUSD("1.1"), money.ErrCurrencyMismatch},
		{"no resolution", []Line{{1, money.Must("10", "GBP")}, {2, money.Must("-10", "GBP")}}, eurToUSD("1.1"), ErrRateMissing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ConvertEntry(c.lines, c.rates, HalfAwayFromZero); !errors.Is(err, c.err) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
		})
	}
}

func TestAllocateResidualBeyondTolerance(t *testing.T) {
	lines := []Line{{1, money.Must("1", "EUR")}, {2, money.Must("-1", "EUR")}}
	rounded := []decimal.Decimal{dec("1.00"), dec("1.00")}
	if _, _, err := allocateResidual(lines, rounded, "USD"); !errors.Is(err, ErrRoundingExceeded) {
		t.Fatalf("err = %v", err)
	}
}

type fixtureRates struct {
	acct, rpt map[string]string
}

var tenKRates = fixtureRates{
	acct: map[string]string{"EUR": "1.0850000000", "JPY": "0.0066700000", "KWD": "3.2500000000", "USD": "1"},
	rpt:  map[string]string{"EUR": "1", "JPY": "0.0061000000", "KWD": "2.9954000000", "USD": "0.9216589862"},
}

func fixtureRateSet() RateSet {
	rs := RateSet{
		Acct: Target{Currency: "USD", Resolutions: map[string]Resolution{}},
		Rpt:  Target{Currency: "EUR", Resolutions: map[string]Resolution{}},
	}
	for c, r := range tenKRates.acct {
		rs.Acct.Resolutions[c] = res(r)
	}
	for c, r := range tenKRates.rpt {
		rs.Rpt.Resolutions[c] = res(r)
	}
	return rs
}

type failer interface {
	Helper()
	Fatalf(string, ...any)
}

func assertColumnsReconcile(t failer, out []ConvertedLine) {
	t.Helper()
	acct, rpt := decimal.Zero, decimal.Zero
	for _, l := range out {
		if l.Acct.Value.Exponent() < -money.MinorUnits("USD") || l.Rpt.Value.Exponent() < -money.MinorUnits("EUR") {
			t.Fatalf("line %d exceeds minor units: %v %v", l.No, l.Acct, l.Rpt)
		}
		acct, rpt = acct.Add(l.Acct.Value), rpt.Add(l.Rpt.Value)
	}
	if !acct.IsZero() || !rpt.IsZero() {
		t.Fatalf("columns do not reconcile: acct %s rpt %s", acct, rpt)
	}
}

func TestTenThousandSeededEntriesReconcile(t *testing.T) {
	rng := rand.New(rand.NewPCG(12, 2026))
	currencies := []string{"JPY", "KWD", "USD", "EUR"}
	rs := fixtureRateSet()
	extremes := []string{"0.001", "9999999999.999"}
	for i := 0; i < 10000; i++ {
		cur := currencies[rng.IntN(len(currencies))]
		draw := func() decimal.Decimal {
			return money.Amount{Value: decimal.New(rng.Int64N(9_999_999_999_999)+1, -3), Currency: cur}.Round().Value
		}
		a, b := draw(), draw()
		if i < len(extremes) {
			a = dec(extremes[i])
		}
		a = money.Amount{Value: a, Currency: cur}.Round().Value
		lines := []Line{
			{1, money.Amount{Value: a, Currency: cur}},
			{2, money.Amount{Value: b, Currency: cur}},
			{3, money.Amount{Value: a.Add(b).Neg(), Currency: cur}},
		}
		if i%3 == 0 {
			other := currencies[(rng.IntN(3)+1+indexOf(currencies, cur))%len(currencies)]
			c := money.Amount{Value: decimal.New(rng.Int64N(1_000_000)+1, -3), Currency: other}.Round()
			lines = append(lines, Line{4, c}, Line{5, c.Neg()})
		}
		out, err := ConvertEntry(lines, rs, HalfAwayFromZero)
		if err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
		assertColumnsReconcile(t, out)
	}
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

func TestConvertEntryPropertyBalances(t *testing.T) {
	rs := fixtureRateSet()
	rapid.Check(t, func(t *rapid.T) {
		a := rapid.Int64Range(-999_999_999_999, 999_999_999_999).Draw(t, "a")
		b := rapid.Int64Range(-999_999_999_999, 999_999_999_999).Draw(t, "b")
		rateTicks := rapid.Int64Range(1, 100_000_000_000).Draw(t, "rate")
		rs.Acct.Resolutions["EUR"] = Resolution{Path: PathDirect, Factors: []decimal.Decimal{decimal.New(rateTicks, -10)}}
		lines := []Line{
			{1, money.Amount{Value: decimal.New(a, -2), Currency: "EUR"}},
			{2, money.Amount{Value: decimal.New(b, -2), Currency: "EUR"}},
			{3, money.Amount{Value: decimal.New(-(a + b), -2), Currency: "EUR"}},
		}
		out, err := ConvertEntry(lines, rs, HalfAwayFromZero)
		if err != nil {
			t.Fatal(err)
		}
		assertColumnsReconcile(t, out)
	})
}
