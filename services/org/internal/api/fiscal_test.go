package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	fiscalPath     = "/v1/org/fiscal-calendars"
	fiscalDuties   = "org.read,org.fiscal-calendar.write"
	calendarID     = "0190b6c0-0000-7000-8000-0000000000c1"
	otherCalendar  = "0190b6c0-0000-7000-8000-0000000000c2"
	createCalendar = `{"code":"fy","name":" Retail ","pattern":"4-4-5"}`
)

func (f *fakeStore) CreateFiscalCalendar(_ context.Context, in store.FiscalCalendar, _ string) (store.FiscalCalendar, error) {
	if f.err != nil {
		return store.FiscalCalendar{}, f.err
	}
	in.ID, in.Status, in.Version = calendarID, "active", 1
	f.calendars = append(f.calendars, in)
	return in, nil
}

func (f *fakeStore) calendar(id string) (store.FiscalCalendar, error) {
	if f.err != nil {
		return store.FiscalCalendar{}, f.err
	}
	for _, c := range f.calendars {
		if c.ID == id {
			return c, nil
		}
	}
	return store.FiscalCalendar{}, httpx.NewProblem(http.StatusNotFound, store.CodeFiscalCalendarNotFound, "fiscal calendar not found")
}

func (f *fakeStore) GetFiscalCalendar(_ context.Context, id string) (store.FiscalCalendar, error) {
	return f.calendar(id)
}

func (f *fakeStore) ListFiscalCalendars(_ context.Context, filter store.FiscalCalendarFilter, after string, limit int) (store.FiscalCalendarPage, error) {
	if f.err != nil {
		return store.FiscalCalendarPage{}, f.err
	}
	page := store.FiscalCalendarPage{Items: []store.FiscalCalendar{}}
	for _, c := range f.calendars {
		if c.Code > after && (filter.Status == "" || c.Status == filter.Status) && (filter.Pattern == "" || c.Pattern == filter.Pattern) {
			page.Items = append(page.Items, c)
		}
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCode = page.Items[limit-1].Code
	}
	return page, nil
}

func (f *fakeStore) UpdateFiscalCalendar(_ context.Context, id string, version int, in store.FiscalCalendarPatch) (store.FiscalCalendar, error) {
	f.updateCalls++
	for i := range f.calendars {
		c := &f.calendars[i]
		if c.ID != id {
			continue
		}
		if c.Version != version {
			return store.FiscalCalendar{}, httpx.NewProblem(http.StatusConflict, store.CodeVersionConflict, "current version is 1")
		}
		if in.Name != nil && *in.Name != c.Name {
			c.Name = *in.Name
			c.Version++
		}
		return *c, nil
	}
	return store.FiscalCalendar{}, httpx.NewProblem(http.StatusNotFound, store.CodeFiscalCalendarNotFound, "fiscal calendar not found")
}

func (f *fakeStore) GenerateFiscalYear(_ context.Context, id string, year int, _ []domain.DateRange, force bool) (store.FiscalYear, error) {
	f.generateCalls++
	f.generatedID = id
	if _, err := f.calendar(id); err != nil {
		return store.FiscalYear{}, err
	}
	if f.generateErr != nil {
		return store.FiscalYear{}, f.generateErr
	}
	key := id + "/" + strconv.Itoa(year)
	replay := f.generatedYears[key] && !force
	if f.generatedYears == nil {
		f.generatedYears = map[string]bool{}
	}
	f.generatedYears[key] = true
	return store.FiscalYear{CalendarID: id, FiscalYear: year, Generated: !replay, Periods: []store.FiscalPeriod{{FiscalYear: year, PeriodNo: 1, Name: "P1"}}}, nil
}

func (f *fakeStore) ListFiscalPeriods(_ context.Context, id string, _ int) ([]store.FiscalPeriod, error) {
	if _, err := f.calendar(id); err != nil {
		return nil, err
	}
	return []store.FiscalPeriod{}, nil
}

func seededCalendars() *fakeStore {
	return &fakeStore{calendars: []store.FiscalCalendar{{ID: calendarID, Code: "FY", Name: "Retail", Pattern: "4-4-5", StartMonth: 1, WeekStart: "monday", Status: "active", Version: 1}}}
}

type checkerFake struct {
	locked bool
	err    error
}

func (c checkerFake) HasPostings(context.Context, string, int) (bool, error) { return c.locked, c.err }

func TestFiscalCalendarCreate(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		duties     string
		headers    []string
		withTenant bool
		wantStatus int
		wantBody   string
	}{
		{"defaults", createCalendar, fiscalDuties, nil, true, http.StatusCreated, `"code":"FY","name":"Retail","pattern":"4-4-5","startMonth":1,"weekStart":"monday","openingClosing":true,"specialPeriods":0`},
		{"explicit values", `{"code":"FY","name":"n","pattern":"custom","startMonth":2,"weekStart":"sunday","openingClosing":false,"specialPeriods":4}`, fiscalDuties, nil, true, http.StatusCreated, `"startMonth":2,"weekStart":"sunday","openingClosing":false,"specialPeriods":4`},
		{"every field invalid", `{"code":"bad code","name":" ","pattern":"weekly","startMonth":13,"weekStart":"funday","specialPeriods":5}`, fiscalDuties, nil, true, http.StatusBadRequest, `"specialPeriods"`},
		{"upper-case weekday", `{"code":"FY","name":"n","pattern":"calendar","weekStart":"Monday"}`, fiscalDuties, nil, true, http.StatusBadRequest, `"weekStart"`},
		{"fractional start month", `{"code":"FY","name":"n","pattern":"calendar","startMonth":2.5}`, fiscalDuties, nil, true, http.StatusBadRequest, "COM-0001"},
		{"unknown field", `{"code":"FY","name":"n","pattern":"calendar","x":1}`, fiscalDuties, nil, true, http.StatusBadRequest, "COM-0001"},
		{"no tenant", createCalendar, "", nil, false, http.StatusUnauthorized, "AUTH-0001"},
		{"read duty only", createCalendar, "org.read", nil, true, http.StatusForbidden, "AUTH-0003"},
		{"legal-entity scoped caller", createCalendar, fiscalDuties, []string{"X-Passage-Legal-Entity", testkit.TenantID(2)}, true, http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			r, _ := newRouter(f)
			rec := do(r, http.MethodPost, fiscalPath, tc.body, tc.duties, tc.withTenant, tc.headers...)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if tc.wantStatus != http.StatusCreated && len(f.calendars) != 0 {
				t.Fatalf("store was called for a rejected request")
			}
		})
	}
}

func TestFiscalCalendarCreateStoreErrors(t *testing.T) {
	r, _ := newRouter(&fakeStore{err: httpx.NewProblem(http.StatusConflict, store.CodeFiscalCalendarCode, "exists")})
	if rec := do(r, http.MethodPost, fiscalPath, createCalendar, fiscalDuties, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ORG-CAL-CODE-DUPLICATE") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	r, _ = newRouter(&fakeStore{err: errors.New("pg secret")})
	if rec := do(r, http.MethodPost, fiscalPath, createCalendar, fiscalDuties, true); rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestFiscalCalendarReads(t *testing.T) {
	periods := fiscalPath + "/" + calendarID + "/periods"
	tests := []struct {
		name       string
		path       string
		duties     string
		wantStatus int
		wantBody   string
	}{
		{"get known id", fiscalPath + "/" + calendarID, fiscalDuties, http.StatusOK, `"code":"FY"`},
		{"get unknown id", fiscalPath + "/" + otherCalendar, fiscalDuties, http.StatusNotFound, "ORG-CAL-NOT-FOUND"},
		{"get non-UUID id", fiscalPath + "/nope", fiscalDuties, http.StatusNotFound, "ORG-CAL-NOT-FOUND"},
		{"get no duty", fiscalPath + "/" + calendarID, "", http.StatusForbidden, "AUTH-0003"},
		{"periods one year", periods + "?fiscalYear=2026", fiscalDuties, http.StatusOK, `"items":[]`},
		{"periods year out of range", periods + "?fiscalYear=1899", fiscalDuties, http.StatusBadRequest, `"fiscalYear"`},
		{"periods year not numeric", periods + "?fiscalYear=abc", fiscalDuties, http.StatusBadRequest, `"fiscalYear"`},
		{"periods unknown calendar", fiscalPath + "/" + otherCalendar + "/periods", fiscalDuties, http.StatusNotFound, "ORG-CAL-NOT-FOUND"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(seededCalendars())
			rec := do(r, http.MethodGet, tc.path, "", tc.duties, true)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestFiscalYearGenerate(t *testing.T) {
	generate := fiscalPath + "/" + calendarID + ":generate"
	gap := &domain.Error{Code: domain.CodeCalGap, Detail: "2028-03-01 is not covered"}
	tests := []struct {
		name       string
		store      *fakeStore
		path       string
		body       string
		duties     string
		headers    []string
		wantStatus int
		wantBody   string
	}{
		{"unknown calendar", seededCalendars(), fiscalPath + "/" + otherCalendar + ":generate", `{"fiscalYear":2028}`, fiscalDuties, nil, http.StatusNotFound, "ORG-CAL-NOT-FOUND"},
		{"missing year", seededCalendars(), generate, `{}`, fiscalDuties, nil, http.StatusBadRequest, `"fiscalYear"`},
		{"year below range", seededCalendars(), generate, `{"fiscalYear":1899}`, fiscalDuties, nil, http.StatusBadRequest, `"fiscalYear"`},
		{"string year", seededCalendars(), generate, `{"fiscalYear":"2028"}`, fiscalDuties, nil, http.StatusBadRequest, "COM-0001"},
		{"malformed date", seededCalendars(), generate, `{"fiscalYear":2028,"periods":[{"start":"2028-13-40","end":"2028-12-31"}]}`, fiscalDuties, nil, http.StatusBadRequest, `periods[0].start`},
		{"generator gap", &fakeStore{calendars: seededCalendars().calendars, generateErr: gap}, generate, `{"fiscalYear":2028,"periods":[{"start":"2028-01-01","end":"2028-02-28"}]}`, fiscalDuties, nil, http.StatusUnprocessableEntity, "ORG-CAL-GAP"},
		{"no duty", seededCalendars(), generate, `{"fiscalYear":2028}`, "org.read", nil, http.StatusForbidden, "AUTH-0003"},
		{"scoped caller", seededCalendars(), generate, `{"fiscalYear":2028}`, fiscalDuties, []string{"X-Passage-Legal-Entity", testkit.TenantID(2)}, http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, http.MethodPost, tc.path, tc.body, tc.duties, true, tc.headers...)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if tc.wantStatus == http.StatusBadRequest && tc.store.generateCalls != 0 {
				t.Fatalf("store was called for a malformed request")
			}
		})
	}
}

func TestFiscalYearGenerateReplayAndMetrics(t *testing.T) {
	f := seededCalendars()
	r, a := newRouter(f)
	generate := fiscalPath + "/" + calendarID + ":generate"
	first := do(r, http.MethodPost, generate, `{"fiscalYear":2028}`, fiscalDuties, true)
	replay := do(r, http.MethodPost, generate, `{"fiscalYear":2028}`, fiscalDuties, true)
	if first.Code != http.StatusCreated || !strings.Contains(first.Body.String(), `"generated":true`) || replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), `"generated":false`) {
		t.Fatalf("first %d %s, replay %d %s", first.Code, first.Body.String(), replay.Code, replay.Body.String())
	}
	if f.generatedID != calendarID {
		t.Fatalf("handler saw id %q; want %q", f.generatedID, calendarID)
	}
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"org_fiscal_calendars_created_total 0", "org_fiscal_years_generated_total 1"} {
		if !strings.Contains(metrics.Body.String(), want) {
			t.Fatalf("metrics lack %q: %s", want, metrics.Body.String())
		}
	}
}

func TestFiscalYearForceUsesPostingChecker(t *testing.T) {
	generate := fiscalPath + "/" + calendarID + ":generate"
	tests := []struct {
		name       string
		checker    checkerFake
		force      string
		wantStatus int
		wantBody   string
		wantCalls  int
	}{
		{"postings lock the year", checkerFake{locked: true}, `true`, http.StatusConflict, "Fiscal year 2028 has postings and cannot be regenerated.", 0},
		{"checker failure fails closed", checkerFake{err: errors.New("ledger down")}, `true`, http.StatusServiceUnavailable, "COM-0503", 0},
		{"no postings regenerates", checkerFake{}, `true`, http.StatusCreated, `"generated":true`, 1},
		{"checker ignored without force", checkerFake{locked: true}, `false`, http.StatusCreated, `"generated":true`, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := seededCalendars()
			r, a := newRouter(f)
			a.WithPostingChecker(tc.checker)
			rec := do(r, http.MethodPost, generate, `{"fiscalYear":2028,"force":`+tc.force+`}`, fiscalDuties, true)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) || f.generateCalls != tc.wantCalls || strings.Contains(rec.Body.String(), "ledger down") {
				t.Fatalf("got %d %s with %d store calls; want %d containing %s with %d", rec.Code, rec.Body.String(), f.generateCalls, tc.wantStatus, tc.wantBody, tc.wantCalls)
			}
		})
	}
}

func TestFiscalCalendarList(t *testing.T) {
	three := func() *fakeStore {
		return &fakeStore{calendars: []store.FiscalCalendar{
			{ID: calendarID, Code: "A", Pattern: "4-4-5", Status: "active", GeneratedYears: []int{2026, 2027}},
			{ID: otherCalendar, Code: "B", Pattern: "calendar", Status: "inactive", GeneratedYears: []int{}},
			{ID: "0190b6c0-0000-7000-8000-0000000000c3", Code: "C", Pattern: "4-4-5", Status: "active", GeneratedYears: []int{}},
		}}
	}
	tests := []struct {
		name       string
		query      string
		duties     string
		withTenant bool
		store      *fakeStore
		wantStatus int
		wantBody   string
	}{
		{"first page", "?limit=2", fiscalDuties, true, three(), http.StatusOK, `"generatedYears":[2026,2027]`},
		{"cursor", "?limit=2", fiscalDuties, true, three(), http.StatusOK, `"nextCursor":"Qg"`},
		{"next page", "?cursor=Qg", fiscalDuties, true, three(), http.StatusOK, `"code":"C"`},
		{"status filter", "?status=inactive", fiscalDuties, true, three(), http.StatusOK, `"generatedYears":[]`},
		{"pattern filter", "?pattern=calendar", fiscalDuties, true, three(), http.StatusOK, `"code":"B"`},
		{"pattern invalid", "?pattern=weekly", fiscalDuties, true, three(), http.StatusBadRequest, `"pattern"`},
		{"status invalid", "?status=closed", fiscalDuties, true, three(), http.StatusBadRequest, `"status"`},
		{"limit zero", "?limit=0", fiscalDuties, true, three(), http.StatusBadRequest, "COM-0001"},
		{"cursor not base64", "?cursor=***", fiscalDuties, true, three(), http.StatusBadRequest, "COM-0001"},
		{"no tenant", "", "", false, three(), http.StatusUnauthorized, "AUTH-0001"},
		{"no duty", "", "", true, three(), http.StatusForbidden, "AUTH-0003"},
		{"store down", "", fiscalDuties, true, &fakeStore{err: errors.New("pg secret")}, http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, http.MethodGet, fiscalPath+tc.query, "", tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) || strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestFiscalCalendarUpdate(t *testing.T) {
	path := fiscalPath + "/" + calendarID
	scoped := []string{"X-Passage-Legal-Entity", testkit.TenantID(2)}
	tests := []struct {
		name       string
		path       string
		body       string
		duties     string
		headers    []string
		wantStatus int
		wantBody   string
		wantCalls  int
	}{
		{"rename", path, `{"version":1,"name":" Renamed "}`, fiscalDuties, nil, http.StatusOK, `"name":"Renamed","pattern":"4-4-5"`, 1},
		{"stale version", path, `{"version":2,"name":"x"}`, fiscalDuties, nil, http.StatusConflict, "ORG-VERSION-CONFLICT", 1},
		{"unknown id", fiscalPath + "/" + otherCalendar, `{"version":1}`, fiscalDuties, nil, http.StatusNotFound, "ORG-CAL-NOT-FOUND", 1},
		{"non-UUID id", fiscalPath + "/nope", `{"version":1}`, fiscalDuties, nil, http.StatusNotFound, "ORG-CAL-NOT-FOUND", 0},
		{"immutable pattern", path, `{"version":1,"pattern":"calendar"}`, fiscalDuties, nil, http.StatusBadRequest, "COM-0001", 0},
		{"immutable code", path, `{"version":1,"code":"X"}`, fiscalDuties, nil, http.StatusBadRequest, "COM-0001", 0},
		{"immutable specialPeriods", path, `{"version":1,"specialPeriods":1}`, fiscalDuties, nil, http.StatusBadRequest, "COM-0001", 0},
		{"missing version", path, `{"name":"x"}`, fiscalDuties, nil, http.StatusBadRequest, `"version"`, 0},
		{"version zero", path, `{"version":0}`, fiscalDuties, nil, http.StatusBadRequest, `"version"`, 0},
		{"string version", path, `{"version":"1"}`, fiscalDuties, nil, http.StatusBadRequest, "COM-0001", 0},
		{"empty name", path, `{"version":1,"name":"  "}`, fiscalDuties, nil, http.StatusBadRequest, `"name"`, 0},
		{"name over 120", path, `{"version":1,"name":"` + strings.Repeat("n", 121) + `"}`, fiscalDuties, nil, http.StatusBadRequest, `"name"`, 0},
		{"status closed", path, `{"version":1,"status":"closed"}`, fiscalDuties, nil, http.StatusBadRequest, `"status"`, 0},
		{"no duty", path, `{"version":1}`, "org.read", nil, http.StatusForbidden, "AUTH-0003", 0},
		{"scoped caller", path, `{"version":1}`, fiscalDuties, scoped, http.StatusForbidden, "AUTH-0003", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := seededCalendars()
			r, _ := newRouter(f)
			rec := do(r, http.MethodPatch, tc.path, tc.body, tc.duties, true, tc.headers...)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) || f.updateCalls != tc.wantCalls {
				t.Fatalf("got %d %s with %d store calls; want %d containing %s with %d", rec.Code, rec.Body.String(), f.updateCalls, tc.wantStatus, tc.wantBody, tc.wantCalls)
			}
		})
	}
}

func TestFiscalCalendarUpdateCountsOnlyVersionBumps(t *testing.T) {
	r, a := newRouter(seededCalendars())
	path := fiscalPath + "/" + calendarID
	do(r, http.MethodPatch, path, `{"version":1}`, fiscalDuties, true)
	do(r, http.MethodPatch, path, `{"version":1,"name":"Renamed"}`, fiscalDuties, true)
	do(r, http.MethodPatch, path, `{"version":1,"name":"Again"}`, fiscalDuties, true)
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "org_fiscal_calendars_updated_total 1\n") {
		t.Fatalf("metrics: %s", metrics.Body.String())
	}
}
