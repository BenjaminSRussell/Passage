package domain

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const repoPacks = "../../../../countrypacks"

// statutoryFixtures is the fx-statutory-7 manifest: one statutory-report
// fixture per country pack, keyed by pack code.
var statutoryFixtures = map[string]struct{ report, file string }{
	"UK": {"UK_MTD_VAT", "uk/fixtures/mtd-vat-26a1.json"},
	"US": {"US_1099_FIRE", "us/fixtures/fire-1099nec-2025.txt"},
	"DE": {"DE_UStVA", "de/fixtures/ustva-2025-01.xml"},
	"FR": {"FR_FEC", "fr/fixtures/732829320FEC20251231.txt"},
	"IN": {"IN_GSTR1", "in/fixtures/gstr1-082025.json"},
	"MX": {"MX_CFDI40", "mx/fixtures/cfdi40-ingreso.xml"},
	"CA": {"CA_GST34", "ca/fixtures/gst34-2025q3.xml"},
}

func readFixture(t *testing.T, file string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoPacks, file))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// AC1 (AC-48.1): each of the seven country packs ships a statutory-report
// fixture for one of its declared reports, and the fixture validates against
// the regulator's schema or layout with 0 errors.
func TestEveryPackShipsAValidStatutoryFixture(t *testing.T) {
	packs := loadRepoPacks(t)
	if len(packs) != len(statutoryFixtures) {
		t.Fatalf("%d packs, %d fixtures", len(packs), len(statutoryFixtures))
	}
	for code, p := range packs {
		fx, ok := statutoryFixtures[code]
		if !ok {
			t.Errorf("pack %s ships no statutory fixture", code)
			continue
		}
		var report *StatutoryReport
		for i := range p.StatutoryReports {
			if p.StatutoryReports[i].Code == fx.report {
				report = &p.StatutoryReports[i]
			}
		}
		if report == nil {
			t.Errorf("pack %s does not declare report %s", code, fx.report)
			continue
		}
		raw := readFixture(t, fx.file)
		errs, err := ValidateStatutory(fx.report, filepath.Base(fx.file), raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(errs) != 0 {
			t.Errorf("%s fixture %s has %d errors: %+v", code, fx.file, len(errs), errs)
		}
		if report.Format == "FIXED" {
			continue // fixed-position records carry no field names
		}
		lower := strings.ToLower(string(raw))
		for _, f := range report.RequiredFields {
			if !strings.Contains(lower, strings.ToLower(f)) {
				t.Errorf("%s fixture lacks the pack's required field %s", code, f)
			}
		}
	}
}

// Every file in a pack's fixtures directory is in the manifest, so no fixture
// ships unvalidated.
func TestStatutoryFixtureDirectoriesHoldOnlyManifestFiles(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(repoPacks, "*", "fixtures", "*"))
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, fx := range statutoryFixtures {
		listed[fx.file] = true
	}
	for _, fx := range secondaryStatutoryFixtures {
		listed[fx.valid] = true
	}
	var stray []string
	for _, f := range files {
		rel, _ := filepath.Rel(repoPacks, f)
		if filepath.Base(rel) != ".gitattributes" && !listed[filepath.ToSlash(rel)] {
			stray = append(stray, rel)
		}
	}
	sort.Strings(stray)
	if len(stray) != 0 {
		t.Fatalf("fixtures outside the manifest: %v", stray)
	}
}

func TestValidateStatutory_UnknownReport(t *testing.T) {
	if _, err := ValidateStatutory("US_941", "x", nil); codeOf(err) != CodeInvalid {
		t.Fatalf("want %s for a report without a validator, got %v", CodeInvalid, err)
	}
	if got := len(StatutoryValidatorCodes()); got != 10 {
		t.Fatalf("want 10 validators, got %d", got)
	}
}

var secondaryStatutoryFixtures = map[string]struct{ valid, invalid string }{
	"MX_DIOT": {"mx/fixtures/diot-2025-01.txt", "testdata/invalid/diot.txt"},
	"FR_CA3":  {"fr/fixtures/ca3-2025-01.csv", "testdata/invalid/ca3.csv"},
	"IN_26Q":  {"in/fixtures/26q-2025-26-q2.txt", "testdata/invalid/26q.txt"},
}

func TestSecondaryStatutoryFixturesValidate(t *testing.T) {
	for report, fx := range secondaryStatutoryFixtures {
		errs, err := ValidateStatutory(report, filepath.Base(fx.valid), readFixture(t, fx.valid))
		if err != nil || len(errs) != 0 {
			t.Errorf("%s valid fixture: err %v, errors %+v", report, err, errs)
		}
	}
}

func TestSecondaryStatutoryMutations(t *testing.T) {
	cases := []struct{ report, old, repl, field string }{
		{"MX_DIOT", "GOMR800101AB2", "GOMR801301AB2", "line2.rfc"},
		{"MX_DIOT", "ABC8501012A0", "ABC8501012A", "line1.rfc"},
		{"MX_DIOT", "10000||||||||||1600", "10000||||||||||1601", "line1.col22"},
		{"MX_DIOT", "|10000|", "|10000.5|", "line1.col12"},
		{"MX_DIOT", "04|03|", "07|03|", "line2.tipoTercero"},
		{"MX_DIOT", "05|03|", "05|85|", "line3.tipoOperacion"},
		{"MX_DIOT", "|DEU|", "|ZZZ|", "line3.jurisdiccion"},
		{"MX_DIOT", "|DEU|", "|DE|", "line3.pais"},
		{"MX_DIOT", "XAXX010101000", "XEXX010101000", "line5.rfc"},
		{"MX_DIOT", "|02\n", "|03\n", "line4.col54"},
		{"MX_DIOT", "|20000|", "|99999999999999999|", "line3.col16"},
		{"MX_DIOT", "04|85|ABC8501012A0", "04|87|ABC8501012A0", "line1.tipoOperacion"},
		{"FR_CA3", "siren;732829320", "siren;732829321", "siren"},
		{"FR_CA3", "periode;2025-01", "periode;2025-1", "periode"},
		{"FR_CA3", "periode;2025-01", "periode;2025-Q5", "periode"},
		{"FR_CA3", "ligne28;17050", "ligne28;17051", "ligne28"},
		{"FR_CA3", "ligne23;14500", "ligne23;14400", "ligne23"},
		{"FR_CA3", "ligne19;2000\n", "", "ligne19"},
		{"FR_CA3", "ligne20;12000", "ligne20;-12000", "ligne20"},
		{"FR_CA3", "ligne01;150000", "ligne01;150000.50", "ligne01"},
		{"FR_CA3", "ligne09;550", "ligne09;550\nligne09;1", "ligne09"},
		{"IN_26Q", "202526", "202527", "financialYear"},
		{"IN_26Q", "202627", "202526", "assessmentYear"},
		{"IN_26Q", "^Q2^", "^Q5^", "quarter"},
		{"IN_26Q", "^26Q^", "^27Q^", "form"},
		{"IN_26Q", "\n6^DD", "\n9^DD", "lineNo"},
		{"IN_26Q", "1250.00", "1100.00", "BH.totalDeposit"},
		{"IN_26Q", "^ABCPK1234D^", "^ABCPK1234^", "line5.pan"},
		{"IN_26Q", "^ABCPK1234D^", "^ABCXK1234D^", "line5.pan"},
		{"IN_26Q", "^00123^", "^0123^", "line3.challanNo"},
		{"IN_26Q", "^10082025^", "^10102025^", "line7.paymentDate"},
		{"IN_26Q", "^4JB", "^194J", "line5.section"},
		{"IN_26Q", "^94H", "^196A", "line7.section"},
		{"IN_26Q", "^94C", "^194ZZ", "line6.section"},
		{"IN_26Q", "^700.00^", "^701.00^", "line3.totalDeposit"},
		{"IN_26Q", "^500.00^0.00^0.00^30.00", "^500.50^0.00^0.00^30.00", "line4.amount22"},
		{"IN_26Q", "^07082025^", "^07032024^", "line3.depositDate"},
		{"IN_26Q", "^1^^^^^^^^^\n2^BH", "^1^^^^^^^^\n2^BH", "line1.record"},
		{"IN_26Q", "^AABCP1234C^", "^AABCP1234^", "line2.deductorPAN"},
		{"IN_26Q", "^DELA12345B^1^", "^DELA12345B^x^", "line1.batchCount"},
	}
	for _, tc := range cases {
		fx := secondaryStatutoryFixtures[tc.report]
		base := string(readFixture(t, fx.valid))
		if !strings.Contains(base, tc.old) {
			t.Fatalf("%s fixture lacks %q", tc.report, tc.old)
		}
		raw := strings.Replace(base, tc.old, tc.repl, 1)
		errs, err := ValidateStatutory(tc.report, filepath.Base(fx.valid), []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range errs {
			found = found || strings.Contains(e.Field, tc.field)
		}
		if !found {
			t.Errorf("%s %q -> %q: no error on %s: %+v", tc.report, tc.old, tc.repl, tc.field, errs)
		}
	}
}

func TestSecondaryStatutoryAcceptsRegulatorVariants(t *testing.T) {
	cases := []struct{ report, old, repl string }{
		{"FR_CA3", "periode;2025-01", "periode;2025-Q1"},
		{"FR_CA3", "siren;732829320", "siren;356000000"},
		{"IN_26Q", "^AABCP1234C^", "^PANNOTREQD^"},
		{"IN_26Q", "^PANNOTAVBL^", "^PANINVALID^"},
		{"IN_26Q", "^4JB", "^94Q"},
		{"IN_26Q", "^94C", "^4JA"},
		{"IN_26Q", "^94H", "^94R"},
		{"IN_26Q", "^^^^^^^^00123^^^^6360002^^07082025", "^^Y^^^^^^^^^^^^07082025"},
		{"IN_26Q", "^^^^^^^^00123^^^^6360002^^07082025", "^^^^^^^^^^88^^7654321^^07082025"},
		{"IN_26Q", "^07082025^", "^15032025^"},
		{"IN_26Q", "^700.00^0.00^0.00^0.00^0.00^700.00", "^600.00^50.00^20.00^20.00^10.00^700.00"},
		{"MX_DIOT", "|ZZZ|ISLA SIN CATALOGO|", "|ZZZ|OTRA JURISDICCION|"},
	}
	for _, tc := range cases {
		fx := secondaryStatutoryFixtures[tc.report]
		base := string(readFixture(t, fx.valid))
		if !strings.Contains(base, tc.old) {
			t.Fatalf("%s fixture lacks %q", tc.report, tc.old)
		}
		raw := strings.Replace(base, tc.old, tc.repl, 1)
		errs, err := ValidateStatutory(tc.report, filepath.Base(fx.valid), []byte(raw))
		if err != nil || len(errs) != 0 {
			t.Errorf("%s %q -> %q: err %v, errors %+v", tc.report, tc.old, tc.repl, err, errs)
		}
	}
}

func TestForm26QUnparsableChallanAmountDoesNotRaiseBatchTotalError(t *testing.T) {
	fx := secondaryStatutoryFixtures["IN_26Q"]
	raw := strings.Replace(string(readFixture(t, fx.valid)), "^500.00^0.00^0.00^30.00", "^500.50^0.00^0.00^30.00", 1)
	for _, e := range ValidateForm26Q([]byte(raw)) {
		if strings.Contains(e.Field, "BH.totalDeposit") {
			t.Fatalf("spurious batch total error: %+v", e)
		}
	}
}

func TestSecondaryStatutoryInvalidFixturesReportEveryDefect(t *testing.T) {
	want := map[string][]string{
		"MX_DIOT": {"line1.rfc", "line2.tipoOperacion", "line2.pais", "line3.col22", "line5.columns", "line6.rfc", "line7.col54"},
		"FR_CA3":  {"siren", "periode", "ligne16", "ligne25", "ligne28", "ligne99"},
		"IN_26Q": {"line1.record", "FH.batchCount", "BH.tan", "line2.deductorPAN", "line2.assessmentYear", "line3.bsrCode",
			"line3.depositDate", "line3.amount22", "line4.pan", "line4.section", "line4.paymentDate", "line5.section"},
	}
	for report, fx := range secondaryStatutoryFixtures {
		raw, err := os.ReadFile(fx.invalid)
		if err != nil {
			t.Fatal(err)
		}
		errs, err := ValidateStatutory(report, filepath.Base(fx.invalid), raw)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, e := range errs {
			got[e.Field] = true
		}
		for _, f := range want[report] {
			if !got[f] {
				t.Errorf("%s: no error on %s; got %+v", report, f, errs)
			}
		}
	}
}

// The fixtures' derived values (FIRE records and control totals, Kz83, FEC
// balances, GST split and HSN totals, CFDI totals, GST34 lines, check digits)
// are recomputed by an independent stdlib Python reference that shares no
// code with the validators.
func TestStatutoryReferenceScriptAgrees(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH")
	}
	out, err := exec.Command(py, "-I", "testdata/statutory_ref.py", repoPacks).CombinedOutput()
	if err != nil {
		t.Fatalf("reference script disagrees: %v\n%s", err, out)
	}
	if n := strings.Count(string(out), " ok: "); n != 7 {
		t.Fatalf("want 7 fixtures checked, got %d:\n%s", n, out)
	}
}

// MX against SAT's own schema: set PASSAGE_SAT_CFDI_XSD to a local copy of
// cfdv40.xsd whose imports (catCFDI.xsd, tdCFDI.xsd) resolve locally, e.g.
//
//	curl -O http://www.sat.gob.mx/sitio_internet/cfd/4/cfdv40.xsd (and the two imports),
//	then point the schemaLocation attributes at the local files.
//
// The fixture must validate and a mutated copy (19-digit NoCertificado) must not.
func TestMXFixtureAgainstSATXSD(t *testing.T) {
	xsd := os.Getenv("PASSAGE_SAT_CFDI_XSD")
	if xsd == "" {
		t.Skip("PASSAGE_SAT_CFDI_XSD not set")
	}
	xmllint, err := exec.LookPath("xmllint")
	if err != nil {
		t.Skip("xmllint not on PATH")
	}
	good := filepath.Join(repoPacks, statutoryFixtures["MX"].file)
	if out, err := exec.Command(xmllint, "--noout", "--nonet", "--schema", xsd, good).CombinedOutput(); err != nil {
		t.Fatalf("fixture fails the SAT XSD: %v\n%s", err, out)
	}
	bad := filepath.Join(t.TempDir(), "bad.xml")
	raw := strings.Replace(string(readFixture(t, statutoryFixtures["MX"].file)), `NoCertificado="30001000000500003416"`, `NoCertificado="3000100000050000341"`, 1)
	if err := os.WriteFile(bad, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(xmllint, "--noout", "--nonet", "--schema", xsd, bad).CombinedOutput(); err == nil {
		t.Fatalf("the SAT XSD accepted a 19-digit NoCertificado:\n%s", out)
	}
}
