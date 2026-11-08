package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/api"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
	"github.com/BenjaminSRussell/passage/services/org/migrations"
)

func (m *memOrg) ListUnits(context.Context, store.UnitFilter) ([]store.Unit, error) {
	return append([]store.Unit{{Code: "kg", Name: "Kilogram", Class: "mass", DecimalPrecision: 3, System: true, Status: "active"},
		{Code: "lb", Name: "Pound", Class: "mass", DecimalPrecision: 3, System: true, Status: "active"}}, m.refUnits...), nil
}

func (m *memOrg) CreateUnit(_ context.Context, in store.Unit, _ string) (store.Unit, error) {
	m.id()
	in.Status = "active"
	m.refUnits = append(m.refUnits, in)
	return in, nil
}

func (m *memOrg) ListConversions(context.Context, store.ConversionFilter) ([]store.Conversion, error) {
	return append([]store.Conversion{{FromCode: "kg", ToCode: "lb", Class: "mass", Factor: decimal.RequireFromString("2.20462262185"), System: true}}, m.refConvs...), nil
}

func (m *memOrg) CreateConversion(_ context.Context, in store.Conversion, _ string) (store.Conversion, bool, error) {
	m.id()
	for _, u := range m.refUnits {
		if u.Code == in.FromCode {
			in.Class = u.Class
		}
	}
	m.refConvs = append(m.refConvs, in)
	return in, true, nil
}

func (m *memOrg) ListCurrencyRoundings(context.Context, string) ([]store.CurrencyRounding, error) {
	return m.rules, nil
}

func (m *memOrg) PutCurrencyRounding(_ context.Context, in store.CurrencyRounding) (store.CurrencyRounding, bool, error) {
	in.ID = m.id()
	m.rules = append(m.rules, in)
	return in, true, nil
}

func (m *memOrg) ListNameTranslations(context.Context, string, string) ([]store.NameTranslation, error) {
	return m.names, nil
}

func (m *memOrg) PutNameTranslation(_ context.Context, ownerType, ownerID, language, name string) (store.NameTranslation, bool, error) {
	m.id()
	row := store.NameTranslation{OwnerType: ownerType, OwnerID: ownerID, Language: language, Name: name}
	m.names = append(m.names, row)
	return row, true, nil
}

func (m *memOrg) ListUnitsOnly() int { return len(m.refUnits) }

// refBody adds the four reference kinds to packageBody: 2 units, 1 conversion, 2 rounding rules, 3 translations.
var refBody = strings.TrimSuffix(packageBody, "}") + `,
 "units":[{"code":"BOX","name":"Box","class":"each","decimalPrecision":0},{"code":"CASE","name":"Case","class":"each","decimalPrecision":0}],
 "conversions":[{"fromCode":"CASE","toCode":"BOX","factor":"12"}],
 "currencyRoundingRules":[{"currency":"USD","purpose":"general","unit":"0.05","method":"half_up"},{"currency":"EUR","purpose":"price","legalEntity":"demf","unit":"0.01","method":"half_even"}],
 "nameTranslations":[{"ownerType":"legal_entity","owner":"DEMF","language":"de","name":"Demo Deutschland"},
   {"ownerType":"operating_unit","owner":"department/fin","language":"de","name":"Finanzen"},
   {"ownerType":"warehouse","owner":"usmf/det-qa","language":"de","name":"Quarantaene"}]}`

const refRecords = 8

func TestImportPackage_ReferenceKinds(t *testing.T) {
	mem := &memOrg{}
	r, _ := memRouter(mem)
	code, first, raw := importPackage(t, r, "", refBody)
	if code != http.StatusOK || first.Created != packageRecords+refRecords {
		t.Fatalf("first import = %d %+v %v", code, first, raw)
	}
	for kind, want := range map[string]int{"units": 2, "conversions": 1, "currencyRoundingRules": 2, "nameTranslations": 3} {
		if first.Kinds[kind].Created != want {
			t.Fatalf("%s = %+v", kind, first.Kinds[kind])
		}
	}
	if len(mem.rules) != 2 || mem.rules[0].LegalEntityID != nil || mem.rules[1].LegalEntityID == nil || *mem.rules[1].LegalEntityID != mem.entities[1].ID {
		t.Fatalf("rounding rules = %+v", mem.rules)
	}
	if len(mem.names) != 3 || mem.names[1].OwnerID != mem.units[0].ID {
		t.Fatalf("translations = %+v", mem.names)
	}
	writes := mem.creates
	code, second, raw := importPackage(t, r, "", refBody)
	if code != http.StatusOK || second.Created != 0 || second.Unchanged != packageRecords+refRecords || len(second.Differences) != 0 || mem.creates != writes {
		t.Fatalf("second import = %d %+v %v", code, second, raw)
	}
}

func TestImportPackage_ReferenceKindsDryRunMatchesRealRun(t *testing.T) {
	mem := &memOrg{}
	r, _ := memRouter(mem)
	code, dry, raw := importPackage(t, r, "?dryRun=true", refBody)
	if code != http.StatusOK || mem.creates != 0 || len(mem.names) != 0 || len(mem.rules) != 0 || mem.ListUnitsOnly() != 0 {
		t.Fatalf("dry run = %d %+v %v", code, dry, raw)
	}
	_, real, _ := importPackage(t, r, "", refBody)
	if dry.Created != real.Created || dry.Kinds["nameTranslations"] != real.Kinds["nameTranslations"] || dry.Kinds["conversions"] != real.Kinds["conversions"] {
		t.Fatalf("dry %+v != real %+v", dry, real)
	}
}

func TestExportPackage_ReferenceKindsRoundTrip(t *testing.T) {
	source := &memOrg{}
	r, _ := memRouter(source)
	if code, _, raw := importPackage(t, r, "", refBody); code != http.StatusOK {
		t.Fatal(raw)
	}
	rec := do(r, http.MethodPost, "/v1/org/export", "", packageDuties, true)
	var exported struct {
		Units                 []map[string]any `json:"units"`
		Conversions           []map[string]any `json:"conversions"`
		CurrencyRoundingRules []map[string]any `json:"currencyRoundingRules"`
		NameTranslations      []map[string]any `json:"nameTranslations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &exported); err != nil {
		t.Fatal(err)
	}
	if len(exported.Units) != 2 || exported.Units[0]["code"] != "BOX" || len(exported.Conversions) != 1 || exported.Conversions[0]["factor"] != "12" {
		t.Fatalf("units or conversions = %+v", exported)
	}
	if len(exported.CurrencyRoundingRules) != 2 || exported.CurrencyRoundingRules[0]["legalEntity"] != "DEMF" || exported.CurrencyRoundingRules[1]["legalEntity"] != nil {
		t.Fatalf("rounding rules = %+v", exported.CurrencyRoundingRules)
	}
	owners := []string{}
	for _, tr := range exported.NameTranslations {
		owners = append(owners, fmt.Sprint(tr["ownerType"], " ", tr["owner"]))
	}
	if strings.Join(owners, ",") != "legal_entity DEMF,operating_unit department/FIN,warehouse USMF/DET-QA" {
		t.Fatalf("translation owners = %v", owners)
	}
	target := &memOrg{}
	r2, _ := memRouter(target)
	code, report, raw := importPackage(t, r2, "", rec.Body.String())
	if code != http.StatusOK || report.Created != packageRecords+refRecords {
		t.Fatalf("import of export = %d %+v %v", code, report, raw)
	}
}

func TestImportPackage_ReferenceKindsReportDifferencesAndLeaveRecordsAlone(t *testing.T) {
	mem := &memOrg{}
	r, _ := memRouter(mem)
	importPackage(t, r, "", refBody)
	changed := strings.NewReplacer(`"name":"Box"`, `"name":"Carton"`, `"factor":"12"`, `"factor":"6"`, `"unit":"0.05"`, `"unit":"0.10"`, `"name":"Finanzen"`, `"name":"Finanz"`).Replace(refBody)
	code, report, raw := importPackage(t, r, "", changed)
	if code != http.StatusOK || report.Created != 0 {
		t.Fatalf("= %d %+v %v", code, report, raw)
	}
	got := map[string]bool{}
	for _, d := range report.Differences {
		got[d.Kind+" "+d.Code+" "+d.Field] = true
	}
	for _, want := range []string{"units BOX name", "conversions BOX|CASE factor", "currencyRoundingRules USD/general unit", "nameTranslations operating_unit/department/FIN/de name"} {
		if !got[want] {
			t.Fatalf("missing difference %q in %+v", want, report.Differences)
		}
	}
	if mem.refUnits[0].Name != "Box" || mem.names[1].Name != "Finanzen" {
		t.Fatalf("import changed existing records: %+v %+v", mem.refUnits, mem.names)
	}
}

func TestImportPackage_ReferenceKindRejections(t *testing.T) {
	cases := []struct {
		name, body, path, message string
	}{
		{"unit class", strings.Replace(refBody, `"class":"each"`, `"class":"nope"`, 1), "units[0].class", "must be one of"},
		{"unit code", strings.Replace(refBody, `"code":"BOX"`, `"code":"B O X"`, 1), "units[0].code", "1 to 10 characters"},
		{"unit precision", strings.Replace(refBody, `"decimalPrecision":0}`, `"decimalPrecision":9}`, 1), "units[0].decimalPrecision", "0 to 6"},
		{"duplicate unit", strings.Replace(refBody, `"code":"CASE","name"`, `"code":"BOX","name"`, 1), "units[1].code", "duplicate unit"},
		{"conversion factor", strings.Replace(refBody, `"factor":"12"`, `"factor":"0"`, 1), "conversions[0].factor", "greater than 0"},
		{"conversion same unit", strings.Replace(refBody, `"toCode":"BOX"`, `"toCode":"CASE"`, 1), "conversions[0].toCode", "must differ"},
		{"conversion unknown unit", strings.Replace(refBody, `"toCode":"BOX"`, `"toCode":"NOPE"`, 1), "conversions[0].toCode", "unknown unit NOPE"},
		{"conversion class mismatch", strings.Replace(refBody, `"toCode":"BOX"`, `"toCode":"kg"`, 1), "conversions[0]", "each and kg is mass"},
		{"rounding purpose", strings.Replace(refBody, `"purpose":"general"`, `"purpose":"nope"`, 1), "currencyRoundingRules[0].purpose", "general, price"},
		{"rounding method", strings.Replace(refBody, `"method":"half_up"`, `"method":"nope"`, 1), "currencyRoundingRules[0].method", "half_even"},
		{"rounding unit not a power of ten", strings.Replace(refBody, `"unit":"0.05"`, `"unit":"0.03"`, 1), "currencyRoundingRules[0].unit", "power of ten"},
		{"rounding unknown legal entity", strings.Replace(refBody, `"legalEntity":"demf"`, `"legalEntity":"nope"`, 1), "currencyRoundingRules[1].legalEntity", "unknown legal entity"},
		{"rounding unknown currency", strings.Replace(refBody, `"currency":"USD","purpose"`, `"currency":"XXX","purpose"`, 1), "currencyRoundingRules[0].currency", "unknown currency"},
		{"translation owner type", strings.Replace(refBody, `"ownerType":"warehouse"`, `"ownerType":"team"`, 1), "nameTranslations[2].owner", "ownerType must be"},
		{"translation unknown owner", strings.Replace(refBody, `"owner":"usmf/det-qa"`, `"owner":"usmf/nope"`, 1), "nameTranslations[2].owner", "unknown warehouse"},
		{"translation language", strings.Replace(refBody, `"language":"de","name":"Finanzen"`, `"language":"DE","name":"Finanzen"`, 1), "nameTranslations[1].language", "language tag"},
		{"translation inactive language", strings.Replace(refBody, `"language":"de","name":"Finanzen"`, `"language":"fr","name":"Finanzen"`, 1), "nameTranslations[1].language", "unknown or inactive"},
		{"duplicate translation", strings.Replace(refBody, `"ownerType":"operating_unit","owner":"department/fin"`, `"ownerType":"legal_entity","owner":"demf"`, 1), "nameTranslations[1]", "duplicate name translation"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mem := &memOrg{}
			r, _ := memRouter(mem)
			code, _, raw := importPackage(t, r, "", c.body)
			errs, _ := raw["errors"].(map[string]any)
			if code != http.StatusUnprocessableEntity || raw["code"] != "ORG-PKG-INVALID" || !strings.Contains(fmt.Sprint(errs[c.path]), c.message) {
				t.Fatalf("= %d %v", code, raw)
			}
			if mem.creates != 0 {
				t.Fatalf("a refused package wrote %d records", mem.creates)
			}
		})
	}
}

// Units, conversions, rounding rules and name translations move through PostgreSQL under
// row-level security: the legal-entity scoped translations and rules are read by an
// administrator without a legal-entity claim, and a second import creates nothing.
func TestOrgPackage_Postgres_ReferenceKindsRoundTrip(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedReference(ctx, admin, migrations.Reference); err != nil {
		t.Fatal(err)
	}
	r, _ := memRouterFor(store.New(app))
	first := do(r, http.MethodPost, "/v1/org/import", refBody, packageDuties, true)
	var report api.ImportReport
	if err := json.Unmarshal(first.Body.Bytes(), &report); err != nil || first.Code != http.StatusOK || report.Created != packageRecords+refRecords {
		t.Fatalf("first import = %d %s", first.Code, first.Body)
	}
	second := do(r, http.MethodPost, "/v1/org/import", refBody, packageDuties, true)
	var again api.ImportReport
	if err := json.Unmarshal(second.Body.Bytes(), &again); err != nil || second.Code != http.StatusOK || again.Created != 0 || again.Unchanged != packageRecords+refRecords || len(again.Differences) != 0 {
		t.Fatalf("second import = %d %s", second.Code, second.Body)
	}
	exported := do(r, http.MethodPost, "/v1/org/export", "", packageDuties, true)
	if exported.Code != http.StatusOK || len(mustList(t, exported.Body.Bytes(), "units")) != 2 || len(mustList(t, exported.Body.Bytes(), "conversions")) != 1 ||
		len(mustList(t, exported.Body.Bytes(), "currencyRoundingRules")) != 2 || len(mustList(t, exported.Body.Bytes(), "nameTranslations")) != 3 {
		t.Fatalf("export = %d %s", exported.Code, exported.Body)
	}
	other := do(r, http.MethodPost, "/v1/org/import", exported.Body.String(), packageDuties, true, "X-Passage-Tenant", testkit.TenantID(2))
	var otherReport api.ImportReport
	if err := json.Unmarshal(other.Body.Bytes(), &otherReport); err != nil || other.Code != http.StatusOK || otherReport.Created != packageRecords+refRecords {
		t.Fatalf("import into another tenant = %d %s", other.Code, other.Body)
	}
	// Tenant two's rows are its own: it exports the same three translations, naming its own owners.
	again2 := do(r, http.MethodPost, "/v1/org/export", "", packageDuties, true, "X-Passage-Tenant", testkit.TenantID(2))
	if again2.Code != http.StatusOK || len(mustList(t, again2.Body.Bytes(), "nameTranslations")) != 3 || len(mustList(t, again2.Body.Bytes(), "currencyRoundingRules")) != 2 {
		t.Fatalf("tenant two export = %d %s", again2.Code, again2.Body)
	}
}

// A conversion the tenant already holds in the other direction is the same pair: unchanged,
// never a second conversion, and its factor is not compared against the package's.
func TestImportPackage_ConversionHeldInReverseIsUnchanged(t *testing.T) {
	mem := &memOrg{
		refUnits: []store.Unit{{Code: "BOX", Name: "Box", Class: "each", Status: "active"}, {Code: "CASE", Name: "Case", Class: "each", Status: "active"}},
		refConvs: []store.Conversion{{FromCode: "BOX", ToCode: "CASE", Class: "each", Factor: decimal.RequireFromString("0.083333333333")}},
	}
	r, _ := memRouter(mem)
	code, report, raw := importPackage(t, r, "", refBody)
	if code != http.StatusOK || report.Kinds["conversions"].Created != 0 || report.Kinds["conversions"].Unchanged != 1 || len(mem.refConvs) != 1 {
		t.Fatalf("= %d %+v %v", code, report, raw)
	}
	for _, d := range report.Differences {
		if d.Kind == "conversions" {
			t.Fatalf("reverse conversion reported as differing: %+v", d)
		}
	}
}

// A conversion naming a unit the tenant has deactivated is refused before the first write.
func TestImportPackage_ConversionOfInactiveUnitIsRefused(t *testing.T) {
	mem := &memOrg{refUnits: []store.Unit{{Code: "BOX", Name: "Box", Class: "each", Status: "inactive"}}}
	r, _ := memRouter(mem)
	code, _, raw := importPackage(t, r, "", refBody)
	errs, _ := raw["errors"].(map[string]any)
	if code != http.StatusUnprocessableEntity || !strings.Contains(fmt.Sprint(errs["conversions[0].toCode"]), "unit BOX is inactive") || mem.creates != 0 {
		t.Fatalf("= %d %v (creates %d)", code, raw, mem.creates)
	}
}
