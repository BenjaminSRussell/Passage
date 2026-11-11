package domain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The builder's output that the official schematron passed offline (testdata/peppol) passes the Go subset too.
func TestCheckPeppolRules_PassesTheSchematronValidatedDocuments(t *testing.T) {
	files, err := filepath.Glob("../../testdata/peppol/xml/*.xml")
	if err != nil || len(files) != 50 {
		t.Fatalf("files: %d %v", len(files), err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if issues := CheckPeppolRules(string(raw)); len(issues) != 0 {
			t.Errorf("%s: %+v", filepath.Base(f), issues)
		}
	}
}

func TestCheckPeppolRules_CatchesBrokenDocuments(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/peppol/xml/01.xml")
	if err != nil {
		t.Fatal(err)
	}
	good := string(raw)
	for _, tc := range []struct{ name, from, to, rule string }{
		{"no buyer reference", "<cbc:BuyerReference>BR-1001</cbc:BuyerReference>", "", "PEPPOL-EN16931-R003"},
		{"wrong profile", "billing:01:1.0", "billing:02:1.0", "PEPPOL-EN16931-R001"},
		{"no currency", "<cbc:DocumentCurrencyCode>EUR</cbc:DocumentCurrencyCode>", "", "BR-05"},
		{"payable off", "<cbc:PayableAmount currencyID=\"EUR\">2889.45", "<cbc:PayableAmount currencyID=\"EUR\">2889.46", "BR-CO-16"},
		{"vat off", "<cbc:TaxAmount currencyID=\"EUR\">461.34</cbc:TaxAmount><cac:TaxCategory>", "<cbc:TaxAmount currencyID=\"EUR\">461.35</cbc:TaxAmount><cac:TaxCategory>", "BR-CO-17"},
		{"line sum off", "<cbc:LineExtensionAmount currencyID=\"EUR\">2428.11</cbc:LineExtensionAmount><cbc:TaxExclusiveAmount", "<cbc:LineExtensionAmount currencyID=\"EUR\">2428.12</cbc:LineExtensionAmount><cbc:TaxExclusiveAmount", "BR-CO-10"},
		{"no unit", " unitCode=\"KGM\"", "", "BR-23"},
		{"bad gln", ">0614141000029<", ">0614141000028<", "PEPPOL-COMMON-R040"},
	} {
		doc := strings.Replace(good, tc.from, tc.to, 1)
		if doc == good {
			t.Fatalf("%s: the fixture does not contain the text to change", tc.name)
		}
		found := false
		for _, is := range CheckPeppolRules(doc) {
			found = found || strings.HasPrefix(is.Detail, tc.rule)
		}
		if !found {
			t.Errorf("%s: want %s, got %+v", tc.name, tc.rule, CheckPeppolRules(doc))
		}
	}
	if is := CheckPeppolRules("<Invoice"); len(is) != 1 {
		t.Errorf("not well-formed: %+v", is)
	}
}
