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
		wantBody   []string
	}{
		{"defaults", createCalendar, fiscalDuties, nil, true, http.StatusCreated, []string{`"code":"FY","name":"Retail","pattern":"4-4-5","startMonth":1,"weekStart":"monday","openingClosing":true,"specialPeriods":0`}},
		{"explicit values", `{"code":"FY","name":"n","pattern":"custom","startMonth":2,"weekStart":"sunday","openingClosing":false,"specialPeriods":4}`, fiscalDuties, nil, true, http.StatusCreated, []string{`"startMonth":2,"weekStart":"sunday","openingClosing":false,"specialPeriods":4`}},
		{"null optional fields take defaults", `{"code":"FY","name":"n","pattern":"calendar","startMonth":null,"weekStart":null}`, fiscalDuties, nil, true, http.StatusCreated, []string{`"startMonth":1,"weekStart":"monday"`}},
		{"every field invalid", `{"code":"bad code","name":" ","pattern":"weekly","startMonth":13,"weekStart":"funday","specialPeriods":5}`, fiscalDuties, nil, true, http.StatusBadRequest,
			[]string{`"code"`, `"name"`, `"pattern"`, `"startMonth"`, `"weekStart"`, `"specialPeriods"`}},
		{"start month zero", `{"code":"FY","name":"n","pattern":"calendar","startMonth":0}`, fiscalDuties, nil, true, http.StatusBadRequest, []string{`"startMonth"`}},
		{"negative special periods", `{"code":"FY","name":"n","pattern":"calendar","specialPeriods":-1}`, fiscalDuties, nil, true, http.StatusBadRequest, []string{`"specialPeriods"`}},
		{"upper-case weekday", `{"code":"FY","name":"n","pattern":"calendar","weekStart":"Monday"}`, fiscalDuties, nil, true, http.StatusBadRequest, []string{`"weekStart"`}},
		{"empty weekday", `{"code":"FY","name":"n","pattern":"calendar","weekStart":""}`, fiscalDuties, nil, true, http.StatusBadRequest, []string{`"weekStart"`}},
		{"missing pattern", `{"code":"FY","name":"n"}`, fiscalDuties, nil, true, http.StatusBadRequest, []string{`"pattern"`}},
		{"string start month", `{"code":"FY","name":"n","pattern":"calendar","startMonth":"2"}`, fiscalDuties, nil, true, http.StatusBadRequest, []string{"COM-0001"}},
		{"fractional start month", `{"code":"FY","name":"n","pattern":"calendar","startMonth":2.5}`, fiscalDuties, nil, true, http.StatusBadRequest, []string{"COM-0001"}},
		{"string opening closing", `{"code":"FY","name":"n","pattern":"calendar","openingClosing":"true"}`, fiscalDuties, nil, true, http.StatusBadRequest, []string{"COM-0001"}},
		{"unknown field", `{"code":"FY","name":"n","pattern":"calendar","x":1}`, fiscalDuties, nil, true, http.StatusBadRequest, []string{"COM-0001"}},
		{"no tenant", createCalendar, "", nil, false, http.StatusUnauthorized, []string{"AUTH-0001"}},
		{"read duty only", createCalendar, "org.read", nil, true, http.StatusForbidden, []string{"AUTH-0003"}},
		{"legal-entity scoped caller", createCalendar, fiscalDuties, []string{"X-Passage-Legal-Entity", testkit.TenantID(2)}, true, http.StatusForbidden, []string{"AUTH-0003"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			r, _ := newRouter(f)
			rec := do(r, http.MethodPost, fiscalPath, tc.body, tc.duties, tc.withTenant, tc.headers...)
			if rec.Code != tc.wantStatus {
				t.Fatalf("got %d %s; want %d", rec.Code, rec.Body.String(), tc.wantStatus)
			}
			for _, want := range tc.wantBody {
				if !strings.Contains(rec.Body.String(), want) {
					t.Fatalf("body %s lacks %s", rec.Body.String(), want)
				}
			}
			if tc.wantStatus != http.StatusCreated && len(f.calendars) != 0 {
				t.Fatalf("store was called for a rejected request")
			}
		})
	}
}

func TestFiscalCalendarCreateStoreErrors(t *testing.T) {
	duplicate := httpx.NewProblem(http.StatusConflict, store.CodeFiscalCalendarCode, "exists")
	r, _ := newRouter(&fakeStore{err: duplicate})
	if rec := do(r, http.MethodPost, fiscalPath, createCalendar, fiscalDuties, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ORG-CAL-CODE-DUPLICATE") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	r, _ = newRouter(&fakeStore{err: errors.New("pg secret")})
	if rec := do(r, http.MethodPost, fiscalPath, createCalendar, fiscalDuties, true); rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestFiscalCalendarGet(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		duties     string
		wantStatus int
		wantBody   string
	}{
		{"known id", fiscalPath + "/" + calendarID, fiscalDuties, http.StatusOK, `"code":"FY"`},
		{"unknown id", fiscalPath + "/" + otherCalendar, fiscalDuties, http.StatusNotFound, "ORG-CAL-NOT-FOUND"},
		{"non-UUID id", fiscalPath + "/nope", fiscalDuties, http.StatusNotFound, "ORG-CAL-NOT-FOUND"},
		{"no duty", fiscalPath + "/" + calendarID, "", http.StatusForbidden, "AUTH-0003"},
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
		{"first call generates", seededCalendars(), generate, `{"fiscalYear":2028}`, fiscalDuties, nil, http.StatusCreated, `"generated":true`},
		{"unknown calendar", seededCalendars(), fiscalPath + "/" + otherCalendar + ":generate", `{"fiscalYear":2028}`, fiscalDuties, nil, http.StatusNotFound, "ORG-CAL-NOT-FOUND"},
		{"non-UUID id", seededCalendars(), fiscalPath + "/nope:generate", `{"fiscalYear":2028}`, fiscalDuties, nil, http.StatusNotFound, "ORG-CAL-NOT-FOUND"},
		{"missing year", seededCalendars(), generate, `{}`, fiscalDuties, nil, http.StatusBadRequest, `"fiscalYear"`},
		{"string year", seededCalendars(), generate, `{"fiscalYear":"2028"}`, fiscalDuties, nil, http.StatusBadRequest, "COM-0001"},
		{"fractional year", seededCalendars(), generate, `{"fiscalYear":2028.5}`, fiscalDuties, nil, http.StatusBadRequest, "COM-0001"},
		{"year below range", seededCalendars(), generate, `{"fiscalYear":1899}`, fiscalDuties, nil, http.StatusBadRequest, `"fiscalYear"`},
		{"string force", seededCalendars(), generate, `{"fiscalYear":2028,"force":"yes"}`, fiscalDuties, nil, http.StatusBadRequest, "COM-0001"},
		{"periods not an array", seededCalendars(), generate, `{"fiscalYear":2028,"periods":"x"}`, fiscalDuties, nil, http.StatusBadRequest, "COM-0001"},
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

func TestFiscalYearGenerateReplayAndRouting(t *testing.T) {
	f := seededCalendars()
	r, a := newRouter(f)
	generate := fiscalPath + "/" + calendarID + ":generate"
	first := do(r, http.MethodPost, generate, `{"fiscalYear":2028}`, fiscalDuties, true)
	replay := do(r, http.MethodPost, generate, `{"fiscalYear":2028}`, fiscalDuties, true)
	if first.Code != http.StatusCreated || replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), `"generated":false`) {
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
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) || f.generateCalls != tc.wantCalls {
				t.Fatalf("got %d %s with %d store calls; want %d containing %s with %d", rec.Code, rec.Body.String(), f.generateCalls, tc.wantStatus, tc.wantBody, tc.wantCalls)
			}
			if strings.Contains(rec.Body.String(), "ledger down") {
				t.Fatalf("checker error leaked: %s", rec.Body.String())
			}
		})
	}
}

func TestFiscalPeriodsRead(t *testing.T) {
	periods := fiscalPath + "/" + calendarID + "/periods"
	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{"all years", periods, http.StatusOK, `"items":[]`},
		{"one year", periods + "?fiscalYear=2026", http.StatusOK, `"items":[]`},
		{"empty year means all", periods + "?fiscalYear=", http.StatusOK, `"items":[]`},
		{"year below range", periods + "?fiscalYear=1899", http.StatusBadRequest, `"fiscalYear"`},
		{"year above range", periods + "?fiscalYear=3000", http.StatusBadRequest, `"fiscalYear"`},
		{"year not numeric", periods + "?fiscalYear=abc", http.StatusBadRequest, `"fiscalYear"`},
		{"unknown calendar", fiscalPath + "/" + otherCalendar + "/periods", http.StatusNotFound, "ORG-CAL-NOT-FOUND"},
		{"non-UUID calendar", fiscalPath + "/nope/periods", http.StatusNotFound, "ORG-CAL-NOT-FOUND"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(seededCalendars())
			rec := do(r, http.MethodGet, tc.path, "", fiscalDuties, true)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestFiscalMetricsExposeBothCounters(t *testing.T) {
	_, a := newRouter(&fakeStore{})
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"org_fiscal_calendars_created_total", "org_fiscal_years_generated_total"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("metrics lack %s", want)
		}
	}
}
