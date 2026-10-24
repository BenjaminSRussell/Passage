package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func date(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func testBook(t *testing.T) *Book {
	t.Helper()
	b := NewBook(date("2026-03-31"), RateType{Code: "SPOT", AllowInverse: true}, RateType{Code: "FIXED"})
	rows := []Rate{
		{ID: "r1", Type: "SPOT", From: "EUR", To: "USD", ValidFrom: date("2026-03-01"), Value: dec("1.0850000000")},
		{ID: "r2", Type: "SPOT", From: "GBP", To: "EUR", ValidFrom: date("2026-03-01"), Value: dec("1.1700000000")},
		{ID: "r3", Type: "SPOT", From: "EUR", To: "CHF", ValidFrom: date("2026-03-01"), Value: dec("0.9500000000")},
		{ID: "r4", Type: "FIXED", From: "EUR", To: "USD", ValidFrom: date("2026-03-01"), Value: dec("1.1000000000")},
	}
	for _, r := range rows {
		if err := b.Add(r); err != nil {
			t.Fatalf("add %s: %v", r.ID, err)
		}
	}
	return b
}

func TestResolve(t *testing.T) {
	b := testBook(t)
	cases := []struct {
		name     string
		typ      string
		from, to string
		on       string
		ref      string
		path     Path
		product  string
		err      error
	}{
		{"direct", "SPOT", "EUR", "USD", "2026-03-15", "", PathDirect, "1.085", nil},
		{"inverse allowed", "SPOT", "USD", "EUR", "2026-03-15", "", PathInverse, "0.9216589861751152", nil},
		{"inverse disallowed", "FIXED", "USD", "EUR", "2026-03-15", "", "", "", ErrRateMissing},
		{"triangulated", "SPOT", "GBP", "CHF", "2026-03-15", "EUR", PathTriangulated, "1.1115", nil},
		{"triangulation needs reference", "SPOT", "GBP", "CHF", "2026-03-15", "", "", "", ErrRateMissing},
		{"missing pair", "SPOT", "JPY", "USD", "2026-03-15", "EUR", "", "", ErrRateMissing},
		{"before first valid from", "SPOT", "EUR", "USD", "2026-02-28", "", "", "", ErrRateMissing},
		{"future date", "SPOT", "EUR", "USD", "2026-04-01", "", "", "", ErrFutureRate},
		{"unknown type", "NOPE", "EUR", "USD", "2026-03-15", "", "", "", ErrRateMissing},
		{"same currency", "SPOT", "USD", "USD", "2026-03-15", "", PathDirect, "1", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := b.Resolve(c.typ, c.from, c.to, date(c.on), c.ref)
			if !errors.Is(err, c.err) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			if c.err != nil {
				return
			}
			product := decimal.NewFromInt(1)
			for _, f := range res.Factors {
				product = product.Mul(f)
			}
			if res.Path != c.path || !product.Equal(dec(c.product)) {
				t.Fatalf("got path %s product %s, want %s %s", res.Path, product, c.path, c.product)
			}
		})
	}
}

func TestSupersededRateIgnored(t *testing.T) {
	b := testBook(t)
	if err := b.Add(Rate{ID: "r5", Type: "SPOT", From: "EUR", To: "USD", ValidFrom: date("2026-03-01"), Value: dec("1.0900000000")}); err != nil {
		t.Fatal(err)
	}
	res, err := b.Resolve("SPOT", "EUR", "USD", date("2026-03-20"), "")
	if err != nil || !res.Factors[0].Equal(dec("1.09")) {
		t.Fatalf("res = %+v err = %v, want successor 1.09", res, err)
	}
	if b.rates[0].SupersededBy != "r5" {
		t.Fatalf("original row not flagged superseded: %+v", b.rates[0])
	}
	if len(b.rates) != 5 {
		t.Fatalf("book is append-only, got %d rows", len(b.rates))
	}
}

func TestLaterRateWinsOnItsDate(t *testing.T) {
	b := testBook(t)
	if err := b.Add(Rate{ID: "r6", Type: "SPOT", From: "EUR", To: "USD", ValidFrom: date("2026-03-20"), Value: dec("1.2")}); err != nil {
		t.Fatal(err)
	}
	early, _ := b.Resolve("SPOT", "EUR", "USD", date("2026-03-19"), "")
	late, _ := b.Resolve("SPOT", "EUR", "USD", date("2026-03-20"), "")
	if !early.Factors[0].Equal(dec("1.085")) || !late.Factors[0].Equal(dec("1.2")) {
		t.Fatalf("early %s late %s", early.Factors[0], late.Factors[0])
	}
}

func TestAddRejects(t *testing.T) {
	cases := []struct {
		name string
		rate Rate
		err  error
	}{
		{"zero value rate", Rate{}, ErrInvalidRate},
		{"zero rate", Rate{Type: "SPOT", From: "EUR", To: "USD", ValidFrom: date("2026-03-01"), Value: decimal.Zero}, ErrInvalidRate},
		{"negative rate", Rate{Type: "SPOT", From: "EUR", To: "USD", ValidFrom: date("2026-03-01"), Value: dec("-1")}, ErrInvalidRate},
		{"same pair", Rate{Type: "SPOT", From: "EUR", To: "EUR", ValidFrom: date("2026-03-01"), Value: dec("1")}, ErrInvalidRate},
		{"empty currency", Rate{Type: "SPOT", From: "", To: "USD", ValidFrom: date("2026-03-01"), Value: dec("1")}, ErrInvalidRate},
		{"short currency", Rate{Type: "SPOT", From: "EU", To: "USD", ValidFrom: date("2026-03-01"), Value: dec("1")}, ErrInvalidRate},
		{"lowercase currency", Rate{Type: "SPOT", From: "eur", To: "USD", ValidFrom: date("2026-03-01"), Value: dec("1")}, ErrInvalidRate},
		{"unknown type", Rate{Type: "NOPE", From: "EUR", To: "USD", ValidFrom: date("2026-03-01"), Value: dec("1")}, ErrInvalidRate},
		{"future valid from", Rate{Type: "SPOT", From: "EUR", To: "USD", ValidFrom: date("2026-04-01"), Value: dec("1")}, ErrFutureRate},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := NewBook(date("2026-03-31"), RateType{Code: "SPOT"})
			if err := b.Add(c.rate); !errors.Is(err, c.err) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			if len(b.rates) != 0 {
				t.Fatalf("rejected row was added")
			}
		})
	}
}

func TestResolveEmptyBook(t *testing.T) {
	b := NewBook(date("2026-03-31"), RateType{Code: "SPOT", AllowInverse: true})
	if _, err := b.Resolve("SPOT", "EUR", "USD", date("2026-03-15"), "EUR"); !errors.Is(err, ErrRateMissing) {
		t.Fatalf("err = %v", err)
	}
}
