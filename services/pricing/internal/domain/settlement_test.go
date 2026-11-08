package domain_test

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

func readCSV(t interface{ Fatal(...any) }, raw []byte) [][]string {
	recs, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

// AC-26.12 totals as the payroll file shows them: 300.00 and 200.00 USD with the minor units
// of the currency, the target and the period on every row.
func TestSettlementCSV_HeaderAndRows(t *testing.T) {
	rows := []domain.SettlementRow{
		{RepID: "R1", Currency: "USD", Amount: decimal.RequireFromString("300"), Transactions: 2},
		{RepID: "R2", Currency: "JPY", Amount: decimal.RequireFromString("1250.000000"), Transactions: 1},
		{RepID: "R3", Currency: "USD", Amount: decimal.RequireFromString("-12.5"), Transactions: 1},
	}
	raw, err := domain.SettlementCSV(domain.SettlementTargetPayroll, "2027-03-01", "2027-03-31", rows)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	want := []string{
		"target,rep_id,currency,amount,transactions,from,to",
		"PAYROLL,R1,USD,300.00,2,2027-03-01,2027-03-31",
		"PAYROLL,R2,JPY,1250,1,2027-03-01,2027-03-31",
		"PAYROLL,R3,USD,-12.50,1,2027-03-01,2027-03-31",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("export =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestSettlementCSV_NeutralisesFormulaRepIDs(t *testing.T) {
	rows := []domain.SettlementRow{
		{RepID: "=HYPERLINK(\"x\")", Currency: "EUR", Amount: decimal.NewFromInt(1), Transactions: 1},
		{RepID: "@SUM(A1)", Currency: "EUR", Amount: decimal.NewFromInt(1), Transactions: 1},
		{RepID: `R,"1"`, Currency: "EUR", Amount: decimal.NewFromInt(1), Transactions: 1},
	}
	raw, err := domain.SettlementCSV(domain.SettlementTargetAP, "2027-03-01", "2027-03-31", rows)
	if err != nil {
		t.Fatal(err)
	}
	recs := readCSV(t, raw)
	if recs[1][1] != `'=HYPERLINK("x")` || recs[2][1] != "'@SUM(A1)" || recs[3][1] != `R,"1"` {
		t.Fatalf("rep cells = %q, %q, %q", recs[1][1], recs[2][1], recs[3][1])
	}
}

func TestValidSettlementTarget(t *testing.T) {
	cases := map[string]bool{"AP": true, "PAYROLL": true, "NONE": false, "": false, "ap": false}
	for target, want := range cases {
		if got := domain.ValidSettlementTarget(target); got != want {
			t.Errorf("ValidSettlementTarget(%q) = %v; want %v", target, got, want)
		}
	}
}

// Property: every export reads back as one row per input row with the same rep, currency,
// count and amount (to the cent), so payroll and AP receive exactly the inquiry totals.
func TestSettlementCSV_ReadsBackProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 8).Draw(t, "rows")
		rows := make([]domain.SettlementRow, n)
		for i := range rows {
			rows[i] = domain.SettlementRow{
				RepID:        rapid.StringMatching(`[A-Za-z0-9,"]{1,12}`).Draw(t, "rep"),
				Currency:     rapid.SampledFrom([]string{"USD", "EUR"}).Draw(t, "ccy"),
				Amount:       decimal.New(rapid.Int64Range(-100_000_000, 100_000_000).Draw(t, "cents"), -2),
				Transactions: rapid.IntRange(0, 500).Draw(t, "transactions"),
			}
		}
		raw, err := domain.SettlementCSV(domain.SettlementTargetAP, "2027-01-01", "2027-01-31", rows)
		if err != nil {
			t.Fatal(err)
		}
		recs := readCSV(t, raw)
		if len(recs) != n+1 {
			t.Fatalf("read %d records, wrote %d rows", len(recs)-1, n)
		}
		for i, r := range rows {
			rec := recs[i+1]
			amount, err := decimal.NewFromString(rec[3])
			if err != nil || rec[1] != r.RepID || rec[2] != r.Currency || !amount.Equal(r.Amount) || rec[4] != decimal.NewFromInt(int64(r.Transactions)).String() {
				t.Fatalf("row %d: read %q, wrote %+v", i, rec, r)
			}
		}
	})
}

func TestExportTotals_PerCurrencyInMinorUnits(t *testing.T) {
	rows := []domain.SettlementRow{
		{RepID: "r1", Currency: "USD", Amount: decimal.RequireFromString("100.005"), Transactions: 2},
		{RepID: "r2", Currency: "USD", Amount: decimal.RequireFromString("50.10"), Transactions: 1},
		{RepID: "r1", Currency: "JPY", Amount: decimal.RequireFromString("1200"), Transactions: 1},
		{RepID: "r3", Currency: "USD", Amount: decimal.RequireFromString("-20"), Transactions: 1},
	}
	got := domain.ExportTotals(rows)
	if len(got) != 2 || got[0].Currency != "JPY" || got[0].Amount.String() != "1200" ||
		got[1].Currency != "USD" || got[1].Amount.String() != "130.1" {
		t.Fatalf("totals = %+v", got)
	}
	if len(domain.ExportTotals(nil)) != 0 {
		t.Fatal("no rows must give no totals")
	}
}

func TestCSVDigest_StableAndSensitive(t *testing.T) {
	a, _ := domain.SettlementCSV("AP", "2027-03-01", "2027-03-31", []domain.SettlementRow{{RepID: "r1", Currency: "USD", Amount: decimal.NewFromInt(5), Transactions: 1}})
	b, _ := domain.SettlementCSV("AP", "2027-03-01", "2027-03-31", []domain.SettlementRow{{RepID: "r1", Currency: "USD", Amount: decimal.NewFromInt(6), Transactions: 1}})
	if domain.CSVDigest(a) != domain.CSVDigest(a) || domain.CSVDigest(a) == domain.CSVDigest(b) || len(domain.CSVDigest(a)) != 64 {
		t.Fatal("digest must be stable, 64 hex characters and change with the content")
	}
}

// The recorded digest of every settlement export depends on these exact bytes: a change to the
// CSV layout makes the files of earlier exports unservable (PRC-0068), so it must be deliberate.
func TestSettlementCSV_LayoutIsPinned(t *testing.T) {
	raw, err := domain.SettlementCSV("PAYROLL", "2027-03-01", "2027-03-31", []domain.SettlementRow{
		{RepID: "R1", Currency: "USD", Amount: decimal.RequireFromString("360"), Transactions: 2},
		{RepID: "=R2", Currency: "JPY", Amount: decimal.RequireFromString("-1200"), Transactions: 1},
	})
	want := "target,rep_id,currency,amount,transactions,from,to\n" +
		"PAYROLL,R1,USD,360.00,2,2027-03-01,2027-03-31\n" +
		"PAYROLL,'=R2,JPY,-1200,1,2027-03-01,2027-03-31\n"
	if err != nil || string(raw) != want {
		t.Fatalf("csv = %q, %v; want %q", raw, err, want)
	}
}
