package domain

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func journalMap(lines []JournalLine) map[string]decimal.Decimal {
	m := map[string]decimal.Decimal{}
	for _, l := range lines {
		m[l.Account] = l.Amount
	}
	return m
}

// TestRunPosting_BR431GLExample posts the BR-43.1 worker on the pre-2020 W-4
// (the PRD's own figures) and expects the PRD GL lines exactly: Dr salary
// 4,000.00, employer tax 330.00, 401(k) match 120.00; Cr net pay 2,711.13,
// 401(k) payable 360.00, federal WH 577.43, FICA 612.00, FUTA 24.00, CO 165.44.
func TestRunPosting_BR431GLExample(t *testing.T) {
	in := workerBR431()
	in.W4 = W4{FilingStatus: FilingSingle, Legacy: true}
	st, err := Calculate(US2025(), in)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := RunPosting([]Statement{st})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		AccountSalaryExpense: "4000.00", AccountEmployerTaxExpense: "330.00", AccountMatchExpense("401K"): "120.00",
		AccountNetPayClearing: "-2711.13", AccountPayable("401K"): "-360.00", AccountFederalWHPayable: "-577.43",
		AccountFICAPayable: "-612.00", AccountFUTAPayable: "-24.00", AccountPayable("STATE_CO_WH"): "-165.44",
	}
	got := journalMap(journal)
	if len(got) != len(want) {
		t.Fatalf("journal %+v, want %v", journal, want)
	}
	for a, w := range want {
		if !got[a].Equal(dec(w)) {
			t.Errorf("%s = %s, want %s", a, got[a], w)
		}
	}
}

// TestRunPosting_US200ReconcilesToRegister is AC4 / AC-43.5 at the journal: the
// 200 fixture statements post as one run, and every payroll expense and
// liability account ties to the register totals summed from the independent
// reference's expected columns with 0.00 variance; debits = gross + employer cost.
func TestRunPosting_US200ReconcilesToRegister(t *testing.T) {
	rows := readUS200(t)
	rs := US2025()
	statements := make([]Statement, 0, len(rows))
	sum := func(cols ...string) func(map[string]string) decimal.Decimal {
		return func(r map[string]string) decimal.Decimal {
			total := decimal.Zero
			for _, c := range cols {
				total = total.Add(dec(r[c]))
			}
			return total
		}
	}
	register := map[string]func(map[string]string) decimal.Decimal{
		AccountSalaryExpense:          sum("regular", "overtime"),
		AccountEmployerTaxExpense:     sum("exp_ss", "exp_medicare_er", "exp_futa"),
		AccountNetPayClearing:         sum("exp_net"),
		AccountFederalWHPayable:       sum("exp_fed_wh"),
		AccountFICAPayable:            sum("exp_ss", "exp_ss", "exp_medicare", "exp_medicare_er"),
		AccountFUTAPayable:            sum("exp_futa"),
		AccountPayable("STATE_CO_WH"): sum("exp_state"),
		AccountPayable("401K"):        sum("pretax_401k"),
		AccountPayable("MED125"):      sum("pretax_s125"),
	}
	credit := map[string]bool{AccountNetPayClearing: true, AccountFederalWHPayable: true, AccountFICAPayable: true,
		AccountFUTAPayable: true, AccountPayable("STATE_CO_WH"): true, AccountPayable("401K"): true, AccountPayable("MED125"): true}
	want := map[string]decimal.Decimal{}
	grossPlusEmployer := decimal.Zero
	for _, r := range rows {
		periods, _ := strconv.Atoi(r["periods"])
		allowances, _ := strconv.Atoi(r["allowances"])
		w := fixtureW4(r["filing"], r["legacy"] == "true", allowances, r["step2"] == "true",
			r["step3"], r["step4a"], r["step4b"], r["step4c"])
		in := fixtureInput(periods, w, r["state"], map[string]string{"REGULAR": r["regular"], "OVERTIME": r["overtime"]},
			r["pretax_401k"], r["pretax_s125"], r["ytd_fica"])
		in.YTD.FUTAWages = dec(r["ytd_futa"])
		st, err := Calculate(rs, in)
		if err != nil {
			t.Fatalf("%s: %v", r["id"], err)
		}
		statements = append(statements, st)
		for a, f := range register {
			want[a] = want[a].Add(f(r))
		}
		grossPlusEmployer = grossPlusEmployer.Add(sum("regular", "overtime", "exp_employer_cost")(r))
	}
	journal, err := RunPosting(statements)
	if err != nil {
		t.Fatal(err)
	}
	got := journalMap(journal)
	if len(got) != len(want) {
		t.Fatalf("journal accounts %v, want %v", got, want)
	}
	debits := decimal.Zero
	for a, w := range want {
		if credit[a] {
			w = w.Neg()
		}
		if variance := got[a].Sub(w); !variance.IsZero() {
			t.Errorf("%s = %s, register %s, variance %s", a, got[a].StringFixed(2), w.StringFixed(2), variance.StringFixed(2))
		}
		if got[a].IsPositive() {
			debits = debits.Add(got[a])
		}
	}
	if !debits.Equal(grossPlusEmployer) {
		t.Errorf("debits %s, want gross + employer cost %s", debits, grossPlusEmployer)
	}
	t.Logf("payroll-us-200 run: %d accounts, debits %s, 0.00 variance", len(journal), debits.StringFixed(2))
}

func TestRunPosting_PostTaxDeductionsAndRetroEarnings(t *testing.T) {
	in := workerBR431()
	in.Earnings = append(in.Earnings, Earning{Code: "RETRO_REGULAR", Amount: dec("400")})
	in.PostTax = []Deduction{{Code: "LOAN", Amount: dec("75.25"), Policy: PolicyPartial, RecoveryLimitPct: dec("1")}}
	st, err := Calculate(US2025(), in)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := RunPosting([]Statement{st})
	if err != nil {
		t.Fatal(err)
	}
	got := journalMap(journal)
	if !got[AccountSalaryExpense].Equal(dec("4400")) || !got[AccountPayable("LOAN")].Equal(dec("-75.25")) {
		t.Fatalf("salary %s loan payable %s", got[AccountSalaryExpense], got[AccountPayable("LOAN")])
	}
}

func TestRunPosting_UnknownLineFailsClosed(t *testing.T) {
	st := Statement{Gross: dec("10"), Net: dec("10"), Lines: []Line{
		{Code: "REGULAR", Kind: KindEarning, Amount: dec("10")},
		{Code: "SDI", Kind: KindTax, Amount: dec("1")},
	}}
	if _, err := RunPosting([]Statement{st}); err == nil {
		t.Fatal("want an error for a tax line the posting matrix does not know")
	}
	unbalanced := Statement{Net: dec("9"), Lines: []Line{{Code: "REGULAR", Kind: KindEarning, Amount: dec("10")}}}
	if _, err := RunPosting([]Statement{unbalanced}); !errors.Is(err, ErrUnbalancedPosting) {
		t.Fatalf("want ErrUnbalancedPosting, got %v", err)
	}
}

func TestMapToAccounts_ReplacesRolesSumsSharedAccountsAndFailsClosed(t *testing.T) {
	journal := []JournalLine{{Account: AccountSalaryExpense, Amount: decimal.RequireFromString("100")}, {Account: AccountEmployerTaxExpense, Amount: decimal.RequireFromString("8")},
		{Account: AccountNetPayClearing, Amount: decimal.RequireFromString("-108")}}
	got, err := MapToAccounts(journal, map[string]string{AccountSalaryExpense: "6000", AccountEmployerTaxExpense: "6000", AccountNetPayClearing: "2100"})
	if err != nil || len(got) != 2 || got[0].Account != "6000" || !got[0].Amount.Equal(decimal.RequireFromString("108")) || got[1].Account != "2100" {
		t.Fatalf("%+v %v", got, err)
	}
	_, err = MapToAccounts(journal, map[string]string{AccountSalaryExpense: "6000"})
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeNoPostingAccount || !strings.Contains(de.Detail, AccountNetPayClearing) || !strings.Contains(de.Detail, AccountEmployerTaxExpense) {
		t.Fatalf("missing roles: %v", err)
	}
}
