package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/shopspring/decimal"
)

// The AC-35.2 demo set (testdata/peppol): 50 invoices and credit notes built
// by gen_peppol50.py with amounts computed there from the EN 16931 rules, the
// UBL the engine produced for them (xml/), and schematron-report.json, written
// by validate.py from the official Peppol BIS Billing 3.0.21 schematron (CEN
// EN 16931 UBL and Peppol rules) run with Saxon-HE.
//
// Regenerate after an engine change:
//
//	EDI_PEPPOL50_UPDATE=1 go test ./internal/api -run TestPeppol50
//	python -I testdata/peppol/validate.py     # with saxonche; rewrites the report
const peppolDir = "../../testdata/peppol"

type peppolFixture struct {
	Documents []struct {
		Family  string          `json:"family"`
		Request json.RawMessage `json:"request"`
		Expect  struct {
			LineAmounts    []string `json:"lineAmounts"`
			LineExtension  string   `json:"lineExtension"`
			AllowanceTotal string   `json:"allowanceTotal"`
			ChargeTotal    string   `json:"chargeTotal"`
			TaxExclusive   string   `json:"taxExclusive"`
			TaxAmount      string   `json:"taxAmount"`
			TaxInclusive   string   `json:"taxInclusive"`
			Prepaid        string   `json:"prepaid"`
			Payable        string   `json:"payable"`
			Subtotals      []struct {
				Category string `json:"category"`
				Percent  string `json:"percent"`
				Taxable  string `json:"taxable"`
				Tax      string `json:"tax"`
			} `json:"subtotals"`
		} `json:"expect"`
	} `json:"documents"`
}

// ublAmounts is what the test reads back from the built XML. Invoice and
// CreditNote share these element names apart from the line element.
type ublAmounts struct {
	ID       string `xml:"ID"`
	TaxTotal struct {
		TaxAmount string `xml:"TaxAmount"`
		Subtotals []struct {
			Taxable  string `xml:"TaxableAmount"`
			Tax      string `xml:"TaxAmount"`
			Category string `xml:"TaxCategory>ID"`
			Percent  string `xml:"TaxCategory>Percent"`
		} `xml:"TaxSubtotal"`
	} `xml:"TaxTotal"`
	Total struct {
		LineExtension string `xml:"LineExtensionAmount"`
		TaxExclusive  string `xml:"TaxExclusiveAmount"`
		TaxInclusive  string `xml:"TaxInclusiveAmount"`
		Allowances    string `xml:"AllowanceTotalAmount"`
		Charges       string `xml:"ChargeTotalAmount"`
		Prepaid       string `xml:"PrepaidAmount"`
		Payable       string `xml:"PayableAmount"`
	} `xml:"LegalMonetaryTotal"`
	InvoiceLines    []string `xml:"InvoiceLine>LineExtensionAmount"`
	CreditNoteLines []string `xml:"CreditNoteLine>LineExtensionAmount"`
}

func sameAmount(got, want string) bool {
	if got == "" {
		got = "0"
	}
	g, err1 := decimal.NewFromString(got)
	w, err2 := decimal.NewFromString(want)
	return err1 == nil && err2 == nil && g.Equal(w)
}

func TestPeppol50_BuildsEveryDemoInvoiceWithReferenceAmounts(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(peppolDir, "invoices-50.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx peppolFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Documents) != 50 {
		t.Fatalf("fixture has %d documents, want 50", len(fx.Documents))
	}
	update := os.Getenv("EDI_PEPPOL50_UPDATE") == "1"
	for i, doc := range fx.Documents {
		name := fmt.Sprintf("%02d.xml", i+1)
		rec := call(t, http.MethodPost, "/edi/v1/peppol/invoices:build", string(doc.Request), "EINV_OPERATE", nil)
		var res struct {
			XML    string `json:"xml"`
			Valid  bool   `json:"valid"`
			Issues []struct {
				Code   string `json:"code"`
				Detail string `json:"detail"`
			} `json:"issues"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("%s (%s): %d %s", name, doc.Family, rec.Code, rec.Body.String())
		}
		if !res.Valid {
			t.Errorf("%s (%s) refused: %+v", name, doc.Family, res.Issues)
			continue
		}
		var got ublAmounts
		if err := xml.Unmarshal([]byte(res.XML), &got); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		e := doc.Expect
		lines := append(got.InvoiceLines, got.CreditNoteLines...)
		for _, c := range []struct{ field, got, want string }{
			{"LineExtensionAmount", got.Total.LineExtension, e.LineExtension},
			{"AllowanceTotalAmount", got.Total.Allowances, e.AllowanceTotal},
			{"ChargeTotalAmount", got.Total.Charges, e.ChargeTotal},
			{"TaxExclusiveAmount", got.Total.TaxExclusive, e.TaxExclusive},
			{"TaxAmount", got.TaxTotal.TaxAmount, e.TaxAmount},
			{"TaxInclusiveAmount", got.Total.TaxInclusive, e.TaxInclusive},
			{"PrepaidAmount", got.Total.Prepaid, e.Prepaid},
			{"PayableAmount", got.Total.Payable, e.Payable},
		} {
			if !sameAmount(c.got, c.want) {
				t.Errorf("%s (%s) %s = %q, reference %s", name, doc.Family, c.field, c.got, c.want)
			}
		}
		if len(lines) != len(e.LineAmounts) {
			t.Errorf("%s: %d lines, reference %d", name, len(lines), len(e.LineAmounts))
		} else {
			for j := range lines {
				if !sameAmount(lines[j], e.LineAmounts[j]) {
					t.Errorf("%s line %d = %s, reference %s", name, j+1, lines[j], e.LineAmounts[j])
				}
			}
		}
		if len(got.TaxTotal.Subtotals) != len(e.Subtotals) {
			t.Errorf("%s: %d VAT breakdowns, reference %d", name, len(got.TaxTotal.Subtotals), len(e.Subtotals))
		} else {
			for j, st := range got.TaxTotal.Subtotals {
				w := e.Subtotals[j]
				pct := st.Percent
				if st.Category == "O" {
					pct = "0" // BR-O-05: no rate is written
				}
				if st.Category != w.Category || !sameAmount(pct, w.Percent) || !sameAmount(st.Taxable, w.Taxable) || !sameAmount(st.Tax, w.Tax) {
					t.Errorf("%s breakdown %d = %+v, reference %+v", name, j+1, st, w)
				}
			}
		}
		path := filepath.Join(peppolDir, "xml", name)
		if update {
			if err := os.WriteFile(path, []byte(res.XML), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		golden, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run with EDI_PEPPOL50_UPDATE=1, then validate.py)", name, err)
		}
		if string(golden) != res.XML {
			t.Errorf("%s (%s): the engine's XML differs from the schematron-validated file; regenerate and revalidate", name, doc.Family)
		}
	}
}

// The committed XML is exactly what validate.py checked, the official Peppol
// BIS 3.0.21 rules raised no fatal assertion on any of the 50 files, and each
// is valid against the OASIS UBL 2.1 XSD.
func TestPeppol50_MatchesSchematronReport(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(peppolDir, "schematron-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Rules struct {
			Release string            `json:"release"`
			Files   map[string]string `json:"files"`
		} `json:"rules"`
		Documents int `json:"documents"`
		Passed    int `json:"passed"`
		Results   []struct {
			File   string   `json:"file"`
			SHA256 string   `json:"sha256"`
			Fatal  []string `json:"fatal"`
			XSD    *bool    `json:"xsdValid"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Rules.Release != "3.0.21" || len(report.Rules.Files) != 2 {
		t.Fatalf("rules = %+v, want the CEN and Peppol UBL schematron of release 3.0.21", report.Rules)
	}
	if report.Documents != 50 || len(report.Results) != 50 {
		t.Fatalf("report covers %d documents, want 50", len(report.Results))
	}
	for _, r := range report.Results {
		if len(r.Fatal) != 0 {
			t.Errorf("%s: fatal schematron assertions %v", r.File, r.Fatal)
		}
		if r.XSD == nil || !*r.XSD {
			t.Errorf("%s: not validated against the UBL 2.1 XSD, or invalid", r.File)
		}
		data, err := os.ReadFile(filepath.Join(peppolDir, "xml", r.File))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != r.SHA256 {
			t.Errorf("%s changed after it was validated", r.File)
		}
	}
	if report.Passed != 50 {
		t.Errorf("%d of 50 documents pass the schematron", report.Passed)
	}
}
