package store_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
	"github.com/BenjaminSRussell/passage/services/org/migrations"
)

func seededStore(t *testing.T) (*store.Postgres, *pgxpool.Pool) {
	t.Helper()
	pool := testkit.DB(t)
	ctx := principalContext(1)
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedReference(ctx, pool, migrations.Reference); err != nil {
		t.Fatal(err)
	}
	return store.New(pool), pool
}

func TestPostgres_ReferenceListsSeedForFreshTenant(t *testing.T) {
	s, _ := seededStore(t)
	ctx := principalContext(3)
	countries, err := s.ListReference(ctx, store.KindCountry, store.ReferenceFilter{})
	if err != nil || len(countries) != 249 {
		t.Fatalf("countries = %d, %v; want 249", len(countries), err)
	}
	regions, err := s.ListReference(ctx, store.KindRegion, store.ReferenceFilter{Country: "US"})
	if err != nil || len(regions) != 57 {
		t.Fatalf("US regions = %d, %v; want 57", len(regions), err)
	}
	for _, item := range append(countries, regions...) {
		if item.Status != "active" {
			t.Fatalf("%s is %s for a fresh tenant; want active", item.Code, item.Status)
		}
	}
	kwd, err := s.GetReference(ctx, store.KindCurrency, "KWD")
	if err != nil || kwd.MinorUnits == nil || *kwd.MinorUnits != 3 || kwd.NumericCode != "414" {
		t.Fatalf("KWD = %+v, %v; want minor units 3", kwd, err)
	}
}

func TestPostgres_ReferenceDeactivationIsTenantIsolated(t *testing.T) {
	s, pool := seededStore(t)
	tenant1, tenant2 := principalContext(1), principalContext(2)

	if got, err := s.SetReferenceStatus(tenant1, store.KindCurrency, "EUR", "inactive"); err != nil || got.Status != "inactive" {
		t.Fatalf("deactivate = %+v, %v", got, err)
	}
	if got, err := s.GetReference(tenant1, store.KindCurrency, "EUR"); err != nil || got.Status != "inactive" {
		t.Fatalf("tenant 1 get = %+v, %v; want inactive", got, err)
	}
	inactive, err := s.ListReference(tenant1, store.KindCurrency, store.ReferenceFilter{Status: "inactive"})
	if err != nil || len(inactive) != 1 || inactive[0].Code != "EUR" {
		t.Fatalf("tenant 1 inactive list = %+v, %v; want only EUR", inactive, err)
	}
	if got, err := s.GetReference(tenant2, store.KindCurrency, "EUR"); err != nil || got.Status != "active" {
		t.Fatalf("tenant 2 get = %+v, %v; want active", got, err)
	}
	if other, err := s.ListReference(tenant2, store.KindCurrency, store.ReferenceFilter{Status: "inactive"}); err != nil || len(other) != 0 {
		t.Fatalf("tenant 2 inactive list = %+v, %v; want none", other, err)
	}
	sys := tenant.With(tenant1, tenant.Principal{TenantID: store.SysTenantID})
	var sysStatus string
	if err := dbx.WithTenantTx(sys, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(sys, "select status from currency where tenant_id = $1 and code = 'EUR'", store.SysTenantID).Scan(&sysStatus)
	}); err != nil || sysStatus != "active" {
		t.Fatalf("sys EUR status = %q, %v; want active", sysStatus, err)
	}

	if got, err := s.SetReferenceStatus(tenant1, store.KindCurrency, "EUR", "active"); err != nil || got.Status != "active" {
		t.Fatalf("reactivate = %+v, %v", got, err)
	}
	if got, err := s.GetReference(tenant1, store.KindCurrency, "EUR"); err != nil || got.Status != "active" {
		t.Fatalf("tenant 1 after reactivation = %+v, %v; want active", got, err)
	}
}

func TestPostgres_ReferenceStatusRepeatAndUnknownCodeWriteNoExtraRows(t *testing.T) {
	s, pool := seededStore(t)
	ctx := principalContext(1)
	rows := func(code string) (n int) {
		t.Helper()
		if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, "select count(*) from reference_status where kind = 'currency' and code = $1", code).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	first, err := s.SetReferenceStatus(ctx, store.KindCurrency, "GBP", "inactive")
	again, err2 := s.SetReferenceStatus(ctx, store.KindCurrency, "GBP", "inactive")
	if err != nil || err2 != nil || first.Status != again.Status || rows("GBP") != 1 {
		t.Fatalf("repeat = %+v / %+v, %v %v, rows %d; want one row", first, again, err, err2, rows("GBP"))
	}
	var problem *httpx.Problem
	if _, err := s.SetReferenceStatus(ctx, store.KindCurrency, "ZZZ", "inactive"); !errors.As(err, &problem) || problem.Code != store.CodeReferenceNotFound || rows("ZZZ") != 0 {
		t.Fatalf("unknown code = %v, rows %d; want %s and no row", err, rows("ZZZ"), store.CodeReferenceNotFound)
	}
	if _, err := s.SetReferenceStatus(ctx, store.KindCurrency, "USD ", "inactive"); !errors.As(err, &problem) || problem.Code != store.CodeReferenceNotFound || rows("USD ") != 0 {
		t.Fatalf("padded code = %v, rows %d; want %s and no row", err, rows("USD "), store.CodeReferenceNotFound)
	}
	if _, err := s.GetReference(ctx, store.KindCurrency, "USD "); !errors.As(err, &problem) || problem.Status != 404 {
		t.Fatalf("padded get = %v; want 404", err)
	}
	if _, err := s.GetReference(ctx, store.KindRegion, "US-ZZ"); !errors.As(err, &problem) || problem.Status != 404 {
		t.Fatalf("unknown region = %v; want 404", err)
	}
}
