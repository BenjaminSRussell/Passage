package domain

import (
	"os"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// mt940Sample holds two statements that reuse the :20: reference, as many
// banks do, with a structured and an unstructured :86: block.
const mt940Sample = `{1:F01BANKDEFFAXXX0000000000}{2:O9400000000000BANKDEFFAXXX00000000000000000000N}{4:
:20:STARTUMS
:25:DE89 3704 0044 0532 0130 00
:28C:00001/001
:60F:C241001EUR1000,00
:61:2410011001C50,00NTRFNONREF//BR1
:86:166?00GUTSCHRIFT?20INVOICE 555?21 PART 2?32ACME
 CORP?33GMBH
:61:2410021002D150,00NCHGFEE1//BR2
:86:BANK FEE
OCTOBER
:62F:C241002EUR900,00
-}
:20:STARTUMS
:25:DE89370400440532013000
:28C:00002/001
:60F:C241002EUR900,00
:61:241002RC10,00S202REV1
:62F:C241002EUR890,00
-}
`

func TestParseMT940_SplitsStatementsAndReadsEntries(t *testing.T) {
	stmts, err := ParseMT940([]byte(mt940Sample))
	if err != nil {
		t.Fatalf("ParseMT940: %v", err)
	}
	if len(stmts) != 2 {
		t.Fatalf("want 2 statements, got %d", len(stmts))
	}
	s := stmts[0]
	if s.Format != FormatMT940 || s.StatementID != "STARTUMS/DE89370400440532013000/00001/001" || s.Sequence != 1 || s.Account != "DE89370400440532013000" || s.Currency != "EUR" {
		t.Fatalf("header: %+v", s)
	}
	if s.OpeningDate != "2024-10-01" || s.ClosingDate != "2024-10-02" || !s.ClosingAmount.Equal(decimal.RequireFromString("900")) {
		t.Fatalf("balances: %+v", s)
	}
	if len(s.Lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(s.Lines))
	}
	first, second := s.Lines[0], s.Lines[1]
	if !first.Amount.Equal(decimal.RequireFromString("50.00")) || first.EndToEndRef != "" || first.Reference != "INVOICE 555 PART 2" ||
		first.CounterpartyName != "ACME CORPGMBH" || first.BTCCode != "MT940/NTRF" {
		t.Fatalf("first line: %+v", first)
	}
	if !strings.Contains(first.RawFragment, ":86:166?00GUTSCHRIFT") {
		t.Fatalf("raw fragment must keep :86: for audit: %q", first.RawFragment)
	}
	if !second.Amount.Equal(decimal.RequireFromString("-150.00")) || second.EndToEndRef != "FEE1" || second.Reference != "BANK FEE OCTOBER" ||
		second.ValueDate != "2024-10-02" || second.BookingDate != "2024-10-02" || second.BTCCode != "MT940/NCHG" {
		t.Fatalf("second line: %+v", second)
	}
	// RC (reversal of a credit) is a debit; transaction type S is accepted; the
	// :20: reused by the bank still gives a distinct statement id.
	next := stmts[1]
	if next.StatementID != "STARTUMS/DE89370400440532013000/00002/001" || next.Sequence != 2 || len(next.Lines) != 1 ||
		!next.Lines[0].Amount.Equal(decimal.RequireFromString("-10")) || next.Lines[0].Reference != "" || next.Lines[0].EndToEndRef != "REV1" {
		t.Fatalf("second statement: %+v", next)
	}
}

func TestParseMT940_EntryDateCrossesYearEnd(t *testing.T) {
	in := ":20:S1\n:25:12345678/0001\n:60F:C241231EUR0,00\n" +
		":61:2412310102C5,00NTRFA//B\n" + // value 31 Dec 2024, booked 2 Jan 2025
		":61:2501011231D5,00NTRFC//D\n" + // value 1 Jan 2025, booked 31 Dec 2024
		":62F:C250102EUR0,00\n"
	stmts, err := ParseMT940([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	l := stmts[0].Lines
	if l[0].BookingDate != "2025-01-02" || l[0].ValueDate != "2024-12-31" || l[1].BookingDate != "2024-12-31" || l[1].ValueDate != "2025-01-01" {
		t.Fatalf("dates: %+v / %+v", l[0], l[1])
	}
	if stmts[0].Account != "12345678/0001" || stmts[0].StatementID != "S1/12345678/0001/2024-12-31" || stmts[0].Sequence != 0 {
		t.Fatalf("bank code/account in :25: is kept whole, no :28C: keeps :20:: %+v", stmts[0])
	}
}

func TestParseMT940_ReusedReferenceWithoutStatementNumberStaysDistinctPerDayAndAccount(t *testing.T) {
	day := func(account, date string) string {
		return ":20:DAILY\n:25:" + account + "\n:60F:C" + date + "EUR1,00\n:62F:C" + date + "EUR1,00\n-\n"
	}
	stmts, err := ParseMT940([]byte(day("DE89370400440532013000", "241001") + day("DE89370400440532013000", "241002") + day("DE02120300000000202051", "241002")))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, s := range stmts {
		seen[s.StatementID] = true
	}
	if len(seen) != 3 || !seen["DAILY/DE89370400440532013000/2024-10-01"] || !seen["DAILY/DE89370400440532013000/2024-10-02"] || !seen["DAILY/DE02120300000000202051/2024-10-02"] {
		t.Fatalf("statement ids = %v", seen)
	}
}

func TestParseMT940_Errors(t *testing.T) {
	cases := []struct {
		name string
		in   string
		code string
		line int
	}{
		{"malformed entry", ":20:S1\n:25:X\n:60F:C241001EUR1,00\n:61:BADLINE\n:62F:C241001EUR1,00\n", CodeStatementFormat, 4},
		{"missing closing", ":20:S1\n:25:X\n:60F:C241001EUR1,00\n", CodeStatementFormat, 1},
		{"missing account", ":20:S1\n:60F:C241001EUR1,00\n:62F:C241001EUR1,00\n", CodeStatementFormat, 1},
		{"entry before opening", ":20:S1\n:25:X\n:61:2410011001C5,00NTRFNONREF\n", CodeStatementFormat, 3},
		{"closing currency differs", ":20:S1\n:25:X\n:60F:C241001EUR1,00\n:62F:C241001USD1,00\n", CodeStatementFormat, 4},
		{"tag before :20:", ":25:X\n:20:S1\n", CodeStatementFormat, 1},
		{"no statement", "hello\n", CodeStatementFormat, 1},
		{"bad statement number", ":20:S1\n:25:X\n:28C:ABC\n", CodeStatementFormat, 3},
		{"invalid date", ":20:S1\n:25:X\n:60F:C241301EUR1,00\n", CodeStatementFormat, 3},
		{"lines do not add up", ":20:S1\n:25:X\n:60F:C241001EUR1,00\n:61:2410011001C5,00NTRFNONREF\n:62F:C241001EUR1,00\n", CodeStatementBalance, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseMT940([]byte(tc.in))
			if got := errCode(err); got != tc.code {
				t.Fatalf("want %s, got %q (%v)", tc.code, got, err)
			}
			if got := errLine(err); got != tc.line {
				t.Fatalf("line = %d, want %d (%v)", got, tc.line, err)
			}
		})
	}
}

func TestParseStatements_DetectsFormat(t *testing.T) {
	camt, err := os.ReadFile("../../testdata/camt053/sample-08.xml")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		in     string
		format string
	}{
		{"camt.053", string(camt), "camt.053.001.08"},
		{"bai2", bai2Sample, FormatBAI2},
		{"mt940 with SWIFT blocks", mt940Sample, FormatMT940},
		{"leading blank lines", "\n\n" + bai2Sample, FormatBAI2},
		{"byte order mark", "\xef\xbb\xbf" + bai2Sample, FormatBAI2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stmts, err := ParseStatements([]byte(tc.in))
			if err != nil {
				t.Fatalf("ParseStatements: %v", err)
			}
			if stmts[0].Format != tc.format {
				t.Fatalf("format %q, want %q", stmts[0].Format, tc.format)
			}
		})
	}
	if _, err := ParseStatements([]byte("just text")); errCode(err) != CodeStatementFormat {
		t.Fatalf("unknown content: want TRS-0493, got %v", err)
	}
	// Line numbers count from the top of the file, including skipped blank lines.
	if _, err := ParseStatements([]byte("\n\n01,A,B,1,1/\n77,x/\n")); errLine(err) != 4 {
		t.Fatalf("line = %d, want 4 (%v)", errLine(err), err)
	}
}

func TestParseMT940_LegacyIDIsTheOldReferenceAndNumber(t *testing.T) {
	for in, want := range map[string]string{
		":20:REF\n:25:DE89370400440532013000\n:28C:00007/001\n:60F:C261021EUR1,00\n:62F:C261021EUR1,00\n": "REF/00007/001",
		":20:REF\n:25:DE89370400440532013000\n:60F:C261021EUR1,00\n:62F:C261021EUR1,00\n":                 "REF",
	} {
		st, err := ParseMT940([]byte(in))
		if err != nil || len(st) != 1 || st[0].LegacyID != want {
			t.Errorf("legacy id of %d statements (%v), want %q: %+v", len(st), err, want, st)
		}
	}
}
