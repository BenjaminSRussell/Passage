package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func codeOf(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func pcts(ss ...string) []decimal.Decimal {
	out := make([]decimal.Decimal, len(ss))
	for i, s := range ss {
		out[i] = d(s)
	}
	return out
}

func TestValidateTransfer_ShapeAndPercentages(t *testing.T) {
	tests := []struct {
		name string
		kind TransferKind
		pcts []string
		want string
	}{
		{"transfer whole", TransferKindTransfer, []string{"100"}, ""},
		{"transfer with two targets", TransferKindTransfer, []string{"50", "50"}, CodeInvalidTransfer},
		{"transfer of a part", TransferKindTransfer, []string{"50"}, CodeSplitPercentage},
		{"split 60/40", TransferKindSplit, []string{"60", "40"}, ""},
		{"split into one part", TransferKindSplit, []string{"100"}, CodeInvalidTransfer},
		{"split 60/30 short", TransferKindSplit, []string{"60", "30"}, CodeSplitPercentage},
		{"split 70/40 over", TransferKindSplit, []string{"70", "40"}, CodeSplitPercentage},
		{"split with a zero share", TransferKindSplit, []string{"100", "0"}, CodeSplitPercentage},
		{"split with a negative share", TransferKindSplit, []string{"110", "-10"}, CodeSplitPercentage},
		{"split with three decimals", TransferKindSplit, []string{"33.335", "66.665"}, CodeSplitPercentage},
		{"split 33.33 x3 short", TransferKindSplit, []string{"33.33", "33.33", "33.33"}, CodeSplitPercentage},
		{"split 33.34/33.33/33.33", TransferKindSplit, []string{"33.34", "33.33", "33.33"}, ""},
		{"unknown kind", TransferKind("merge"), []string{"100"}, CodeInvalidTransfer},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTransfer(tc.kind, pcts(tc.pcts...))
			if got := codeOf(err); got != tc.want {
				t.Fatalf("code = %q, want %q (err %v)", got, tc.want, err)
			}
		})
	}
	many := make([]string, MaxTransferParts+1)
	for i := range many {
		many[i] = "1"
	}
	if codeOf(ValidateTransfer(TransferKindSplit, pcts(many...))) != CodeInvalidTransfer {
		t.Fatal("a split into more than MaxTransferParts targets must be refused")
	}
}

func TestTransferKind_TransactionType(t *testing.T) {
	if TransferKindTransfer.TransactionType() != "Transfer" || TransferKindSplit.TransactionType() != "Split" {
		t.Fatal("transfer writes Transfer, split writes Split transactions")
	}
}

func TestCheckTransferDate(t *testing.T) {
	inService := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		date time.Time
		last string
		want string
	}{
		{"before in service", time.Date(2026, 1, 14, 0, 0, 0, 0, time.UTC), "", CodeInvalidTransfer},
		{"never depreciated", inService, "", ""},
		{"inside the last depreciated month", time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC), "2026-03", CodeBackdated},
		{"on the last depreciated month end", time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC), "2026-03", ""},
		{"inside the last depreciated year", time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC), "2026", CodeBackdated},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := codeOf(CheckTransferDate(tc.date, inService, tc.last)); got != tc.want {
				t.Fatalf("code = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAllocateShares_ResidualOnLastShare(t *testing.T) {
	// USD 100.00 with 30.00 accumulated split 33.33 / 33.33 / 33.34: the
	// rounded shares go first and the last share takes the residual cents.
	shares, err := AllocateShares(BookAmounts{Cost: d("100.00"), Accumulated: d("30.00"), Impairment: d("3.00"), Salvage: d("10.00"), AcquisitionCost: d("90.00")},
		pcts("33.33", "33.33", "33.34"), "USD")
	if err != nil {
		t.Fatal(err)
	}
	want := []BookAmounts{
		{Cost: d("33.33"), Accumulated: d("10.00"), Impairment: d("1.00"), Salvage: d("3.33"), AcquisitionCost: d("30.00")},
		{Cost: d("33.33"), Accumulated: d("10.00"), Impairment: d("1.00"), Salvage: d("3.33"), AcquisitionCost: d("30.00")},
		{Cost: d("33.34"), Accumulated: d("10.00"), Impairment: d("1.00"), Salvage: d("3.34"), AcquisitionCost: d("30.00")},
	}
	for i, s := range shares {
		w := want[i]
		if !s.Cost.Equal(w.Cost) || !s.Accumulated.Equal(w.Accumulated) || !s.Impairment.Equal(w.Impairment) ||
			!s.Salvage.Equal(w.Salvage) || !s.AcquisitionCost.Equal(w.AcquisitionCost) {
			t.Errorf("share %d = %+v, want %+v", i, s, w)
		}
	}
}

func TestAllocateShares_KeepsAccumulatedWithinCost(t *testing.T) {
	// Cost 0.05 with 0.04 accumulated split in three: rounding each share on
	// its own would give the last share 0.01 cost and 0.02 accumulated.
	shares, err := AllocateShares(BookAmounts{Cost: d("0.05"), Accumulated: d("0.04")}, pcts("33.33", "33.33", "33.34"), "USD")
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range shares {
		if s.Accumulated.GreaterThan(s.Cost) || s.Accumulated.IsNegative() {
			t.Errorf("share %d: accumulated %s outside 0..cost %s", i, s.Accumulated, s.Cost)
		}
	}
}

func TestAllocateShares_RejectsBadInput(t *testing.T) {
	if _, err := AllocateShares(BookAmounts{Cost: d("10")}, pcts("100"), "US"); codeOf(err) != CodeInvalidTransfer {
		t.Fatalf("bad currency: %v", err)
	}
	if _, err := AllocateShares(BookAmounts{Cost: d("10")}, pcts("60", "30"), "USD"); codeOf(err) != CodeSplitPercentage {
		t.Fatalf("short percentages: %v", err)
	}
	if _, err := AllocateShares(BookAmounts{Cost: d("10"), Accumulated: d("11")}, pcts("100"), "USD"); codeOf(err) != CodeInvalidTransfer {
		t.Fatalf("accumulated above cost: %v", err)
	}
}

func TestAllocateShares_JPYHasNoMinorUnit(t *testing.T) {
	shares, err := AllocateShares(BookAmounts{Cost: d("1000"), Accumulated: d("100")}, pcts("33.33", "66.67"), "JPY")
	if err != nil {
		t.Fatal(err)
	}
	if !shares[0].Cost.Equal(d("333")) || !shares[1].Cost.Equal(d("667")) {
		t.Fatalf("JPY shares = %s / %s, want 333 / 667", shares[0].Cost, shares[1].Cost)
	}
}

// TestAllocateShares_Properties: for any balances and shares, the shares sum
// back to the source exactly and every share keeps 0 <= impairment <=
// accumulated <= cost.
func TestAllocateShares_Properties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		costCents := rapid.Int64Range(0, 1_000_000_000_000).Draw(t, "costCents")
		accCents := rapid.Int64Range(0, costCents).Draw(t, "accCents")
		impCents := rapid.Int64Range(0, accCents).Draw(t, "impCents")
		salvCents := rapid.Int64Range(0, costCents).Draw(t, "salvCents")
		n := rapid.IntRange(2, 8).Draw(t, "parts")
		ps := make([]decimal.Decimal, 0, n)
		left := int64(10000)
		for i := 0; i < n-1; i++ {
			p := rapid.Int64Range(1, left-int64(n-1-i)).Draw(t, "pctBasisPoints")
			left -= p
			ps = append(ps, decimal.New(p, -2))
		}
		ps = append(ps, decimal.New(left, -2))
		src := BookAmounts{Cost: decimal.New(costCents, -2), Accumulated: decimal.New(accCents, -2), Impairment: decimal.New(impCents, -2),
			Salvage: decimal.New(salvCents, -2), AcquisitionCost: decimal.New(costCents, -2)}
		shares, err := AllocateShares(src, ps, "USD")
		if err != nil {
			t.Fatal(err)
		}
		var sum BookAmounts
		for i, s := range shares {
			if s.Impairment.IsNegative() || s.Impairment.GreaterThan(s.Accumulated) || s.Accumulated.GreaterThan(s.Cost) {
				t.Fatalf("share %d breaks 0 <= impairment <= accumulated <= cost: %+v", i, s)
			}
			sum.Cost = sum.Cost.Add(s.Cost)
			sum.Accumulated = sum.Accumulated.Add(s.Accumulated)
			sum.Impairment = sum.Impairment.Add(s.Impairment)
			sum.Salvage = sum.Salvage.Add(s.Salvage)
			sum.AcquisitionCost = sum.AcquisitionCost.Add(s.AcquisitionCost)
		}
		if !sum.Cost.Equal(src.Cost) || !sum.Accumulated.Equal(src.Accumulated) || !sum.Impairment.Equal(src.Impairment) ||
			!sum.Salvage.Equal(src.Salvage) || !sum.AcquisitionCost.Equal(src.AcquisitionCost) {
			t.Fatalf("shares sum to %+v, want %+v", sum, src)
		}
	})
}

func TestGroupBasis(t *testing.T) {
	spec := GroupSpec{LegalEntityID: "le", ClassCode: "GWG", BookCode: "DE-TAX", TaxYear: 2026}
	member := func(number string) GroupMember {
		return GroupMember{AssetNumber: number, LegalEntityID: "le", ClassCode: "GWG", BookCode: "DE-TAX", Currency: "EUR", Status: "Open",
			InService: time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC), Cost: d("600.00")}
	}
	total, cur, err := GroupBasis(spec, []GroupMember{member("A1"), member("A2")})
	if err != nil || !total.Equal(d("1200.00")) || cur != "EUR" {
		t.Fatalf("basis = %s %s, err %v", total, cur, err)
	}
	with := func(f func(*GroupMember)) GroupMember {
		m := member("B1")
		f(&m)
		return m
	}
	cases := map[string][]GroupMember{
		"no members":     nil,
		"listed twice":   {member("A1"), member("A1")},
		"other entity":   {with(func(m *GroupMember) { m.LegalEntityID = "le-2" })},
		"other class":    {with(func(m *GroupMember) { m.ClassCode = "IT" })},
		"other book":     {with(func(m *GroupMember) { m.BookCode = "IFRS" })},
		"mixed currency": {member("A1"), with(func(m *GroupMember) { m.Currency = "USD" })},
		"book sold":      {with(func(m *GroupMember) { m.Status = "Sold" })},
		"other tax year": {with(func(m *GroupMember) { m.InService = time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC) })},
	}
	for name, members := range cases {
		if _, _, err := GroupBasis(spec, members); codeOf(err) != CodeInvalidGroup {
			t.Errorf("%s: code %q, want %s", name, codeOf(err), CodeInvalidGroup)
		}
	}
}

func TestYearlySchedule_SammelpostenFY2026(t *testing.T) {
	// AC-37.10: 40 DE assets at EUR 600.00 pooled in 2026, five years straight
	// line from 1 January: FY2026 is 24,000.00 / 5 = 4,800.00.
	basis := d("600.00").Mul(decimal.NewFromInt(40))
	years, err := YearlySchedule(Input{Cost: basis, Life: 60, Method: MethodStraightLine, InService: GroupStart(2026),
		Convention: ConventionNone, Currency: "EUR"})
	if err != nil {
		t.Fatal(err)
	}
	if len(years) != 5 {
		t.Fatalf("years = %d, want 5", len(years))
	}
	for i, y := range years {
		if !y.Amount.Equal(d("4800.00")) {
			t.Errorf("FY%s = %s, want 4800.00", y.Year, y.Amount)
		}
		if want := 2026 + i; y.Year != time.Date(want, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006") {
			t.Errorf("year %d = %s, want %d", i, y.Year, want)
		}
	}
	if last := years[4]; !last.Accumulated.Equal(basis) || !last.NBV.IsZero() {
		t.Fatalf("final year accumulated %s NBV %s, want %s and 0", last.Accumulated, last.NBV, basis)
	}
}

func TestYearlySchedule_YearlyLinesPassThrough(t *testing.T) {
	// A UK-style pool on a declining-balance book keeps its yearly lines.
	years, err := YearlySchedule(Input{Cost: d("10000"), Life: 5, Method: MethodDecliningBalance, Factor: d("2"), SwitchToStraightLine: true,
		InService: GroupStart(2026), Convention: ConventionHalfYear, Currency: "GBP"})
	if err != nil {
		t.Fatal(err)
	}
	if len(years) != 6 || years[0].Year != "2026" || !years[0].Amount.Equal(d("2000.00")) {
		t.Fatalf("years = %+v", years)
	}
}

func TestYearlySchedule_InvalidProfile(t *testing.T) {
	if _, err := YearlySchedule(Input{Cost: d("1"), Life: 0, Method: MethodStraightLine, InService: GroupStart(2026), Convention: ConventionNone, Currency: "EUR"}); codeOf(err) != CodeInvalidDepreciation {
		t.Fatalf("life 0: %v", err)
	}
}
