package domain_test

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

type spec struct {
	name, pct string
	seq       int
	mech      string
}

func buildConfig(rounding string, specs ...spec) domain.Config {
	cfg := domain.Config{DefaultRounding: rounding, Groups: map[string][]string{"G": {}}, ItemGroups: map[string][]string{"I": {}}}
	for _, s := range specs {
		cfg.Codes = append(cfg.Codes, domain.Code{
			Code: s.name, Jurisdiction: "J-" + s.name, Mechanism: s.mech, CompoundSequence: s.seq,
			Rates: []domain.Rate{{ValidFrom: domain.Day("2020-01-01"), RatePct: domain.D(s.pct)}},
		})
		cfg.Groups["G"] = append(cfg.Groups["G"], s.name)
		cfg.ItemGroups["I"] = append(cfg.ItemGroups["I"], s.name)
	}
	return cfg
}

func buildDoc(currency, direction string, inclusive bool, amounts ...string) domain.Document {
	doc := domain.Document{
		Currency: currency, Direction: direction, DocumentDate: domain.Day("2026-03-15"),
		Party: domain.Party{TaxGroup: "G", Country: "DE", TaxID: "DE123456789"}, ShipFromCountry: "FR",
	}
	for i, a := range amounts {
		doc.Lines = append(doc.Lines, domain.Line{LineRef: fmt.Sprintf("L%d", i+1), Amount: domain.D(a), ItemTaxGroup: "I", PriceIncludesTax: inclusive})
	}
	return doc
}

func mustEqual(t *testing.T, label string, got domain.Dec, want string) {
	t.Helper()
	if !got.Equal(decimal.RequireFromString(want)) {
		t.Errorf("%s = %s, want %s", label, got, want)
	}
}

func hasWarning(res domain.Result, code string) bool {
	for _, w := range res.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func TestCalculate_Table(t *testing.T) {
	vat19 := buildConfig(domain.RoundPerDocument, spec{name: "VAT", pct: "19"})
	tests := []struct {
		name      string
		doc       domain.Document
		cfg       domain.Config
		wantErr   string
		wantBase  []string
		wantTax   []string
		wantWarns []string
	}{
		{"AC-13.2 inclusive 119.00 at 19%", buildDoc("EUR", domain.DirOutput, true, "119.00"), vat19, "", []string{"100.00"}, []string{"19.00"}, nil},
		{"AC-13.2 three inclusive lines share the document tax", buildDoc("EUR", domain.DirOutput, true, "33.33", "33.33", "33.33"), vat19, "",
			[]string{"28.01", "28.01", "28.01"}, []string{"5.32", "5.32", "5.32"}, nil},
		{"exclusive per_document largest remainder", buildDoc("EUR", domain.DirOutput, false, "0.15", "0.15", "0.15"), buildConfig(domain.RoundPerDocument, spec{name: "VAT", pct: "10"}), "",
			[]string{"0.15", "0.15", "0.15"}, []string{"0.02", "0.02", "0.01"}, nil},
		{"exclusive per_line rounds each line", buildDoc("USD", domain.DirOutput, false, "0.10", "0.10", "0.10"), buildConfig(domain.RoundPerLine, spec{name: "ST", pct: "50"}), "",
			[]string{"0.10", "0.10", "0.10"}, []string{"0.05", "0.05", "0.05"}, nil},
		{"compound stack applies on base plus earlier tax", buildDoc("CAD", domain.DirOutput, false, "100.00"),
			buildConfig(domain.RoundPerLine, spec{name: "GST", pct: "5"}, spec{name: "QST", pct: "9.975", seq: 1}), "",
			[]string{"100.00"}, []string{"5.00", "10.47"}, nil},
		{"compound inclusive round trip", buildDoc("CAD", domain.DirOutput, true, "115.47"),
			buildConfig(domain.RoundPerLine, spec{name: "GST", pct: "5"}, spec{name: "QST", pct: "9.975", seq: 1}), "",
			[]string{"100.00"}, []string{"5.00", "10.47"}, nil},
		{"credit note keeps the sign", buildDoc("EUR", domain.DirOutput, false, "-100.00"), vat19, "", []string{"-100.00"}, []string{"-19.00"}, nil},
		{"zero decimal currency", buildDoc("JPY", domain.DirOutput, false, "1000"), buildConfig(domain.RoundPerLine, spec{name: "JCT", pct: "10"}), "",
			[]string{"1000"}, []string{"100"}, nil},
		{"unknown group", func() domain.Document {
			d := buildDoc("EUR", domain.DirOutput, false, "1.00")
			d.Party.TaxGroup = "X"
			return d
		}(), vat19, domain.CodeGroupUnresolved, nil, nil, nil},
		{"groups share no code", func() domain.Document {
			d := buildDoc("EUR", domain.DirOutput, false, "1.00")
			d.Lines[0].ItemTaxGroup = "E"
			return d
		}(),
			func() domain.Config {
				c := buildConfig(domain.RoundPerDocument, spec{name: "VAT", pct: "19"})
				c.ItemGroups["E"] = []string{"OTHER"}
				return c
			}(), "",
			[]string{"1.00"}, nil, []string{domain.CodeNoIntersection}},
		{"no rate on tax date", func() domain.Document {
			d := buildDoc("EUR", domain.DirOutput, false, "1.00")
			d.TaxDate = domain.Day("2019-12-31")
			return d
		}(), vat19, domain.CodeRateNotFound, nil, nil, nil},
		{"negative rate", buildDoc("EUR", domain.DirOutput, false, "1.00"), buildConfig(domain.RoundPerDocument, spec{name: "VAT", pct: "-1"}), domain.CodeRateInvalid, nil, nil, nil},
		{"rate above 100", buildDoc("EUR", domain.DirOutput, false, "1.00"), buildConfig(domain.RoundPerDocument, spec{name: "VAT", pct: "100.5"}), domain.CodeRateInvalid, nil, nil, nil},
		{"overlapping rates", buildDoc("EUR", domain.DirOutput, false, "1.00"), func() domain.Config {
			c := buildConfig(domain.RoundPerDocument, spec{name: "VAT", pct: "19"})
			c.Codes[0].Rates = append(c.Codes[0].Rates, domain.Rate{ValidFrom: domain.Day("2025-01-01"), RatePct: domain.D("20")})
			return c
		}(), domain.CodeRateInvalid, nil, nil, nil},
		{"effective-dated rate picks the one valid on tax date", func() domain.Document {
			d := buildDoc("EUR", domain.DirOutput, false, "100.00")
			d.TaxDate = domain.Day("2020-06-01")
			return d
		}(), func() domain.Config {
			c := buildConfig(domain.RoundPerDocument, spec{name: "VAT", pct: "16"})
			c.Codes[0].Rates = []domain.Rate{
				{ValidFrom: domain.Day("2020-01-01"), ValidTo: domain.Day("2020-12-31"), RatePct: domain.D("16")},
				{ValidFrom: domain.Day("2021-01-01"), RatePct: domain.D("19")},
			}
			return c
		}(), "", []string{"100.00"}, []string{"16.00"}, nil},
		{"bracket by absolute base", buildDoc("USD", domain.DirOutput, false, "-600.00"), func() domain.Config {
			c := buildConfig(domain.RoundPerLine, spec{name: "ST", pct: "1"})
			c.Codes[0].Rates = []domain.Rate{
				{ValidFrom: domain.Day("2020-01-01"), ToAmount: &domain.Dec{Decimal: decimal.NewFromInt(500)}, RatePct: domain.D("1")},
				{ValidFrom: domain.Day("2020-01-01"), FromAmount: &domain.Dec{Decimal: decimal.NewFromInt(500)}, RatePct: domain.D("2")},
			}
			return c
		}(), "", []string{"-600.00"}, []string{"-12.00"}, nil},
		{"max cap", buildDoc("USD", domain.DirOutput, false, "1000.00"), func() domain.Config {
			c := buildConfig(domain.RoundPerLine, spec{name: "ST", pct: "10"})
			c.Codes[0].Rates[0].MaxTax = &domain.Dec{Decimal: decimal.NewFromInt(25)}
			return c
		}(), "", []string{"1000.00"}, []string{"25.00"}, nil},
		{"document basis sums lines before the bracket", buildDoc("USD", domain.DirOutput, false, "300.00", "300.00"), func() domain.Config {
			c := buildConfig(domain.RoundPerLine, spec{name: "ST", pct: "1"})
			c.Codes[0].CalcBasis = domain.BasisDocument
			c.Codes[0].Rates = []domain.Rate{
				{ValidFrom: domain.Day("2020-01-01"), ToAmount: &domain.Dec{Decimal: decimal.NewFromInt(500)}, RatePct: domain.D("1")},
				{ValidFrom: domain.Day("2020-01-01"), FromAmount: &domain.Dec{Decimal: decimal.NewFromInt(500)}, RatePct: domain.D("2")},
			}
			return c
		}(), "", []string{"300.00", "300.00"}, []string{"6.00", "6.00"}, nil},
		{"invalid currency", buildDoc("eur", domain.DirOutput, false, "1.00"), vat19, domain.CodeValidation, nil, nil, nil},
		{"invalid direction", buildDoc("EUR", "sideways", false, "1.00"), vat19, domain.CodeValidation, nil, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := domain.Calculate(tc.doc, tc.cfg)
			if tc.wantErr != "" {
				var de *domain.Error
				if !errors.As(err, &de) || de.Code != tc.wantErr {
					t.Fatalf("got %v, want error %s", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			taxIndex := 0
			for i, want := range tc.wantBase {
				mustEqual(t, "base", res.Lines[i].Base, want)
			}
			perLine := 0
			if len(tc.wantBase) > 0 {
				perLine = len(tc.wantTax) / len(tc.wantBase)
			}
			for i := range res.Lines {
				for _, x := range res.Lines[i].Taxes {
					mustEqual(t, "tax", x.TaxAmount, tc.wantTax[taxIndex])
					taxIndex++
				}
				if len(res.Lines[i].Taxes) != perLine {
					t.Errorf("line %d has %d taxes, want %d", i, len(res.Lines[i].Taxes), perLine)
				}
			}
			for _, w := range tc.wantWarns {
				if !hasWarning(res, w) {
					t.Errorf("missing warning %s in %v", w, res.Warnings)
				}
			}
			if len(res.DeterminationTrace) == 0 {
				t.Error("determination trace is empty")
			}
		})
	}
}

func TestCalculate_Mechanisms(t *testing.T) {
	rc := buildConfig(domain.RoundPerDocument, spec{name: "VAT", pct: "19", mech: domain.MechReverseCharge})
	use := buildConfig(domain.RoundPerLine, spec{name: "USE", pct: "8", mech: domain.MechUseTax})
	partial := buildConfig(domain.RoundPerLine, spec{name: "VAT", pct: "20"})
	half := domain.D("50")
	partial.Codes[0].RecoverablePct = &half
	certified := buildConfig(domain.RoundPerDocument, spec{name: "VAT", pct: "19"})
	certified.Exemptions = []domain.Exemption{{Number: "CERT-1", Codes: []string{"VAT"}, ValidFrom: domain.Day("2026-01-01"), ValidTo: domain.Day("2026-12-31")}}
	expired := certified
	expired.Exemptions = []domain.Exemption{{Number: "CERT-1", Codes: []string{"VAT"}, ValidFrom: domain.Day("2024-01-01"), ValidTo: domain.Day("2024-12-31")}}

	t.Run("reverse charge purchase emits equal output and input", func(t *testing.T) {
		res, err := domain.Calculate(buildDoc("EUR", domain.DirInput, false, "100.00"), rc)
		if err != nil {
			t.Fatal(err)
		}
		taxes := res.Lines[0].Taxes
		if len(taxes) != 2 || taxes[0].Direction != domain.DirOutput || taxes[1].Direction != domain.DirInput {
			t.Fatalf("unexpected taxes %+v", taxes)
		}
		mustEqual(t, "output", taxes[0].TaxAmount, "19.00")
		mustEqual(t, "input", taxes[1].TaxAmount, "19.00")
	})
	t.Run("reverse charge on a sale is self-assessed by the customer", func(t *testing.T) {
		res, err := domain.Calculate(buildDoc("EUR", domain.DirOutput, false, "100.00"), rc)
		if err != nil || len(res.Lines[0].Taxes) != 0 {
			t.Fatalf("got %+v, %v", res.Lines, err)
		}
	})
	t.Run("ineligible reverse charge falls back to standard with warning", func(t *testing.T) {
		doc := buildDoc("EUR", domain.DirInput, false, "100.00")
		doc.Party.TaxID = ""
		res, err := domain.Calculate(doc, rc)
		if err != nil || !hasWarning(res, domain.CodeReverseChargeIneligible) {
			t.Fatalf("got %+v, %v", res.Warnings, err)
		}
		if len(res.Lines[0].Taxes) != 1 || res.Lines[0].Taxes[0].Direction != domain.DirInput {
			t.Fatalf("unexpected taxes %+v", res.Lines[0].Taxes)
		}
	})
	t.Run("same member state is ineligible", func(t *testing.T) {
		doc := buildDoc("EUR", domain.DirInput, false, "100.00")
		doc.ShipFromCountry = doc.Party.Country
		res, _ := domain.Calculate(doc, rc)
		if !hasWarning(res, domain.CodeReverseChargeIneligible) {
			t.Fatal("expected ineligible warning")
		}
	})
	t.Run("use tax is a liability line", func(t *testing.T) {
		res, err := domain.Calculate(buildDoc("USD", domain.DirInput, false, "100.00"), use)
		if err != nil || res.Lines[0].Taxes[0].Direction != domain.DirUse {
			t.Fatalf("got %+v, %v", res.Lines, err)
		}
		mustEqual(t, "use tax", res.Lines[0].Taxes[0].TaxAmount, "8.00")
	})
	t.Run("partial recoverability splits input tax", func(t *testing.T) {
		res, err := domain.Calculate(buildDoc("EUR", domain.DirInput, false, "100.00"), partial)
		if err != nil {
			t.Fatal(err)
		}
		x := res.Lines[0].Taxes[0]
		mustEqual(t, "recoverable", x.Recoverable, "10.00")
		mustEqual(t, "nonrecoverable", x.Nonrecoverable, "10.00")
	})
	t.Run("output tax is fully recoverable by definition", func(t *testing.T) {
		res, _ := domain.Calculate(buildDoc("EUR", domain.DirOutput, false, "100.00"), partial)
		mustEqual(t, "nonrecoverable", res.Lines[0].Taxes[0].Nonrecoverable, "0")
	})
	t.Run("valid exemption certificate removes the code", func(t *testing.T) {
		doc := buildDoc("EUR", domain.DirOutput, false, "100.00")
		doc.Party.Certificate = "CERT-1"
		res, err := domain.Calculate(doc, certified)
		if err != nil || len(res.Lines[0].Taxes) != 0 {
			t.Fatalf("got %+v, %v", res.Lines, err)
		}
	})
	t.Run("expired certificate warns and taxes in full", func(t *testing.T) {
		doc := buildDoc("EUR", domain.DirOutput, false, "100.00")
		doc.Party.Certificate = "CERT-1"
		res, err := domain.Calculate(doc, expired)
		if err != nil || !hasWarning(res, domain.CodeExemptionExpired) || len(res.Lines[0].Taxes) != 1 {
			t.Fatalf("got %+v, %v", res, err)
		}
	})
	t.Run("unknown certificate is rejected", func(t *testing.T) {
		doc := buildDoc("EUR", domain.DirOutput, false, "100.00")
		doc.Party.Certificate = "NOPE"
		if _, err := domain.Calculate(doc, certified); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("document totals aggregate by code and direction", func(t *testing.T) {
		res, _ := domain.Calculate(buildDoc("EUR", domain.DirOutput, false, "10.00", "20.00"), buildConfig(domain.RoundPerDocument, spec{name: "VAT", pct: "10"}))
		if len(res.DocumentTaxesByCode) != 1 {
			t.Fatalf("got %+v", res.DocumentTaxesByCode)
		}
		mustEqual(t, "document tax", res.DocumentTaxesByCode[0].TaxAmount, "3.00")
		mustEqual(t, "document base", res.DocumentTaxesByCode[0].Base, "30.00")
	})
}

func roundRat(x *big.Rat, exp int) *big.Rat {
	scale := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exp)), nil))
	scaled := new(big.Rat).Mul(x, scale)
	abs := new(big.Rat).Abs(scaled)
	abs.Add(abs, big.NewRat(1, 2))
	floored := new(big.Int).Div(abs.Num(), abs.Denom())
	out := new(big.Rat).SetInt(floored)
	if scaled.Sign() < 0 {
		out.Neg(out)
	}
	return out.Quo(out, scale)
}

func ratDistribute(total *big.Rat, raws []*big.Rat, exp int) []*big.Rat {
	scale := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exp)), nil))
	sum := new(big.Rat)
	for _, r := range raws {
		sum.Add(sum, r)
	}
	out := make([]*big.Rat, len(raws))
	if sum.Sign() == 0 {
		for i := range out {
			out[i] = new(big.Rat)
		}
		return out
	}
	units := new(big.Rat).Mul(total, scale)
	floors := make([]*big.Int, len(raws))
	fractions := make([]*big.Rat, len(raws))
	allocated := new(big.Int)
	for i, r := range raws {
		share := new(big.Rat).Mul(units, r)
		share.Quo(share, sum)
		floors[i] = new(big.Int).Div(share.Num(), share.Denom())
		fractions[i] = share.Sub(share, new(big.Rat).SetInt(floors[i]))
		allocated.Add(allocated, floors[i])
	}
	left := new(big.Int).Sub(units.Num(), allocated).Int64()
	order := make([]int, len(raws))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return fractions[order[a]].Cmp(fractions[order[b]]) > 0 })
	for k := int64(0); k < left && k < int64(len(order)); k++ {
		floors[order[k]].Add(floors[order[k]], big.NewInt(1))
	}
	for i := range out {
		out[i] = new(big.Rat).Quo(new(big.Rat).SetInt(floors[i]), scale)
	}
	return out
}

type expected struct {
	bases []*big.Rat
	taxes [][]*big.Rat
}

func oracle(amounts []*big.Rat, inclusive, charged, perDocument bool, specs []spec, exp int) expected {
	one := big.NewRat(1, 1)
	pct := func(s spec) *big.Rat {
		r, _ := new(big.Rat).SetString(s.pct)
		return r.Quo(r, big.NewRat(100, 1))
	}
	raw := make([][]*big.Rat, len(amounts))
	for i, gross := range amounts {
		factor := new(big.Rat).Set(one)
		if inclusive && charged {
			parallel, compound := new(big.Rat), new(big.Rat).Set(one)
			for _, s := range specs {
				if s.seq == 0 {
					parallel.Add(parallel, pct(s))
				} else {
					compound.Mul(compound, new(big.Rat).Add(one, pct(s)))
				}
			}
			factor.Mul(new(big.Rat).Add(one, parallel), compound)
		}
		base := new(big.Rat).Quo(gross, factor)
		acc := new(big.Rat)
		for _, s := range specs {
			onto := base
			if s.seq > 0 {
				onto = new(big.Rat).Add(base, acc)
			}
			tax := new(big.Rat).Mul(onto, pct(s))
			raw[i] = append(raw[i], tax)
			acc.Add(acc, tax)
		}
	}
	want := expected{taxes: make([][]*big.Rat, len(amounts))}
	for i := range amounts {
		want.taxes[i] = make([]*big.Rat, len(specs))
	}
	for j := range specs {
		column := make([]*big.Rat, len(amounts))
		total := new(big.Rat)
		for i := range amounts {
			column[i] = raw[i][j]
			total.Add(total, raw[i][j])
		}
		if perDocument {
			for i, v := range ratDistribute(roundRat(total, exp), column, exp) {
				want.taxes[i][j] = v
			}
			continue
		}
		for i := range amounts {
			want.taxes[i][j] = roundRat(column[i], exp)
		}
	}
	for i, gross := range amounts {
		base := roundRat(gross, exp)
		if inclusive && charged {
			for _, v := range want.taxes[i] {
				base = new(big.Rat).Sub(base, v)
			}
		}
		want.bases = append(want.bases, base)
	}
	return want
}

func ratsOf(t *testing.T, amounts []string) []*big.Rat {
	t.Helper()
	out := make([]*big.Rat, len(amounts))
	for i, a := range amounts {
		r, ok := new(big.Rat).SetString(a)
		if !ok {
			t.Fatalf("bad amount %q", a)
		}
		out[i] = r
	}
	return out
}

type scenarioSet struct {
	region     string
	currency   string
	exp        int
	stacks     [][]spec
	amountSets [][]string
	roundings  []string
	modes      []string
	wantCount  int
}

func TestMatrix500(t *testing.T) {
	sets := []scenarioSet{
		{
			region: "US", currency: "USD", exp: 2, wantCount: 200,
			stacks: [][]spec{
				{{name: "ST", pct: "6.25"}},
				{{name: "ST", pct: "4"}, {name: "CO", pct: "1.5"}},
				{{name: "ST", pct: "6.25"}, {name: "CO", pct: "1.5"}, {name: "CI", pct: "0.5"}},
				{{name: "ST", pct: "7"}, {name: "CO", pct: "2.25"}, {name: "CI", pct: "0.875"}},
				{{name: "ST", pct: "2.9"}, {name: "CO", pct: "0.1"}},
			},
			amountSets: [][]string{{"100.00"}, {"19.99"}, {"0.01"}, {"1234.56"}, {"33.33", "33.33", "33.33"}, {"9.99", "9.99"},
				{"0.50", "0.50", "0.50", "0.50"}, {"-75.25"}, {"49.95", "50.05", "12.34"}, {"1000000.00"}},
			roundings: []string{domain.RoundPerLine, domain.RoundPerDocument},
			modes:     []string{"domestic"},
		},
		{
			region: "EU", currency: "EUR", exp: 2, wantCount: 180,
			stacks: [][]spec{
				{{name: "VAT", pct: "19"}}, {{name: "VAT", pct: "7"}}, {{name: "VAT", pct: "20"}},
				{{name: "VAT", pct: "5.5"}}, {{name: "VAT", pct: "21"}}, {{name: "VAT", pct: "23"}, {name: "LOC", pct: "0.5"}},
			},
			amountSets: [][]string{{"119.00"}, {"33.33", "33.33", "33.33"}, {"0.99", "14.99"}, {"250.00", "-40.00"}, {"8.88"}},
			roundings:  []string{domain.RoundPerDocument},
			modes:      []string{"domestic", "reverse_charge", "exempt"},
		},
		{
			region: "IN", currency: "INR", exp: 2, wantCount: 70,
			stacks: [][]spec{
				{{name: "CGST", pct: "9"}, {name: "SGST", pct: "9"}}, {{name: "IGST", pct: "18"}}, {{name: "GST", pct: "5"}},
				{{name: "GST", pct: "12"}}, {{name: "GST", pct: "28"}, {name: "CESS", pct: "12", seq: 1}},
				{{name: "CGST", pct: "2.5"}, {name: "SGST", pct: "2.5"}}, {{name: "GST", pct: "18"}, {name: "CESS", pct: "1", seq: 1}},
			},
			amountSets: [][]string{{"1180.00"}, {"999.99", "0.01"}, {"500.00", "250.00", "125.00"}, {"12345.67"}, {"3.33", "3.33", "3.33"}},
			roundings:  []string{domain.RoundPerLine},
			modes:      []string{"domestic"},
		},
		{
			region: "CA", currency: "CAD", exp: 2, wantCount: 50,
			stacks: [][]spec{
				{{name: "GST", pct: "5"}}, {{name: "HST", pct: "13"}}, {{name: "GST", pct: "5"}, {name: "PST", pct: "7"}},
				{{name: "GST", pct: "5"}, {name: "QST", pct: "9.975", seq: 1}}, {{name: "HST", pct: "15"}},
			},
			amountSets: [][]string{{"100.00"}, {"29.99", "29.99"}, {"0.10", "0.20", "0.30"}, {"4567.89"}, {"59.97"}},
			roundings:  []string{domain.RoundPerDocument},
			modes:      []string{"domestic"},
		},
	}
	total := 0
	for _, set := range sets {
		count := 0
		for si, stack := range set.stacks {
			for ai, amounts := range set.amountSets {
				for _, inclusive := range []bool{false, true} {
					for _, rounding := range set.roundings {
						for _, mode := range set.modes {
							count++
							name := fmt.Sprintf("%s/stack%d/amounts%d/incl=%v/%s/%s", set.region, si, ai, inclusive, rounding, mode)
							t.Run(name, func(t *testing.T) {
								runScenario(t, set, stack, amounts, inclusive, rounding, mode)
							})
						}
					}
				}
			}
		}
		if count != set.wantCount {
			t.Fatalf("%s grid has %d scenarios, want %d", set.region, count, set.wantCount)
		}
		total += count
	}
	if total != 500 {
		t.Fatalf("matrix has %d scenarios, want 500", total)
	}
}

func runScenario(t *testing.T, set scenarioSet, stack []spec, amounts []string, inclusive bool, rounding, mode string) {
	t.Helper()
	specs := append([]spec(nil), stack...)
	direction := domain.DirOutput
	if mode == "reverse_charge" {
		direction = domain.DirInput
		for i := range specs {
			specs[i].mech = domain.MechReverseCharge
		}
	}
	cfg := buildConfig(rounding, specs...)
	doc := buildDoc(set.currency, direction, inclusive, amounts...)
	if mode == "exempt" {
		doc.Party.Certificate = "CERT"
		names := make([]string, len(specs))
		for i, s := range specs {
			names[i] = s.name
		}
		cfg.Exemptions = []domain.Exemption{{Number: "CERT", Codes: names, ValidFrom: domain.Day("2026-01-01")}}
	}
	res, err := domain.Calculate(doc, cfg)
	if err != nil {
		t.Fatal(err)
	}
	charged := mode == "domestic"
	want := oracle(ratsOf(t, amounts), inclusive, charged, rounding == domain.RoundPerDocument, specs, set.exp)
	if mode == "exempt" {
		want = oracle(ratsOf(t, amounts), false, false, false, nil, set.exp)
	}
	for i, line := range res.Lines {
		mustEqual(t, "base", line.Base, want.bases[i].FloatString(set.exp))
		wantTaxes := want.taxes[i]
		if mode == "reverse_charge" {
			if len(line.Taxes) != 2*len(wantTaxes) {
				t.Fatalf("line %d has %d taxes, want %d", i, len(line.Taxes), 2*len(wantTaxes))
			}
			for j, w := range wantTaxes {
				mustEqual(t, "output leg", line.Taxes[2*j].TaxAmount, w.FloatString(set.exp))
				mustEqual(t, "input leg", line.Taxes[2*j+1].TaxAmount, w.FloatString(set.exp))
			}
			continue
		}
		if len(line.Taxes) != len(wantTaxes) {
			t.Fatalf("line %d has %d taxes, want %d", i, len(line.Taxes), len(wantTaxes))
		}
		for j, w := range wantTaxes {
			mustEqual(t, "tax "+line.Taxes[j].Code, line.Taxes[j].TaxAmount, w.FloatString(set.exp))
		}
	}
}

func TestInclusive_RoundTrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 5).Draw(rt, "lines")
		amounts := make([]string, n)
		for i := range amounts {
			amounts[i] = decimal.New(rapid.Int64Range(1, 5_000_000).Draw(rt, "gross"), -2).StringFixed(2)
		}
		rate := decimal.New(rapid.Int64Range(1, 3_000).Draw(rt, "ratebp"), -2).StringFixed(2)
		rounding := rapid.SampledFrom([]string{domain.RoundPerLine, domain.RoundPerDocument}).Draw(rt, "rounding")
		cfg := buildConfig(rounding, spec{name: "A", pct: rate}, spec{name: "B", pct: "2.5", seq: 1})
		res, err := domain.Calculate(buildDoc("EUR", domain.DirOutput, true, amounts...), cfg)
		if err != nil {
			rt.Fatal(err)
		}
		for i, line := range res.Lines {
			sum := line.Base.Decimal
			for _, x := range line.Taxes {
				sum = sum.Add(x.TaxAmount.Decimal)
			}
			if !sum.Equal(decimal.RequireFromString(amounts[i])) {
				rt.Fatalf("line %d: base+taxes = %s, want gross %s", i, sum, amounts[i])
			}
		}
	})
}

func TestRounding_Distribution(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(rt, "lines")
		amounts := make([]string, n)
		sum := decimal.Zero
		for i := range amounts {
			v := decimal.New(rapid.Int64Range(-1_000_000, 1_000_000).Draw(rt, "amount"), -2)
			amounts[i] = v.StringFixed(2)
			sum = sum.Add(v)
		}
		rate := decimal.New(rapid.Int64Range(1, 2_500).Draw(rt, "ratebp"), -2)
		cfg := buildConfig(domain.RoundPerDocument, spec{name: "VAT", pct: rate.StringFixed(2)})
		res, err := domain.Calculate(buildDoc("EUR", domain.DirOutput, false, amounts...), cfg)
		if err != nil {
			rt.Fatal(err)
		}
		got := decimal.Zero
		for _, line := range res.Lines {
			got = got.Add(line.Taxes[0].TaxAmount.Decimal)
		}
		want := sum.Mul(rate).Div(decimal.NewFromInt(100)).Round(2)
		if !got.Equal(want) {
			rt.Fatalf("line taxes sum to %s, want round(sum of unrounded) %s", got, want)
		}
	})
}
