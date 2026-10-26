package store_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const fiscalGeneratedType = "dev.passage.org.fiscalcalendar.generated.v1"

func fiscalCalendar(code, pattern string) store.FiscalCalendar {
	return store.FiscalCalendar{Code: code, Name: "Calendar " + code, Pattern: pattern, StartMonth: 1, WeekStart: "monday", OpeningClosing: true}
}

func retail445(t *testing.T, s *store.Postgres, ctx context.Context) store.FiscalCalendar {
	t.Helper()
	in := fiscalCalendar("RETAIL", "4-4-5")
	in.StartMonth, in.WeekStart = 2, "sunday"
	c, err := s.CreateFiscalCalendar(ctx, in, "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func fiscalRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from `+table).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestPostgres_FiscalCalendarCreateReplayReuseAndDuplicate(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	first, err := s.CreateFiscalCalendar(ctx, fiscalCalendar("FY", "calendar"), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.WeekStart != "monday" || first.StartMonth != 1 || !first.OpeningClosing || first.SpecialPeriods != 0 || first.Status != "active" || first.Version != 1 {
		t.Fatalf("created = %+v; want the defaults read back, weekStart monday", first)
	}
	replay, err := s.CreateFiscalCalendar(ctx, fiscalCalendar("FY", "calendar"), "key-1")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %+v, %v; want the original calendar", replay, err)
	}
	changed := fiscalCalendar("FY", "calendar")
	changed.SpecialPeriods = 2
	if _, err := s.CreateFiscalCalendar(ctx, changed, "key-1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key with a different body error = %v", err)
	}
	if _, err := s.CreateFiscalCalendar(ctx, fiscalCalendar("FY", "4-4-5"), "key-2"); problemCode(t, err) != store.CodeFiscalCalendarCode {
		t.Fatalf("duplicate code error = %v", err)
	}
	if _, err := s.GetFiscalCalendar(principalContext(2), first.ID); problemCode(t, err) != store.CodeFiscalCalendarNotFound {
		t.Fatalf("other tenant get error = %v", err)
	}
}

func TestPostgres_GenerateFiscalYearFiscal2028IsIdempotentAndEmitsOneEvent(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	c := retail445(t, s, ctx)
	first, err := s.GenerateFiscalYear(ctx, c.ID, 2028, nil, false)
	if err != nil || !first.Generated || len(first.Periods) != 14 {
		t.Fatalf("first = %+v, %v; want 14 generated periods", first, err)
	}
	p12 := first.Periods[12]
	if p12.PeriodNo != 12 || !p12.Week53 || p12.EndDate != "2029-02-03" {
		t.Fatalf("period 12 = %+v; want week53 ending 2029-02-03", p12)
	}
	replay, err := s.GenerateFiscalYear(ctx, c.ID, 2028, nil, false)
	if err != nil || replay.Generated || len(replay.Periods) != 14 {
		t.Fatalf("replay = %+v, %v; want 14 stored periods and generated false", replay, err)
	}
	events := unitEvents(t, ctx, pool, fiscalGeneratedType)
	if len(events) != 1 || events[0]["calendar_id"] != c.ID || events[0]["fiscal_year"] != float64(2028) {
		t.Fatalf("events = %v; want exactly one for the calendar and year", events)
	}
	eventPeriods, _ := events[0]["periods"].([]any)
	listed, err := s.ListFiscalPeriods(ctx, c.ID, 2028)
	if err != nil || len(eventPeriods) != 14 || len(listed) != 14 {
		t.Fatalf("event periods = %d, listed = %d, %v; want 14 each", len(eventPeriods), len(listed), err)
	}
	for i, l := range listed {
		e := eventPeriods[i].(map[string]any)
		if e["period_no"] != float64(l.PeriodNo) || e["name"] != l.Name || e["start_date"] != l.StartDate || e["end_date"] != l.EndDate || e["type"] != l.Type || e["week53"] != l.Week53 {
			t.Fatalf("event period %v != listed %+v", e, l)
		}
	}
	opening, _ := time.Parse("2006-01-02", listed[0].StartDate)
	closing, _ := time.Parse("2006-01-02", listed[13].EndDate)
	days := 0
	for _, p := range listed[1:13] {
		start, _ := time.Parse("2006-01-02", p.StartDate)
		end, _ := time.Parse("2006-01-02", p.EndDate)
		days += int(end.Sub(start).Hours()/24) + 1
	}
	if want := int(closing.Sub(opening).Hours()/24) + 1; days != want || days != 371 {
		t.Fatalf("operating days = %d, fiscal year days = %d; want both 371", days, want)
	}
}

func TestPostgres_GenerateFiscalYearForceReplacesRowsAndEmitsASecondEvent(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	c := retail445(t, s, ctx)
	if _, err := s.GenerateFiscalYear(ctx, c.ID, 2028, nil, false); err != nil {
		t.Fatal(err)
	}
	forced, err := s.GenerateFiscalYear(ctx, c.ID, 2028, nil, true)
	if err != nil || !forced.Generated || len(forced.Periods) != 14 {
		t.Fatalf("forced = %+v, %v", forced, err)
	}
	if n := fiscalRows(t, ctx, pool, "fiscal_period"); n != 14 {
		t.Fatalf("fiscal_period rows = %d; want 14 after replacement", n)
	}
	if got := len(unitEvents(t, ctx, pool, fiscalGeneratedType)); got != 2 {
		t.Fatalf("events = %d; want 2", got)
	}
}

func TestPostgres_GenerateFiscalYearCustomRangesAreValidatedAndWriteNothingOnFailure(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	custom, err := s.CreateFiscalCalendar(ctx, fiscalCalendar("CUSTOM", "custom"), "")
	if err != nil {
		t.Fatal(err)
	}
	standard := retail445(t, s, ctx)
	ranges := func(ends ...[2]time.Time) []domain.DateRange {
		out := make([]domain.DateRange, len(ends))
		for i, e := range ends {
			out[i] = domain.DateRange{Start: e[0], End: e[1]}
		}
		return out
	}
	tests := []struct {
		name     string
		id       string
		custom   []domain.DateRange
		wantCode string
		wantText string
	}{
		{"gap names the first uncovered day", custom.ID, ranges([2]time.Time{day(2028, 1, 1), day(2028, 1, 31)}, [2]time.Time{day(2028, 2, 5), day(2028, 12, 31)}), domain.CodeCalGap, "2028-02-01"},
		{"overlap", custom.ID, ranges([2]time.Time{day(2028, 1, 1), day(2028, 2, 15)}, [2]time.Time{day(2028, 2, 10), day(2028, 12, 31)}), domain.CodeCalOverlap, ""},
		{"custom calendar without ranges", custom.ID, nil, domain.CodeCalPattern, ""},
		{"ranges on a non-custom calendar", standard.ID, ranges([2]time.Time{day(2028, 1, 1), day(2028, 12, 31)}), domain.CodeCalPattern, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.GenerateFiscalYear(ctx, tc.id, 2028, tc.custom, false)
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != tc.wantCode || !strings.Contains(de.Detail, tc.wantText) {
				t.Fatalf("error = %v; want domain error %s containing %q", err, tc.wantCode, tc.wantText)
			}
			if n := fiscalRows(t, ctx, pool, "fiscal_period"); n != 0 {
				t.Fatalf("fiscal_period rows = %d; want none after a failure", n)
			}
			if n := len(unitEvents(t, ctx, pool, fiscalGeneratedType)); n != 0 {
				t.Fatalf("events = %d; want none after a failure", n)
			}
		})
	}
	ok, err := s.GenerateFiscalYear(ctx, custom.ID, 2028, ranges([2]time.Time{day(2028, 1, 1), day(2028, 6, 30)}, [2]time.Time{day(2028, 7, 1), day(2028, 12, 31)}), false)
	if err != nil || !ok.Generated || len(ok.Periods) != 4 {
		t.Fatalf("tiling custom ranges = %+v, %v; want 4 periods", ok, err)
	}
}

func TestPostgres_ListFiscalPeriodsOrdersAndIsolatesTenants(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	c := retail445(t, s, ctx)
	empty, err := s.ListFiscalPeriods(ctx, c.ID, 0)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("no generated year = %#v, %v; want an empty non-nil slice", empty, err)
	}
	for _, year := range []int{2029, 2028} {
		if _, err := s.GenerateFiscalYear(ctx, c.ID, year, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.ListFiscalPeriods(ctx, c.ID, 0)
	if err != nil || len(all) != 28 || all[0].FiscalYear != 2028 || all[14].FiscalYear != 2029 {
		t.Fatalf("all years = %d rows, %v; want 28 ordered by year", len(all), err)
	}
	other := principalContext(2)
	if _, err := s.ListFiscalPeriods(other, c.ID, 0); problemCode(t, err) != store.CodeFiscalCalendarNotFound {
		t.Fatalf("other tenant list error = %v", err)
	}
	if _, err := s.GenerateFiscalYear(other, c.ID, 2030, nil, false); problemCode(t, err) != store.CodeFiscalCalendarNotFound {
		t.Fatalf("other tenant generate error = %v", err)
	}
	if calendars, periods := fiscalRows(t, other, pool, "fiscal_calendar"), fiscalRows(t, other, pool, "fiscal_period"); calendars != 0 || periods != 0 {
		t.Fatalf("other tenant sees %d calendars and %d periods; want none", calendars, periods)
	}
}

func TestPostgres_ConcurrentGenerateFiscalYearProducesOneYearAndOneEvent(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	c := retail445(t, s, ctx)
	results := make([]store.FiscalYear, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = s.GenerateFiscalYear(ctx, c.ID, 2028, nil, false)
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || results[0].Generated == results[1].Generated {
		t.Fatalf("results = %+v, %+v, errors = %v; want exactly one Generated", results[0], results[1], errs)
	}
	if n := len(unitEvents(t, ctx, pool, fiscalGeneratedType)); n != 1 || fiscalRows(t, ctx, pool, "fiscal_period") != 14 {
		t.Fatalf("events = %d, rows = %d; want 1 and 14", n, fiscalRows(t, ctx, pool, "fiscal_period"))
	}
}

func generate(t *testing.T, s *store.Postgres, ctx context.Context, id string, years ...int) {
	t.Helper()
	for _, y := range years {
		if _, err := s.GenerateFiscalYear(ctx, id, y, nil, false); err != nil {
			t.Fatal(err)
		}
	}
}

func assignCalendar(t *testing.T, s *store.Postgres, ctx context.Context, entity store.LegalEntity, calendarID string) store.LegalEntity {
	t.Helper()
	assigned, err := s.UpdateLegalEntity(ctx, entity.ID, entity.Version, store.LegalEntityPatch{FiscalCalendarID: &calendarID})
	if err != nil {
		t.Fatal(err)
	}
	return assigned
}

func TestPostgres_ListFiscalCalendarsPagesFiltersAndReportsGeneratedYears(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	a, b, c := fiscalCalendar("A", "calendar"), fiscalCalendar("B", "4-4-5"), fiscalCalendar("C", "calendar")
	created := map[string]store.FiscalCalendar{}
	for _, in := range []store.FiscalCalendar{c, a, b} {
		got, err := s.CreateFiscalCalendar(ctx, in, "")
		if err != nil || got.GeneratedYears == nil || len(got.GeneratedYears) != 0 {
			t.Fatalf("created = %+v, %v; want generatedYears as an empty non-nil slice", got, err)
		}
		created[in.Code] = got
	}
	generate(t, s, ctx, created["A"].ID, 2027, 2026)
	first, err := s.ListFiscalCalendars(ctx, store.FiscalCalendarFilter{}, "", 2)
	if err != nil || len(first.Items) != 2 || first.NextCode != "B" || first.Items[0].Code != "A" {
		t.Fatalf("first page = %+v, %v; want A, B and cursor B", first, err)
	}
	if !reflect.DeepEqual(first.Items[0].GeneratedYears, []int{2026, 2027}) || first.Items[1].GeneratedYears == nil || len(first.Items[1].GeneratedYears) != 0 {
		t.Fatalf("generatedYears = %v and %v; want [2026 2027] and []", first.Items[0].GeneratedYears, first.Items[1].GeneratedYears)
	}
	second, err := s.ListFiscalCalendars(ctx, store.FiscalCalendarFilter{}, first.NextCode, 2)
	if err != nil || len(second.Items) != 1 || second.Items[0].Code != "C" || second.NextCode != "" {
		t.Fatalf("second page = %+v, %v; want C and no cursor", second, err)
	}
	byPattern, err := s.ListFiscalCalendars(ctx, store.FiscalCalendarFilter{Pattern: "4-4-5"}, "", store.MaxPageSize)
	if err != nil || len(byPattern.Items) != 1 || byPattern.Items[0].Code != "B" {
		t.Fatalf("pattern filter = %+v, %v", byPattern, err)
	}
	if _, err := s.UpdateFiscalCalendar(ctx, created["C"].ID, 1, store.FiscalCalendarPatch{Status: ptr("inactive")}); err != nil {
		t.Fatal(err)
	}
	byStatus, err := s.ListFiscalCalendars(ctx, store.FiscalCalendarFilter{Status: "inactive"}, "", store.MaxPageSize)
	if err != nil || len(byStatus.Items) != 1 || byStatus.Items[0].Code != "C" {
		t.Fatalf("status filter = %+v, %v", byStatus, err)
	}
	got, err := s.GetFiscalCalendar(ctx, created["A"].ID)
	if err != nil || !reflect.DeepEqual(got.GeneratedYears, []int{2026, 2027}) {
		t.Fatalf("get = %+v, %v; want the list's generatedYears", got, err)
	}
	other, err := s.ListFiscalCalendars(principalContext(2), store.FiscalCalendarFilter{}, "", store.MaxPageSize)
	if err != nil || len(other.Items) != 0 {
		t.Fatalf("other tenant list = %+v, %v; want none", other, err)
	}
}

func TestPostgres_UpdateFiscalCalendarVersionsNoOpAndTenantIsolation(t *testing.T) {
	s, ctx, _ := lifecycleStore(t)
	c, err := s.CreateFiscalCalendar(ctx, fiscalCalendar("FY", "calendar"), "")
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := s.UpdateFiscalCalendar(ctx, c.ID, 1, store.FiscalCalendarPatch{Name: ptr("Renamed")})
	if err != nil || renamed.Name != "Renamed" || renamed.Version != 2 || renamed.Code != "FY" {
		t.Fatalf("rename = %+v, %v; want version 2", renamed, err)
	}
	same, err := s.UpdateFiscalCalendar(ctx, c.ID, 2, store.FiscalCalendarPatch{Name: ptr("Renamed")})
	if err != nil || same.Version != 2 || !same.UpdatedAt.Equal(renamed.UpdatedAt) {
		t.Fatalf("unchanged patch = %+v, %v; want the same version and updatedAt", same, err)
	}
	if _, err := s.UpdateFiscalCalendar(ctx, c.ID, 1, store.FiscalCalendarPatch{Name: ptr("Stale")}); problemCode(t, err) != store.CodeVersionConflict {
		t.Fatalf("stale version error = %v", err)
	}
	if got, _ := s.GetFiscalCalendar(ctx, c.ID); got.Name != "Renamed" || got.Version != 2 {
		t.Fatalf("stale patch wrote: %+v", got)
	}
	if _, err := s.UpdateFiscalCalendar(principalContext(2), c.ID, 2, store.FiscalCalendarPatch{Name: ptr("Hijacked")}); problemCode(t, err) != store.CodeFiscalCalendarNotFound {
		t.Fatalf("other tenant update error = %v", err)
	}
}

func TestPostgres_FiscalCalendarInactiveRefusedWhileAssigned(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	c, err := s.CreateFiscalCalendar(ctx, fiscalCalendar("FY", "calendar"), "")
	if err != nil {
		t.Fatal(err)
	}
	generate(t, s, ctx, c.ID, 2026)
	one, two := mustCreate(t, s, ctx, newEntity("US01")), mustCreate(t, s, ctx, newEntity("US02"))
	one, two = assignCalendar(t, s, ctx, one, c.ID), assignCalendar(t, s, ctx, two, c.ID)
	var scopes string
	if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select current_setting('passage.legal_entity_scopes')`).Scan(&scopes)
	}); err != nil || scopes != "*" {
		t.Fatalf("unscoped caller scopes = %q, %v; want *", scopes, err)
	}
	inactive := store.FiscalCalendarPatch{Status: ptr("inactive")}
	_, err = s.UpdateFiscalCalendar(ctx, c.ID, 1, inactive)
	var problem *httpx.Problem
	if !errors.As(err, &problem) || problem.Code != store.CodeFiscalCalendarInUse || problem.Detail != "Fiscal calendar FY is assigned to 2 active legal entities." {
		t.Fatalf("in-use error = %v; want ORG-CAL-IN-USE counting both entities", err)
	}
	if got, _ := s.GetFiscalCalendar(ctx, c.ID); got.Status != "active" || got.Version != 1 {
		t.Fatalf("refused patch wrote: %+v", got)
	}
	cleared := ""
	if _, err := s.UpdateLegalEntity(ctx, one.ID, one.Version, store.LegalEntityPatch{FiscalCalendarID: &cleared}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateFiscalCalendar(ctx, c.ID, 1, inactive); problemCode(t, err) != store.CodeFiscalCalendarInUse {
		t.Fatalf("one entity still assigned, error = %v", err)
	}
	if _, err := s.UpdateLegalEntity(ctx, two.ID, two.Version, store.LegalEntityPatch{Status: ptr("inactive")}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.UpdateFiscalCalendar(ctx, c.ID, 1, inactive); err != nil || got.Status != "inactive" || got.Version != 2 {
		t.Fatalf("after clearing and deactivating = %+v, %v; want inactive version 2", got, err)
	}
}

func TestPostgres_GenerateFiscalYearOnInactiveCalendarRefusesNewYearsOnly(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	c, err := s.CreateFiscalCalendar(ctx, fiscalCalendar("FY", "calendar"), "")
	if err != nil {
		t.Fatal(err)
	}
	generate(t, s, ctx, c.ID, 2026)
	if _, err := s.UpdateFiscalCalendar(ctx, c.ID, 1, store.FiscalCalendarPatch{Status: ptr("inactive")}); err != nil {
		t.Fatal(err)
	}
	periods, outbox := fiscalRows(t, ctx, pool, "fiscal_period"), len(outboxRows(t, ctx, pool))
	if _, err := s.GenerateFiscalYear(ctx, c.ID, 2027, nil, false); problemCode(t, err) != store.CodeFiscalCalendarInactive {
		t.Fatalf("new year error = %v; want ORG-CAL-INACTIVE", err)
	}
	if _, err := s.GenerateFiscalYear(ctx, c.ID, 2026, nil, true); problemCode(t, err) != store.CodeFiscalCalendarInactive {
		t.Fatalf("force error = %v; want ORG-CAL-INACTIVE", err)
	}
	if fiscalRows(t, ctx, pool, "fiscal_period") != periods || len(outboxRows(t, ctx, pool)) != outbox {
		t.Fatal("a refused generate wrote periods or an outbox row")
	}
	replay, err := s.GenerateFiscalYear(ctx, c.ID, 2026, nil, false)
	if err != nil || replay.Generated || len(replay.Periods) != 14 {
		t.Fatalf("replay = %+v, %v; want 14 stored periods and generated false", replay, err)
	}
}
