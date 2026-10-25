package store_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
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
	got, err := s.GetFiscalCalendar(ctx, first.ID)
	if err != nil || got.ID != first.ID {
		t.Fatalf("get = %+v, %v", got, err)
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
	one, err := s.ListFiscalPeriods(ctx, c.ID, 2028)
	if err != nil || len(one) != 14 || one[0].PeriodNo != 0 || one[1].PeriodNo != 1 || one[12].PeriodNo != 12 || one[13].PeriodNo != 99 {
		t.Fatalf("2028 = %+v, %v; want 0, 1..12, 99", one, err)
	}
	other := principalContext(2)
	if _, err := s.ListFiscalPeriods(other, c.ID, 0); problemCode(t, err) != store.CodeFiscalCalendarNotFound {
		t.Fatalf("other tenant list error = %v", err)
	}
	if _, err := s.GenerateFiscalYear(other, c.ID, 2030, nil, false); problemCode(t, err) != store.CodeFiscalCalendarNotFound {
		t.Fatalf("other tenant generate error = %v", err)
	}
	if _, err := s.ListFiscalPeriods(ctx, "00000000-0000-0000-0000-0000000000ff", 0); problemCode(t, err) != store.CodeFiscalCalendarNotFound {
		t.Fatalf("unknown calendar list error = %v", err)
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

func TestPostgres_FiscalPeriodRejectsBadPeriodNumberAndReversedDates(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	c := retail445(t, s, ctx)
	tests := []struct {
		name       string
		periodNo   int
		start, end time.Time
	}{
		{"period number 50", 50, day(2028, 1, 1), day(2028, 1, 2)},
		{"end before start", 1, day(2028, 1, 2), day(2028, 1, 1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `insert into fiscal_period (tenant_id, calendar_id, fiscal_year, period_no, name, start_date, end_date, type)
					values ($1, $2, 2028, $3, 'x', $4, $5, 'operating')`, testkit.TenantID(1), c.ID, tc.periodNo, tc.start, tc.end)
				return err
			})
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
				t.Fatalf("insert error = %v; want a check violation", err)
			}
		})
	}
}
