package domain

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func d52(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestParseCAMT052_SampleFile(t *testing.T) {
	data, err := os.ReadFile("../../testdata/camt052/sample-08.xml")
	if err != nil {
		t.Fatal(err)
	}
	reps, err := ParseCAMT052(data)
	if err != nil || len(reps) != 1 {
		t.Fatalf("parse = %d reports, %v", len(reps), err)
	}
	r := reps[0]
	if r.Format != "camt.052.001.08" || r.ReportID != "RPT-20261022-1400" || r.Account != "DE89370400440532013000" || r.Currency != "EUR" {
		t.Fatalf("header = %+v", r)
	}
	if !r.CreatedAt.Equal(time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)) {
		t.Fatalf("created = %v", r.CreatedAt)
	}
	got := map[string]string{}
	for _, b := range r.Balances {
		got[b.Code] = b.Amount.String()
	}
	if len(got) != 3 || got["OPBD"] != "1000" || got["ITBD"] != "1250.5" || got["ITAV"] != "51250.5" {
		t.Fatalf("balances = %v", got)
	}
	for _, b := range r.Balances {
		if b.Code == "ITBD" && !b.AsOf.Equal(time.Date(2026, 10, 22, 13, 55, 0, 0, time.UTC)) {
			t.Fatalf("ITBD as of %v, want its own DtTm", b.AsOf)
		}
		if b.Code == "OPBD" && !b.AsOf.Equal(time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("OPBD as of %v, want the date", b.AsOf)
		}
	}
}

func TestParseCAMT052_DebitBalanceIsNegativeAndOtherCodesAreIgnored(t *testing.T) {
	doc := func(bals string) []byte {
		return []byte(`<Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.052.001.02"><BkToCstmrAcctRpt><Rpt><Id>R1</Id><CreDtTm>2026-10-22T09:00:00</CreDtTm><Acct><Id><Othr><Id>000123</Id></Othr></Id><Ccy>USD</Ccy></Acct>` + bals + `</Rpt></BkToCstmrAcctRpt></Document>`)
	}
	bal := func(code, amt, ind string) string {
		return `<Bal><Tp><CdOrPrtry><Cd>` + code + `</Cd></CdOrPrtry></Tp><Amt Ccy="USD">` + amt + `</Amt><CdtDbtInd>` + ind + `</CdtDbtInd></Bal>`
	}
	reps, err := ParseCAMT052(doc(bal("ITBD", "75.25", "DBIT") + bal("XPCD", "9", "CRDT")))
	if err != nil {
		t.Fatal(err)
	}
	if len(reps[0].Balances) != 1 || !reps[0].Balances[0].Amount.Equal(d52("-75.25")) || reps[0].Account != "000123" {
		t.Fatalf("report = %+v", reps[0])
	}
	if !reps[0].Balances[0].AsOf.Equal(reps[0].CreatedAt) {
		t.Fatalf("a balance without a date is as of the report time, got %v", reps[0].Balances[0].AsOf)
	}
	cases := map[string][]byte{
		"not xml":        []byte("nope"),
		"wrong message":  []byte(`<Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.053.001.02"><BkToCstmrAcctRpt></BkToCstmrAcctRpt></Document>`),
		"no report":      []byte(`<Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.052.001.02"><BkToCstmrAcctRpt></BkToCstmrAcctRpt></Document>`),
		"no useful bal":  doc(bal("XPCD", "1", "CRDT")),
		"bad indicator":  doc(bal("ITBD", "1", "XXXX")),
		"currency clash": []byte(strings.Replace(string(doc(bal("ITBD", "1", "CRDT"))), `Ccy="USD"`, `Ccy="EUR"`, 1)),
	}
	for name, data := range cases {
		_, err := ParseCAMT052(data)
		var de *Error
		if !errors.As(err, &de) || de.Code != CodeStatementFormat {
			t.Errorf("%s: err = %v, want TRS-0493", name, err)
		}
	}
}

func report(at time.Time, bals ...IntradayBalance) *IntradayReport {
	return &IntradayReport{ReportID: "R", CreatedAt: at, Currency: "EUR", Balances: bals}
}

func TestPosition_IntradayWinsOnlyWhenNewerThanTheStatement(t *testing.T) {
	now := time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)
	stmt := &StatementBalance{ClosingDate: "2026-10-21", ClosingAmount: d52("1000")}
	itbd := IntradayBalance{Code: "ITBD", Amount: d52("1250.50")}
	itav := IntradayBalance{Code: "ITAV", Amount: d52("51250.50")}

	p := Position(PositionInput{Account: "A", Currency: "EUR", Statement: stmt, Intraday: report(time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC), itbd, itav)}, now, DefaultStaleHours)
	if p.Source != SourceIntraday || !p.Booked.Equal(d52("1250.50")) || !p.Available.Equal(d52("51250.50")) || p.Stale {
		t.Fatalf("fresh report: %+v", p)
	}
	// A report created before the statement's closing day ended is older than the statement.
	p = Position(PositionInput{Account: "A", Currency: "EUR", Statement: stmt, Intraday: report(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC), itbd)}, now, DefaultStaleHours)
	if p.Source != SourceStatement || !p.Booked.Equal(d52("1000")) || !p.Available.Equal(d52("1000")) {
		t.Fatalf("older report: %+v", p)
	}
	p = Position(PositionInput{Account: "A", Currency: "EUR", Intraday: report(time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC), IntradayBalance{Code: "ITAV", Amount: d52("7")})}, now, DefaultStaleHours)
	if p.Source != SourceIntraday || !p.Booked.Equal(d52("7")) || !p.Available.Equal(d52("7")) {
		t.Fatalf("available only: %+v", p)
	}
}

func TestPosition_StaleAndEmpty(t *testing.T) {
	now := time.Date(2026, 10, 25, 15, 0, 0, 0, time.UTC)
	p := Position(PositionInput{Account: "A", Currency: "EUR", Statement: &StatementBalance{ClosingDate: "2026-10-21", ClosingAmount: d52("5")}}, now, DefaultStaleHours)
	if !p.Stale || p.Source != SourceStatement {
		t.Fatalf("old statement: %+v", p)
	}
	p = Position(PositionInput{Account: "A", Currency: "EUR", Statement: &StatementBalance{ClosingDate: "2026-10-24", ClosingAmount: d52("5")}}, now, DefaultStaleHours)
	if p.Stale {
		t.Fatalf("yesterday's statement is within 24 hours of the end of its day: %+v", p)
	}
	p = Position(PositionInput{Account: "A", Currency: "EUR"}, now, DefaultStaleHours)
	if p.Source != SourceNone || !p.Stale || p.AsOf != nil || !p.Booked.IsZero() {
		t.Fatalf("no data: %+v", p)
	}
}

func TestTotals_NeverMixCurrencies(t *testing.T) {
	got := Totals([]AccountPosition{
		{Currency: "USD", Booked: d52("10"), Available: d52("12")},
		{Currency: "EUR", Booked: d52("5"), Available: d52("5")},
		{Currency: "USD", Booked: d52("-3"), Available: d52("-3")},
	})
	if len(got) != 2 || got[0].Currency != "EUR" || !got[1].Booked.Equal(d52("7")) || !got[1].Available.Equal(d52("9")) {
		t.Fatalf("totals = %+v", got)
	}
}
