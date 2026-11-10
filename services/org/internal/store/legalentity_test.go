package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
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

func TestPostgres_ListSearchesCodeAndNameAndEscapesWildcards(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	us := mustCreate(t, s, ctx, newEntity("US01"))
	odd := newEntity("PCT01")
	odd.Name = "100% Contoso_Ltd"
	pct := mustCreate(t, s, ctx, odd)
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"us0", []string{us.ID}},
		{"CONTOSO_L", []string{pct.ID}},
		{"100%", []string{pct.ID}},
		{"%", []string{pct.ID}},
		{"_", []string{pct.ID}},
		{"nothing-matches", nil},
	} {
		page, err := s.ListLegalEntities(ctx, store.LegalEntityFilter{Query: tc.query}, "", store.MaxPageSize)
		got := []string{}
		for _, e := range page.Items {
			got = append(got, e.ID)
		}
		if err != nil || !slices.Equal(got, append([]string{}, tc.want...)) {
			t.Fatalf("query %q = %v, %v; want %v", tc.query, got, err, tc.want)
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

func TestPostgres_LegalEntityCalendarAssignmentRequiresActiveGeneratedCalendar(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	ready, empty, retired := retail445(t, s, ctx), fiscalCalendarNamed(t, s, ctx, "EMPTY"), fiscalCalendarNamed(t, s, ctx, "OLD")
	generate(t, s, ctx, ready.ID, 2026)
	generate(t, s, ctx, retired.ID, 2026)
	if _, err := s.UpdateFiscalCalendar(ctx, retired.ID, 1, store.FiscalCalendarPatch{Status: ptr("inactive")}); err != nil {
		t.Fatal(err)
	}
	entity := mustCreate(t, s, ctx, newEntity("US01"))
	if entity.FiscalCalendarID != nil {
		t.Fatalf("new entity has calendar %v; want none", *entity.FiscalCalendarID)
	}
	for name, id := range map[string]string{"no generated year": empty.ID, "inactive": retired.ID, "unknown": "0190b6c0-0000-7000-8000-0000000000ff", "another tenant": ready.ID} {
		caller := ctx
		if name == "another tenant" {
			caller = principalContext(2)
			entity2 := mustCreate(t, s, caller, newEntity("US01"))
			_, err := s.UpdateLegalEntity(caller, entity2.ID, 1, store.LegalEntityPatch{FiscalCalendarID: &id})
			assertCalendarInvalid(t, err, name)
			continue
		}
		_, err := s.UpdateLegalEntity(caller, entity.ID, 1, store.LegalEntityPatch{FiscalCalendarID: &id})
		assertCalendarInvalid(t, err, name)
	}
	if got, _ := s.GetLegalEntity(ctx, entity.ID); got.Version != 1 || got.FiscalCalendarID != nil || len(updatedChangedFields(t, ctx, pool)) != 0 {
		t.Fatalf("refused assignments wrote: %+v", got)
	}
	assigned := assignCalendar(t, s, ctx, entity, ready.ID)
	if assigned.Version != 2 || assigned.FiscalCalendarID == nil || *assigned.FiscalCalendarID != ready.ID {
		t.Fatalf("assigned = %+v; want version 2 and the calendar", assigned)
	}
	if got := updatedChangedFields(t, ctx, pool); !reflect.DeepEqual(got, [][]string{{"fiscal_calendar_id"}}) {
		t.Fatalf("changed_fields = %v; want one event naming fiscal_calendar_id", got)
	}
	page, err := s.ListLegalEntities(ctx, store.LegalEntityFilter{}, "", store.MaxPageSize)
	if err != nil || len(page.Items) != 1 || page.Items[0].FiscalCalendarID == nil {
		t.Fatalf("list = %+v, %v; want fiscalCalendarId on the list", page, err)
	}
	if body, _ := json.Marshal(assigned); !containsJSON(body, `"fiscalCalendarId":"`+ready.ID+`"`) {
		t.Fatalf("json = %s", body)
	}
	same := ready.ID
	if again, err := s.UpdateLegalEntity(ctx, entity.ID, 2, store.LegalEntityPatch{FiscalCalendarID: &same}); err != nil || again.Version != 2 {
		t.Fatalf("same calendar = %+v, %v; want no change", again, err)
	}
	cleared := ""
	back, err := s.UpdateLegalEntity(ctx, entity.ID, 2, store.LegalEntityPatch{FiscalCalendarID: &cleared})
	if err != nil || back.Version != 3 || back.FiscalCalendarID != nil {
		t.Fatalf("cleared = %+v, %v; want version 3 and no calendar", back, err)
	}
	if body, _ := json.Marshal(back); containsJSON(body, "fiscalCalendarId") {
		t.Fatalf("json = %s; want fiscalCalendarId omitted", body)
	}
}

func fiscalCalendarNamed(t *testing.T, s *store.Postgres, ctx context.Context, code string) store.FiscalCalendar {
	t.Helper()
	c, err := s.CreateFiscalCalendar(ctx, fiscalCalendar(code, "calendar"), "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func containsJSON(body []byte, want string) bool { return strings.Contains(string(body), want) }

func assertCalendarInvalid(t *testing.T, err error, name string) {
	t.Helper()
	var problem *httpx.Problem
	if !errors.As(err, &problem) || problem.Code != store.CodeLegalEntityCalendarInvalid || problem.Errors["fiscalCalendarId"] != "must be an active fiscal calendar with at least one generated year" {
		t.Fatalf("%s: error = %v; want ORG-LE-CALENDAR-INVALID on fiscalCalendarId", name, err)
	}
}

func TestPostgres_CalendarForeignKeyRefusesAnotherTenantsCalendar(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	calendar := fiscalCalendarNamed(t, s, ctx, "FY")
	other := principalContext(2)
	entity := mustCreate(t, s, other, newEntity("US01"))
	err := dbx.WithTenantTx(other, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(other, `update legal_entity set fiscal_calendar_id = $2 where tenant_id = $1 and id = $3`, testkit.TenantID(2), calendar.ID, entity.ID)
		return err
	})
	if err == nil {
		t.Fatal("the composite foreign key accepted another tenant's calendar")
	}
}

func TestPostgres_ScopedCallerCannotAssignCalendarOfAnotherEntity(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	calendar := retail445(t, s, ctx)
	generate(t, s, ctx, calendar.ID, 2026)
	a, b := mustCreate(t, s, ctx, newEntity("US01")), mustCreate(t, s, ctx, newEntity("US02"))
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: a.ID})
	if _, err := s.UpdateLegalEntity(scoped, b.ID, 1, store.LegalEntityPatch{FiscalCalendarID: &calendar.ID}); problemCode(t, err) != store.CodeLegalEntityNotFound {
		t.Fatalf("scoped assignment error = %v; want %s", err, store.CodeLegalEntityNotFound)
	}
	if got, _ := s.GetLegalEntity(ctx, b.ID); got.FiscalCalendarID != nil {
		t.Fatalf("entity B changed: %+v", got)
	}
}

func TestPostgres_Migration0023AppliesTwiceAndKeepsExistingEntitiesUnassigned(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := principalContext(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	seed(t, ctx, admin)
	s := store.New(app)
	entity := mustCreate(t, s, ctx, newEntity("US01"))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if got, err := s.GetLegalEntity(ctx, entity.ID); err != nil || got.FiscalCalendarID != nil {
		t.Fatalf("entity after re-migrate = %+v, %v; want no calendar", got, err)
	}
}

func TestPostgres_Migration0024AppliesTwiceAndKeepsExistingSitesUnassigned(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := principalContext(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	seed(t, ctx, admin)
	s := store.New(app)
	site := mustCreateSite(t, s, ctx, newSite(mustCreate(t, s, ctx, newEntity("US01")).ID, "S1"))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if got, err := s.GetSite(ctx, site.ID); err != nil || got.FiscalCalendarID != nil {
		t.Fatalf("site after re-migrate = %+v, %v; want no calendar", got, err)
	}
}

func TestPostgres_LegalEntitiesAreTenantLevelForNoEntityPrincipal(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	created := mustCreate(t, s, ctx, newEntity("US01"))
	noEntity := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1)})
	page, err := s.ListLegalEntities(noEntity, store.LegalEntityFilter{}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateLegalEntity(noEntity, newEntity("US02"), ""); err != nil {
		t.Fatalf("no-entity principal must create entities: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != created.ID {
		t.Fatalf("no-entity principal must list its tenant's entities, got %+v", page.Items)
	}
	other, err := s.ListLegalEntities(principalContext(2), store.LegalEntityFilter{}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Items) != 0 {
		t.Fatalf("another tenant saw %d entities", len(other.Items))
	}
}

func TestPostgres_NoEntityPrincipalCannotReachAnotherTenantsEntities(t *testing.T) {
	s, _, _ := lifecycleStore(t)
	foreign := mustCreate(t, s, principalContext(2), newEntity("US01"))
	noEntity := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1)})
	name := "hijacked"
	if _, err := s.UpdateLegalEntity(noEntity, foreign.ID, foreign.Version, store.LegalEntityPatch{Name: &name}); err == nil {
		t.Fatal("no-entity principal updated another tenant's legal entity")
	}
	if _, err := s.GetLegalEntity(noEntity, foreign.ID); err == nil {
		t.Fatal("no-entity principal read another tenant's legal entity")
	}
	if got, err := s.GetLegalEntity(principalContext(2), foreign.ID); err != nil || got.Name == name {
		t.Fatalf("foreign entity after attempted update = %+v, %v", got, err)
	}
}
