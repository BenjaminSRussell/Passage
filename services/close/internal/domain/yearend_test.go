package domain_test

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

type expectedEntry struct {
	Account    string
	Dimensions string
	Amount     decimal.Decimal
}

type yearFixture struct {
	RetainedEarnings           string
	Periods                    []struct{ Balances []domain.Balance }
	NetIncomeDebit             decimal.Decimal
	ExpectedOpeningByDimension []expectedEntry
	ExpectedOpeningCombined    []expectedEntry
}

func loadYear(t *testing.T) (yearFixture, []domain.Balance) {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/fy-12-periods.json")
	if err != nil {
		t.Fatal(err)
	}
	var f yearFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Periods) != 12 {
		t.Fatalf("fixture has %d periods, want 12", len(f.Periods))
	}
	type k struct{ account, dimensions string }
	sums := map[k]domain.Balance{}
	var order []k
	for _, p := range f.Periods {
		for _, b := range p.Balances {
			key := k{b.Account, b.Dimensions}
			if cur, ok := sums[key]; ok {
				b.Amount = b.Amount.Add(cur.Amount)
			} else {
				order = append(order, key)
			}
			sums[key] = b
		}
	}
	annual := make([]domain.Balance, 0, len(order))
	for _, key := range order {
		annual = append(annual, sums[key])
	}
	return f, annual
}

func assertEntries(t *testing.T, got []domain.Entry, want []expectedEntry) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Account != want[i].Account || got[i].Dimensions != want[i].Dimensions || !got[i].Amount.Equal(want[i].Amount) {
			t.Errorf("entry %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCloseYear_TwelvePeriods(t *testing.T) {
	f, annual := loadYear(t)
	ye, err := domain.CloseYear(annual, f.RetainedEarnings, true)
	if err != nil {
		t.Fatal(err)
	}
	assertEntries(t, ye.Opening, f.ExpectedOpeningByDimension)
	net := decimal.Zero
	for _, e := range ye.Closing {
		if e.Account == f.RetainedEarnings {
			net = net.Add(e.Amount)
		}
	}
	if !net.Equal(f.NetIncomeDebit) {
		t.Errorf("retained earnings moved by %s, want %s", net, f.NetIncomeDebit)
	}
	for _, e := range ye.Opening {
		if e.Account == "4000" || e.Account == "5000" || e.Account == "6000" {
			t.Errorf("profit and loss account %s must open at zero, got %s", e.Account, e.Amount)
		}
	}
}

func TestCloseYear_RetainDimensions(t *testing.T) {
	f, annual := loadYear(t)
	ye, err := domain.CloseYear(annual, f.RetainedEarnings, false)
	if err != nil {
		t.Fatal(err)
	}
	assertEntries(t, ye.Opening, f.ExpectedOpeningCombined)
}

func TestCloseYear_Errors(t *testing.T) {
	pl := domain.Balance{Account: "4000", Class: domain.ClassProfitLoss, Amount: decimal.NewFromInt(-5)}
	bs := domain.Balance{Account: "1000", Class: domain.ClassBalanceSheet, Amount: decimal.NewFromInt(5)}
	tests := []struct {
		name     string
		balances []domain.Balance
		re       string
		wantErr  bool
	}{
		{"empty balances", nil, "", false},
		{"missing retained earnings account", []domain.Balance{pl, bs}, "", true},
		{"unbalanced", []domain.Balance{pl}, "3000", true},
		{"unknown class", []domain.Balance{{Account: "9", Class: "zz", Amount: decimal.Zero}}, "3000", true},
		{"retained earnings classed as profit and loss", []domain.Balance{{Account: "3000", Class: domain.ClassProfitLoss, Amount: decimal.Zero}}, "3000", true},
		{"balanced", []domain.Balance{pl, bs}, "3000", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ye, err := domain.CloseYear(tc.balances, tc.re, true)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && (len(ye.Closing) != 0 || len(ye.Opening) != 0) {
				t.Fatalf("failure must return no proposals, got %+v", ye)
			}
		})
	}
}

func TestVerifyOpening_DetectsMismatch(t *testing.T) {
	f, annual := loadYear(t)
	ye, err := domain.CloseYear(annual, f.RetainedEarnings, true)
	if err != nil {
		t.Fatal(err)
	}
	ye.Opening[0].Amount = ye.Opening[0].Amount.Add(decimal.NewFromInt(1))
	if err := domain.VerifyOpening(annual, f.RetainedEarnings, ye); err == nil {
		t.Fatal("tampered opening balance must be rejected")
	}
}

func TestCloseYear_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 12).Draw(t, "accounts")
		var balances []domain.Balance
		total := decimal.Zero
		for i := 0; i < n; i++ {
			amount := decimal.New(rapid.Int64Range(-1_000_000, 1_000_000).Draw(t, "amount"), -2)
			class := domain.ClassBalanceSheet
			if rapid.Bool().Draw(t, "pl") {
				class = domain.ClassProfitLoss
			}
			balances = append(balances, domain.Balance{
				Account: string(rune('a' + i)), Class: class,
				Dimensions: rapid.SampledFrom([]string{"", "dept=A", "dept=B"}).Draw(t, "dims"), Amount: amount,
			})
			total = total.Add(amount)
		}
		balances = append(balances, domain.Balance{Account: "cash", Class: domain.ClassBalanceSheet, Amount: total.Neg()})
		ye, err := domain.CloseYear(balances, "re", rapid.Bool().Draw(t, "retainDimensions"))
		if err != nil {
			t.Fatal(err)
		}
		closing, opening := decimal.Zero, decimal.Zero
		for _, e := range ye.Closing {
			closing = closing.Add(e.Amount)
		}
		for _, e := range ye.Opening {
			opening = opening.Add(e.Amount)
		}
		if !closing.IsZero() || !opening.IsZero() {
			t.Fatalf("closing sums to %s and opening to %s, both must be zero", closing, opening)
		}
	})
}

func span(id, start, end string, rows ...domain.Row) domain.Period {
	from, _ := time.Parse("2006-01-02", start)
	to, _ := time.Parse("2006-01-02", end)
	return domain.Period{FiscalPeriodID: id, Start: from, End: to, Rows: rows}
}

func TestPlanYear(t *testing.T) {
	closed := domain.Row{Module: domain.ModuleAP, Status: domain.StatusClosed}
	held := domain.Row{Module: domain.ModuleGL, Status: domain.StatusOnHold}
	permanent := domain.Row{Module: domain.ModuleAll, Status: domain.StatusPermanentlyClosed}
	tests := []struct {
		name    string
		periods []domain.Period
		closing string
		opening string
		code    string
	}{
		{"periods in any order close on the year end", []domain.Period{span("h2", "2026-07-01", "2026-12-31", closed, held), span("h1", "2026-01-01", "2026-06-30", closed)}, "2026-12-31", "2027-01-01", ""},
		{"one period covering the year", []domain.Period{span("fy", "2026-01-01", "2026-12-31", closed)}, "2026-12-31", "2027-01-01", ""},
		{"earlier periods may be permanently closed", []domain.Period{span("h1", "2026-01-01", "2026-06-30", permanent), span("h2", "2026-07-01", "2026-12-31", closed)}, "2026-12-31", "2027-01-01", ""},
		{"no periods", nil, "", "", domain.CodeNoPeriod},
		{"gap between periods", []domain.Period{span("a", "2026-01-01", "2026-06-30", closed), span("b", "2026-08-01", "2026-12-31", closed)}, "", "", domain.CodeNoPeriod},
		{"missing december", []domain.Period{span("a", "2026-01-01", "2026-11-30", closed)}, "", "", domain.CodeNoPeriod},
		{"missing january", []domain.Period{span("a", "2026-02-01", "2026-12-31", closed)}, "", "", domain.CodeNoPeriod},
		{"overlapping periods", []domain.Period{span("a", "2026-01-01", "2026-07-15", closed), span("b", "2026-07-01", "2026-12-31", closed)}, "", "", domain.CodeAmbiguousPeriod},
		{"open module", []domain.Period{span("p", "2026-01-01", "2026-12-31", closed, domain.Row{Module: domain.ModuleAR, Status: domain.StatusOpen})}, "", "", domain.CodeInvalidTransition},
		{"non gl module on hold", []domain.Period{span("p", "2026-01-01", "2026-12-31", domain.Row{Module: domain.ModuleAP, Status: domain.StatusOnHold})}, "", "", domain.CodeInvalidTransition},
		{"last period permanently closed cannot take the closing entry", []domain.Period{span("p", "2026-01-01", "2026-12-31", permanent)}, "", "", domain.CodeInvalidTransition},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := domain.PlanYear("2026", tc.periods)
			var de *domain.Error
			if tc.code != "" {
				if !errors.As(err, &de) || de.Code != tc.code {
					t.Fatalf("got %v, want code %s", err, tc.code)
				}
				return
			}
			if err != nil || plan.ClosingDate.Format("2006-01-02") != tc.closing || plan.OpeningDate.Format("2006-01-02") != tc.opening {
				t.Fatalf("got %+v %v", plan, err)
			}
		})
	}
}

func TestClassOf(t *testing.T) {
	want := map[string]domain.Class{"revenue": domain.ClassProfitLoss, "expense": domain.ClassProfitLoss, "asset": domain.ClassBalanceSheet, "liability": domain.ClassBalanceSheet, "equity": domain.ClassBalanceSheet}
	for accountType, class := range want {
		if got, err := domain.ClassOf(accountType); err != nil || got != class {
			t.Errorf("%s: got %q %v, want %q", accountType, got, err, class)
		}
	}
	var de *domain.Error
	if _, err := domain.ClassOf("statistical"); !errors.As(err, &de) || de.Code != domain.CodeLedgerData {
		t.Fatalf("statistical account must not carry a year-end balance: %v", err)
	}
}

func TestDimensionKey_RoundTripsAndChecksAttributeCount(t *testing.T) {
	dims := map[string]string{"CC": "A", "DEPT": "B"}
	if got := domain.ParseDimensions(domain.DimensionKey(dims)); got["CC"] != "A" || got["DEPT"] != "B" || len(got) != 2 {
		t.Fatalf("round trip lost data: %v", got)
	}
	if domain.DimensionKey(nil) != "" || domain.ParseDimensions("") != nil {
		t.Fatal("no dimensions must be the empty key")
	}
	if err := domain.CheckDimensionAttributes([]string{"A", "B", "C", "D", "E"}); err != nil {
		t.Fatal(err)
	}
	if err := domain.CheckDimensionAttributes([]string{"A", "B", "C", "D", "E", "F"}); err == nil {
		t.Fatal("more than five attributes must be refused")
	}
}
