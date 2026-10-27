package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

var (
	siteA, siteB, groupG = rid(101), rid(102), rid(201)
	weld, paint          = rid(301), rid(302)
)

func listFixtures() *fakeStore {
	held := func(id string) domain.ResourceCapability {
		return domain.ResourceCapability{CapabilityID: id, Level: 2, EffectiveFrom: day("2026-01-01")}
	}
	resource := func(n int, site, kind, group string, caps ...domain.ResourceCapability) store.ResourceRecord {
		return store.ResourceRecord{Resource: domain.Resource{ID: rid(n), Code: "R", Name: "R", Type: kind, SiteID: site, GroupID: group, EfficiencyPct: dec("100"), CapacityPerDayH: dec("8"), Capabilities: caps}, EffectiveCalendarID: rid(900)}
	}
	return &fakeStore{
		resList: []store.ResourceRecord{
			resource(3, siteB, domain.TypeMachine, groupG, held(weld), held(paint)),
			resource(1, siteA, domain.TypeMachine, groupG, held(weld)),
			resource(2, siteA, domain.TypeTool, ""),
		},
		groupList: []domain.ResourceGroup{{ID: rid(202), Code: "B", Name: "B", SiteID: siteB}, {ID: groupG, Code: "A", Name: "A", SiteID: siteA, CalendarID: rid(900)}},
		capList:   []domain.Capability{{ID: paint, Code: "PAINT", Name: "Paint"}, {ID: weld, Code: "WELD", Name: "Welding"}},
	}
}

func pageOf[T any](items []T, after string, limit int, id func(T) string) store.Page[T] {
	sorted := slices.SortedFunc(slices.Values(items), func(a, b T) int { return strings.Compare(id(a), id(b)) })
	var rest []T
	for _, item := range sorted {
		if id(item) > after {
			rest = append(rest, item)
		}
	}
	if len(rest) > limit {
		return store.Page[T]{Items: rest[:limit], NextID: id(rest[limit-1])}
	}
	return store.Page[T]{Items: rest}
}

func (f *fakeStore) ListResources(_ context.Context, flt store.ResourceFilter, after string, limit int) (store.Page[store.ResourceRecord], error) {
	if f.err != nil {
		return store.Page[store.ResourceRecord]{}, f.err
	}
	var matched []store.ResourceRecord
	for _, rec := range f.resList {
		r := rec.Resource
		hasCapability := flt.CapabilityID == "" || slices.ContainsFunc(r.Capabilities, func(c domain.ResourceCapability) bool { return c.CapabilityID == flt.CapabilityID })
		if (flt.SiteID == "" || r.SiteID == flt.SiteID) && (flt.Type == "" || r.Type == flt.Type) && (flt.GroupID == "" || r.GroupID == flt.GroupID) && hasCapability {
			matched = append(matched, rec)
		}
	}
	return pageOf(matched, after, limit, func(r store.ResourceRecord) string { return r.Resource.ID }), nil
}

func (f *fakeStore) ListResourceGroups(_ context.Context, site, after string, limit int) (store.Page[domain.ResourceGroup], error) {
	if f.err != nil {
		return store.Page[domain.ResourceGroup]{}, f.err
	}
	var matched []domain.ResourceGroup
	for _, g := range f.groupList {
		if site == "" || g.SiteID == site {
			matched = append(matched, g)
		}
	}
	return pageOf(matched, after, limit, func(g domain.ResourceGroup) string { return g.ID }), nil
}

func (f *fakeStore) ListCapabilities(_ context.Context, after string, limit int) (store.Page[domain.Capability], error) {
	if f.err != nil {
		return store.Page[domain.Capability]{}, f.err
	}
	return pageOf(f.capList, after, limit, func(c domain.Capability) string { return c.ID }), nil
}

func listAll(t *testing.T, r http.Handler, path, query string) []string {
	t.Helper()
	var got []string
	cursor := ""
	for range 20 {
		rec := do(r, http.MethodGet, path+"?"+query+"&cursor="+cursor, "", readOnly, true)
		var body routeList
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			t.Fatalf("GET %s?%s = %d %s", path, query, rec.Code, rec.Body)
		}
		for _, item := range body.Items {
			got = append(got, item.ID)
		}
		if body.NextCursor == "" {
			return got
		}
		cursor = body.NextCursor
	}
	t.Fatal("paging did not terminate")
	return nil
}

func TestListResources_FiltersAndPages(t *testing.T) {
	r, _ := newRouter(listFixtures(), approves)
	tests := []struct {
		name, query string
		want        []int
	}{
		{"every row across pages", "limit=1", []int{1, 2, 3}},
		{"site", "siteId=" + siteA, []int{1, 2}},
		{"type", "type=machine&limit=1", []int{1, 3}},
		{"group", "groupId=" + groupG, []int{1, 3}},
		{"capability", "capabilityId=" + paint, []int{3}},
		{"combined", "siteId=" + siteA + "&type=machine&groupId=" + groupG + "&capabilityId=" + weld, []int{1}},
		{"combined with no match", "siteId=" + siteB + "&type=tool", nil},
		{"empty values are no filter", "siteId=&type=&groupId=&capabilityId=", []int{1, 2, 3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var want []string
			for _, n := range tc.want {
				want = append(want, rid(n))
			}
			if got := listAll(t, r, resourcesPath, tc.query); !slices.Equal(got, want) {
				t.Fatalf("ids = %v; want %v", got, want)
			}
		})
	}
	rec := do(r, http.MethodGet, resourcesPath+"?capabilityId="+paint, "", readOnly, true)
	for _, want := range []string{`"capabilityId":"` + weld + `"`, `"capabilityId":"` + paint + `"`, `"effectiveCalendarId":"` + rid(900) + `"`, `"capabilities":[`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("body missing %s: %s", want, rec.Body)
		}
	}
	if rec := do(r, http.MethodGet, resourcesPath+"?siteId="+rid(999), "", readOnly, true); !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("empty page = %s; want items []", rec.Body)
	}
}

func TestListGroupsAndCapabilities_FilterAndPage(t *testing.T) {
	r, _ := newRouter(listFixtures(), approves)
	if got := listAll(t, r, groupsPath, "limit=1"); !slices.Equal(got, []string{groupG, rid(202)}) {
		t.Fatalf("groups = %v", got)
	}
	if got := listAll(t, r, groupsPath, "siteId="+siteB); !slices.Equal(got, []string{rid(202)}) {
		t.Fatalf("groups by site = %v", got)
	}
	if got := listAll(t, r, capabilitiesPath, "limit=1"); !slices.Equal(got, []string{weld, paint}) {
		t.Fatalf("capabilities = %v", got)
	}
	if rec := do(r, http.MethodGet, groupsPath+"?siteId="+siteA, "", readOnly, true); !strings.Contains(rec.Body.String(), `"calendarId":"`+rid(900)+`"`) {
		t.Fatalf("group body = %s; want calendarId", rec.Body)
	}
}

func TestLists_RejectBadInputWith422(t *testing.T) {
	r, _ := newRouter(listFixtures(), approves)
	tests := []struct {
		path, query, field string
	}{
		{resourcesPath, "type=bogus", "type"},
		{resourcesPath, "type=Machine", "type"},
		{resourcesPath, "siteId=not-a-uuid", "siteId"},
		{resourcesPath, "groupId=x", "groupId"},
		{resourcesPath, "capabilityId=x", "capabilityId"},
		{resourcesPath, "siteId=" + strings.Repeat("s", 65), "siteId"},
		{resourcesPath, "limit=0", "limit"},
		{resourcesPath, "limit=101", "limit"},
		{resourcesPath, "limit=x", "limit"},
		{resourcesPath, "cursor=!!", "cursor"},
		{resourcesPath, "cursor=YWJj", "cursor"},
		{groupsPath, "siteId=x", "siteId"},
		{groupsPath, "cursor=!!", "cursor"},
		{capabilitiesPath, "cursor=!!", "cursor"},
		{capabilitiesPath, "limit=0", "limit"},
	}
	for _, tc := range tests {
		rec := do(r, http.MethodGet, tc.path+"?"+tc.query, "", readOnly, true)
		var problem struct {
			Code   string            `json:"code"`
			Errors map[string]string `json:"errors"`
		}
		if rec.Code != http.StatusUnprocessableEntity || json.Unmarshal(rec.Body.Bytes(), &problem) != nil || problem.Code != "COM-0001" || problem.Errors[tc.field] == "" {
			t.Fatalf("%s?%s = %d %s; want 422 COM-0001 with error on %s", tc.path, tc.query, rec.Code, rec.Body, tc.field)
		}
	}
	if rec := do(r, http.MethodGet, groupsPath+"?type=bogus", "", readOnly, true); rec.Code != http.StatusOK {
		t.Fatalf("groups ignore type: %d %s", rec.Code, rec.Body)
	}
}

func TestLists_Authz(t *testing.T) {
	r, _ := newRouter(listFixtures(), approves)
	for _, path := range []string{resourcesPath, groupsPath, capabilitiesPath} {
		if rec := do(r, http.MethodGet, path, "", "", false); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "AUTH-0001") {
			t.Fatalf("%s without tenant = %d %s; want 401", path, rec.Code, rec.Body)
		}
		if rec := do(r, http.MethodGet, path, "", writeOnly, true); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") {
			t.Fatalf("%s without read duty = %d %s; want 403", path, rec.Code, rec.Body)
		}
	}
}

func TestLists_StoreErrorIsHiddenAndCounted(t *testing.T) {
	r, a := newRouter(&fakeStore{err: errors.New("pg secret detail")}, approves)
	for _, path := range []string{resourcesPath, groupsPath, capabilitiesPath} {
		if rec := do(r, http.MethodGet, path, "", readOnly, true); rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret") || !strings.Contains(rec.Body.String(), "COM-0500") {
			t.Fatalf("%s store error = %d %s; want 500 with hidden detail", path, rec.Code, rec.Body)
		}
	}
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "engineering_resource_requests_failed_total 3") {
		t.Fatalf("metrics = %s; want 3 resource failures", rec.Body)
	}
}
