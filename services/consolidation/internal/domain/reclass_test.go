package domain

import (
	"errors"
	"testing"
	"time"
)

var periodEnd2026 = time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)

func line(rule, account, party, debit, credit string, reverses bool) ReclassLine {
	l := ReclassLine{RuleCode: rule, Account: account, Party: party, Debit: d("0"), Credit: d("0"), Reverses: reverses}
	if debit != "" {
		l.Debit = d(debit)
	}
	if credit != "" {
		l.Credit = d(credit)
	}
	return l
}

func sameLines(t *testing.T, got, want []ReclassLine) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("lines = %+v; want %+v", got, want)
	}
	for i, w := range want {
		g := got[i]
		if g.RuleCode != w.RuleCode || g.Account != w.Account || g.Party != w.Party || !g.Debit.Equal(w.Debit) || !g.Credit.Equal(w.Credit) || g.Reverses != w.Reverses {
			t.Fatalf("line %d = %+v; want %+v", i, g, w)
		}
	}
}

// TestRunReclassification_AC4013 is AC-40.13: a vendor with a debit balance of
// USD 5,000.00 moves to other receivables and reverses on the first day of the
// next period; a vendor in credit stays.
func TestRunReclassification_AC4013(t *testing.T) {
	rules := []ReclassRule{{Code: "VDB", Level: LevelEntity, Type: TypeDebitBalanceVendors, Order: 1, Version: 1, SourceAccount: "VENDOR_RECLASS", TargetAccount: "OTHER_RECEIVABLES"}}
	items := []OpenItem{
		{Party: "V1", Account: "VENDOR_RECLASS", Amount: d("6000")},
		{Party: "V1", Account: "VENDOR_RECLASS", Amount: d("-1000")},
		{Party: "V2", Account: "VENDOR_RECLASS", Amount: d("-800")},
		{Party: "V3", Account: "OTHER", Amount: d("50")},
	}
	res, err := RunReclassification(rules, items, nil, periodEnd2026, "USD")
	if err != nil {
		t.Fatal(err)
	}
	sameLines(t, res.Lines, []ReclassLine{
		line("VDB", "OTHER_RECEIVABLES", "V1", "5000", "", true),
		line("VDB", "VENDOR_RECLASS", "V1", "", "5000", true),
	})
	if got := res.ReversalDate.Format("2006-01-02"); got != "2027-01-01" || len(res.Applied) != 1 {
		t.Fatalf("reversal %s applied %v; want 2027-01-01 [VDB]", got, res.Applied)
	}
}

func TestRunReclassification_CreditBalanceCustomersAndThreshold(t *testing.T) {
	rules := []ReclassRule{{Code: "CCB", Level: LevelEntity, Type: TypeCreditBalanceCustomers, Order: 1, Version: 1, SourceAccount: "AR", TargetAccount: "OTHER_PAYABLES", Threshold: d("100")}}
	items := []OpenItem{
		{Party: "C1", Account: "AR", Amount: d("-300")},
		{Party: "C2", Account: "AR", Amount: d("200")},
		{Party: "C3", Account: "AR", Amount: d("-100")},
	}
	res, err := RunReclassification(rules, items, nil, time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC), "USD")
	if err != nil {
		t.Fatal(err)
	}
	sameLines(t, res.Lines, []ReclassLine{
		line("CCB", "AR", "C1", "300", "", true),
		line("CCB", "OTHER_PAYABLES", "C1", "", "300", true),
	})
	if got := res.ReversalDate.Format("2006-01-02"); got != "2026-12-01" {
		t.Fatalf("reversal date = %s", got)
	}
}

// TestRunReclassification_RemainingTerm moves only what is due more than 12
// months after the period end, per party: a receivable and a loan liability.
func TestRunReclassification_RemainingTerm(t *testing.T) {
	due := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	rules := []ReclassRule{
		{Code: "RT-AR", Level: LevelEntity, Type: TypeRemainingTerm, Order: 2, Version: 1, SourceAccount: "AR", TargetAccount: "AR_NONCURRENT"},
		{Code: "RT-LOAN", Level: LevelEntity, Type: TypeRemainingTerm, Order: 1, Version: 2, SourceAccount: "LOAN", TargetAccount: "LOAN_NONCURRENT"},
	}
	items := []OpenItem{
		{Party: "C1", Account: "AR", DueDate: due(2028, 6, 30), Amount: d("1000")},
		{Party: "C1", Account: "AR", DueDate: due(2027, 12, 31), Amount: d("400")},
		{Party: "B1", Account: "LOAN", DueDate: due(2029, 1, 31), Amount: d("-25000")},
	}
	res, err := RunReclassification(rules, items, nil, periodEnd2026, "USD")
	if err != nil {
		t.Fatal(err)
	}
	sameLines(t, res.Lines, []ReclassLine{
		line("RT-LOAN", "LOAN", "B1", "25000", "", true),
		line("RT-LOAN", "LOAN_NONCURRENT", "B1", "", "25000", true),
		line("RT-AR", "AR_NONCURRENT", "C1", "1000", "", true),
		line("RT-AR", "AR", "C1", "", "1000", true),
	})
	if len(res.Applied) != 2 || res.Applied[0] != "RT-LOAN" {
		t.Fatalf("applied = %v; want rule order RT-LOAN, RT-AR", res.Applied)
	}
}

// TestRunReclassification_SignBased shows negative consolidated cash as bank
// overdrafts; group-level lines do not reverse and set no reversal date.
func TestRunReclassification_SignBased(t *testing.T) {
	rules := []ReclassRule{{Code: "CASH", Level: LevelGroup, Type: TypeSignBased, Order: 1, Version: 1, SourceAccount: "CASH", TargetAccount: "BANK_OVERDRAFTS"}}
	balances := []GroupBalance{{Account: "CASH", Amount: d("-1200.004")}, {Account: "CASH", Amount: d("200")}, {Account: "AR", Amount: d("-5")}}
	res, err := RunReclassification(rules, nil, balances, periodEnd2026, "USD")
	if err != nil {
		t.Fatal(err)
	}
	sameLines(t, res.Lines, []ReclassLine{
		line("CASH", "CASH", "", "1000.00", "", false),
		line("CASH", "BANK_OVERDRAFTS", "", "", "1000.00", false),
	})
	if !res.ReversalDate.IsZero() {
		t.Fatalf("group-level reclassification set a reversal date %s", res.ReversalDate)
	}
	pos, err := RunReclassification(rules, nil, []GroupBalance{{Account: "CASH", Amount: d("10")}}, periodEnd2026, "USD")
	if err != nil || len(pos.Lines) != 0 || len(pos.Applied) != 0 {
		t.Fatalf("positive cash must not move: %+v %v", pos, err)
	}
}

func TestRunReclassification_Rejections(t *testing.T) {
	ok := ReclassRule{Code: "R", Level: LevelEntity, Type: TypeDebitBalanceVendors, Order: 1, Version: 1, SourceAccount: "AP", TargetAccount: "OR"}
	cases := map[string]func(*ReclassRule){
		"no code":            func(r *ReclassRule) { r.Code = "" },
		"no target":          func(r *ReclassRule) { r.TargetAccount = "" },
		"same accounts":      func(r *ReclassRule) { r.TargetAccount = "AP" },
		"negative threshold": func(r *ReclassRule) { r.Threshold = d("-1") },
		"version zero":       func(r *ReclassRule) { r.Version = 0 },
		"negative term":      func(r *ReclassRule) { r.TermMonths = -1 },
		"unknown type":       func(r *ReclassRule) { r.Type = "FSRemap" },
		"wrong level":        func(r *ReclassRule) { r.Level = LevelGroup },
		"sign-based entity":  func(r *ReclassRule) { r.Type = TypeSignBased },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			r := ok
			edit(&r)
			_, err := RunReclassification([]ReclassRule{r}, nil, nil, periodEnd2026, "USD")
			var de *Error
			if !errors.As(err, &de) || de.Code != CodeInvalid {
				t.Fatalf("err = %v; want COM-0001", err)
			}
		})
	}
	if _, err := RunReclassification([]ReclassRule{ok, ok}, nil, nil, periodEnd2026, "USD"); err == nil {
		t.Fatal("duplicate rule codes must be rejected")
	}
	if _, err := RunReclassification([]ReclassRule{ok}, nil, nil, periodEnd2026, "US"); err == nil {
		t.Fatal("bad currency must be rejected")
	}
}
