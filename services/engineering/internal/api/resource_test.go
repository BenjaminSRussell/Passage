package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/api"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
	"github.com/BenjaminSRussell/passage/services/engineering/migrations"
)

const (
	calendarsPath    = "/v1/engineering/calendars"
	groupsPath       = "/v1/engineering/resource-groups"
	capabilitiesPath = "/v1/engineering/capabilities"
	resourcesPath    = "/v1/engineering/resources"
	weekdayShift     = `[{"start":"08:00","end":"16:00"}]`
	readOnly         = "engineering.read"
	writeOnly        = "engineering.resource.write"
	unknownID        = "00000000-0000-0000-0000-0000000000ff"
	idemHeader       = "Idempotency-Key"
)

var validCalendar = fmt.Sprintf(`{"code":"STD","name":"Standard","template":[%[1]s,%[1]s,%[1]s,%[1]s,%[1]s,[],[]],"closedDates":["2026-12-25"]}`, weekdayShift)

type resourceState struct {
	calendars    map[string]store.Calendar
	groups       map[string]domain.ResourceGroup
	capabilities map[string]domain.Capability
	resources    map[string]domain.Resource
	codes        map[string]bool
	keys         map[string]string
	hashes       map[string]string
	days         map[string]map[time.Time]domain.CalendarDay
}

func (f *fakeStore) state() *resourceState {
	if f.res == nil {
		f.res = &resourceState{
			calendars: map[string]store.Calendar{}, groups: map[string]domain.ResourceGroup{}, capabilities: map[string]domain.Capability{},
			resources: map[string]domain.Resource{}, codes: map[string]bool{}, keys: map[string]string{}, hashes: map[string]string{}, days: map[string]map[time.Time]domain.CalendarDay{},
		}
	}
	return f.res
}

func (s *resourceState) replayed(idem store.Idem) (string, bool, error) {
	id, found := s.keys[idem.Key]
	if idem.Key == "" || !found {
		return "", false, nil
	}
	if s.hashes[idem.Key] != idem.Hash {
		return "", false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reused")
	}
	return id, true, nil
}

func (s *resourceState) claim(kind, code string, idem store.Idem, id string) error {
	if s.codes[kind+code] {
		return httpx.NewProblem(http.StatusConflict, store.CodeDuplicate, "duplicate code")
	}
	s.codes[kind+code] = true
	if idem.Key != "" {
		s.keys[idem.Key], s.hashes[idem.Key] = id, idem.Hash
	}
	return nil
}

func (s *resourceState) requireCalendar(id string) error {
	if _, ok := s.calendars[id]; id != "" && !ok {
		return httpx.NewProblem(http.StatusNotFound, store.CodeCalNotFound, "calendar not found")
	}
	return nil
}

func (f *fakeStore) CreateCalendar(_ context.Context, c store.Calendar, idem store.Idem) (store.Calendar, []string, error) {
	s := f.state()
	if f.err != nil {
		return store.Calendar{}, nil, f.err
	}
	id, found, err := s.replayed(idem)
	if found {
		c = s.calendars[id]
	}
	warnings, validation := domain.ValidateCalendar(c.WorkCalendar)
	switch {
	case err != nil:
		return store.Calendar{}, nil, err
	case validation != nil:
		return store.Calendar{}, nil, validation
	case found:
		return c, warnings, nil
	}
	c.Version = 1
	if err := s.claim("calendar", c.Code, idem, c.ID); err != nil {
		return store.Calendar{}, nil, err
	}
	s.calendars[c.ID] = c
	return c, warnings, nil
}

func (f *fakeStore) GenerateCalendar(_ context.Context, id string, from, to time.Time) (store.CalendarGeneration, error) {
	s := f.state()
	if f.err != nil {
		return store.CalendarGeneration{}, f.err
	}
	cal, ok := s.calendars[id]
	if !ok {
		return store.CalendarGeneration{}, s.requireCalendar(unknownID)
	}
	days, err := domain.Generate(cal.WorkCalendar, from, to)
	if err != nil {
		return store.CalendarGeneration{}, err
	}
	if s.days[id] == nil {
		s.days[id] = map[time.Time]domain.CalendarDay{}
	}
	var out store.CalendarGeneration
	for _, d := range days {
		s.days[id][d.Date] = d
		out.TotalHours = out.TotalHours.Add(d.TotalH)
		if d.Open {
			out.OpenDays++
		}
	}
	return out, nil
}

func (f *fakeStore) ListCalendarDays(_ context.Context, id string, from, to time.Time) ([]domain.CalendarDay, error) {
	s := f.state()
	if f.err != nil {
		return nil, f.err
	}
	if _, ok := s.calendars[id]; !ok {
		return nil, s.requireCalendar(unknownID)
	}
	if _, err := domain.Generate(domain.WorkCalendar{}, from, to); err != nil {
		return nil, err
	}
	var days []domain.CalendarDay
	for date, d := range s.days[id] {
		if !date.Before(from) && !date.After(to) {
			days = append(days, d)
		}
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Date.Before(days[j].Date) })
	return days, nil
}

func (f *fakeStore) CreateResourceGroup(_ context.Context, g domain.ResourceGroup, idem store.Idem) (domain.ResourceGroup, error) {
	s := f.state()
	if f.err != nil {
		return domain.ResourceGroup{}, f.err
	}
	if id, found, err := s.replayed(idem); found || err != nil {
		return s.groups[id], err
	}
	if err := s.requireCalendar(g.CalendarID); err != nil {
		return domain.ResourceGroup{}, err
	}
	if err := s.claim("group", g.Code, idem, g.ID); err != nil {
		return domain.ResourceGroup{}, err
	}
	s.groups[g.ID] = g
	return g, nil
}

func (f *fakeStore) CreateCapability(_ context.Context, c domain.Capability, idem store.Idem) (domain.Capability, error) {
	s := f.state()
	if f.err != nil {
		return domain.Capability{}, f.err
	}
	if id, found, err := s.replayed(idem); found || err != nil {
		return s.capabilities[id], err
	}
	if err := s.claim("capability", c.Code, idem, c.ID); err != nil {
		return domain.Capability{}, err
	}
	s.capabilities[c.ID] = c
	return c, nil
}

func (f *fakeStore) CreateResource(_ context.Context, r domain.Resource, idem store.Idem) (store.ResourceRecord, error) {
	s := f.state()
	if f.err != nil {
		return store.ResourceRecord{}, f.err
	}
	if id, found, err := s.replayed(idem); found || err != nil {
		return s.record(s.resources[id]), err
	}
	if err := domain.ValidateResource(r); err != nil {
		return store.ResourceRecord{}, err
	}
	if g, ok := s.groups[r.GroupID]; r.GroupID != "" && !ok {
		return store.ResourceRecord{}, httpx.NewProblem(http.StatusNotFound, store.CodeResNotFound, "resource group not found")
	} else if r.GroupID != "" && g.SiteID != r.SiteID {
		return store.ResourceRecord{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeResSite, "resource site must equal the site of its resource group")
	}
	if err := s.requireCalendar(r.CalendarID); err != nil {
		return store.ResourceRecord{}, err
	}
	for _, c := range r.Capabilities {
		if _, ok := s.capabilities[c.CapabilityID]; !ok {
			return store.ResourceRecord{}, httpx.NewProblem(http.StatusNotFound, store.CodeResNotFound, "a referenced capability does not exist")
		}
	}
	if err := s.claim("resource", r.Code, idem, r.ID); err != nil {
		return store.ResourceRecord{}, err
	}
	s.resources[r.ID] = r
	return s.record(r), nil
}

func (s *resourceState) record(r domain.Resource) store.ResourceRecord {
	return store.ResourceRecord{Resource: r, EffectiveCalendarID: domain.EffectiveCalendar(r, s.groups[r.GroupID])}
}

func (f *fakeStore) GetResource(_ context.Context, id string) (store.ResourceRecord, error) {
	s := f.state()
	if f.err != nil {
		return store.ResourceRecord{}, f.err
	}
	r, ok := s.resources[id]
	if !ok {
		return store.ResourceRecord{}, httpx.NewProblem(http.StatusNotFound, store.CodeResNotFound, "resource not found")
	}
	return s.record(r), nil
}

func created(t *testing.T, r http.Handler, path, body string) map[string]any {
	t.Helper()
	rec := do(r, http.MethodPost, path, body, allDuties, true)
	var out map[string]any
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("POST %s = %d %s; want 201", path, rec.Code, rec.Body)
	}
	return out
}

func seedResource(t *testing.T, r http.Handler) (calendarID, resourceID string) {
	t.Helper()
	calendarID = created(t, r, calendarsPath, validCalendar)["id"].(string)
	resourceID = created(t, r, resourcesPath, `{"code":"M1","name":"Mill","type":"machine","siteId":"S1","calendarId":"`+calendarID+`"}`)["id"].(string)
	return calendarID, resourceID
}

func TestResourceDuties(t *testing.T) {
	r, _ := newRouter(&fakeStore{}, approves)
	calendarID, resourceID := seedResource(t, r)
	routes := []struct {
		method, path, body string
		read               bool
	}{
		{http.MethodPost, calendarsPath, strings.Replace(validCalendar, "STD", "STD2", 1), false},
		{http.MethodPost, calendarsPath + "/" + calendarID + ":generate", `{"from":"2026-01-01","to":"2026-01-31"}`, false},
		{http.MethodGet, calendarsPath + "/" + calendarID + "/days?from=2026-01-01&to=2026-01-02", "", true},
		{http.MethodPost, groupsPath, `{"code":"G9","name":"Group","siteId":"S1"}`, false},
		{http.MethodPost, capabilitiesPath, `{"code":"C9","name":"Weld"}`, false},
		{http.MethodPost, resourcesPath, `{"code":"M9","name":"Mill","type":"machine","siteId":"S1"}`, false},
		{http.MethodGet, resourcesPath + "/" + resourceID, "", true},
	}
	for _, tc := range routes {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			if rec := do(r, tc.method, tc.path, tc.body, "", false); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "AUTH-0001") {
				t.Fatalf("no tenant = %d %s", rec.Code, rec.Body)
			}
			readerWants, writerWants := http.StatusForbidden, http.StatusOK
			if tc.read {
				readerWants, writerWants = http.StatusOK, http.StatusForbidden
			}
			if tc.method == http.MethodPost {
				writerWants = http.StatusCreated
				if strings.Contains(tc.path, ":generate") {
					writerWants = http.StatusOK
				}
			}
			if rec := do(r, tc.method, tc.path, tc.body, readOnly, true); rec.Code != readerWants {
				t.Fatalf("engineering.read only = %d %s; want %d", rec.Code, rec.Body, readerWants)
			}
			if rec := do(r, tc.method, tc.path, tc.body, writeOnly, true); rec.Code != writerWants {
				t.Fatalf("engineering.resource.write only = %d %s; want %d", rec.Code, rec.Body, writerWants)
			}
		})
	}
}

func TestCalendarRoutes(t *testing.T) {
	heavy := strings.ReplaceAll(`{"code":"HEAVY","name":"Heavy","template":[W,W,W,W,W,W,W]}`, "W", `[{"start":"00:00","end":"08:00"},{"start":"08:00","end":"16:00"}]`)
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{"created with an empty warnings array", validCalendar, http.StatusCreated, `"warnings":[]`},
		{"weekly hours over the limit warn", heavy, http.StatusCreated, "exceed"},
		{"six day patterns", strings.Replace(validCalendar, ",[],[]]", ",[]]", 1), http.StatusBadRequest, `"template"`},
		{"bad closed date", strings.Replace(validCalendar, "2026-12-25", "2026-13-40", 1), http.StatusBadRequest, `"closedDates"`},
		{"efficiency as object", strings.Replace(validCalendar, `"end":"16:00"`, `"end":"16:00","efficiencyPct":{}`, 1), http.StatusBadRequest, "COM-0001"},
		{"null body", `null`, http.StatusBadRequest, `"code"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&fakeStore{}, approves)
			if rec := do(r, http.MethodPost, calendarsPath, tc.body, allDuties, true); rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body, tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestCalendarRoutes_IdempotencyKey(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f, approves)
	post := func(body string) *httptest.ResponseRecorder {
		return do(r, http.MethodPost, calendarsPath, body, allDuties, true, idemHeader, "cal-key")
	}
	first, replay := post(validCalendar), post(validCalendar)
	if first.Code != http.StatusCreated || replay.Code != http.StatusCreated || first.Body.String() != replay.Body.String() || len(f.res.calendars) != 1 {
		t.Fatalf("replay must return the original calendar: %d %s / %d %s, rows %d", first.Code, first.Body, replay.Code, replay.Body, len(f.res.calendars))
	}
	if reuse := post(strings.Replace(validCalendar, "STD", "OTHER", 1)); reuse.Code != http.StatusUnprocessableEntity || !strings.Contains(reuse.Body.String(), store.CodeIdempotencyReuse) {
		t.Fatalf("different body under the same key = %d %s; want 422 %s", reuse.Code, reuse.Body, store.CodeIdempotencyReuse)
	}
}

func TestGenerateCalendar_Fixture(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/calendars.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		From, To           string
		Template           map[string][]domain.Shift
		ClosedDates        []string
		ExpectedOpenDays   int
		ExpectedTotalHours string
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	week := make([][]domain.Shift, 7)
	for i := range week {
		week[i] = []domain.Shift{}
		if i < 5 {
			week[i] = fixture.Template["mon-fri"]
		}
	}
	calendar, err := json.Marshal(map[string]any{"code": "FIX", "name": "Fixture", "template": week, "closedDates": fixture.ClosedDates})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := newRouter(&fakeStore{}, approves)
	calendarID := created(t, r, calendarsPath, string(calendar))["id"].(string)
	rec := do(r, http.MethodPost, calendarsPath+"/"+calendarID+":generate", fmt.Sprintf(`{"from":%q,"to":%q}`, fixture.From, fixture.To), allDuties, true)
	want := fmt.Sprintf(`{"openDays":%d,"totalHours":%q}`, fixture.ExpectedOpenDays, fixture.ExpectedTotalHours)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("generate = %d %s; want 200 %s", rec.Code, rec.Body, want)
	}
	for _, tc := range []struct{ name, path, body string }{
		{"bad id", calendarsPath + "/not-a-uuid:generate", `{"from":"2026-01-01","to":"2026-01-31"}`},
		{"missing to", calendarsPath + "/" + calendarID + ":generate", `{"from":"2026-01-01"}`},
		{"number for from", calendarsPath + "/" + calendarID + ":generate", `{"from":20260101,"to":"2026-01-31"}`},
		{"free text date", calendarsPath + "/" + calendarID + ":generate", `{"from":"tomorrow","to":"2026-01-31"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := do(r, http.MethodPost, tc.path, tc.body, allDuties, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") {
				t.Fatalf("got %d %s; want 400 COM-0001", rec.Code, rec.Body)
			}
		})
	}
}

func TestCalendarDays_RangeOnly(t *testing.T) {
	r, _ := newRouter(&fakeStore{}, approves)
	calendarID := created(t, r, calendarsPath, validCalendar)["id"].(string)
	generate := do(r, http.MethodPost, calendarsPath+"/"+calendarID+":generate", `{"from":"2026-01-01","to":"2026-01-04"}`, allDuties, true)
	if generate.Code != http.StatusOK {
		t.Fatalf("generate = %d %s", generate.Code, generate.Body)
	}
	path := calendarsPath + "/" + calendarID + "/days"
	rec := do(r, http.MethodGet, path+"?from=2026-01-02&to=2026-01-03", "", readOnly, true)
	var body struct {
		Items []struct {
			Date   string
			Open   bool
			Shifts []any
			TotalH string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || len(body.Items) != 2 {
		t.Fatalf("days = %d %s; want 2 items", rec.Code, rec.Body)
	}
	if first, second := body.Items[0], body.Items[1]; first.Date != "2026-01-02" || !first.Open || first.TotalH != "8" || len(first.Shifts) != 1 || second.Date != "2026-01-03" || second.Shifts == nil {
		t.Fatalf("unexpected days %+v", body.Items)
	}
	for _, tc := range []struct {
		name, query string
		status      int
		code        string
	}{
		{"missing to", "?from=2026-01-02", http.StatusBadRequest, "COM-0001"},
		{"bad from", "?from=x&to=2026-01-03", http.StatusBadRequest, "COM-0001"},
		{"inverted range", "?from=2026-01-03&to=2026-01-02", http.StatusUnprocessableEntity, domain.CodeCalRange},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := do(r, http.MethodGet, path+tc.query, "", readOnly, true); rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.code) {
				t.Fatalf("got %d %s; want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
		})
	}
	if rec := do(r, http.MethodGet, calendarsPath+"/not-a-uuid/days?from=2026-01-02&to=2026-01-03", "", readOnly, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed id = %d %s", rec.Code, rec.Body)
	}
}

func TestGroupAndCapabilityRoutes(t *testing.T) {
	for _, tc := range []struct{ name, path, body, other string }{
		{"group", groupsPath, `{"code":"G1","name":"Group","siteId":"S1"}`, `{"code":"G1","name":"Renamed","siteId":"S1"}`},
		{"capability", capabilitiesPath, `{"code":"C1","name":"Weld"}`, `{"code":"C1","name":"Renamed"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&fakeStore{}, approves)
			post := func(body string, headers ...string) *httptest.ResponseRecorder {
				return do(r, http.MethodPost, tc.path, body, allDuties, true, headers...)
			}
			first, replay := post(tc.body, idemHeader, "k1"), post(tc.body, idemHeader, "k1")
			if first.Code != http.StatusCreated || replay.Code != http.StatusCreated || first.Body.String() != replay.Body.String() {
				t.Fatalf("replay = %d %s / %d %s", first.Code, first.Body, replay.Code, replay.Body)
			}
			if rec := post(tc.other, idemHeader, "k1"); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodeIdempotencyReuse) {
				t.Fatalf("reused key = %d %s", rec.Code, rec.Body)
			}
			if rec := post(tc.body, idemHeader, "k2"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodeDuplicate) {
				t.Fatalf("duplicate code = %d %s; want 409 %s", rec.Code, rec.Body, store.CodeDuplicate)
			}
			if rec := post(`{"code":"","name":""}`); rec.Code != http.StatusBadRequest {
				t.Fatalf("empty code = %d %s", rec.Code, rec.Body)
			}
		})
	}
	r, _ := newRouter(&fakeStore{}, approves)
	for _, tc := range []struct {
		name, body string
		status     int
		want       string
	}{
		{"unknown calendar", `{"code":"G2","name":"Group","siteId":"S1","calendarId":"` + unknownID + `"}`, http.StatusNotFound, store.CodeCalNotFound},
		{"non-uuid calendar", `{"code":"G3","name":"Group","siteId":"S1","calendarId":"nope"}`, http.StatusBadRequest, `"calendarId"`},
		{"missing site", `{"code":"G4","name":"Group"}`, http.StatusBadRequest, `"siteId"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := do(r, http.MethodPost, groupsPath, tc.body, allDuties, true); rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("got %d %s; want %d %s", rec.Code, rec.Body, tc.status, tc.want)
			}
		})
	}
}

func TestResourceRoutes(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f, approves)
	calendarID := created(t, r, calendarsPath, validCalendar)["id"].(string)
	group := created(t, r, groupsPath, `{"code":"G1","name":"Group","siteId":"S1","calendarId":"`+calendarID+`"}`)["id"].(string)
	capability := created(t, r, capabilitiesPath, `{"code":"WELD","name":"Welding"}`)["id"].(string)
	withGroup := `{"code":"M1","name":"Mill","type":"machine","siteId":"S1","groupId":"` + group + `","capabilities":[{"capabilityId":"` + capability + `","level":2,"effectiveFrom":"2026-01-01","effectiveTo":"2026-12-31"}]}`

	resource := created(t, r, resourcesPath, withGroup)
	if resource["efficiencyPct"] != "100" || resource["capacityPerDayH"] != "0" || resource["effectiveCalendarId"] != calendarID {
		t.Fatalf("defaults or effective calendar wrong: %v", resource)
	}
	rec := do(r, http.MethodGet, resourcesPath+"/"+resource["id"].(string), "", readOnly, true)
	for _, want := range []string{`"effectiveCalendarId":"` + calendarID + `"`, `"capabilityId":"` + capability + `"`, `"level":2`, `"effectiveTo":"2026-12-31"`} {
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("GET = %d %s; want %s", rec.Code, rec.Body, want)
		}
	}
	own := created(t, r, resourcesPath, `{"code":"M2","name":"Lathe","type":"machine","siteId":"S1","groupId":"`+group+`","calendarId":"`+calendarID+`","efficiencyPct":85.5,"capacityPerDayH":"7.25","exclusive":true}`)
	if own["efficiencyPct"] != "85.5" || own["capacityPerDayH"] != "7.25" || own["exclusive"] != true {
		t.Fatalf("explicit values lost: %v", own)
	}
	vendor := created(t, r, resourcesPath, `{"code":"V1","name":"Vendor","type":"vendor","siteId":"S1","vendorPartyId":"P-1"}`)
	if vendor["vendorPartyId"] != "P-1" {
		t.Fatalf("vendor party lost: %v", vendor)
	}

	base := func(extra string) string {
		return `{"code":"X` + fmt.Sprint(len(extra)) + `","name":"Thing","type":"machine","siteId":"S1"` + extra + `}`
	}
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{"vendor without party", `{"code":"V2","name":"Vendor","type":"vendor","siteId":"S1"}`, http.StatusUnprocessableEntity, domain.CodeResVendor},
		{"machine with party", base(`,"vendorPartyId":"P-1"`), http.StatusUnprocessableEntity, domain.CodeResVendor},
		{"group on another site", `{"code":"M3","name":"Mill","type":"machine","siteId":"S2","groupId":"` + group + `"}`, http.StatusUnprocessableEntity, domain.CodeResSite},
		{"unknown capability", base(`,"capabilities":[{"capabilityId":"` + unknownID + `","level":1,"effectiveFrom":"2026-01-01"}]`), http.StatusNotFound, store.CodeResNotFound},
		{"unknown calendar", base(`,"calendarId":"` + unknownID + `"`), http.StatusNotFound, store.CodeCalNotFound},
		{"duplicate code", withGroup, http.StatusConflict, store.CodeDuplicate},
		{"unknown type", `{"code":"R","name":"Robot","type":"robot","siteId":"S1"}`, http.StatusBadRequest, `"type"`},
		{"efficiency zero", base(`,"efficiencyPct":0`), http.StatusBadRequest, `"efficiencyPct"`},
		{"efficiency above 100", base(`,"efficiencyPct":101`), http.StatusBadRequest, `"efficiencyPct"`},
		{"efficiency text", base(`,"efficiencyPct":"abc"`), http.StatusBadRequest, "COM-0001"},
		{"capacity negative", base(`,"capacityPerDayH":-1`), http.StatusBadRequest, `"capacityPerDayH"`},
		{"non-uuid group", base(`,"groupId":"nope"`), http.StatusBadRequest, `"groupId"`},
		{"capability level zero", base(`,"capabilities":[{"capabilityId":"` + capability + `","level":0,"effectiveFrom":"2026-01-01"}]`), http.StatusBadRequest, `"capabilities[0]"`},
		{"capability dates inverted", base(`,"capabilities":[{"capabilityId":"` + capability + `","level":1,"effectiveFrom":"2026-02-01","effectiveTo":"2026-01-01"}]`), http.StatusBadRequest, `"capabilities[0]"`},
		{"too many capabilities", base(`,"capabilities":[` + strings.TrimSuffix(strings.Repeat(`{},`, 101), ",") + `]`), http.StatusBadRequest, `"capabilities"`},
		{"vendor party too long", base(`,"vendorPartyId":"` + strings.Repeat("p", 65) + `"`), http.StatusBadRequest, `"vendorPartyId"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if rec := do(r, http.MethodPost, resourcesPath, tc.body, allDuties, true); rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body, tc.wantStatus, tc.wantBody)
			}
		})
	}

	if rec := do(r, http.MethodGet, resourcesPath+"/"+unknownID, "", readOnly, true); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeResNotFound) {
		t.Fatalf("unknown resource = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodGet, resourcesPath+"/not-a-uuid", "", readOnly, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed resource id = %d %s", rec.Code, rec.Body)
	}
	replayKey := []string{idemHeader, "res-key"}
	first := do(r, http.MethodPost, resourcesPath, `{"code":"R1","name":"Robot arm","type":"tool","siteId":"S1"}`, allDuties, true, replayKey...)
	again := do(r, http.MethodPost, resourcesPath, `{"code":"R1","name":"Robot arm","type":"tool","siteId":"S1"}`, allDuties, true, replayKey...)
	if first.Code != http.StatusCreated || again.Code != http.StatusCreated || first.Body.String() != again.Body.String() {
		t.Fatalf("resource replay = %d %s / %d %s", first.Code, first.Body, again.Code, again.Body)
	}
	if reuse := do(r, http.MethodPost, resourcesPath, `{"code":"R1","name":"Other","type":"tool","siteId":"S1"}`, allDuties, true, replayKey...); reuse.Code != http.StatusUnprocessableEntity {
		t.Fatalf("resource key reuse = %d %s", reuse.Code, reuse.Body)
	}
}

func TestResourceErrorMapping(t *testing.T) {
	r0, _ := newRouter(&fakeStore{}, approves)
	calendarID, resourceID := seedResource(t, r0)
	calendarPath := calendarsPath + "/" + calendarID
	problem := func(status int, code string) error { return httpx.NewProblem(status, code, "from store") }
	tests := []struct {
		name, method, path, body string
		err                      error
		wantStatus               int
		wantCode                 string
	}{
		{"calendar shift", http.MethodPost, calendarsPath, validCalendar, &domain.Error{Code: domain.CodeCalShift, Detail: "overlap"}, http.StatusUnprocessableEntity, domain.CodeCalShift},
		{"calendar range on generate", http.MethodPost, calendarPath + ":generate", `{"from":"2026-01-01","to":"2026-01-31"}`, &domain.Error{Code: domain.CodeCalRange, Detail: "range"}, http.StatusUnprocessableEntity, domain.CodeCalRange},
		{"resource vendor", http.MethodPost, resourcesPath, `{"code":"V","name":"V","type":"vendor","siteId":"S1","vendorPartyId":"P"}`, &domain.Error{Code: domain.CodeResVendor, Detail: "vendor"}, http.StatusUnprocessableEntity, domain.CodeResVendor},
		{"resource site", http.MethodPost, resourcesPath, `{"code":"V","name":"V","type":"machine","siteId":"S1"}`, &domain.Error{Code: domain.CodeResSite, Detail: "site"}, http.StatusUnprocessableEntity, domain.CodeResSite},
		{"calendar not found on days", http.MethodGet, calendarPath + "/days?from=2026-01-01&to=2026-01-02", "", problem(http.StatusNotFound, store.CodeCalNotFound), http.StatusNotFound, store.CodeCalNotFound},
		{"calendar not found on generate", http.MethodPost, calendarPath + ":generate", `{"from":"2026-01-01","to":"2026-01-31"}`, problem(http.StatusNotFound, store.CodeCalNotFound), http.StatusNotFound, store.CodeCalNotFound},
		{"duplicate group", http.MethodPost, groupsPath, `{"code":"G","name":"G","siteId":"S1"}`, problem(http.StatusConflict, store.CodeDuplicate), http.StatusConflict, store.CodeDuplicate},
		{"database down hides detail", http.MethodGet, resourcesPath + "/" + resourceID, "", errors.New("pg secret detail"), http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			r, _ := newRouter(f, approves)
			f.err = tc.err
			rec := do(r, tc.method, tc.path, tc.body, allDuties, true)
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != tc.wantStatus || body["code"] != tc.wantCode ||
				rec.Header().Get("Content-Type") != "application/problem+json" || body["traceId"] == nil || body["instance"] != tc.path[:strings.IndexAny(tc.path+"?", "?")] {
				t.Fatalf("got %d %s (%s); want %d %s", rec.Code, rec.Body, rec.Header().Get("Content-Type"), tc.wantStatus, tc.wantCode)
			}
			if strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("internal detail leaked: %s", rec.Body)
			}
		})
	}
	for _, tc := range []struct{ name, query string }{
		{"over the generate limit", `{"from":"2026-01-01","to":"2031-01-02"}`},
		{"to before from", `{"from":"2026-02-01","to":"2026-01-01"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := do(r0, http.MethodPost, calendarPath+":generate", tc.query, allDuties, true); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeCalRange) {
				t.Fatalf("got %d %s; want 422 %s", rec.Code, rec.Body, domain.CodeCalRange)
			}
		})
	}
	overlap := strings.Replace(validCalendar, `"template":[`+weekdayShift, `"template":[[{"start":"08:00","end":"12:00"},{"start":"11:00","end":"13:00"}]`, 1)
	if rec := do(r0, http.MethodPost, calendarsPath, overlap, allDuties, true); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeCalShift) {
		t.Fatalf("overlapping shifts = %d %s; want 422 %s", rec.Code, rec.Body, domain.CodeCalShift)
	}
	if rec := do(r0, http.MethodPost, calendarsPath, validCalendar, allDuties, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodeDuplicate) {
		t.Fatalf("duplicate calendar code = %d %s; want 409", rec.Code, rec.Body)
	}
}

func TestResourceFailures_LogCodesNotBodies(t *testing.T) {
	var logs strings.Builder
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	f := &fakeStore{err: errors.New("database down")}
	r, _ := newRouter(f, approves)
	do(r, http.MethodPost, resourcesPath, `{"code":"M1","name":"private-name","type":"machine","siteId":"S1"}`, allDuties, true)
	f.err = &domain.Error{Code: domain.CodeResVendor, Detail: "vendor"}
	do(r, http.MethodPost, resourcesPath, `{"code":"M1","name":"private-name","type":"machine","siteId":"S1"}`, allDuties, true)
	if !strings.Contains(logs.String(), `"create resource failed"`) || !strings.Contains(logs.String(), domain.CodeResVendor) || !strings.Contains(logs.String(), "correlationId") || strings.Contains(logs.String(), "private-name") {
		t.Fatalf("logs must name the operation, code and correlationId and never the body: %s", logs.String())
	}
}

func TestResourceMetrics(t *testing.T) {
	f := &fakeStore{}
	r, a := newRouter(f, approves)
	calendarID, resourceID := seedResource(t, r)
	f.err = errors.New("boom")
	failing := []struct{ method, path, body string }{
		{http.MethodPost, calendarsPath, validCalendar},
		{http.MethodPost, calendarsPath + "/" + calendarID + ":generate", `{"from":"2026-01-01","to":"2026-01-31"}`},
		{http.MethodGet, calendarsPath + "/" + calendarID + "/days?from=2026-01-01&to=2026-01-02", ""},
		{http.MethodPost, groupsPath, `{"code":"G","name":"G","siteId":"S1"}`},
		{http.MethodPost, capabilitiesPath, `{"code":"C","name":"C"}`},
		{http.MethodPost, resourcesPath, `{"code":"M","name":"M","type":"machine","siteId":"S1"}`},
		{http.MethodGet, resourcesPath + "/" + resourceID, ""},
	}
	for _, tc := range failing {
		if rec := do(r, tc.method, tc.path, tc.body, allDuties, true); rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s %s = %d %s", tc.method, tc.path, rec.Code, rec.Body)
		}
	}

	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{
		"engineering_calendars_created_total 1",
		"engineering_resources_created_total 1",
		"engineering_resource_requests_failed_total 7",
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("metrics missing %q in %s", want, rec.Body)
		}
	}
}

func TestPostgres_ResourceAPI_EndToEnd(t *testing.T) {
	pool := testkit.DB(t)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "alice"})
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	a := api.New(store.New(pool), approves, func() time.Time { return today })
	r := httpx.NewRouter()
	a.Register(r)

	calendar := created(t, r, calendarsPath, validCalendar)
	calendarID := calendar["id"].(string)
	group := created(t, r, groupsPath, `{"code":"G1","name":"Group","siteId":"S1","calendarId":"`+calendarID+`"}`)["id"].(string)
	capability := created(t, r, capabilitiesPath, `{"code":"WELD","name":"Welding"}`)["id"].(string)
	resource := created(t, r, resourcesPath, `{"code":"M1","name":"Mill","type":"machine","siteId":"S1","groupId":"`+group+`","capabilities":[{"capabilityId":"`+capability+`","level":2,"effectiveFrom":"2026-01-01"}]}`)

	if rec := do(r, http.MethodPost, calendarsPath+"/"+calendarID+":generate", `{"from":"2026-01-05","to":"2026-01-11"}`, allDuties, true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"openDays":5,"totalHours":"40"`) {
		t.Fatalf("generate = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodGet, calendarsPath+"/"+calendarID+"/days?from=2026-01-05&to=2026-01-06", "", readOnly, true); rec.Code != http.StatusOK || strings.Count(rec.Body.String(), `"date"`) != 2 {
		t.Fatalf("days = %d %s", rec.Code, rec.Body)
	}
	rec := do(r, http.MethodGet, resourcesPath+"/"+resource["id"].(string), "", readOnly, true)
	for _, want := range []string{`"effectiveCalendarId":"` + calendarID + `"`, `"capabilityId":"` + capability + `"`, `"level":2`, `"code":"M1"`} {
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("GET resource = %d %s; want %s", rec.Code, rec.Body, want)
		}
	}
	if rec := do(r, http.MethodPost, capabilitiesPath, `{"code":"WELD","name":"Again"}`, allDuties, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodeDuplicate) {
		t.Fatalf("duplicate capability = %d %s", rec.Code, rec.Body)
	}
}

// The resource lookup serves the lean work cell board and the EAM downtime form, so their duties read resources;
// they still cannot write one or read anything else of engineering.
func TestResourceReads_TakeTheLookupUsersDuties(t *testing.T) {
	r, _ := newRouter(&fakeStore{}, approves)
	calendarID, resourceID := seedResource(t, r)
	for _, duty := range []string{"lean.kanban.work", "eam.work-order.execute", "eam.work.plan"} {
		for _, path := range []string{resourcesPath, resourcesPath + "/" + resourceID} {
			if rec := do(r, http.MethodGet, path, "", duty, true); rec.Code != http.StatusOK {
				t.Fatalf("%s reading %s = %d %s; want 200", duty, path, rec.Code, rec.Body)
			}
		}
		if rec := do(r, http.MethodPost, resourcesPath, `{"code":"M8","name":"Mill","type":"machine","siteId":"S1"}`, duty, true); rec.Code != http.StatusForbidden {
			t.Fatalf("%s creating a resource = %d; want 403", duty, rec.Code)
		}
		if rec := do(r, http.MethodGet, calendarsPath+"/"+calendarID+"/days?from=2026-01-01&to=2026-01-02", "", duty, true); rec.Code != http.StatusForbidden {
			t.Fatalf("%s reading calendar days = %d; want 403", duty, rec.Code)
		}
	}
}
