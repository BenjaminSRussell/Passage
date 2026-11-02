package saft_test

import (
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
