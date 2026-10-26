package domain

import (
	"encoding/csv"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

type workbookRow struct {
	balance  Balance
	rate     decimal.Decimal
	expected decimal.Decimal
}

func loadWorkbook(t *testing.T) []workbookRow {
	t.Helper()
	f, err := os.Open("../../testdata/reval-workbook.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]workbookRow, 0, len(records)-1)
	for _, r := range records[1:] {
		rows = append(rows, workbookRow{
			balance:  Balance{Account: r[0], Currency: r[1], BalanceTxn: dec(r[2]), BalanceAcct: dec(r[3])},
			rate:     dec(r[4]),
			expected: dec(r[5]),
		})
	}
	return rows
}

func closingRates(rows []workbookRow) map[string]decimal.Decimal {
	m := map[string]decimal.Decimal{}
	for _, r := range rows {
		m[r.balance.Currency] = r.rate
	}
	return m
}

func TestRevalueMatchesWorkbook(t *testing.T) {
	rows := loadWorkbook(t)
	if len(rows) != 12 {
		t.Fatalf("workbook rows = %d, want 12", len(rows))
	}
	bals := make([]Balance, len(rows))
	for i, r := range rows {
		bals[i] = r.balance
	}
	periodEnd := date("2026-03-31")
	lines, err := Revalue(bals, closingRates(rows), "USD", periodEnd)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != len(rows) {
		t.Fatalf("lines = %d, want %d", len(lines), len(rows))
	}
	adjustments, gains, losses := decimal.Zero, decimal.Zero, decimal.Zero
	for i, l := range lines {
		if !l.Adjustment.Value.Equal(rows[i].expected) {
			t.Errorf("%s %s: adjustment %s, workbook %s", l.Account, l.Currency, l.Adjustment.Value, rows[i].expected)
		}
		if !l.ReversalDate.Equal(date("2026-04-01")) || !l.PostingDate.Equal(periodEnd) {
			t.Errorf("dates %v %v", l.PostingDate, l.ReversalDate)
		}
		if l.Gain.Value.Sign() < 0 || l.Loss.Value.Sign() < 0 || (l.Gain.Value.Sign() > 0 && l.Loss.Value.Sign() > 0) ||
			!l.Gain.Value.Sub(l.Loss.Value).Equal(l.Adjustment.Value) {
			t.Errorf("%s %s: gain %s loss %s do not split adjustment %s", l.Account, l.Currency, l.Gain.Value, l.Loss.Value, l.Adjustment.Value)
		}
		adjustments = adjustments.Add(l.Adjustment.Value)
		gains, losses = gains.Add(l.Gain.Value), losses.Add(l.Loss.Value)
	}
	if !adjustments.Equal(gains.Sub(losses)) {
		t.Fatalf("sum adjustments %s != gain %s - loss %s", adjustments, gains, losses)
	}
}

func TestRevalueMissingClosingRateReturnsNoPartialResult(t *testing.T) {
	bals := []Balance{
		{Account: "1100", Currency: "EUR", BalanceTxn: dec("100"), BalanceAcct: dec("100")},
		{Account: "1100", Currency: "GBP", BalanceTxn: dec("100"), BalanceAcct: dec("100")},
	}
	lines, err := Revalue(bals, map[string]decimal.Decimal{"EUR": dec("1.1")}, "USD", date("2026-03-31"))
	if !errors.Is(err, ErrRateMissing) || lines != nil {
		t.Fatalf("lines %v err %v", lines, err)
	}
}

func TestRevalueRejectsNonPositiveClosingRate(t *testing.T) {
	bals := []Balance{{Account: "1100", Currency: "EUR", BalanceTxn: dec("100"), BalanceAcct: dec("100")}}
	for _, rate := range []string{"0", "-1.1"} {
		lines, err := Revalue(bals, map[string]decimal.Decimal{"EUR": dec(rate)}, "USD", date("2026-03-31"))
		if !errors.Is(err, ErrRateMissing) || lines != nil {
			t.Fatalf("rate %s: lines %v err %v", rate, lines, err)
		}
	}
}

func TestRevalueNamesAllMissingCurrenciesSorted(t *testing.T) {
	bals := []Balance{
		{Account: "1", Currency: "JPY", BalanceTxn: dec("1")},
		{Account: "2", Currency: "GBP", BalanceTxn: dec("1")},
		{Account: "3", Currency: "CHF", BalanceTxn: dec("1")},
	}
	for i := 0; i < 50; i++ {
		_, err := Revalue(bals, nil, "USD", date("2026-03-31"))
		if err == nil || !strings.Contains(err.Error(), "for CHF, GBP, JPY") {
			t.Fatalf("run %d: err = %v", i, err)
		}
	}
}

func TestRevalueSkipsZeroBalanceAndAccountingCurrency(t *testing.T) {
	bals := []Balance{
		{Account: "1100", Currency: "EUR", BalanceTxn: decimal.Zero, BalanceAcct: dec("5")},
		{Account: "1200", Currency: "USD", BalanceTxn: dec("50"), BalanceAcct: dec("50")},
	}
	lines, err := Revalue(bals, nil, "USD", date("2026-03-31"))
	if err != nil || len(lines) != 0 {
		t.Fatalf("lines %v err %v", lines, err)
	}
}

func TestRevalueEmptyAndNil(t *testing.T) {
	lines, err := Revalue(nil, nil, "USD", date("2026-03-31"))
	if err != nil || len(lines) != 0 {
		t.Fatalf("lines %v err %v", lines, err)
	}
}

func TestReversalDateCrossesMonthAndYear(t *testing.T) {
	if got := ReversalDate(date("2026-12-31")); !got.Equal(date("2027-01-01")) {
		t.Fatalf("got %v", got)
	}
}
