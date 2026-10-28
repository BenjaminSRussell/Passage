package domain_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

var update = flag.Bool("update", false, "rewrite golden files")

const (
	pain001FixtureDir = "../../testdata/pain001"
	xsdSourceFile     = "../../testdata/xsd/SOURCE.md"
)

var (
	requiredFixtures = []string{"ch", "sepa-50", "sepa-structured", "uk", "us", "wire"}
	sha256Pattern    = regexp.MustCompile(`\b[0-9a-f]{64}\b`)
	instdAmtPattern  = regexp.MustCompile(`(<InstdAmt Ccy="[A-Z]{3}">\d+\.\d{2})(</InstdAmt>)`)
	diacriticVendors = map[string]string{
		"Müller Straße GmbH":           "Muller Strasse GmbH",
		"Société Générale Équipements": "Societe Generale Equipements",
		"Øresund Ærø Åkerström AB":     "Oresund AEro Akerstrom AB",
		"Fábrica Señor Núñez S.L.":     "Fabrica Senor Nunez S.L.",
		"Łódź Zakłady Żółć Sp. z o.o.": "Lodz Zaklady Zolc Sp. z o.o.",
	}
)

func requireDecimal(t tester, raw string) decimal.Decimal {
	t.Helper()
	d, err := decimal.NewFromString(raw)
	if err != nil {
		t.Fatalf("decimal %q: %v", raw, err)
	}
	return d
}

func loadFixture(t tester, name string) domain.Pain001Input {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(pain001FixtureDir, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var dto domain.Pain001RequestDTO
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&dto); err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	in, err := domain.ParsePain001Request(dto)
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return in
}

func fixtureNames(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(pain001FixtureDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = strings.TrimSuffix(filepath.Base(f), ".json")
	}
	sort.Strings(names)
	for _, required := range requiredFixtures {
		i := sort.SearchStrings(names, required)
		if i == len(names) || names[i] != required {
			t.Fatalf("required pain.001 fixture %q is missing", required)
		}
	}
	return names
}

func generateFixture(t tester, name string) (domain.Pain001Input, domain.PaymentFile) {
	t.Helper()
	in := loadFixture(t, name)
	file, err := domain.GeneratePain001(in)
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return in, file
}

func TestGeneratePain001_Golden(t *testing.T) {
	for _, name := range fixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			_, file := generateFixture(t, name)
			golden := filepath.Join(pain001FixtureDir, name+".golden.xml")
			if *update {
				if err := os.WriteFile(golden, file.Content, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("golden missing, regenerate with -update: %v", err)
			}
			if normalised(file.Content) != normalised(want) {
				t.Fatalf("generated file differs from %s; regenerate with go test ./internal/domain -run Golden -update", golden)
			}
		})
	}
}

func TestPain001Fixtures_XSD(t *testing.T) {
	for _, name := range fixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			_, file := generateFixture(t, name)
			validateXSD(t, pain001Schema, file.Content)
		})
	}
}

func TestPain001_CorruptedFileFailsXSD(t *testing.T) {
	_, file := generateFixture(t, "wire")
	content := string(file.Content)
	cases := []struct {
		name, path, corrupted string
	}{
		{"six extra decimals", "InstdAmt", instdAmtPattern.ReplaceAllString(content, "${1}123456${2}")},
		{"unknown element", "Bogus", strings.Replace(content, "</GrpHdr>", "<Bogus/></GrpHdr>", 1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.corrupted == content {
				t.Fatal("corruption did not change the file")
			}
			parsed, stderr := runXmllint(t, pain001Schema, []byte(c.corrupted))
			if stderr == "" {
				t.Fatal("corrupted file validated")
			}
			for _, e := range parsed {
				if e.Path == c.path && e.AsError().Code == domain.CodeFileXSD {
					return
				}
			}
			t.Fatalf("no %s error naming %s in %v (stderr %q)", domain.CodeFileXSD, c.path, parsed, stderr)
		})
	}
}

type fixtureDoc struct {
	XMLName xml.Name     `xml:"urn:iso:std:iso:20022:tech:xsd:pain.001.001.09 Document"`
	MsgID   string       `xml:"CstmrCdtTrfInitn>GrpHdr>MsgId"`
	NbOfTxs int          `xml:"CstmrCdtTrfInitn>GrpHdr>NbOfTxs"`
	CtrlSum string       `xml:"CstmrCdtTrfInitn>GrpHdr>CtrlSum"`
	Name    string       `xml:"CstmrCdtTrfInitn>GrpHdr>InitgPty>Nm"`
	Infs    []fixtureInf `xml:"CstmrCdtTrfInitn>PmtInf"`
}

type fixtureInf struct {
	ID       string      `xml:"PmtInfId"`
	NbOfTxs  int         `xml:"NbOfTxs"`
	CtrlSum  string      `xml:"CtrlSum"`
	DbtrName string      `xml:"Dbtr>Nm"`
	Txs      []fixtureTx `xml:"CdtTrfTxInf"`
}

type fixtureTx struct {
	InstrID  string `xml:"PmtId>InstrId"`
	E2EID    string `xml:"PmtId>EndToEndId"`
	Amount   string `xml:"Amt>InstdAmt"`
	CdtrName string `xml:"Cdtr>Nm"`
}

func inProfileCharset(r rune, charset string) bool {
	if charset == domain.CharsetASCII {
		return r >= 0x20 && r <= 0x7e
	}
	return r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/-?:().,'+ ", r))
}

func requireCharset(t *testing.T, field, value, charset string) {
	t.Helper()
	for _, r := range value {
		if !inProfileCharset(r, charset) {
			t.Fatalf("%s %q has %q outside the %s charset", field, value, r, charset)
		}
	}
}

func TestPain001Fixtures_Structure(t *testing.T) {
	for _, name := range fixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			in, file := generateFixture(t, name)
			var doc fixtureDoc
			if err := xml.Unmarshal(file.Content, &doc); err != nil {
				t.Fatal(err)
			}
			charset := in.Profile.Overrides.Charset
			want := decimal.Zero
			for _, p := range in.Payments {
				want = want.Add(p.Amount)
			}
			if doc.NbOfTxs != len(in.Payments) || !requireDecimal(t, doc.CtrlSum).Equal(want) {
				t.Fatalf("group NbOfTxs %d CtrlSum %s, want %d and %s", doc.NbOfTxs, doc.CtrlSum, len(in.Payments), want)
			}
			ids := []string{doc.MsgID}
			names := map[string]string{"InitgPty/Nm": doc.Name}
			txs := 0
			for _, inf := range doc.Infs {
				ids = append(ids, inf.ID)
				names["Dbtr/Nm"] = inf.DbtrName
				sum := decimal.Zero
				for _, tx := range inf.Txs {
					txs++
					ids = append(ids, tx.InstrID, tx.E2EID)
					names["Cdtr/Nm "+tx.E2EID] = tx.CdtrName
					sum = sum.Add(requireDecimal(t, tx.Amount))
				}
				if inf.NbOfTxs != len(inf.Txs) || !requireDecimal(t, inf.CtrlSum).Equal(sum) {
					t.Fatalf("PmtInf %s NbOfTxs %d CtrlSum %s, want %d and %s", inf.ID, inf.NbOfTxs, inf.CtrlSum, len(inf.Txs), sum)
				}
			}
			if txs != len(in.Payments) {
				t.Fatalf("file holds %d transactions, want %d", txs, len(in.Payments))
			}
			for _, id := range ids {
				if !idPattern.MatchString(id) {
					t.Fatalf("id %q breaks BR-19.9", id)
				}
			}
			for field, value := range names {
				requireCharset(t, field, value, charset)
			}
		})
	}
}

func TestPain001Fixtures_Sepa50Shape(t *testing.T) {
	in, file := generateFixture(t, "sepa-50")
	vendors, dates := map[string]bool{}, map[string]bool{}
	for _, p := range in.Payments {
		vendors[p.Creditor.Party.Name] = true
		dates[p.ExecutionDate.Format("2006-01-02")] = true
	}
	if len(in.Payments) != 50 || len(vendors) < 10 || len(dates) < 2 || in.Profile.Overrides.MaxUstrdLength >= 140 {
		t.Fatalf("sepa-50 shape: %d payments, %d vendors, %d dates, max_ustrd_length %d", len(in.Payments), len(vendors), len(dates), in.Profile.Overrides.MaxUstrdLength)
	}
	var doc fixtureDoc
	if err := xml.Unmarshal(file.Content, &doc); err != nil {
		t.Fatal(err)
	}
	generated := map[string]bool{}
	for _, inf := range doc.Infs {
		for _, tx := range inf.Txs {
			generated[tx.CdtrName] = true
		}
	}
	for original, want := range diacriticVendors {
		if !vendors[original] || !generated[want] {
			t.Fatalf("vendor %q should appear in the fixture and as %q in the file", original, want)
		}
	}
}

func TestVendoredXSD_SHA256(t *testing.T) {
	source, err := os.ReadFile(xsdSourceFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{pain001Schema, pain002Schema} {
		file := filepath.Base(schema)
		content, err := os.ReadFile(schema)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content)
		recorded := ""
		for _, line := range strings.Split(string(source), "\n") {
			if strings.Contains(line, file) {
				recorded = sha256Pattern.FindString(line)
			}
		}
		if recorded == "" || recorded != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s sha256 is %x but SOURCE.md records %q", file, sum, recorded)
		}
	}
}
