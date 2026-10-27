package store_test

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
	"github.com/BenjaminSRussell/passage/services/org/migrations"
)

func TestPostgres_UnitMigrationIsRepeatableAndWidensReferenceKinds(t *testing.T) {
	s, pool := seededStore(t)
	ctx := principalContext(1)
	body, err := fs.ReadFile(migrations.FS, "0019_uom_tenant_units.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("second application: %v", err)
	}
	if got, err := s.SetUnitStatus(ctx, "kg", "inactive"); err != nil || got.Status != "inactive" {
		t.Fatalf("kind uom rejected: %+v, %v", got, err)
	}
	err = dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into reference_status (tenant_id, kind, code, status) values ($1, 'gone', 'x', 'active')`, testkit.TenantID(1))
		return err
	})
	if pgErr, ok := err.(*pgconn.PgError); !ok || pgErr.Code != "23514" {
		t.Fatalf("unknown kind error = %v; want check violation 23514", err)
	}
	var indexes int
	if err := pool.QueryRow(ctx, `select count(*) from pg_indexes where indexname = 'unit_of_measure_idempotency' and indexdef like '%WHERE (idempotency_key IS NOT NULL)%'`).Scan(&indexes); err != nil || indexes != 1 {
		t.Fatalf("partial idempotency index count = %d, %v; want 1", indexes, err)
	}
}

func TestPostgres_UnitsListGetAndCreate(t *testing.T) {
	s, _ := seededStore(t)
	one, two := principalContext(1), principalContext(2)

	all, err := s.ListUnits(two, store.UnitFilter{})
	if err != nil || len(all) != 59 {
		t.Fatalf("seeded units = %d, %v; want 59", len(all), err)
	}
	kg, err := s.GetUnit(two, "kg")
	if err != nil || !kg.System || kg.DecimalPrecision != 3 || kg.Class != "mass" || kg.Status != "active" {
		t.Fatalf("kg = %+v, %v", kg, err)
	}
	if _, err := s.GetUnit(two, "KG"); problemCode(t, err) != store.CodeUnitNotFound {
		t.Fatalf("case-mismatched code error = %v; want %s", err, store.CodeUnitNotFound)
	}

	bx, err := s.CreateUnit(one, store.Unit{Code: "bx", Name: "Box", Class: "each", DecimalPrecision: 0}, "")
	if err != nil || bx.System || bx.Status != "active" || bx.Code != "bx" {
		t.Fatalf("create = %+v, %v", bx, err)
	}
	for _, code := range []string{"bx", "kg"} {
		if _, err := s.CreateUnit(one, store.Unit{Code: code, Name: "Again", Class: "each"}, ""); problemCode(t, err) != store.CodeUnitCodeDuplicate {
			t.Fatalf("duplicate %s error = %v; want %s", code, err, store.CodeUnitCodeDuplicate)
		}
	}
	if mine, err := s.ListUnits(one, store.UnitFilter{}); err != nil || len(mine) != 60 {
		t.Fatalf("tenant 1 units = %d, %v; want 60", len(mine), err)
	}
	if others, err := s.ListUnits(two, store.UnitFilter{}); err != nil || len(others) != 59 {
		t.Fatalf("tenant 2 units = %d, %v; want 59 (another tenant's unit leaked)", len(others), err)
	}
	if _, err := s.GetUnit(two, "bx"); problemCode(t, err) != store.CodeUnitNotFound {
		t.Fatalf("tenant 2 get bx error = %v; want %s", err, store.CodeUnitNotFound)
	}
	if _, err := s.CreateUnit(two, store.Unit{Code: "bx", Name: "Other box", Class: "each"}, ""); err != nil {
		t.Fatalf("tenant 2 may reuse tenant 1's code: %v", err)
	}
	masses, err := s.ListUnits(one, store.UnitFilter{Class: "each"})
	if err != nil || len(masses) == 0 {
		t.Fatalf("each units = %d, %v", len(masses), err)
	}
	for _, u := range masses {
		if u.Class != "each" {
			t.Fatalf("class filter returned %+v", u)
		}
	}
}

func TestPostgres_UnitCreateReplaysIdempotencyKey(t *testing.T) {
	s, _ := seededStore(t)
	ctx := principalContext(1)
	in := store.Unit{Code: "bx", Name: "Box", Class: "each", DecimalPrecision: 1}
	first, err := s.CreateUnit(ctx, in, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := s.CreateUnit(ctx, in, "key-1"); err != nil || replay != first {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, first)
	}
	changed := in
	changed.Name = "Crate"
	if _, err := s.CreateUnit(ctx, changed, "key-1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("changed body error = %v; want %s", err, store.CodeIdempotencyReuse)
	}
	if units, err := s.ListUnits(ctx, store.UnitFilter{Class: "each"}); err != nil || countCode(units, "bx") != 1 {
		t.Fatalf("units after replay = %+v, %v; want one bx", units, err)
	}
}

func countCode(units []store.Unit, code string) (n int) {
	for _, u := range units {
		if u.Code == code {
			n++
		}
	}
	return n
}

func TestPostgres_UnitStatusIsTenantLocal(t *testing.T) {
	s, _ := seededStore(t)
	one, two := principalContext(1), principalContext(2)
	sys := tenant.With(one, tenant.Principal{TenantID: store.SysTenantID})

	if got, err := s.SetUnitStatus(one, "lb", "inactive"); err != nil || got.Status != "inactive" {
		t.Fatalf("deactivate = %+v, %v", got, err)
	}
	if got, err := s.GetUnit(two, "lb"); err != nil || got.Status != "active" {
		t.Fatalf("tenant 2 lb = %+v, %v; want active", got, err)
	}
	if got, err := s.GetUnit(sys, "lb"); err != nil || got.Status != "active" {
		t.Fatalf("sys lb = %+v, %v; want active", got, err)
	}
	if inactive, err := s.ListUnits(one, store.UnitFilter{Status: "inactive"}); err != nil || len(inactive) != 1 || inactive[0].Code != "lb" {
		t.Fatalf("tenant 1 inactive = %+v, %v; want only lb", inactive, err)
	}
	if _, err := s.CreateUnit(one, store.Unit{Code: "bx", Name: "Box", Class: "each"}, ""); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SetUnitStatus(one, "bx", "inactive"); err != nil || got.Status != "inactive" || got.System {
		t.Fatalf("deactivate custom = %+v, %v", got, err)
	}
	if _, err := s.SetUnitStatus(two, "bx", "inactive"); problemCode(t, err) != store.CodeUnitNotFound {
		t.Fatalf("tenant 2 patching tenant 1's unit error = %v; want %s", err, store.CodeUnitNotFound)
	}
	if got, err := s.SetUnitStatus(one, "lb", "active"); err != nil || got.Status != "active" {
		t.Fatalf("reactivate = %+v, %v", got, err)
	}
}

func hasConversion(convs []domain.Conversion, a, b string) bool {
	for _, c := range convs {
		if (c.From == a && c.To == b) || (c.From == b && c.To == a) {
			return true
		}
	}
	return false
}

func TestPostgres_ConversionGraphOmitsInactiveUnits(t *testing.T) {
	s, _ := seededStore(t)
	one, two := principalContext(1), principalContext(2)

	convs, precision, err := s.ConversionGraph(one)
	if err != nil || len(convs) != 49 || len(precision) != 59 {
		t.Fatalf("graph = %d conversions, %d precisions, %v; want 49 and 59", len(convs), len(precision), err)
	}
	for _, c := range convs {
		if c.Class == "" {
			t.Fatalf("conversion %s to %s has no class", c.From, c.To)
		}
	}
	if precision["kg"] != 3 || precision["ea"] != 0 {
		t.Fatalf("precision kg %d, ea %d; want 3 and 0", precision["kg"], precision["ea"])
	}

	if _, err := domain.Convert(convs, "yd", "in", decimal.NewFromInt(1)); err != nil {
		t.Fatalf("two-hop path before deactivation: %v", err)
	}
	if _, err := s.SetUnitStatus(one, "ft", "inactive"); err != nil {
		t.Fatal(err)
	}
	after, afterPrecision, err := s.ConversionGraph(one)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := afterPrecision["ft"]; present || hasConversion(after, "ft", "in") || hasConversion(after, "ft", "m") || hasConversion(after, "yd", "ft") {
		t.Fatalf("inactive unit still in the graph")
	}
	if _, err := domain.Convert(after, "yd", "in", decimal.NewFromInt(1)); err == nil {
		t.Fatal("a two-hop path through an inactive unit was used")
	}
	if untouched, _, err := s.ConversionGraph(two); err != nil || len(untouched) != 49 {
		t.Fatalf("tenant 2 graph = %d, %v; want 49", len(untouched), err)
	}
}

func TestPostgres_ConversionGraphRoundTripsFixturePairs(t *testing.T) {
	s, _ := seededStore(t)
	ctx := principalContext(1)
	convs, precision, err := s.ConversionGraph(ctx)
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
		got, err := domain.Convert(convs, from, to, qty)
		if err != nil {
			t.Errorf("%s to %s: %v", from, to, err)
			continue
		}
		tolerance := decimal.RequireFromString("1e-9").Mul(decimal.Max(decimal.NewFromInt(1), want.Abs()))
		if got.Sub(want).Abs().GreaterThan(tolerance) {
			t.Errorf("%s to %s of %s = %s, want %s", from, to, qty, got, want)
		}
		back, err := domain.Convert(convs, to, from, got)
		if err != nil || !domain.Display(back, domain.DisplayPrecision).Equal(qty) {
			t.Errorf("%s back to %s = %s, %v; want %s", to, from, back, err, qty)
		}
		if _, ok := precision[to]; !ok {
			t.Errorf("no display precision for %s", to)
		}
	}
}

func mustCreateConversion(t *testing.T, s *store.Postgres, ctx context.Context, from, to, factor, key string) (store.Conversion, bool) {
	t.Helper()
	conv, created, err := s.CreateConversion(ctx, store.Conversion{FromCode: from, ToCode: to, Factor: decimal.RequireFromString(factor)}, key)
	if err != nil {
		t.Fatal(err)
	}
	return conv, created
}

func TestPostgres_ConversionMigrationIsRepeatable(t *testing.T) {
	_, pool := seededStore(t)
	ctx := principalContext(1)
	body, err := fs.ReadFile(migrations.FS, "0020_uom_tenant_conversions.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("second application: %v", err)
	}
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatalf("re-running the migrations: %v", err)
	}
	var indexes int
	if err := pool.QueryRow(ctx, `select count(*) from pg_indexes where indexname = 'uom_conversion_idempotency' and indexdef like '%WHERE (idempotency_key IS NOT NULL)%'`).Scan(&indexes); err != nil || indexes != 1 {
		t.Fatalf("partial idempotency index count = %d, %v; want 1", indexes, err)
	}
}

func TestPostgres_ConversionsListSeedFiltersAndInactiveStatus(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	all, err := s.ListConversions(ctx, store.ConversionFilter{})
	if err != nil || len(all) != 49 {
		t.Fatalf("seeded conversions = %d, %v; want 49", len(all), err)
	}
	for i, c := range all {
		inverse := domain.Conversion{Factor: c.Factor, Offset: c.Offset}
		if !c.System || c.Status != "active" || c.Class == "" || !domain.RoundTrip(inverse) {
			t.Fatalf("conversion %+v: want a system, active, classed row that round-trips", c)
		}
		if i > 0 && (all[i-1].FromCode > c.FromCode || (all[i-1].FromCode == c.FromCode && all[i-1].ToCode > c.ToCode)) {
			t.Fatalf("%s-%s listed after %s-%s", c.FromCode, c.ToCode, all[i-1].FromCode, all[i-1].ToCode)
		}
	}
	mass, err := s.ListConversions(ctx, store.ConversionFilter{Class: "mass"})
	if err != nil || len(mass) == 0 || len(mass) == len(all) {
		t.Fatalf("mass conversions = %d, %v", len(mass), err)
	}
	for _, c := range mass {
		if c.Class != "mass" {
			t.Fatalf("class filter returned %+v", c)
		}
	}
	kg, err := s.ListConversions(ctx, store.ConversionFilter{Unit: "kg"})
	if err != nil || len(kg) < 2 || slices.ContainsFunc(kg, func(c store.Conversion) bool { return c.FromCode != "kg" && c.ToCode != "kg" }) {
		t.Fatalf("unit filter = %+v, %v", kg, err)
	}
	if wrongCase, err := s.ListConversions(ctx, store.ConversionFilter{Unit: "KG"}); err != nil || len(wrongCase) != 0 {
		t.Fatalf("wrong-case unit = %d, %v; want none", len(wrongCase), err)
	}

	if _, err := s.SetUnitStatus(ctx, "lb", "inactive"); err != nil {
		t.Fatal(err)
	}
	inactive, err := s.ListConversions(ctx, store.ConversionFilter{Status: "inactive"})
	if err != nil || len(inactive) != 3 {
		t.Fatalf("inactive conversions = %+v, %v; want the three lb rows", inactive, err)
	}
	if active, err := s.ListConversions(ctx, store.ConversionFilter{Status: "active"}); err != nil || len(active) != 46 {
		t.Fatalf("active conversions = %d, %v; want 46", len(active), err)
	}
	if listed, err := s.ListConversions(ctx, store.ConversionFilter{}); err != nil || len(listed) != 49 {
		t.Fatalf("an inactive unit must stay listed: %d, %v", len(listed), err)
	}
	if other, err := s.ListConversions(principalContext(2), store.ConversionFilter{Status: "inactive"}); err != nil || len(other) != 0 {
		t.Fatalf("another tenant's deactivation leaked: %d, %v", len(other), err)
	}
}

func TestPostgres_CreateConversionFeedsConvertAndStaysInTheTenant(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	other := principalContext(2)
	if _, err := s.CreateUnit(ctx, store.Unit{Code: "BX12", Name: "Box of 12", Class: "each"}, ""); err != nil {
		t.Fatal(err)
	}
	conv, created := mustCreateConversion(t, s, ctx, "BX12", "ea", "12", "")
	if !created || conv.System || conv.Status != "active" || conv.Class != "each" || conv.ID == "" || !conv.Factor.Equal(decimal.NewFromInt(12)) || !conv.Offset.IsZero() {
		t.Fatalf("created = %+v, %v", conv, created)
	}
	convs, _, err := s.ConversionGraph(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ from, to, qty, want string }{{"BX12", "ea", "2", "24"}, {"ea", "BX12", "6", "0.5"}} {
		got, err := domain.Convert(convs, tc.from, tc.to, decimal.RequireFromString(tc.qty))
		if err != nil || !got.Equal(decimal.RequireFromString(tc.want)) {
			t.Fatalf("%s to %s = %s, %v; want %s", tc.from, tc.to, got, err, tc.want)
		}
	}
	if mine, err := s.ListConversions(ctx, store.ConversionFilter{Unit: "BX12"}); err != nil || len(mine) != 1 || mine[0].ID != conv.ID {
		t.Fatalf("own list = %+v, %v", mine, err)
	}
	if theirs, err := s.ListConversions(other, store.ConversionFilter{}); err != nil || len(theirs) != 49 {
		t.Fatalf("another tenant lists %d conversions, %v; want 49", len(theirs), err)
	}
	otherConvs, _, err := s.ConversionGraph(other)
	if err != nil {
		t.Fatal(err)
	}
	var de *domain.Error
	if _, err := domain.Convert(otherConvs, "BX12", "ea", decimal.NewFromInt(2)); !errors.As(err, &de) || de.Code != domain.CodeNoPath {
		t.Fatalf("another tenant's convert = %v; want %s", err, domain.CodeNoPath)
	}
}

func TestPostgres_CreateConversionRefusals(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	for _, u := range []store.Unit{{Code: "BX12", Name: "Box of 12", Class: "each"}, {Code: "CS6", Name: "Case of 6", Class: "each"}} {
		if _, err := s.CreateUnit(ctx, u, ""); err != nil {
			t.Fatal(err)
		}
	}
	mustCreateConversion(t, s, ctx, "BX12", "ea", "12", "")
	if _, err := s.SetUnitStatus(ctx, "CS6", "inactive"); err != nil {
		t.Fatal(err)
	}
	tests := []struct{ name, from, to, code, field string }{
		{"class mismatch", "kg", "m", store.CodeClassMismatch, ""},
		{"reverse of a system pair", "g", "kg", store.CodeConversionDuplicate, ""},
		{"same direction as a system pair", "kg", "g", store.CodeConversionDuplicate, ""},
		{"same direction as a tenant pair", "BX12", "ea", store.CodeConversionDuplicate, ""},
		{"reverse of a tenant pair", "ea", "BX12", store.CodeConversionDuplicate, ""},
		{"unknown unit", "kg", "furlong", store.CodeUnitNotFound, ""},
		{"unknown unit beats inactive unit", "CS6", "furlong", store.CodeUnitNotFound, ""},
		{"inactive source unit", "CS6", "ea", store.CodeUnitInactive, "fromCode"},
		{"inactive target unit", "ea", "CS6", store.CodeUnitInactive, "toCode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, created, err := s.CreateConversion(ctx, store.Conversion{FromCode: tt.from, ToCode: tt.to, Factor: decimal.NewFromInt(2)}, "")
			var problem *httpx.Problem
			if created || !errors.As(err, &problem) || problem.Code != tt.code || (tt.field != "" && problem.Errors[tt.field] == "") {
				t.Fatalf("create = created %v, %v; want %s naming %q", created, err, tt.code, tt.field)
			}
		})
	}
	if all, err := s.ListConversions(ctx, store.ConversionFilter{}); err != nil || len(all) != 50 {
		t.Fatalf("conversions after refusals = %d, %v; want 50", len(all), err)
	}
}

func TestPostgres_CreateConversionReplaysIdempotencyKey(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	if _, err := s.CreateUnit(ctx, store.Unit{Code: "BX12", Name: "Box of 12", Class: "each"}, ""); err != nil {
		t.Fatal(err)
	}
	first, created := mustCreateConversion(t, s, ctx, "BX12", "ea", "12", "key-1")
	replay, replayed := mustCreateConversion(t, s, ctx, "BX12", "ea", "12.000", "key-1")
	if !created || replayed || replay.ID != first.ID {
		t.Fatalf("first %+v created %v, replay %+v created %v; want one row", first, created, replay, replayed)
	}
	if _, _, err := s.CreateConversion(ctx, store.Conversion{FromCode: "BX12", ToCode: "ea", Factor: decimal.NewFromInt(13)}, "key-1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("changed factor error = %v; want %s", err, store.CodeIdempotencyReuse)
	}
	if _, _, err := s.CreateConversion(ctx, store.Conversion{FromCode: "ea", ToCode: "BX12", Factor: decimal.NewFromInt(12)}, "key-1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("swapped units error = %v; want %s", err, store.CodeIdempotencyReuse)
	}
	if _, _, err := s.CreateConversion(principalContext(2), store.Conversion{FromCode: "BX12", ToCode: "ea", Factor: decimal.NewFromInt(12)}, "key-1"); problemCode(t, err) != store.CodeUnitNotFound {
		t.Fatalf("another tenant must not replay the key: %v", err)
	}
	if mine, err := s.ListConversions(ctx, store.ConversionFilter{Unit: "BX12"}); err != nil || len(mine) != 1 {
		t.Fatalf("conversions after replay = %d, %v; want 1", len(mine), err)
	}
}

func TestPostgres_UnitConcurrentSameKeyReturnsOneUnit(t *testing.T) {
	s, _ := seededStore(t)
	ctx := principalContext(1)
	in := store.Unit{Code: "rc", Name: "Race", Class: "each", DecimalPrecision: 1}
	const callers = 8
	units := make([]store.Unit, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			units[i], errs[i] = s.CreateUnit(ctx, in, "race-key")
		}()
	}
	wg.Wait()
	for i := range callers {
		if errs[i] != nil || units[i] != units[0] {
			t.Fatalf("caller %d = %+v, %v; want %+v", i, units[i], errs[i], units[0])
		}
	}
}
