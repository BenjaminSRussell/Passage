package domain

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestParseCAMT053_SampleFile(t *testing.T) {
	data, err := os.ReadFile("../../testdata/camt053/sample-08.xml")
	if err != nil {
		t.Fatal(err)
	}
	stmts, err := ParseCAMT053(data)
	if err != nil || len(stmts) != 1 {
		t.Fatalf("parse = %d statements, %v", len(stmts), err)
	}
	s := stmts[0]
	if s.Format != "camt.053.001.08" || s.StatementID != "STMT-20261022" || s.Sequence != 412 || s.Currency != "EUR" {
		t.Fatalf("header = %+v", s)
	}
	if s.Account != "DE89370400440532013000" || !s.OpeningAmount.Equal(decimal.RequireFromString("1000")) || !s.ClosingAmount.Equal(decimal.RequireFromString("1337.5")) {
		t.Fatalf("balances = %+v", s)
	}
	if len(s.Lines) != 3 {
		t.Fatalf("lines = %d; want 3", len(s.Lines))
	}
	if !s.Lines[1].Amount.Equal(decimal.RequireFromString("-12.50")) || s.Lines[1].BTCCode != "PMNT/CHRG/BANK" || s.Lines[1].CounterpartyName != "Example Bank AG" {
		t.Fatalf("debit line = %+v", s.Lines[1])
	}
	if s.Lines[0].EndToEndRef != "INV-778812" || s.Lines[0].Reference != "Invoice 778812" || s.Lines[0].CounterpartyName != "Northwind Traders GmbH" {
		t.Fatalf("credit line = %+v", s.Lines[0])
	}
	if !strings.HasPrefix(s.Lines[0].RawFragment, "<Ntry>") || !strings.Contains(s.Lines[0].RawFragment, "INV-778812") {
		t.Fatalf("raw fragment must keep the source entry: %q", s.Lines[0].RawFragment)
	}
}

func TestParseCAMT053_Errors(t *testing.T) {
	header := `<Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.053.001.02"><BkToCstmrStmt><Stmt><Id>S1</Id><Acct><Id><IBAN>DE89370400440532013000</IBAN></Id><Ccy>EUR</Ccy></Acct>`
	bal := func(code, amt, ind string) string {
		return `<Bal><Tp><CdOrPrtry><Cd>` + code + `</Cd></CdOrPrtry></Tp><Amt Ccy="EUR">` + amt + `</Amt><CdtDbtInd>` + ind + `</CdtDbtInd><Dt><Dt>2026-10-22</Dt></Dt></Bal>`
	}
	ntry := func(amt, ind, date string) string {
		return `<Ntry><Amt Ccy="EUR">` + amt + `</Amt><CdtDbtInd>` + ind + `</CdtDbtInd><BookgDt><Dt>` + date + `</Dt></BookgDt></Ntry>`
	}
	footer := `</Stmt></BkToCstmrStmt></Document>`
	tests := []struct {
		name string
		xml  string
		code string
		line int
	}{
		{"not xml", "<Document", CodeStatementFormat, 0},
		{"wrong namespace", `<Document xmlns="urn:other"><BkToCstmrStmt><Stmt><Id>S</Id></Stmt></BkToCstmrStmt></Document>`, CodeStatementFormat, 0},
		{"no statement", `<Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.053.001.08"><BkToCstmrStmt/></Document>`, CodeStatementFormat, 0},
		{"missing closing balance", header + bal("OPBD", "1", "CRDT") + footer, CodeStatementFormat, 0},
		{"bad indicator on entry 2", header + bal("OPBD", "10", "CRDT") + bal("CLBD", "12", "CRDT") + ntry("1", "CRDT", "2026-10-21") + ntry("1", "XXXX", "2026-10-21") + footer, CodeStatementFormat, 2},
		{"bad booking date on entry 1", header + bal("OPBD", "10", "CRDT") + bal("CLBD", "12", "CRDT") + ntry("2", "CRDT", "21/10/2026") + footer, CodeStatementFormat, 1},
		{"balances do not add up", header + bal("OPBD", "10", "CRDT") + bal("CLBD", "99", "CRDT") + ntry("2", "CRDT", "2026-10-21") + footer, CodeStatementBalance, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCAMT053([]byte(tc.xml))
			var de *Error
			if !errors.As(err, &de) || de.Code != tc.code || de.Entry != tc.line {
				t.Fatalf("err = %v; want code %s at entry %d", err, tc.code, tc.line)
			}
		})
	}
}

func TestParseCAMT053_ValueDateFallsBackToBookingDate(t *testing.T) {
	ok := `<Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.053.001.02"><BkToCstmrStmt><Stmt><Id>S1</Id><Acct><Id><IBAN>DE89370400440532013000</IBAN></Id><Ccy>EUR</Ccy></Acct>` +
		`<Bal><Tp><CdOrPrtry><Cd>OPBD</Cd></CdOrPrtry></Tp><Amt Ccy="EUR">10.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><Dt><Dt>2026-10-22</Dt></Dt></Bal>` +
		`<Bal><Tp><CdOrPrtry><Cd>CLBD</Cd></CdOrPrtry></Tp><Amt Ccy="EUR">8.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><Dt><Dt>2026-10-22</Dt></Dt></Bal>` +
		`<Ntry><Amt Ccy="EUR">2.00</Amt><CdtDbtInd>DBIT</CdtDbtInd><BookgDt><Dt>2026-10-21</Dt></BookgDt></Ntry></Stmt></BkToCstmrStmt></Document>`
	stmts, err := ParseCAMT053([]byte(ok))
	if err != nil || len(stmts) != 1 {
		t.Fatalf("parse = %+v, %v", stmts, err)
	}
	if got := stmts[0].Lines[0]; got.ValueDate != "2026-10-21" || !got.Amount.Equal(decimal.RequireFromString("-2")) {
		t.Fatalf("line = %+v", got)
	}
}
