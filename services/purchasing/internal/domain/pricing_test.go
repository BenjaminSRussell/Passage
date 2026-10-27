package domain_test

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
)

const (
	vendorA = "00000000-0000-7000-8000-00000000000a"
	vendorB = "00000000-0000-7000-8000-00000000000b"
	groupG  = "00000000-0000-7000-8000-0000000000c1"
	prodP   = "00000000-0000-7000-8000-0000000000e1"
	prodGrp = "00000000-0000-7000-8000-0000000000f1"
)

func dec(t testing.TB, s string) decimal.Decimal {
	t.Helper()
	d, err := decimal.NewFromString(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func day(t testing.TB, s string) domain.Day {
	t.Helper()
	d, err := domain.ParseDay(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// priceRow returns an active USD/EA price row valid from 2026-01-01 with no break.
func priceRow(t testing.TB, id, acct, acctID, item, itemID, price string) domain.TradeAgreement {
	return domain.TradeAgreement{ID: id, Relation: domain.RelationPrice, AccountCode: acct, AccountID: acctID,
		ItemCode: item, ItemID: itemID, Currency: "USD", Unit: "EA", FromQty: dec(t, "0"), Price: dec(t, price),
		PriceUnit: dec(t, "1"), ValidFrom: day(t, "2026-01-01"), Status: domain.StatusActive}
}

func query(t testing.TB, date string) domain.Query {
	return domain.Query{VendorID: vendorA, VendorGroupIDs: []string{groupG}, ProductID: prodP, ProductGroupIDs: []string{prodGrp},
		Qty: dec(t, "10"), Currency: "USD", Unit: "EA", Date: day(t, date)}
}

func TestResolve_MostSpecificRowWins(t *testing.T) {
	tests := []struct {
		name   string
		rows   func(testing.TB) []domain.TradeAgreement
		date   string
		winner string
	}{
		{"vendor beats group beats all (AC3 example)", func(tb testing.TB) []domain.TradeAgreement {
			return []domain.TradeAgreement{
				priceRow(tb, "all", domain.AccountAll, "", domain.ItemAll, "", "8"),
				priceRow(tb, "group", domain.AccountGroup, groupG, domain.ItemAll, "", "9"),
				priceRow(tb, "vendor", domain.AccountVendor, vendorA, domain.ItemAll, "", "10"),
			}
		}, "2026-03-01", "vendor"},
		{"equal sum of specificity (4 = 4) ties to the lowest price", func(tb testing.TB) []domain.TradeAgreement {
			// vendor/all scores 4+0 and group/product-group scores 2+2; the tie rule decides.
			return []domain.TradeAgreement{
				priceRow(tb, "vendor-all", domain.AccountVendor, vendorA, domain.ItemAll, "", "5"),
				priceRow(tb, "group-pgroup", domain.AccountGroup, groupG, domain.ItemGroup, prodGrp, "6"),
			}
		}, "2026-03-01", "vendor-all"},
		{"expired vendor row falls back to group", func(tb testing.TB) []domain.TradeAgreement {
			expired := priceRow(tb, "vendor", domain.AccountVendor, vendorA, domain.ItemAll, "", "10")
			to := day(tb, "2026-02-28")
			expired.ValidTo = &to
			return []domain.TradeAgreement{expired, priceRow(tb, "group", domain.AccountGroup, groupG, domain.ItemAll, "", "9")}
		}, "2026-03-01", "group"},
		{"quantity break above the row excludes it", func(tb testing.TB) []domain.TradeAgreement {
			broken := priceRow(tb, "vendor", domain.AccountVendor, vendorA, domain.ItemAll, "", "10")
			broken.FromQty = dec(tb, "100")
			return []domain.TradeAgreement{broken, priceRow(tb, "all", domain.AccountAll, "", domain.ItemAll, "", "8")}
		}, "2026-03-01", "all"},
		{"other vendor's row never applies", func(tb testing.TB) []domain.TradeAgreement {
			return []domain.TradeAgreement{priceRow(tb, "other", domain.AccountVendor, vendorB, domain.ItemAll, "", "1")}
		}, "2026-03-01", ""},
		{"consignment rows are ignored by PO pricing", func(tb testing.TB) []domain.TradeAgreement {
			c := priceRow(tb, "consign", domain.AccountVendor, vendorA, domain.ItemAll, "", "1")
			c.Consignment = true
			return []domain.TradeAgreement{c}
		}, "2026-03-01", ""},
		{"currency mismatch never applies", func(tb testing.TB) []domain.TradeAgreement {
			eur := priceRow(tb, "eur", domain.AccountVendor, vendorA, domain.ItemAll, "", "1")
			eur.Currency = "EUR"
			return []domain.TradeAgreement{eur}
		}, "2026-03-01", ""},
		{"date before validity finds nothing", func(tb testing.TB) []domain.TradeAgreement {
			return []domain.TradeAgreement{priceRow(tb, "vendor", domain.AccountVendor, vendorA, domain.ItemAll, "", "10")}
		}, "2025-12-31", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := domain.Resolve(tc.rows(t), query(t, tc.date))
			switch {
			case tc.winner == "" && res.Winner != nil:
				t.Fatalf("want no winner, got %s", res.Winner.ID)
			case tc.winner != "" && (res.Winner == nil || res.Winner.ID != tc.winner):
				t.Fatalf("want winner %s, got %+v", tc.winner, res.Winner)
			}
		})
	}
}

func TestResolve_ExplainListsEveryRow(t *testing.T) {
	rows := []domain.TradeAgreement{
		priceRow(t, "all", domain.AccountAll, "", domain.ItemAll, "", "8"),
		priceRow(t, "group", domain.AccountGroup, groupG, domain.ItemAll, "", "9"),
		priceRow(t, "vendor", domain.AccountVendor, vendorA, domain.ItemAll, "", "10"),
	}
	res := domain.Resolve(rows, query(t, "2026-03-01"))
	if len(res.Candidates) != 3 {
		t.Fatalf("explain has %d rows, want 3", len(res.Candidates))
	}
	want := map[string]int{"vendor": 1, "group": 2, "all": 3}
	for _, c := range res.Candidates {
		if c.Rank != want[c.AgreementID] || !c.Matched {
			t.Errorf("%s: rank %d matched %v, want rank %d", c.AgreementID, c.Rank, c.Matched, want[c.AgreementID])
		}
	}
}

func TestResolve_TieRules(t *testing.T) {
	cheapOld := priceRow(t, "cheap-old", domain.AccountVendor, vendorA, domain.ItemAll, "", "9")
	dearNew := priceRow(t, "dear-new", domain.AccountVendor, vendorA, domain.ItemAll, "", "10")
	dearNew.ValidFrom = day(t, "2026-02-01")
	rows := []domain.TradeAgreement{cheapOld, dearNew}
	tests := []struct {
		tie, winner string
	}{
		{domain.TieLowestPrice, "cheap-old"},
		{"", "dear-new"}, // FR-17.6 default: latest from_date, then lowest price
		{domain.TieLatestFrom, "dear-new"},
	}
	for _, tc := range tests {
		q := query(t, "2026-03-01")
		q.Tie = tc.tie
		if res := domain.Resolve(rows, q); res.Winner == nil || res.Winner.ID != tc.winner {
			t.Errorf("tie %q: winner %+v, want %s", tc.tie, res.Winner, tc.winner)
		}
	}
}

// TestResolve_MostSpecificValidRowAlwaysWins is the AC3 property: over random
// overlapping row sets the winner is matched and no matched row is more
// specific, and exactly one candidate holds rank 1.
func TestResolve_MostSpecificValidRowAlwaysWins(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 12).Draw(rt, "rows")
		rows := make([]domain.TradeAgreement, n)
		for i := range rows {
			rows[i] = randomRow(rt, i)
		}
		q := domain.Query{VendorID: vendorA, VendorGroupIDs: []string{groupG}, ProductID: prodP, ProductGroupIDs: []string{prodGrp},
			Qty: decimal.NewFromInt(int64(rapid.IntRange(0, 100).Draw(rt, "qty"))), Currency: "USD", Unit: "EA",
			Date: dayAt(rapid.IntRange(0, 60).Draw(rt, "date"))}
		res := domain.Resolve(rows, q)
		if len(res.Candidates) != n {
			rt.Fatalf("explain has %d rows for %d inputs", len(res.Candidates), n)
		}
		winners, maxSpec, anyMatched := 0, -1, false
		for _, c := range res.Candidates {
			if c.Rank == 1 {
				winners++
			}
			if c.Matched {
				anyMatched = true
				maxSpec = max(maxSpec, c.Specificity)
				if c.Rank < 1 {
					rt.Fatalf("matched row %s has no rank", c.AgreementID)
				}
			} else if c.Rank != 0 {
				rt.Fatalf("unmatched row %s ranked %d", c.AgreementID, c.Rank)
			}
		}
		if !anyMatched {
			if res.Winner != nil {
				rt.Fatalf("winner %s without a matched row", res.Winner.ID)
			}
			return
		}
		if winners != 1 || res.Winner == nil {
			rt.Fatalf("want exactly one winner, got %d", winners)
		}
		for _, c := range res.Candidates {
			if c.Rank == 1 && (c.AgreementID != res.Winner.ID || c.Specificity != maxSpec) {
				rt.Fatalf("winner %s spec %d, max matched spec %d", c.AgreementID, c.Specificity, maxSpec)
			}
		}
		// Default tie rule: among the most specific matched rows the winner has
		// the latest valid_from, and the lowest price among rows sharing it.
		for i, c := range res.Candidates {
			if !c.Matched || c.Specificity != maxSpec {
				continue
			}
			row := rows[i]
			if row.ValidFrom.After(res.Winner.ValidFrom.Time) {
				rt.Fatalf("row %s valid from %s beats winner %s valid from %s", row.ID, row.ValidFrom, res.Winner.ID, res.Winner.ValidFrom)
			}
			if row.ValidFrom.Equal(res.Winner.ValidFrom.Time) && row.Price.LessThan(res.Winner.Price) {
				rt.Fatalf("row %s price %s undercuts winner %s price %s on the same date", row.ID, row.Price, res.Winner.ID, res.Winner.Price)
			}
		}
	})
}

func dayAt(offset int) domain.Day {
	d, _ := domain.ParseDay("2026-01-01")
	return domain.Day{Time: d.AddDate(0, 0, offset)}
}

func randomRow(rt *rapid.T, i int) domain.TradeAgreement {
	acct := rapid.SampledFrom([]string{domain.AccountVendor, domain.AccountGroup, domain.AccountAll}).Draw(rt, "acct")
	item := rapid.SampledFrom([]string{domain.ItemProduct, domain.ItemGroup, domain.ItemAll}).Draw(rt, "item")
	acctID, itemID := "", ""
	switch acct {
	case domain.AccountVendor:
		acctID = rapid.SampledFrom([]string{vendorA, vendorB}).Draw(rt, "vendor")
	case domain.AccountGroup:
		acctID = groupG
	}
	switch item {
	case domain.ItemProduct:
		itemID = prodP
	case domain.ItemGroup:
		itemID = prodGrp
	}
	from := rapid.IntRange(0, 40).Draw(rt, "from")
	row := domain.TradeAgreement{ID: fmt.Sprintf("row-%02d", i), Relation: domain.RelationPrice, AccountCode: acct, AccountID: acctID,
		ItemCode: item, ItemID: itemID, Currency: "USD", Unit: "EA", FromQty: decimal.NewFromInt(int64(rapid.IntRange(0, 50).Draw(rt, "fromQty"))),
		Price: decimal.NewFromInt(int64(rapid.IntRange(1, 99).Draw(rt, "price"))), PriceUnit: decimal.NewFromInt(1),
		ValidFrom: dayAt(from), Status: domain.StatusActive,
		Consignment: rapid.Bool().Draw(rt, "consignment")}
	if rapid.Bool().Draw(rt, "hasTo") {
		to := dayAt(from + rapid.IntRange(0, 40).Draw(rt, "span"))
		row.ValidTo = &to
	}
	if rapid.Bool().Draw(rt, "hasBreak") {
		to := row.FromQty.Add(decimal.NewFromInt(int64(rapid.IntRange(1, 60).Draw(rt, "breakSize"))))
		row.ToQty = &to
	}
	return row
}
