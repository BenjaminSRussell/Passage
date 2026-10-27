package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// bai2Sample is one USD account: opening ledger 1,000.00, a 50.00 credit with
// funds type Z, a 150.00 debit with funds type S (three availability amounts),
// a 25.00 credit with funds type V (own value date) whose text has a comma,
// a slash and an 88 continuation, and closing ledger 925.00.
const bai2Sample = `01,SENDERBANK,RECEIVERCO,241001,1200,1,,,2/
02,RECEIVERCO,SENDERBANK,1,241001,1200,USD,2/
03,123456789,USD,010,100000,,,015,92500,,,100,7500,2,Z/
16,195,5000,Z,BREF1,CUST1,PAYMENT A, INVOICE 555/
16,475,15000,S,0,15000,0,BREF2,CHK1001,CHECK PAID/
16,165,2500,V,241002,,BREF3,,ACH CREDIT 1/2
88,SECOND PART/
49,307500,6/
98,307500,1,8/
99,307500,1,10/
`

func errCode(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func errLine(err error) int {
	var de *Error
	if errors.As(err, &de) {
		return de.Line
	}
	return -1
}

func TestParseBAI2_ReadsAccountBalancesAndDetails(t *testing.T) {
	stmts, err := ParseBAI2([]byte(bai2Sample))
	if err != nil {
		t.Fatalf("ParseBAI2: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("want 1 account, got %d", len(stmts))
	}
	s := stmts[0]
	if s.Format != FormatBAI2 || s.StatementID != "123456789-2024-10-01" || s.Account != "123456789" || s.Currency != "USD" || s.Sequence != 0 {
		t.Fatalf("header: %+v", s)
	}
	if !s.OpeningAmount.Equal(decimal.RequireFromString("1000.00")) || !s.ClosingAmount.Equal(decimal.RequireFromString("925.00")) {
		t.Fatalf("balances: %s / %s", s.OpeningAmount, s.ClosingAmount)
	}
	if s.OpeningDate != "2024-10-01" || s.ClosingDate != "2024-10-01" {
		t.Fatalf("dates %q %q", s.OpeningDate, s.ClosingDate)
	}
	if len(s.Lines) != 3 {
		t.Fatalf("want 3 lines, got %d", len(s.Lines))
	}
	want := []struct {
		amount, btc, e2e, ref, value string
	}{
		{"50.00", "BAI2/195", "CUST1", "PAYMENT A, INVOICE 555", "2024-10-01"},
		{"-150.00", "BAI2/475", "CHK1001", "CHECK PAID", "2024-10-01"},
		{"25.00", "BAI2/165", "", "ACH CREDIT 1/2 SECOND PART", "2024-10-02"},
	}
	for i, w := range want {
		l := s.Lines[i]
		if l.LineNo != i+1 || !l.Amount.Equal(decimal.RequireFromString(w.amount)) || l.BTCCode != w.btc ||
			l.EndToEndRef != w.e2e || l.Reference != w.ref || l.ValueDate != w.value || l.BookingDate != "2024-10-01" || l.Currency != "USD" {
			t.Fatalf("line %d = %+v, want %+v", i+1, l, w)
		}
		if !strings.HasPrefix(l.RawFragment, "16,") {
			t.Fatalf("line %d raw fragment %q", i+1, l.RawFragment)
		}
	}
}

func TestParseBAI2_UsesEachGroupsAsOfDateAndMinorUnits(t *testing.T) {
	in := "01,S,R,241001,1200,1,,,2/\n" +
		"02,R,S,1,241001,,USD,2/\n03,111,,010,1000,,,015,1000,,/\n49,2000,2/\n98,2000,1,4/\n" +
		"02,R,S,1,241002,,JPY,2/\n03,222,,010,500,,,015,700,,/\n16,115,200,,B,C/\n49,1400,3/\n98,1400,1,5/\n99,3400,2,11/\n"
	stmts, err := ParseBAI2([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 2 {
		t.Fatalf("want 2 accounts, got %d", len(stmts))
	}
	if stmts[0].StatementID != "111-2024-10-01" || stmts[0].Currency != "USD" || !stmts[0].OpeningAmount.Equal(decimal.RequireFromString("10.00")) {
		t.Fatalf("first account: %+v", stmts[0])
	}
	// JPY has no minor unit, so 500 is 500 yen; the second group has its own date.
	if stmts[1].StatementID != "222-2024-10-02" || stmts[1].OpeningDate != "2024-10-02" || stmts[1].Currency != "JPY" ||
		!stmts[1].OpeningAmount.Equal(decimal.RequireFromString("500")) || !stmts[1].Lines[0].Amount.Equal(decimal.RequireFromString("200")) {
		t.Fatalf("second account: %+v", stmts[1])
	}
}

func TestParseBAI2_88ContinuesBalanceGroups(t *testing.T) {
	in := "01,S,R,241001,1200,1,,,2/\n02,R,S,1,241001,,EUR,2/\n03,ACC-9,,010,-2500,,/\n88,015,-1500,,/\n16,175,1000,0,B,C,TEXT/\n49,0,4/\n"
	stmts, err := ParseBAI2([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0]
	if !s.OpeningAmount.Equal(decimal.RequireFromString("-25.00")) || !s.ClosingAmount.Equal(decimal.RequireFromString("-15.00")) || s.Account != "ACC-9" {
		t.Fatalf("balances from 03+88: %+v", s)
	}
}

func TestParseBAI2_DefaultsCurrencyToUSD(t *testing.T) {
	in := "01,S,R,241001,1200,1,,,2/\n02,R,S,1,241001,,,2/\n03,1,,010,100,,,015,100,,/\n"
	stmts, err := ParseBAI2([]byte(in))
	if err != nil || stmts[0].Currency != "USD" {
		t.Fatalf("got %+v, %v", stmts, err)
	}
}

func TestParseBAI2_Errors(t *testing.T) {
	head := "01,A,B,241001,1200,1,,,2/\n02,B,A,1,241001,1200,USD,2/\n"
	cases := []struct {
		name string
		in   string
		code string
		line int
	}{
		{"balance does not add up", head + "03,1,USD,010,100000,,,015,90001,,/\n", CodeStatementBalance, 0},
		{"unknown record", head + "77,x/\n", CodeStatementFormat, 3},
		{"detail before account", head + "16,195,5000,Z,R,C,T/\n", CodeStatementFormat, 3},
		{"detail after account trailer", head + "03,1,USD,010,0,,,015,0,,/\n49,0,2/\n16,195,5,Z,R,C/\n", CodeStatementFormat, 5},
		{"missing closing balance", head + "03,1,USD,010,100000,,/\n", CodeStatementFormat, 3},
		{"invalid as-of date", "01,A,B,1,1/\n02,B,A,1,241301,1200,USD,2/\n", CodeStatementFormat, 2},
		{"non-transaction type code", head + "03,1,USD,010,100,,,015,95,,/\n16,890,5,Z,R,C,T/\n", CodeStatementFormat, 4},
		{"signed detail amount", head + "03,1,USD,010,100,,,015,95,,/\n16,495,-5,Z,R,C,T/\n", CodeStatementFormat, 4},
		{"non-numeric balance", head + "03,1,USD,010,ABC,,,015,95,,/\n", CodeStatementFormat, 3},
		{"bad type code in 03", head + "03,1,USD,10,100,,,015,95,,/\n", CodeStatementFormat, 3},
		{"bad currency", head + "03,1,usd,010,100,,,015,100,,/\n", CodeStatementFormat, 3},
		{"no account", "01,A,B,1,1/\n02,B,A,1,241001,1200,USD,2/\n99,0,1,2/\n", CodeStatementFormat, 0},
		{"no file header", "02,B,A,1,241001,1200,USD,2/\n", CodeStatementFormat, 1},
		{"continuation first", "88,x/\n", CodeStatementFormat, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseBAI2([]byte(tc.in))
			if got := errCode(err); got != tc.code {
				t.Fatalf("want %s, got %q (%v)", tc.code, got, err)
			}
			if got := errLine(err); got != tc.line {
				t.Fatalf("line = %d, want %d (%v)", got, tc.line, err)
			}
		})
	}
}
