package saft_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
	"github.com/BenjaminSRussell/passage/services/tax/internal/saft"
)

const schemaPath = "../../testdata/saft/saft-2.00-tax-extract.xsd"

func sample() saft.Input {
	return saft.Input{
		CompanyName: "Müller & Söhne <GmbH>", RegistrationNumber: "DE123456789", Country: "DE", Currency: "EUR",
		Period:  domain.Period{From: domain.Day("2026-01-01"), To: domain.Day("2026-01-31")},
		Created: time.Date(2026, 2, 3, 10, 0, 0, 0, time.UTC),
		Entries: []saft.Entry{
			{Code: "VAT19", Direction: domain.DirOutput, Amount: decimal.RequireFromString("150.50"), Count: 2},
			{Code: "VAT19", Direction: domain.DirInput, Amount: decimal.RequireFromString("30.25"), Count: 1},
		},
	}
}

func validate(t *testing.T, doc []byte) (string, error) {
	t.Helper()
	xmllint, err := exec.LookPath("xmllint")
	if err != nil {
		t.Skip("xmllint (libxml2-utils) is not on PATH; skipping SAF-T XSD validation")
	}
	file := filepath.Join(t.TempDir(), "saft.xml")
	if err := os.WriteFile(file, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(xmllint, "--noout", "--nonet", "--schema", schemaPath, file).CombinedOutput()
	return string(out), err
}

func TestExport_ValidatesAgainstTheSchema(t *testing.T) {
	doc, err := saft.Build(sample())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := validate(t, doc); err != nil {
		t.Fatalf("document rejected: %v\n%s\n%s", err, out, doc)
	}
	if !strings.Contains(string(doc), `xmlns="`+saft.Namespace+`"`) || !strings.Contains(string(doc), "Müller &amp; Söhne &lt;GmbH&gt;") {
		t.Fatalf("namespace or escaping wrong:\n%s", doc)
	}
}

func TestExport_EmptyPeriodStillValidates(t *testing.T) {
	in := sample()
	in.Entries = nil
	doc, err := saft.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := validate(t, doc); err != nil {
		t.Fatalf("empty extract rejected: %v\n%s", err, out)
	}
}

func TestSchemaRejectsBrokenDocuments(t *testing.T) {
	doc, err := saft.Build(sample())
	if err != nil {
		t.Fatal(err)
	}
	broken := map[string]string{
		"missing company":    strings.Replace(string(doc), "<Company>", "<Gone>", 1),
		"unknown direction":  strings.Replace(string(doc), "<TaxType>output</TaxType>", "<TaxType>sideways</TaxType>", 1),
		"amount not decimal": strings.Replace(string(doc), "<TaxAmount>150.5</TaxAmount>", "<TaxAmount>lots</TaxAmount>", 1),
		"wrong namespace":    strings.Replace(string(doc), saft.Namespace, "urn:other", 1),
	}
	for name, body := range broken {
		if body == string(doc) {
			t.Fatalf("%s: mutation did not change the document", name)
		}
		t.Run(name, func(t *testing.T) {
			if _, err := validate(t, []byte(body)); err == nil {
				t.Fatal("schema accepted a broken document")
			}
		})
	}
}

func TestBuild_RefusesInvalidHeaderInput(t *testing.T) {
	tests := map[string]func(*saft.Input){
		"no company name":    func(in *saft.Input) { in.CompanyName = " " },
		"no registration":    func(in *saft.Input) { in.RegistrationNumber = "" },
		"lower-case country": func(in *saft.Input) { in.Country = "de" },
		"currency too long":  func(in *saft.Input) { in.Currency = "EURO" },
		"inverted period":    func(in *saft.Input) { in.Period.To = domain.Day("2025-12-31") },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			in := sample()
			mutate(&in)
			if _, err := saft.Build(in); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
}

func line(id, account string, debit, credit string) saft.Line {
	l := saft.Line{RecordID: id, AccountID: account}
	if debit != "" {
		l.Debit = decimal.RequireFromString(debit)
	}
	if credit != "" {
		l.Credit = decimal.RequireFromString(credit)
	}
	return l
}

func withLedger() saft.Input {
	in := sample()
	in.Accounts = []saft.Account{
		{ID: "1200", Description: "Receivables", Type: "asset", Opening: decimal.RequireFromString("10"), Closing: decimal.RequireFromString("129")},
		{ID: "1776", Description: "VAT", Type: "liability", Opening: decimal.RequireFromString("-4"), Closing: decimal.RequireFromString("-23")},
		{ID: "2100", Description: "Payables", Type: "liability"},
		{ID: "4000", Description: "Purchases", Type: "expense"},
		{ID: "8000", Description: "Sales", Type: "revenue"},
	}
	in.Customers = []saft.Party{{ID: "C1", AccountID: "1200", TaxID: "DE111", Name: "Kunde & Co"}}
	in.Suppliers = []saft.Party{{ID: "S1", AccountID: "2100", TaxID: "DE222", Name: "Lieferant"}}
	in.Journals = []saft.Journal{{ID: "J1", Description: "Sales", Type: "GL", Transactions: []saft.Transaction{
		{ID: "T1", Date: time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), Description: "invoice", Lines: []saft.Line{
			{RecordID: "1", AccountID: "1200", CustomerID: "C1", Debit: decimal.RequireFromString("119.00")},
			{RecordID: "2", AccountID: "8000", Credit: decimal.RequireFromString("100.00")},
			{RecordID: "3", AccountID: "1776", Credit: decimal.RequireFromString("19.00")},
		}},
		{ID: "T2", Date: time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC), Description: "bill", Lines: []saft.Line{
			line("1", "4000", "50.00", ""),
			{RecordID: "2", AccountID: "2100", SupplierID: "S1", Credit: decimal.RequireFromString("50.00")},
		}},
	}}}
	return in
}

func TestExport_LedgerAndPartiesValidateAgainstTheSchema(t *testing.T) {
	doc, err := saft.Build(withLedger())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := validate(t, doc); err != nil {
		t.Fatalf("document rejected: %v\n%s\n%s", err, out, doc)
	}
	for _, want := range []string{"<NumberOfEntries>2</NumberOfEntries>", "<TotalDebit>169</TotalDebit>", "<TotalCredit>169</TotalCredit>", "<CustomerID>C1</CustomerID>"} {
		if !strings.Contains(string(doc), want) {
			t.Fatalf("missing %s:\n%s", want, doc)
		}
	}
	if err := saft.CheckDocument(doc); err != nil {
		t.Fatal(err)
	}
}

func TestBuild_RefusesInvalidSections(t *testing.T) {
	tests := map[string]func(*saft.Input){
		"unbalanced transaction": func(in *saft.Input) {
			in.Journals[0].Transactions[0].Lines[2].Credit = decimal.RequireFromString("18.00")
		},
		"line with both sides":     func(in *saft.Input) { in.Journals[0].Transactions[1].Lines[0].Credit = decimal.RequireFromString("1") },
		"line with neither side":   func(in *saft.Input) { in.Journals[0].Transactions[1].Lines[0].Debit = decimal.Zero },
		"negative amount":          func(in *saft.Input) { in.Journals[0].Transactions[1].Lines[0].Debit = decimal.RequireFromString("-50") },
		"single line":              func(in *saft.Input) { in.Journals[0].Transactions[1].Lines = in.Journals[0].Transactions[1].Lines[:1] },
		"missing account":          func(in *saft.Input) { in.Journals[0].Transactions[0].Lines[0].AccountID = "" },
		"duplicate record id":      func(in *saft.Input) { in.Journals[0].Transactions[0].Lines[1].RecordID = "1" },
		"duplicate transaction id": func(in *saft.Input) { in.Journals[0].Transactions[1].ID = "T1" },
		"transaction outside period": func(in *saft.Input) {
			in.Journals[0].Transactions[0].Date = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		},
		"missing journal id":            func(in *saft.Input) { in.Journals[0].ID = "" },
		"unknown customer reference":    func(in *saft.Input) { in.Journals[0].Transactions[0].Lines[0].CustomerID = "C9" },
		"unknown supplier reference":    func(in *saft.Input) { in.Journals[0].Transactions[1].Lines[1].SupplierID = "S9" },
		"customer without tax id":       func(in *saft.Input) { in.Customers[0].TaxID = "" },
		"customer without account":      func(in *saft.Input) { in.Customers[0].AccountID = " " },
		"duplicate customer id":         func(in *saft.Input) { in.Customers = append(in.Customers, in.Customers[0]) },
		"supplier without name":         func(in *saft.Input) { in.Suppliers[0].Name = "" },
		"duplicate supplier id":         func(in *saft.Input) { in.Suppliers = append(in.Suppliers, in.Suppliers[0]) },
		"line account not in the chart": func(in *saft.Input) { in.Journals[0].Transactions[0].Lines[1].AccountID = "9999" },
		"no account list":               func(in *saft.Input) { in.Accounts = nil },
		"duplicate account":             func(in *saft.Input) { in.Accounts = append(in.Accounts, in.Accounts[0]) },
		"account without type":          func(in *saft.Input) { in.Accounts[0].Type = "" },
		"supplier name over 200 char":   func(in *saft.Input) { in.Suppliers[0].Name = strings.Repeat("x", 201) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			in := withLedger()
			mutate(&in)
			_, err := saft.Build(in)
			var de *saft.DocumentError
			if !errors.As(err, &de) {
				t.Fatalf("want a document error, got %v", err)
			}
		})
	}
}

func TestCheckDocument_RejectsBrokenControlTotalsAndStructure(t *testing.T) {
	doc, err := saft.Build(withLedger())
	if err != nil {
		t.Fatal(err)
	}
	broken := map[string][2]string{
		"entry count":       {"<NumberOfEntries>2<", "<NumberOfEntries>3<"},
		"total debit":       {"<TotalDebit>169<", "<TotalDebit>170<"},
		"total credit":      {"<TotalCredit>169<", "<TotalCredit>168<"},
		"total not decimal": {"<TotalDebit>169<", "<TotalDebit>many<"},
		"bad period":        {"<Period>1<", "<Period>13<"},
		"period year skew":  {"<PeriodYear>2026<", "<PeriodYear>2030<"},
		"bad entry stamp":   {"<SystemEntryDate>2026-01-15T00:00:00<", "<SystemEntryDate>yesterday<"},
		"both balances":     {"<OpeningDebitBalance>10</OpeningDebitBalance>", "<OpeningDebitBalance>10</OpeningDebitBalance><OpeningCreditBalance>1</OpeningCreditBalance>"},
		"negative balance":  {"<OpeningDebitBalance>10<", "<OpeningDebitBalance>-10<"},
		"bad date":          {"2026-01-15", "15/01/2026"},
		"debit tampered":    {"<Amount>119<", "<Amount>120<"},
		"wrong namespace":   {saft.Namespace, "urn:other"},
		"not xml":           {"<AuditFile", "<AuditFile<"},
	}
	for name, m := range broken {
		body := strings.Replace(string(doc), m[0], m[1], 1)
		if body == string(doc) {
			t.Fatalf("%s: mutation did not change the document", name)
		}
		t.Run(name, func(t *testing.T) {
			if err := saft.CheckDocument([]byte(body)); err == nil {
				t.Fatal("accepted a broken document")
			}
		})
	}
}

func TestSchemaRejectsBrokenLedgerSections(t *testing.T) {
	doc, err := saft.Build(withLedger())
	if err != nil {
		t.Fatal(err)
	}
	broken := map[string][2]string{
		"missing customer tax id": {"<CustomerTaxID>DE111</CustomerTaxID>", ""},
		"amount not decimal":      {"<Amount>119</Amount>", "<Amount>lots</Amount>"},
		"missing totals":          {"<TotalCredit>169</TotalCredit>", ""},
		"amount without Amount":   {"<Amount>119</Amount>", "119"},
		"missing posting date":    {"<GLPostingDate>2026-01-15</GLPostingDate>", ""},
		"missing opening balance": {"<OpeningDebitBalance>10</OpeningDebitBalance>", ""},
	}
	for name, m := range broken {
		body := strings.Replace(string(doc), m[0], m[1], 1)
		if body == string(doc) {
			t.Fatalf("%s: mutation did not change the document", name)
		}
		t.Run(name, func(t *testing.T) {
			if _, err := validate(t, []byte(body)); err == nil {
				t.Fatal("schema accepted a broken document")
			}
		})
	}
}

func TestCheckDocument_ErrorsNameNoLedgerValues(t *testing.T) {
	in := withLedger()
	in.Journals[0].Transactions[0].ID = "TX-SECRET-77"
	in.Journals[0].Transactions[0].Lines[2].Credit = decimal.RequireFromString("18.123")
	_, err := saft.Build(in)
	if err == nil {
		t.Fatal("accepted an unbalanced transaction")
	}
	for _, leak := range []string{"TX-SECRET-77", "18.123", "119", "1200"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error %q leaks %q", err, leak)
		}
	}
}
