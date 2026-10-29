package domain_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

var asOf = time.Date(2027, 6, 30, 0, 0, 0, 0, time.UTC)

func amount(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestBucketFor_Boundaries(t *testing.T) {
	tests := []struct {
		daysOverdue int
		want        domain.Bucket
	}{
		{-5, domain.BucketCurrent}, {0, domain.BucketCurrent}, {1, domain.Bucket1to30}, {30, domain.Bucket1to30},
		{31, domain.Bucket31to60}, {60, domain.Bucket31to60}, {61, domain.Bucket61to90}, {90, domain.Bucket61to90},
		{91, domain.Bucket91to180}, {180, domain.Bucket91to180}, {181, domain.BucketOver180},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprint(tc.daysOverdue), func(t *testing.T) {
			if got := domain.BucketFor(asOf, asOf.AddDate(0, 0, -tc.daysOverdue)); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestAge_ByVendorAndCurrency(t *testing.T) {
	rows := []domain.AgingRow{
		{VendorID: "v1", Currency: "USD", DueDate: asOf.AddDate(0, 0, 10), OpenAmount: amount("100.00")},
		{VendorID: "v1", Currency: "USD", DueDate: asOf.AddDate(0, 0, -5), OpenAmount: amount("50.50")},
		{VendorID: "v1", Currency: "EUR", DueDate: asOf.AddDate(0, 0, -5), OpenAmount: amount("7.00")},
		{VendorID: "v2", Currency: "USD", DueDate: asOf.AddDate(0, 0, -5), OpenAmount: amount("25.00")},
		{VendorID: "v2", Currency: "USD", DueDate: asOf.AddDate(0, 0, -400), OpenAmount: decimal.Zero},
	}
	byVendor := domain.Age(asOf, rows, true)
	want := []domain.AgingTotal{
		{VendorID: "v1", Currency: "EUR", Bucket: domain.Bucket1to30, Amount: amount("7.00")},
		{VendorID: "v1", Currency: "USD", Bucket: domain.BucketCurrent, Amount: amount("100.00")},
		{VendorID: "v1", Currency: "USD", Bucket: domain.Bucket1to30, Amount: amount("50.50")},
		{VendorID: "v2", Currency: "USD", Bucket: domain.Bucket1to30, Amount: amount("25.00")},
	}
	assertTotals(t, byVendor, want)
	assertTotals(t, domain.Age(asOf, rows, false), []domain.AgingTotal{
		{Currency: "EUR", Bucket: domain.Bucket1to30, Amount: amount("7.00")},
		{Currency: "USD", Bucket: domain.BucketCurrent, Amount: amount("100.00")},
		{Currency: "USD", Bucket: domain.Bucket1to30, Amount: amount("75.50")},
	})
}

func assertTotals(t *testing.T, got, want []domain.AgingTotal) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].VendorID != want[i].VendorID || got[i].Currency != want[i].Currency || got[i].Bucket != want[i].Bucket || !got[i].Amount.Equal(want[i].Amount) {
			t.Fatalf("row %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func sum(rows []domain.AgingRow, currency string) decimal.Decimal {
	total := decimal.Zero
	for _, r := range rows {
		if r.Currency == currency && r.OpenAmount.IsPositive() {
			total = total.Add(r.OpenAmount)
		}
	}
	return total
}

func totalOf(totals []domain.AgingTotal, currency string) decimal.Decimal {
	total := decimal.Zero
	for _, t := range totals {
		if t.Currency == currency {
			total = total.Add(t.Amount)
		}
	}
	return total
}

func TestAge_BucketTotalsEqualOpenItems_Property(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		rows := rapid.SliceOfN(rapid.Custom(func(rt *rapid.T) domain.AgingRow {
			return domain.AgingRow{
				VendorID:   rapid.SampledFrom([]string{"v1", "v2", "v3"}).Draw(rt, "vendor"),
				Currency:   rapid.SampledFrom([]string{"USD", "EUR", "GBP"}).Draw(rt, "currency"),
				DueDate:    asOf.AddDate(0, 0, rapid.IntRange(-400, 120).Draw(rt, "offset")),
				OpenAmount: decimal.New(rapid.Int64Range(0, 10_000_000).Draw(rt, "cents"), -2),
			}
		}), 0, 200).Draw(rt, "rows")
		for _, byVendor := range []bool{false, true} {
			totals := domain.Age(asOf, rows, byVendor)
			for _, c := range []string{"USD", "EUR", "GBP"} {
				if got, want := totalOf(totals, c), sum(rows, c); !got.Equal(want) {
					rt.Fatalf("byVendor=%v %s: buckets sum %s, open items sum %s", byVendor, c, got, want)
				}
			}
		}
	})
}

func generated1000() []domain.AgingRow {
	rows := make([]domain.AgingRow, 0, 1000)
	for i := range 1000 {
		rows = append(rows, domain.AgingRow{
			VendorID:   fmt.Sprintf("v%02d", i%17),
			Currency:   []string{"USD", "EUR"}[i%2],
			DueDate:    asOf.AddDate(0, 0, 40-(i*7)%260),
			OpenAmount: decimal.New(int64(1000+i*37%90000), -2),
		})
	}
	return rows
}

func TestAge_GeneratedThousandItems(t *testing.T) {
	rows := generated1000()
	for _, c := range []string{"USD", "EUR"} {
		if got, want := totalOf(domain.Age(asOf, rows, true), c), sum(rows, c); !got.Equal(want) {
			t.Fatalf("%s: buckets %s, open items %s", c, got, want)
		}
	}
}

func TestReconcile(t *testing.T) {
	totals := domain.Age(asOf, generated1000(), false)
	usd := totalOf(totals, "USD")
	tests := []struct {
		name       string
		ledger     decimal.Decimal
		wantDiff   decimal.Decimal
		reconciled bool
	}{
		{"match", usd, decimal.Zero, true},
		{"ledger short", usd.Sub(amount("0.01")), amount("0.01"), false},
		{"ledger long", usd.Add(amount("12.34")), amount("-12.34"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.Reconcile(totals, "USD", tc.ledger)
			if len(got) != 2 || got[0].Currency != "EUR" || got[1].Currency != "USD" {
				t.Fatalf("currencies = %+v", got)
			}
			if got[0].LedgerAvailable || got[0].Reconciled || got[0].Difference != nil {
				t.Fatalf("EUR has no ledger balance but reports %+v", got[0])
			}
			u := got[1]
			if u.Reconciled != tc.reconciled || !u.Difference.Equal(tc.wantDiff) || !u.SubledgerTotal.Equal(usd) || !u.LedgerBalance.Equal(tc.ledger) {
				t.Fatalf("USD = %+v", u)
			}
		})
	}
}

func TestReconcile_LedgerOnlyBalanceIsADifference(t *testing.T) {
	got := domain.Reconcile(nil, "USD", amount("40.00"))
	if len(got) != 1 || got[0].Reconciled || !got[0].SubledgerTotal.IsZero() || !got[0].Difference.Equal(amount("-40.00")) {
		t.Fatalf("got %+v", got)
	}
}
