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
	if _, err := ValidateStatutory("MX_DIOT", "x", nil); codeOf(err) != CodeInvalid {
		t.Fatalf("want %s for a report without a validator, got %v", CodeInvalid, err)
	}
	if got := len(StatutoryValidatorCodes()); got != 7 {
		t.Fatalf("want 7 validators, got %d", got)
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
