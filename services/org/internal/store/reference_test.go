package store_test

import (
	"context"
	"encoding/csv"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
	"github.com/BenjaminSRussell/passage/services/org/migrations"
)

const (
	countryHeader  = "code,name,region_required,postal_regex\n"
	currencyHeader = "code,numeric_code,name,minor_units\n"
	validCountry   = "US,United States,true,^[0-9]{5}$\n"
	validCurrency  = "USD,840,US Dollar,2\n"
	regionHeader   = "code,country_code,name\n"
	languageHeader = "code,name\n"
	validRegion    = "US-CA,US,California\n"
	validLanguage  = "en,English\n"

	unitHeader       = "code,name,class,decimal_precision\n"
	conversionHeader = "from_code,to_code,factor,offset_value\n"
	validUnits       = "kg,Kilogram,mass,3\ng,Gram,mass,3\nm,Metre,length,3\n"
	validConversion  = "kg,g,1000,0\n"
)

func referenceFiles(countries, currencies, regions, languages, units, conversions string) fstest.MapFS {
	return fstest.MapFS{
		"reference/units.csv":           {Data: []byte(units)},
		"reference/uom-conversions.csv": {Data: []byte(conversions)},
		"reference/countries.csv":       {Data: []byte(countries)},
		"reference/currencies.csv":      {Data: []byte(currencies)},
		"reference/regions.csv":         {Data: []byte(regions)},
		"reference/languages.csv":       {Data: []byte(languages)},
	}
}

func committed(t *testing.T, file string) [][]any {
	t.Helper()
	rows, err := store.ParseReference(migrations.Reference, file)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestReferenceData_CommittedCountries(t *testing.T) {
	rows := committed(t, "countries.csv")
	if len(rows) != 249 {
		t.Fatalf("countries = %d, want 249", len(rows))
	}
	seen := map[string]bool{}
	regionRequired := map[string]bool{}
	postal := map[string]string{}
	for _, row := range rows {
		code := row[0].(string)
		if seen[code] {
			t.Fatalf("duplicate country %s", code)
		}
		seen[code] = true
		if row[2].(bool) {
			regionRequired[code] = true
		}
		if row[3] != nil {
			postal[code] = row[3].(string)
		}
	}
	if len(regionRequired) != 4 || !regionRequired["US"] || !regionRequired["CA"] || !regionRequired["IN"] || !regionRequired["MX"] {
		t.Fatalf("region_required = %v, want exactly US CA IN MX", regionRequired)
	}
	if len(postal) != 7 || postal["US"] == "" || postal["CA"] == "" || postal["GB"] == "" || postal["DE"] == "" || postal["FR"] == "" || postal["IN"] == "" || postal["MX"] == "" {
		t.Fatalf("postal_regex set for %v, want exactly US CA GB DE FR IN MX", postal)
	}
	for _, c := range []struct {
		country, sample string
		want            bool
	}{
		{"US", "94105", true}, {"US", "94105-1234", true}, {"US", "9410", false},
		{"CA", "K1A 0B1", true}, {"GB", "SW1A 1AA", true}, {"DE", "10115", true},
		{"FR", "75001", true}, {"IN", "110001", true}, {"MX", "01000", true},
	} {
		if postal[c.country] == "" {
			t.Fatalf("no postal regex for %s", c.country)
		}
		if got := regexp.MustCompile(postal[c.country]).MatchString(c.sample); got != c.want {
			t.Errorf("%s %q matches = %v, want %v", c.country, c.sample, got, c.want)
		}
	}
}

func TestReferenceData_CommittedCurrencies(t *testing.T) {
	codes, numerics, units := map[string]bool{}, map[string]bool{}, map[string]int{}
	for _, row := range committed(t, "currencies.csv") {
		code, numeric := row[0].(string), row[1].(string)
		if codes[code] || numerics[numeric] {
			t.Fatalf("duplicate currency %s or numeric %s", code, numeric)
		}
		codes[code], numerics[numeric], units[code] = true, true, row[3].(int)
	}
	want := map[string]int{"JPY": 0, "KRW": 0, "KWD": 3, "BHD": 3, "OMR": 3, "USD": 2, "EUR": 2, "CHF": 2, "SEK": 2, "CAD": 2, "MXN": 2, "INR": 2, "CLF": 4}
	for code, minor := range want {
		if got, ok := units[code]; !ok || got != minor {
			t.Errorf("%s minor_units = %d (present %v), want %d", code, got, ok, minor)
		}
	}
}

func TestReferenceData_CommittedRegions(t *testing.T) {
	required := map[string]bool{}
	for _, row := range committed(t, "countries.csv") {
		if row[2].(bool) {
			required[row[0].(string)] = true
		}
	}
	codes, perCountry := map[string]bool{}, map[string]int{}
	for _, row := range committed(t, "regions.csv") {
		code, country := row[0].(string), row[1].(string)
		if codes[code] {
			t.Fatalf("duplicate region %s", code)
		}
		if !required[country] {
			t.Fatalf("region %s: country %s is not region_required in countries.csv", code, country)
		}
		codes[code] = true
		perCountry[country]++
	}
	if len(codes) != 138 {
		t.Fatalf("regions = %d, want 138", len(codes))
	}
	for country, want := range map[string]int{"US": 57, "CA": 13, "IN": 36, "MX": 32} {
		if perCountry[country] != want {
			t.Errorf("%s regions = %d, want %d", country, perCountry[country], want)
		}
	}
	for country := range required {
		if perCountry[country] == 0 {
			t.Errorf("region_required country %s has no regions", country)
		}
	}
	for _, code := range []string{"US-CA", "CA-ON", "IN-MH", "MX-CMX"} {
		if !codes[code] {
			t.Errorf("missing region %s", code)
		}
	}
	for _, code := range []string{"IN-DD", "IN-DN", "MX-DIF"} {
		if codes[code] {
			t.Errorf("retired region %s must be absent", code)
		}
	}
}

func TestReferenceData_CommittedLanguages(t *testing.T) {
	codes := map[string]bool{}
	for _, row := range committed(t, "languages.csv") {
		code := row[0].(string)
		if codes[code] {
			t.Fatalf("duplicate language %s", code)
		}
		codes[code] = true
	}
	if len(codes) != 183 {
		t.Fatalf("languages = %d, want 183", len(codes))
	}
	for _, code := range []string{"en", "fr", "de", "es", "ja", "zh", "hi", "ar"} {
		if !codes[code] {
			t.Errorf("missing language %s", code)
		}
	}
	for _, code := range []string{"iw", "in", "ji", "jw", "mo", "sh"} {
		if codes[code] {
			t.Errorf("deprecated language %s must be absent", code)
		}
	}
}

func fixturePairs(t *testing.T) [][]string {
	t.Helper()
	f, err := os.Open("../../testdata/uom-pairs.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return records[1:]
}

func TestReferenceData_CommittedUnits(t *testing.T) {
	codes, classes, precision := map[string]bool{}, map[string]bool{}, map[string]int{}
	for _, row := range committed(t, "units.csv") {
		code := row[0].(string)
		if codes[code] {
			t.Fatalf("duplicate unit %s", code)
		}
		codes[code], classes[row[2].(string)], precision[code] = true, true, row[3].(int)
	}
	if len(codes) != 59 {
		t.Fatalf("units = %d, want 59", len(codes))
	}
	if len(classes) != 12 {
		t.Errorf("classes = %v, want 12", classes)
	}
	if precision["ea"] != 0 || precision["kg"] != 3 {
		t.Errorf("ea precision %d, kg precision %d; want 0 and 3", precision["ea"], precision["kg"])
	}
	for _, pair := range fixturePairs(t) {
		for _, code := range pair[:2] {
			if !codes[code] {
				t.Errorf("fixture unit %s missing", code)
			}
		}
	}
	for _, c := range domain.StandardConversions() {
		if !codes[c.From] || !codes[c.To] {
			t.Errorf("standard conversion %s to %s names a missing unit", c.From, c.To)
		}
	}
}

func TestReferenceData_CommittedConversions(t *testing.T) {
	seeded, err := store.SeedConversions(migrations.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded) != 49 {
		t.Fatalf("conversions = %d, want 49", len(seeded))
	}
	pairs := map[[2]string]bool{}
	for _, c := range seeded {
		pair := [2]string{min(c.From, c.To), max(c.From, c.To)}
		if pairs[pair] {
			t.Errorf("pair %v repeats", pair)
		}
		pairs[pair] = true
	}
	for _, want := range domain.StandardConversions() {
		found := false
		for _, c := range seeded {
			if c.From == want.From && c.To == want.To && c.Class == want.Class && c.Factor.Equal(want.Factor) && c.Offset.Equal(want.Offset) {
				found = true
			}
		}
		if !found {
			t.Errorf("standard conversion %s to %s missing or different", want.From, want.To)
		}
	}
}

func TestSeededConversions_RoundTripFixturePairs(t *testing.T) {
	seeded, err := store.SeedConversions(migrations.Reference)
	if err != nil {
		t.Fatal(err)
	}
	pairs := fixturePairs(t)
	if len(pairs) != 50 {
		t.Fatalf("fixture pairs = %d, want 50", len(pairs))
	}
	for _, p := range pairs {
		from, to := p[0], p[1]
		qty, want := decimal.RequireFromString(p[5]), decimal.RequireFromString(p[6])
		got, err := domain.Convert(seeded, from, to, qty)
		if err != nil {
			t.Errorf("%s to %s: %v", from, to, err)
			continue
		}
		tolerance := decimal.RequireFromString("1e-9").Mul(decimal.Max(decimal.NewFromInt(1), want.Abs()))
		if got.Sub(want).Abs().GreaterThan(tolerance) {
			t.Errorf("%s to %s of %s = %s, want %s", from, to, qty, got, want)
		}
		back, err := domain.Convert(seeded, to, from, got)
		if err != nil || !domain.Display(back, domain.DisplayPrecision).Equal(qty) {
			t.Errorf("%s back to %s = %s, %v; want %s", to, from, back, err, qty)
		}
	}
}

func TestSeedReference_RejectsMalformedFilesBeforeAnyWrite(t *testing.T) {
	validCountries, validCurrencies := countryHeader+validCountry, currencyHeader+validCurrency
	validRegions, validLanguages := regionHeader+validRegion, languageHeader+validLanguage
	cases := []struct {
		name, countries, currencies, regions, languages, units, conversions, want string
	}{
		{"wrong header", "code,name\nUS,United States\n", validCurrencies, validRegions, validLanguages, "", "", "countries.csv line 1"},
		{"empty file", "", validCurrencies, validRegions, validLanguages, "", "", "countries.csv line 1"},
		{"bad code", validCountries + "us,Nowhere,false,\n", validCurrencies, validRegions, validLanguages, "", "", "countries.csv line 3"},
		{"region_required maybe", validCountries + "CA,Canada,maybe,\n", validCurrencies, validRegions, validLanguages, "", "", "countries.csv line 3"},
		{"uncompilable regex", validCountries + "CA,Canada,true,(\n", validCurrencies, validRegions, validLanguages, "", "", "countries.csv line 3"},
		{"short row", validCountries + "CA,Canada\n", validCurrencies, validRegions, validLanguages, "", "", "countries.csv line 3"},
		{"minor_units 5", validCountries, validCurrencies + "XXA,001,Bad,5\n", validRegions, validLanguages, "", "", "currencies.csv line 3"},
		{"non-numeric numeric_code", validCountries, validCurrencies + "EUR,97x,Euro,2\n", validRegions, validLanguages, "", "", "currencies.csv line 3"},
		{"region code without hyphen", validCountries, validCurrencies, validRegions + "USCA,US,California\n", validLanguages, "", "", "regions.csv line 3"},
		{"region prefix mismatch", validCountries, validCurrencies, validRegions + "US-CA,CA,California\n", validLanguages, "", "", "regions.csv line 3"},
		{"region name empty", validCountries, validCurrencies, validRegions + "US-NY,US,\n", validLanguages, "", "", "regions.csv line 3"},
		{"region wrong header", validCountries, validCurrencies, "code,name\nUS-CA,California\n", validLanguages, "", "", "regions.csv line 1"},
		{"language upper case", validCountries, validCurrencies, validRegions, validLanguages + "EN,English\n", "", "", "languages.csv line 3"},
		{"language three letters", validCountries, validCurrencies, validRegions, validLanguages + "eng,English\n", "", "", "languages.csv line 3"},
		{"language name empty", validCountries, validCurrencies, validRegions, validLanguages + "fr,\n", "", "", "languages.csv line 3"},
		{"language wrong header", validCountries, validCurrencies, validRegions, "iso,name\nen,English\n", "", "", "languages.csv line 1"},
		{"unit code with space", validCountries, validCurrencies, validRegions, validLanguages, unitHeader + validUnits + "k g,Bad,mass,3\n", "", "units.csv line 5"},
		{"unit class weight", validCountries, validCurrencies, validRegions, validLanguages, unitHeader + validUnits + "lb,Pound,weight,3\n", "", "units.csv line 5"},
		{"unit precision 7", validCountries, validCurrencies, validRegions, validLanguages, unitHeader + validUnits + "lb,Pound,mass,7\n", "", "units.csv line 5"},
		{"unit name empty", validCountries, validCurrencies, validRegions, validLanguages, unitHeader + validUnits + "lb,,mass,3\n", "", "units.csv line 5"},
		{"units wrong header", validCountries, validCurrencies, validRegions, validLanguages, "code,name\nkg,Kilogram\n", "", "units.csv line 1"},
		{"factor zero", validCountries, validCurrencies, validRegions, validLanguages, "", conversionHeader + validConversion + "g,kg,0,0\n", "uom-conversions.csv line 3"},
		{"factor negative", validCountries, validCurrencies, validRegions, validLanguages, "", conversionHeader + validConversion + "g,kg,-1,0\n", "uom-conversions.csv line 3"},
		{"factor not a number", validCountries, validCurrencies, validRegions, validLanguages, "", conversionHeader + validConversion + "g,kg,abc,0\n", "uom-conversions.csv line 3"},
		{"factor 13 decimal places", validCountries, validCurrencies, validRegions, validLanguages, "", conversionHeader + validConversion + "g,kg,0.0000000000001,0\n", "uom-conversions.csv line 3"},
		{"factor 17 integer digits", validCountries, validCurrencies, validRegions, validLanguages, "", conversionHeader + validConversion + "g,kg,12345678901234567,0\n", "uom-conversions.csv line 3"},
		{"offset not a number", validCountries, validCurrencies, validRegions, validLanguages, "", conversionHeader + validConversion + "g,kg,2,x\n", "uom-conversions.csv line 3"},
		{"self conversion", validCountries, validCurrencies, validRegions, validLanguages, "", conversionHeader + validConversion + "kg,kg,1,0\n", "uom-conversions.csv line 3"},
		{"unknown unit", validCountries, validCurrencies, validRegions, validLanguages, "", conversionHeader + validConversion + "g,zz,2,0\n", "uom-conversions.csv line 3"},
		{"cross-class conversion", validCountries, validCurrencies, validRegions, validLanguages, "", conversionHeader + validConversion + "kg,m,1,0\n", "uom-conversions.csv line 3"},
		{"reversed duplicate pair", validCountries, validCurrencies, validRegions, validLanguages, "", conversionHeader + validConversion + "g,kg,0.001,0\n", "uom-conversions.csv line 3"},
		{"conversions wrong header", validCountries, validCurrencies, validRegions, validLanguages, "", "from,to\nkg,g\n", "uom-conversions.csv line 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.units == "" {
				tc.units = unitHeader + validUnits
			}
			if tc.conversions == "" {
				tc.conversions = conversionHeader + validConversion
			}
			err := store.SeedReference(context.Background(), nil, referenceFiles(tc.countries, tc.currencies, tc.regions, tc.languages, tc.units, tc.conversions))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	if err := store.SeedReference(context.Background(), nil, fstest.MapFS{}); err == nil {
		t.Fatal("missing reference files must fail")
	}
}

func TestSeedReference_PlatformRowsAreSeededOnceAndReadOnlyForTenants(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalContext(1)
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := store.SeedReference(ctx, pool, migrations.Reference); err != nil {
			t.Fatalf("seed run %d: %v", i+1, err)
		}
	}
	inTx := func(ctx context.Context, fn func(pgx.Tx) error) error { return dbx.WithTenantTx(ctx, pool, fn) }
	count := func(ctx context.Context, query string, args ...any) (n int) {
		t.Helper()
		if err := inTx(ctx, func(tx pgx.Tx) error { return tx.QueryRow(ctx, query, args...).Scan(&n) }); err != nil {
			t.Fatal(err)
		}
		return n
	}

	var secured int
	if err := pool.QueryRow(ctx, `select count(*) from pg_class where relname = any('{country,currency,region,language,reference_status,unit_of_measure,uom_conversion}')
		and relnamespace = current_schema()::regnamespace and relrowsecurity and relforcerowsecurity`).Scan(&secured); err != nil || secured != 7 {
		t.Fatalf("tables with forced RLS = %d, %v; want 7", secured, err)
	}

	sys := tenant.With(ctx, tenant.Principal{TenantID: store.SysTenantID})
	for _, d := range []struct{ table, file string }{{"country", "countries.csv"}, {"currency", "currencies.csv"}, {"region", "regions.csv"}, {"language", "languages.csv"}, {"unit_of_measure", "units.csv"}, {"uom_conversion", "uom-conversions.csv"}} {
		if got, want := count(sys, "select count(*) from "+d.table+" where tenant_id = $1", store.SysTenantID), len(committed(t, d.file)); got != want {
			t.Fatalf("sys %s rows = %d, want %d", d.table, got, want)
		}
	}

	for _, n := range []int{1, 2} {
		tctx := principalContext(n)
		if got := count(tctx, "select count(*) from currency where code = 'JPY' and minor_units = 0"); got != 1 {
			t.Errorf("tenant %d JPY rows = %d, want 1", n, got)
		}
		if got := count(tctx, "select count(*) from country where code = 'US' and region_required"); got != 1 {
			t.Errorf("tenant %d US rows = %d, want 1", n, got)
		}
		if got := count(tctx, "select count(*) from region where code = 'US-CA' and country_code = 'US'"); got != 1 {
			t.Errorf("tenant %d US-CA rows = %d, want 1", n, got)
		}
		if got := count(tctx, "select count(*) from language where code = 'fr'"); got != 1 {
			t.Errorf("tenant %d fr rows = %d, want 1", n, got)
		}
		if got := count(tctx, "select count(*) from unit_of_measure where code = 'kg' and system_flag and decimal_precision = 3"); got != 1 {
			t.Errorf("tenant %d kg rows = %d, want 1", n, got)
		}
		var factor decimal.Decimal
		if err := inTx(tctx, func(tx pgx.Tx) error {
			return tx.QueryRow(tctx, "select factor from uom_conversion where from_code = 'lb' and to_code = 'kg'").Scan(&factor)
		}); err != nil || !factor.Equal(decimal.RequireFromString("0.45359237")) {
			t.Errorf("tenant %d lb to kg factor = %s, %v; want 0.45359237", n, factor, err)
		}
	}

	tenant1 := principalContext(1)
	for _, statement := range []string{"update currency set name = 'x' where code = 'USD'", "delete from country where code = 'US'", "update language set name = 'x' where code = 'en'", "delete from region where code = 'US-CA'", "update unit_of_measure set name = 'x' where code = 'kg'", "delete from uom_conversion where from_code = 'kg'"} {
		var affected int64
		if err := inTx(tenant1, func(tx pgx.Tx) error {
			tag, err := tx.Exec(tenant1, statement)
			affected = tag.RowsAffected()
			return err
		}); err != nil || affected != 0 {
			t.Errorf("%q affected %d rows, err %v; want 0, nil", statement, affected, err)
		}
	}
	var pgErr *pgconn.PgError
	err := inTx(tenant1, func(tx pgx.Tx) error {
		_, err := tx.Exec(tenant1, "insert into currency (tenant_id, code, numeric_code, name, minor_units) values ($1, 'ZZZ', '999', 'x', 2)", store.SysTenantID)
		return err
	})
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("insert under the sys tenant = %v, want SQLSTATE 42501", err)
	}
	err = inTx(tenant1, func(tx pgx.Tx) error {
		_, err := tx.Exec(tenant1, "insert into language (tenant_id, code, name) values ($1, 'zz', 'x')", store.SysTenantID)
		return err
	})
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("language insert under the sys tenant = %v, want SQLSTATE 42501", err)
	}
	err = inTx(tenant1, func(tx pgx.Tx) error {
		_, err := tx.Exec(tenant1, "insert into unit_of_measure (tenant_id, code, name, class, decimal_precision) values ($1, 'zz', 'x', 'mass', 3)", store.SysTenantID)
		return err
	})
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("unit insert under the sys tenant = %v, want SQLSTATE 42501", err)
	}
	for _, pair := range [][2]string{{"kg", "g"}, {"g", "kg"}} {
		err = inTx(sys, func(tx pgx.Tx) error {
			_, err := tx.Exec(sys, "insert into uom_conversion (tenant_id, from_code, to_code, factor) values ($1, $2, $3, 1)", store.SysTenantID, pair[0], pair[1])
			return err
		})
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			t.Fatalf("second conversion %s to %s = %v, want SQLSTATE 23505", pair[0], pair[1], err)
		}
	}
	var customFlag bool
	if err := inTx(tenant1, func(tx pgx.Tx) error {
		return tx.QueryRow(tenant1, "insert into unit_of_measure (tenant_id, code, name, class, decimal_precision) values ($1, 'bag', 'Bag', 'each', 0) returning system_flag", testkit.TenantID(1)).Scan(&customFlag)
	}); err != nil || customFlag {
		t.Fatalf("tenant unit system_flag = %v, %v; want false", customFlag, err)
	}

	if err := inTx(tenant1, func(tx pgx.Tx) error {
		_, err := tx.Exec(tenant1, "insert into reference_status (tenant_id, kind, code, status) values ($1, 'country', 'US', 'inactive')", testkit.TenantID(1))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := count(principalContext(1), "select count(*) from reference_status"); got != 1 {
		t.Errorf("tenant 1 reference_status rows = %d, want 1", got)
	}
	if got := count(principalContext(2), "select count(*) from reference_status"); got != 0 {
		t.Errorf("tenant 2 reference_status rows = %d, want 0", got)
	}
}
