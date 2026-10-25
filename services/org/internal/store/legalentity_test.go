package store_test

import (
	"context"
	"encoding/json"
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

func lifecycleStore(t *testing.T) (*store.Postgres, *pgxpool.Pool) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	ctx := principalContext(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedReference(ctx, admin, migrations.Reference); err != nil {
		t.Fatal(err)
	}
	return store.New(app), app
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

func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string) int {
	t.Helper()
	var n int
	if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error { return tx.QueryRow(ctx, query).Scan(&n) }); err != nil {
		t.Fatal(err)
	}
	return n
}

func updatedEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []outboxRow {
	t.Helper()
	var updated []outboxRow
	for _, row := range outboxRows(t, ctx, pool) {
		if row.Type == "dev.passage.org.legalentity.updated.v1" {
			updated = append(updated, row)
		}
	}
	return updated
}

func rename(name string) store.LegalEntityPatch { return store.LegalEntityPatch{Name: &name} }

func TestPostgres_CreateWritesExactlyOneOpenName(t *testing.T) {
	s, pool := lifecycleStore(t)
	ctx := principalContext(1)
	if _, err := s.CreateLegalEntity(ctx, newEntity("US01"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateLegalEntity(ctx, newEntity("US02"), ""); err != nil {
		t.Fatal(err)
	}
	if open := countRows(t, ctx, pool, `select count(*) from legal_entity_name where valid_to is null`); open != 2 {
		t.Fatalf("open name rows = %d; want one per entity (2)", open)
	}
	if total := countRows(t, ctx, pool, `select count(*) from legal_entity_name`); total != 2 {
		t.Fatalf("name rows = %d; want 2", total)
	}
}

func TestPostgres_UpdateNameBumpsVersionAndEmitsOneEvent(t *testing.T) {
	s, pool := lifecycleStore(t)
	ctx := principalContext(1)
	created, err := s.CreateLegalEntity(ctx, newEntity("US01"), "")
	if err != nil {
		t.Fatal(err)
	}

	updated, err := s.UpdateLegalEntity(ctx, created.ID, 1, rename("Passage USA"))
	if err != nil || updated.Version != 2 || updated.Name != "Passage USA" || !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("update = %+v, %v; want version 2 with the new name", updated, err)
	}
	names, err := s.ListLegalEntityNames(ctx, created.ID)
	if err != nil || len(names) != 2 || names[0].Name != "Passage USA" || names[0].ValidTo != nil || names[1].Name != "Passage US01" || names[1].ValidTo == nil {
		t.Fatalf("names = %+v, %v; want newest first with the old name closed", names, err)
	}
	if open := countRows(t, ctx, pool, `select count(*) from legal_entity_name where valid_to is null`); open != 1 {
		t.Fatalf("open name rows = %d; want 1", open)
	}

	events := updatedEvents(t, ctx, pool)
	if len(events) != 1 {
		t.Fatalf("updated events = %d; want exactly 1", len(events))
	}
	var evt struct {
		Subject string
		Data    struct {
			ID            string   `json:"id"`
			ChangedFields []string `json:"changed_fields"`
			Version       int      `json:"version"`
		}
	}
	if err := json.Unmarshal(events[0].Payload, &evt); err != nil {
		t.Fatal(err)
	}
	if evt.Data.ID != created.ID || len(evt.Data.ChangedFields) != 1 || evt.Data.ChangedFields[0] != "name" || evt.Data.Version != 2 {
		t.Fatalf("event data = %+v; want id %s, changed_fields [name], version 2", evt.Data, created.ID)
	}

	if _, err := s.UpdateLegalEntity(ctx, created.ID, 1, rename("Stale Name")); problemCode(t, err) != store.CodeVersionConflict {
		t.Fatalf("stale version error = %v; want %s", err, store.CodeVersionConflict)
	}
	current, err := s.GetLegalEntity(ctx, created.ID)
	if err != nil || current.Name != "Passage USA" || current.Version != 2 || len(updatedEvents(t, ctx, pool)) != 1 {
		t.Fatalf("a stale update must change nothing: %+v, %v", current, err)
	}

	noop, err := s.UpdateLegalEntity(ctx, created.ID, 2, rename("Passage USA"))
	if err != nil || noop.Version != 2 || len(updatedEvents(t, ctx, pool)) != 1 {
		t.Fatalf("no-op update = %+v, %v; want version 2 and no new event", noop, err)
	}
	if names, _ := s.ListLegalEntityNames(ctx, created.ID); len(names) != 2 {
		t.Fatalf("no-op update changed name history: %+v", names)
	}
}

func TestPostgres_UpdateReportsEverySortedChangedField(t *testing.T) {
	s, pool := lifecycleStore(t)
	ctx := principalContext(1)
	created, err := s.CreateLegalEntity(ctx, newEntity("US01"), "")
	if err != nil {
		t.Fatal(err)
	}
	status, zone, registration, language := "inactive", "America/Chicago", "REG-1", "fr"
	updated, err := s.UpdateLegalEntity(ctx, created.ID, 1, store.LegalEntityPatch{Status: &status, TimeZone: &zone, RegistrationNumber: &registration, DefaultLanguage: &language})
	if err != nil || updated.Version != 2 || updated.Status != "inactive" || updated.TimeZone != zone || updated.DefaultLanguage != "fr" || updated.RegistrationNumber == nil || *updated.RegistrationNumber != registration {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	events := updatedEvents(t, ctx, pool)
	var evt struct {
		Data struct {
			ChangedFields []string `json:"changed_fields"`
		}
	}
	if len(events) != 1 || json.Unmarshal(events[0].Payload, &evt) != nil {
		t.Fatalf("events = %+v", events)
	}
	want := []string{"default_language", "registration_number", "status", "time_zone"}
	if len(evt.Data.ChangedFields) != len(want) {
		t.Fatalf("changed_fields = %v; want %v", evt.Data.ChangedFields, want)
	}
	for i := range want {
		if evt.Data.ChangedFields[i] != want[i] {
			t.Fatalf("changed_fields = %v; want %v", evt.Data.ChangedFields, want)
		}
	}
	if names, _ := s.ListLegalEntityNames(ctx, created.ID); len(names) != 1 {
		t.Fatalf("a non-name change must not touch name history: %+v", names)
	}
}

func TestPostgres_ReferenceMustBeActiveForTheTenant(t *testing.T) {
	s, _ := lifecycleStore(t)
	ctx1, ctx2 := principalContext(1), principalContext(2)
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
	if _, err := s.CreateLegalEntity(ctx2, eur, ""); err != nil {
		t.Fatalf("tenant 2 must still create with EUR: %v", err)
	}

	unknownCountry := newEntity("ZZ01")
	unknownCountry.Country = "ZZ"
	if _, err := s.CreateLegalEntity(ctx1, unknownCountry, ""); problemCode(t, err) != store.CodeReferenceInactive {
		t.Fatalf("unknown country error = %v", err)
	}
	badLanguage := newEntity("XX01")
	badLanguage.DefaultLanguage = "xx-US"
	if _, err := s.CreateLegalEntity(ctx1, badLanguage, ""); problemCode(t, err) != store.CodeReferenceInactive {
		t.Fatalf("unknown language error = %v", err)
	}

	created, err := s.CreateLegalEntity(ctx1, newEntity("US01"), "")
	if err != nil {
		t.Fatal(err)
	}
	unknownLanguage := "xx"
	if _, err := s.UpdateLegalEntity(ctx1, created.ID, 1, store.LegalEntityPatch{DefaultLanguage: &unknownLanguage}); problemCode(t, err) != store.CodeReferenceInactive {
		t.Fatalf("update to an unknown language error = %v", err)
	}
	if after, _ := s.GetLegalEntity(ctx1, created.ID); after.Version != 1 || after.DefaultLanguage != "en-US" {
		t.Fatalf("a rejected update must change nothing: %+v", after)
	}
}

func TestPostgres_ListFiltersByStatusCountryAndCurrency(t *testing.T) {
	s, _ := lifecycleStore(t)
	ctx := principalContext(1)
	active, err := s.CreateLegalEntity(ctx, newEntity("US01"), "")
	if err != nil {
		t.Fatal(err)
	}
	jp := newEntity("JP01")
	jp.Country, jp.Currency = "JP", "JPY"
	retired, err := s.CreateLegalEntity(ctx, jp, "")
	if err != nil {
		t.Fatal(err)
	}
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
	if page, _ := s.ListLegalEntities(ctx, store.LegalEntityFilter{}, "", store.MaxPageSize); len(page.Items) != 2 {
		t.Fatalf("unfiltered list = %d; want 2", len(page.Items))
	}
}

func TestPostgres_ScopedCallerCannotSeeOrChangeAnotherEntity(t *testing.T) {
	s, _ := lifecycleStore(t)
	ctx := principalContext(1)
	a, err := s.CreateLegalEntity(ctx, newEntity("US01"), "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateLegalEntity(ctx, newEntity("US02"), "")
	if err != nil {
		t.Fatal(err)
	}
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
	if _, err := s.ListLegalEntityNames(scoped, b.ID); problemCode(t, err) != store.CodeLegalEntityNotFound {
		t.Fatalf("names of another entity error = %v; want %s", err, store.CodeLegalEntityNotFound)
	}
	if untouched, _ := s.GetLegalEntity(ctx, b.ID); untouched.Name != "Passage US02" || untouched.Version != 1 {
		t.Fatalf("entity B changed: %+v", untouched)
	}
}

func TestPostgres_TenantCapsAtFiveHundredLegalEntities(t *testing.T) {
	s, pool := lifecycleStore(t)
	ctx := principalContext(1)
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
	if _, err := s.CreateLegalEntity(principalContext(2), newEntity("US01"), ""); err != nil {
		t.Fatalf("the cap is per tenant: %v", err)
	}
}
