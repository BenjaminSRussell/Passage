package store_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
	"github.com/BenjaminSRussell/passage/services/org/migrations"
)

const (
	countryHeader  = "code,name,region_required,postal_regex\n"
	currencyHeader = "code,numeric_code,name,minor_units\n"
	validCountry   = "US,United States,true,^[0-9]{5}$\n"
	validCurrency  = "USD,840,US Dollar,2\n"
)

func referenceFiles(countries, currencies string) fstest.MapFS {
	return fstest.MapFS{
		"reference/countries.csv":  {Data: []byte(countries)},
		"reference/currencies.csv": {Data: []byte(currencies)},
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

func TestSeedReference_RejectsMalformedFilesBeforeAnyWrite(t *testing.T) {
	cases := []struct {
		name, countries, currencies, want string
	}{
		{"wrong header", "code,name\nUS,United States\n", validCurrency, "countries.csv line 1"},
		{"empty file", "", validCurrency, "countries.csv line 1"},
		{"bad code", countryHeader + validCountry + "us,Nowhere,false,\n", validCurrency, "countries.csv line 3"},
		{"region_required maybe", countryHeader + validCountry + "CA,Canada,maybe,\n", validCurrency, "countries.csv line 3"},
		{"uncompilable regex", countryHeader + validCountry + "CA,Canada,true,(\n", validCurrency, "countries.csv line 3"},
		{"short row", countryHeader + validCountry + "CA,Canada\n", validCurrency, "countries.csv line 3"},
		{"minor_units 5", countryHeader + validCountry, currencyHeader + validCurrency + "XXA,001,Bad,5\n", "currencies.csv line 3"},
		{"non-numeric numeric_code", countryHeader + validCountry, currencyHeader + validCurrency + "EUR,97x,Euro,2\n", "currencies.csv line 3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := store.SeedReference(context.Background(), nil, referenceFiles(tc.countries, tc.currencies))
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
	if err := pool.QueryRow(ctx, `select count(*) from pg_class where relname = any('{country,currency,reference_status}')
		and relnamespace = current_schema()::regnamespace and relrowsecurity and relforcerowsecurity`).Scan(&secured); err != nil || secured != 3 {
		t.Fatalf("tables with forced RLS = %d, %v; want 3", secured, err)
	}

	sys := tenant.With(ctx, tenant.Principal{TenantID: store.SysTenantID})
	if got, want := count(sys, "select count(*) from country where tenant_id = $1", store.SysTenantID), len(committed(t, "countries.csv")); got != want {
		t.Fatalf("sys countries = %d, want %d", got, want)
	}
	if got, want := count(sys, "select count(*) from currency where tenant_id = $1", store.SysTenantID), len(committed(t, "currencies.csv")); got != want {
		t.Fatalf("sys currencies = %d, want %d", got, want)
	}

	for _, n := range []int{1, 2} {
		tctx := principalContext(n)
		if got := count(tctx, "select count(*) from currency where code = 'JPY' and minor_units = 0"); got != 1 {
			t.Errorf("tenant %d JPY rows = %d, want 1", n, got)
		}
		if got := count(tctx, "select count(*) from country where code = 'US' and region_required"); got != 1 {
			t.Errorf("tenant %d US rows = %d, want 1", n, got)
		}
	}

	tenant1 := principalContext(1)
	for _, statement := range []string{"update currency set name = 'x' where code = 'USD'", "delete from country where code = 'US'"} {
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
