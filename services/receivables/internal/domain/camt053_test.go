package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

const camtSample = `<?xml version="1.0" encoding="UTF-8"?>
<Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.053.001.08">
  <BkToCstmrStmt>
    <GrpHdr><MsgId>MSG1</MsgId></GrpHdr>
    <Stmt>
      <Id>STMT-0327</Id>
      <Acct><Id><IBAN>DE89370400440532013000</IBAN></Id></Acct>
      <Ntry>
        <Amt Ccy="EUR">2380.00</Amt>
        <CdtDbtInd>CRDT</CdtDbtInd>
        <ValDt><Dt>2027-03-20</Dt></ValDt>
        <NtryRef>E2E-1</NtryRef>
        <NtryDtls><TxDtls>
          <Refs><EndToEndId>NOTPROVIDED</EndToEndId></Refs>
          <RltdPties><Dbtr><Nm>Acme GmbH</Nm></Dbtr></RltdPties>
          <RmtInf><Strd><CdtrRefInf><Ref>INV-2001</Ref></CdtrRefInf></Strd></RmtInf>
        </TxDtls></NtryDtls>
      </Ntry>
      <Ntry>
        <Amt Ccy="EUR">75.00</Amt>
        <CdtDbtInd>DBIT</CdtDbtInd>
        <ValDt><Dt>2027-03-20</Dt></ValDt>
        <NtryRef>E2E-2</NtryRef>
      </Ntry>
    </Stmt>
  </BkToCstmrStmt>
</Document>`

func TestParseCAMT053_ReturnsCreditsOnly(t *testing.T) {
	res, err := ParseCAMT053([]byte(camtSample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Statements != 1 || res.Skipped != 1 || len(res.Credits) != 1 {
		t.Fatalf("result = %+v", res)
	}
	c := res.Credits[0]
	if c.StatementID != "STMT-0327" || c.Reference != "E2E-1" || c.Currency != "EUR" ||
		c.DebtorName != "Acme GmbH" || c.RemittanceRef != "INV-2001" || c.ValueDate != "2027-03-20" {
		t.Errorf("credit = %+v", c)
	}
	if !c.Amount.Equal(decimal.RequireFromString("2380.00")) {
		t.Errorf("amount = %s", c.Amount)
	}
	if c.Line == 0 {
		t.Errorf("credit has no line number")
	}
}

func TestParseCAMT053_MalformedEntriesReportLines(t *testing.T) {
	tests := []struct {
		name    string
		replace [2]string
		line    int
		want    string
	}{
		// Entry-level errors report the line of the <Ntry> element (line 8 in camtSample).
		{"bad amount", [2]string{`>2380.00<`, `>23.8.00<`}, 8, "Amt must"},
		{"zero amount", [2]string{`>2380.00<`, `>0.00<`}, 8, "Amt must"},
		{"bad currency", [2]string{`Ccy="EUR">2380`, `Ccy="euro">2380`}, 8, "Ccy"},
		{"bad indicator", [2]string{`>CRDT<`, `>CREDIT<`}, 8, "CdtDbtInd"},
		{"bad value date", [2]string{`2027-03-20</Dt></ValDt>
        <NtryRef>E2E-1`, `20/03/2027</Dt></ValDt>
        <NtryRef>E2E-1`}, 8, "ValDt"},
		{"credit without reference", [2]string{`<NtryRef>E2E-1</NtryRef>`, ``}, 8, "NtryRef"},
		{"empty statement id", [2]string{`<Id>STMT-0327</Id>`, `<Id> </Id>`}, 6, "statement Id"},
		{"broken XML", [2]string{`</Ntry>
      <Ntry>`, `</Ntry>
      <Ntry`}, 20, "malformed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := strings.Replace(camtSample, tc.replace[0], tc.replace[1], 1)
			_, err := ParseCAMT053([]byte(doc))
			var ce *CAMTError
			if !errors.As(err, &ce) {
				t.Fatalf("err = %v, want CAMTError", err)
			}
			if !strings.Contains(ce.Message, tc.want) {
				t.Errorf("message = %q, want it to mention %q", ce.Message, tc.want)
			}
			if ce.Line != tc.line {
				t.Errorf("line = %d, want %d (%v)", ce.Line, tc.line, err)
			}
		})
	}
}

func TestParseCAMT053_RejectsNonStatements(t *testing.T) {
	for _, doc := range []string{"", "not xml at all <", `<Document><BkToCstmrStmt/></Document>`} {
		if _, err := ParseCAMT053([]byte(doc)); err == nil {
			t.Errorf("%q: want an error", doc)
		}
	}
}

// camt.053.001.02 spells the status as text and the debtor without Pty; .001.08
// nests both. Pending entries and reversals are not customer credits.
func TestParseCAMT053_VersionsStatusAndReversals(t *testing.T) {
	doc := `<Document><BkToCstmrStmt><Stmt><Id>S-1</Id>
<Ntry><NtryRef>A</NtryRef><Amt Ccy="USD">10.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><Sts>BOOK</Sts><BookgDt><DtTm>2027-04-01T08:00:00</DtTm></BookgDt>
  <NtryDtls><TxDtls><RltdPties><Dbtr><Pty><Nm>Beta Inc</Nm></Pty></Dbtr></RltdPties><RmtInf><Ustrd> INV-7 </Ustrd></RmtInf></TxDtls></NtryDtls></Ntry>
<Ntry><NtryRef>B</NtryRef><Amt Ccy="USD">20.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><Sts><Cd>PDNG</Cd></Sts><ValDt><Dt>2027-04-01</Dt></ValDt></Ntry>
<Ntry><NtryRef>C</NtryRef><Amt Ccy="USD">30.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><RvslInd>true</RvslInd><ValDt><Dt>2027-04-01</Dt></ValDt></Ntry>
<Ntry><AcctSvcrRef>BANK-9</AcctSvcrRef><Amt Ccy="USD">40.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><Sts><Cd>BOOK</Cd></Sts><ValDt><Dt>2027-04-02</Dt></ValDt></Ntry>
</Stmt><Stmt><Id>S-2</Id>
<Ntry><NtryRef>A</NtryRef><Amt Ccy="EUR">5.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><ValDt><Dt>2027-04-03</Dt></ValDt></Ntry>
</Stmt></BkToCstmrStmt></Document>`
	res, err := ParseCAMT053([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Statements != 2 || res.Skipped != 2 || len(res.Credits) != 3 {
		t.Fatalf("result = %+v, want 2 statements, 3 credits and the pending and reversed entries skipped", res)
	}
	first, bankRef, second := res.Credits[0], res.Credits[1], res.Credits[2]
	if first.DebtorName != "Beta Inc" || first.RemittanceRef != "INV-7" || first.ValueDate != "2027-04-01" {
		t.Errorf("first = %+v, want the .001.08 debtor, trimmed Ustrd and the booking date", first)
	}
	if bankRef.Reference != "BANK-9" || bankRef.StatementID != "S-1" {
		t.Errorf("second = %+v, want AcctSvcrRef when NtryRef is absent", bankRef)
	}
	if second.StatementID != "S-2" || second.Reference != "A" || second.Currency != "EUR" {
		t.Errorf("third = %+v, want the second statement's id: (statement id, entry ref) is the key", second)
	}
}

// Whatever the bytes, the parser returns a result or a CAMTError; it never panics.
func TestParseCAMT053_NeverPanics(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cut := rapid.IntRange(0, len(camtSample)).Draw(t, "cut")
		doc := camtSample[:cut] + rapid.String().Draw(t, "tail")
		_, err := ParseCAMT053([]byte(doc))
		var ce *CAMTError
		if err != nil && !errors.As(err, &ce) {
			t.Fatalf("error %v is not a CAMTError", err)
		}
	})
}
