package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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

func lifecycleStore(t *testing.T) (*store.Postgres, context.Context, *pgxpool.Pool) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	ctx := principalContext(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	seed(t, ctx, admin)
	return store.New(app), ctx, app
}

func newEntity(code string) store.LegalEntity {
	return store.LegalEntity{Code: code, Name: "Passage " + code, Country: "US", Currency: "USD", TimeZone: "UTC", DefaultLanguage: "en-US"}
}

func problemCode(t *testing.T, err error) string {
	t.Helper()
	var problem *httpx.Problem
	if !errors.As(err, &problem) {
		t.Fatalf("want a problem, got %v", err)
	}
	return problem.Code
}

func mustCreate(t *testing.T, s *store.Postgres, ctx context.Context, in store.LegalEntity) store.LegalEntity {
	t.Helper()
	created, err := s.CreateLegalEntity(ctx, in, "")
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func updatedChangedFields(t *testing.T, ctx context.Context, pool *pgxpool.Pool) [][]string {
	t.Helper()
	var fields [][]string
	for _, row := range outboxRows(t, ctx, pool) {
		if row.Type != "dev.passage.org.legalentity.updated.v1" {
			continue
		}
		var evt struct {
			Data struct {
				ChangedFields []string `json:"changed_fields"`
			}
		}
		if err := json.Unmarshal(row.Payload, &evt); err != nil {
			t.Fatal(err)
		}
		fields = append(fields, evt.Data.ChangedFields)
	}
	return fields
}

func rename(name string) store.LegalEntityPatch { return store.LegalEntityPatch{Name: &name} }

func TestPostgres_UpdateNameBumpsVersionAndEmitsOneEvent(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	created := mustCreate(t, s, ctx, newEntity("US01"))
	updated, err := s.UpdateLegalEntity(ctx, created.ID, 1, rename("Passage USA"))
	if err != nil || updated.Version != 2 || updated.Name != "Passage USA" || !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("update = %+v, %v; want version 2 with the new name", updated, err)
	}
	names, err := s.ListLegalEntityNames(ctx, created.ID)
	if err != nil || len(names) != 2 || names[0].Name != "Passage USA" || names[0].ValidTo != nil || names[1].Name != "Passage US01" || names[1].ValidTo == nil {
		t.Fatalf("names = %+v, %v; want newest first with the old name closed", names, err)
	}
	if got := updatedChangedFields(t, ctx, pool); !reflect.DeepEqual(got, [][]string{{"name"}}) {
		t.Fatalf("updated events = %v; want exactly one with changed_fields [name]", got)
	}
	if _, err := s.UpdateLegalEntity(ctx, created.ID, 1, rename("Stale Name")); problemCode(t, err) != store.CodeVersionConflict {
		t.Fatalf("stale version error = %v; want %s", err, store.CodeVersionConflict)
	}
	noop, err := s.UpdateLegalEntity(ctx, created.ID, 2, rename("Passage USA"))
	if err != nil || noop.Version != 2 || noop.Name != "Passage USA" || len(updatedChangedFields(t, ctx, pool)) != 1 {
		t.Fatalf("stale and no-op updates must change nothing: %+v, %v", noop, err)
	}
	if names, _ := s.ListLegalEntityNames(ctx, created.ID); len(names) != 2 {
		t.Fatalf("no-op update changed name history: %+v", names)
	}
	status, zone, registration, language := "inactive", "America/Chicago", "REG-1", "fr"
	patch := store.LegalEntityPatch{Status: &status, TimeZone: &zone, RegistrationNumber: &registration, DefaultLanguage: &language}
	if _, err := s.UpdateLegalEntity(ctx, created.ID, 2, patch); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"name"}, {"default_language", "registration_number", "status", "time_zone"}}
	if got := updatedChangedFields(t, ctx, pool); !reflect.DeepEqual(got, want) {
		t.Fatalf("updated events = %v; want %v", got, want)
	}
}

func TestPostgres_ReferenceMustBeActiveForTheTenant(t *testing.T) {
	s, ctx1, _ := lifecycleStore(t)
	if _, err := s.SetReferenceStatus(ctx1, store.KindCurrency, "EUR", "inactive"); err != nil {
		t.Fatal(err)
	}
	eur := newEntity("EU01")
	eur.Currency = "EUR"
	_, err := s.CreateLegalEntity(ctx1, eur, "")
	var problem *httpx.Problem
	if !errors.As(err, &problem) || problem.Code != store.CodeReferenceInactive || problem.Errors["currency"] == "" {
		t.Fatalf("inactive currency error = %v; want %s naming currency", err, store.CodeReferenceInactive)
	}
	mustCreate(t, s, principalContext(2), eur)
	created := mustCreate(t, s, ctx1, newEntity("US01"))
	unknownLanguage := "xx"
	if _, err := s.UpdateLegalEntity(ctx1, created.ID, 1, store.LegalEntityPatch{DefaultLanguage: &unknownLanguage}); problemCode(t, err) != store.CodeReferenceInactive {
		t.Fatalf("update to an unknown language error = %v", err)
	}
	if after, _ := s.GetLegalEntity(ctx1, created.ID); after.Version != 1 || after.DefaultLanguage != "en-US" {
		t.Fatalf("a rejected update must change nothing: %+v", after)
	}
}

func TestPostgres_ListFiltersByStatusCountryAndCurrency(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	active := mustCreate(t, s, ctx, newEntity("US01"))
	jp := newEntity("JP01")
	jp.Country, jp.Currency = "JP", "JPY"
	retired := mustCreate(t, s, ctx, jp)
	inactive := "inactive"
	if _, err := s.UpdateLegalEntity(ctx, retired.ID, 1, store.LegalEntityPatch{Status: &inactive}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		filter store.LegalEntityFilter
		want   string
	}{
		{store.LegalEntityFilter{Status: "inactive"}, retired.ID},
		{store.LegalEntityFilter{Status: "active"}, active.ID},
		{store.LegalEntityFilter{Country: "JP"}, retired.ID},
		{store.LegalEntityFilter{Currency: "USD"}, active.ID},
	} {
		page, err := s.ListLegalEntities(ctx, tc.filter, "", store.MaxPageSize)
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != tc.want {
			t.Fatalf("filter %+v = %+v, %v; want only %s", tc.filter, page.Items, err, tc.want)
		}
	}
}

func TestPostgres_ScopedCallerCannotSeeOrChangeAnotherEntity(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a, b := mustCreate(t, s, ctx, newEntity("US01")), mustCreate(t, s, ctx, newEntity("US02"))
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: a.ID})
	if got, err := s.GetLegalEntity(scoped, a.ID); err != nil || got.ID != a.ID {
		t.Fatalf("scoped caller must read its own entity: %+v, %v", got, err)
	}
	if _, err := s.GetLegalEntity(scoped, b.ID); problemCode(t, err) != store.CodeLegalEntityNotFound {
		t.Fatalf("get of another entity error = %v; want %s", err, store.CodeLegalEntityNotFound)
	}
	if _, err := s.UpdateLegalEntity(scoped, b.ID, 1, rename("Hijacked")); problemCode(t, err) != store.CodeLegalEntityNotFound {
		t.Fatalf("update of another entity error = %v; want %s", err, store.CodeLegalEntityNotFound)
	}
	if untouched, _ := s.GetLegalEntity(ctx, b.ID); untouched.Name != "Passage US02" || untouched.Version != 1 {
		t.Fatalf("entity B changed: %+v", untouched)
	}
}

func TestPostgres_TenantCapsAtFiveHundredLegalEntities(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	first, err := s.CreateLegalEntity(ctx, newEntity("US01"), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	err = dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into legal_entity (tenant_id, id, code, name, country, currency, time_zone)
			select $1, gen_random_uuid(), 'L' || lpad(g::text, 5, '0'), 'filler', 'US', 'USD', 'UTC' from generate_series(1, 499) g`, testkit.TenantID(1))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateLegalEntity(ctx, newEntity("US02"), ""); problemCode(t, err) != store.CodeLegalEntityLimit {
		t.Fatalf("501st entity error = %v; want %s", err, store.CodeLegalEntityLimit)
	}
	replay, err := s.CreateLegalEntity(ctx, newEntity("US01"), "key-1")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("an idempotent replay at the cap must return the original: %+v, %v", replay, err)
	}
	mustCreate(t, s, principalContext(2), newEntity("US01"))
}
