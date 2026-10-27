package store_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

func macrsBook(code, cost, inService, convention string) store.Book {
	return store.Book{BookCode: code, Currency: "USD", Method: "DecliningBalance", Factor: d("2"), SwitchToStraightLine: true, ServiceLifePeriods: 5,
		Convention: convention, InServiceDate: day(inService), Cost: d(cost), SalvageValue: d("0")}
}

// TestStore_MidQuarterTest covers BR-37.5 over stored books: a MidQuarter book
// is accepted by the database, Q4 basis over 40% of the year applies the
// convention (AST-0511), books of other years, other codes and other entities
// are ignored, a book placed in service and fully disposed of in the year is
// left out, and the books whose convention disagrees are listed for review.
func TestStore_MidQuarterTest(t *testing.T) {
	s, _, ctx := setup(t)
	le := ids.New()
	add := func(number string, b store.Book) store.Book {
		t.Helper()
		a := newAsset(t, s, ctx, le, number)
		created, err := s.AddBook(ctx, a.ID, b, "book-"+number)
		if err != nil {
			t.Fatalf("%s: %v", number, err)
		}
		return created
	}
	add("FA-1", macrsBook("TAX", "30000", "2026-02-10", "HalfYear"))
	q4 := add("FA-2", macrsBook("TAX", "50000", "2026-11-05", "HalfYear"))
	mq := add("FA-3", macrsBook("TAX", "20000", "2026-10-01", "MidQuarter"))
	if mq.Convention != "MidQuarter" {
		t.Fatalf("stored convention = %s", mq.Convention)
	}
	add("FA-4", macrsBook("TAX", "999999", "2025-12-15", "HalfYear"))  // other year
	add("FA-5", macrsBook("IFRS", "999999", "2026-12-15", "HalfYear")) // other book code
	sl := add("FA-6", store.Book{BookCode: "TAX", Currency: "USD", Method: "StraightLine", ServiceLifePeriods: 60, Convention: "None",
		InServiceDate: day("2026-05-01"), Cost: d("10000"), SalvageValue: d("0")})
	// Placed in service and fully scrapped in 2026: excluded from the basis.
	gone := add("FA-7", macrsBook("TAX", "500000", "2026-12-01", "HalfYear"))
	detail, err := s.GetAsset(ctx, gone.AssetID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Dispose(ctx, gone.AssetID, store.DisposalInput{Version: detail.Version, Kind: domain.DisposalScrap, Percentage: d("100"), Proceeds: d("0"),
		Date: day("2026-12-20")}, "scrap-FA-7"); err != nil {
		t.Fatal(err)
	}
	elsewhere := newAsset(t, s, ctx, ids.New(), "FA-X") // another legal entity
	if _, err := s.AddBook(ctx, elsewhere.ID, macrsBook("TAX", "999999", "2026-12-01", "HalfYear"), "book-FA-X"); err != nil {
		t.Fatal(err)
	}

	res, err := s.MidQuarterTest(ctx, store.MidQuarterFilter{LegalEntityID: le, BookCode: "TAX", Year: 2026})
	if err != nil {
		t.Fatal(err)
	}
	// Basis: Q1 30,000; Q2 10,000 (straight line); Q4 50,000 + 20,000 = 70,000 of 110,000.
	if res.BookCount != 4 || !res.TotalBasis.Equal(d("110000")) || !res.QuarterBasis[0].Equal(d("30000")) || !res.QuarterBasis[1].Equal(d("10000")) ||
		!res.QuarterBasis[2].IsZero() || !res.QuarterBasis[3].Equal(d("70000")) {
		t.Fatalf("result = %+v", res)
	}
	if !res.Applies || !res.Q4Share.Equal(d("0.6364")) || len(res.Warnings) != 1 || res.Warnings[0] != domain.CodeMidQuarterApplies || res.Currency == nil || *res.Currency != "USD" {
		t.Fatalf("test = applies %v share %s warnings %v currency %v", res.Applies, res.Q4Share, res.Warnings, res.Currency)
	}
	// FA-1 and FA-2 are declining balance on HalfYear; FA-3 already uses
	// MidQuarter; the straight-line FA-6 cannot take it.
	if res.ReviewCount != 2 || len(res.Review) != 2 || res.Review[0].AssetNumber != "FA-1" || res.Review[1].BookID != q4.ID || res.Review[1].Quarter != 4 {
		t.Fatalf("review = %d %+v", res.ReviewCount, res.Review)
	}
	for _, b := range res.Review {
		if b.BookID == sl.ID || b.BookID == mq.ID {
			t.Fatalf("book %s should not be reviewed", b.AssetNumber)
		}
	}

	// 2025 has a Q4-only addition, so it applies too; 2024 has nothing.
	if prior, err := s.MidQuarterTest(ctx, store.MidQuarterFilter{LegalEntityID: le, BookCode: "TAX", Year: 2025}); err != nil || !prior.Applies || prior.BookCount != 1 {
		t.Fatalf("2025 = %+v, %v", prior, err)
	}
	empty, err := s.MidQuarterTest(ctx, store.MidQuarterFilter{LegalEntityID: le, BookCode: "TAX", Year: 2024})
	if err != nil || empty.Applies || empty.BookCount != 0 || empty.Currency != nil || len(empty.Review) != 0 || len(empty.Warnings) != 0 {
		t.Fatalf("2024 = %+v, %v", empty, err)
	}

	// A MidQuarter book in a year where the test does not apply is listed.
	le2 := ids.New()
	a := newAsset(t, s, ctx, le2, "FA-M")
	if _, err := s.AddBook(ctx, a.ID, macrsBook("TAX", "1000", "2026-03-01", "MidQuarter"), "book-FA-M"); err != nil {
		t.Fatal(err)
	}
	if res, err := s.MidQuarterTest(ctx, store.MidQuarterFilter{LegalEntityID: le2, BookCode: "TAX", Year: 2026}); err != nil || res.Applies || res.ReviewCount != 1 {
		t.Fatalf("no-apply year = %+v, %v", res, err)
	}

	// Books in two currencies cannot be added up.
	eur := macrsBook("TAX", "1000", "2026-04-01", "HalfYear")
	eur.Currency = "EUR"
	b := newAsset(t, s, ctx, le2, "FA-E")
	if _, err := s.AddBook(ctx, b.ID, eur, "book-FA-E"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MidQuarterTest(ctx, store.MidQuarterFilter{LegalEntityID: le2, BookCode: "TAX", Year: 2026}); problemCode(err) != store.CodeMidQuarterCurrency {
		t.Fatalf("two currencies: err = %v", err)
	}
	if res, err := s.MidQuarterTest(scoped(2, ""), store.MidQuarterFilter{LegalEntityID: le, BookCode: "TAX", Year: 2026}); err != nil || res.BookCount != 0 {
		t.Fatalf("tenant 2 = %+v, %v", res, err)
	}
}
