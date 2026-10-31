package api_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

func detailFixtures() *fakeStore {
	f := listFixtures()
	f.calList = []store.Calendar{
		{ID: rid(902), WorkCalendar: domain.WorkCalendar{Code: "NIGHT", Name: "Night"}, Version: 1},
		{ID: rid(900), WorkCalendar: domain.WorkCalendar{Code: "DAY", Name: "Day", ClosedDates: []time.Time{day("2026-12-25")}}, Version: 3, GeneratedFrom: day("2026-01-01"), GeneratedTo: day("2026-12-31")},
	}
	return f
}

func (f *fakeStore) GetResourceGroup(_ context.Context, id string) (domain.ResourceGroup, error) {
	if f.err != nil {
		return domain.ResourceGroup{}, f.err
	}
	for _, g := range f.groupList {
		if g.ID == id {
			return g, nil
		}
	}
	return domain.ResourceGroup{}, httpx.NewProblem(http.StatusNotFound, store.CodeResNotFound, "resource group not found")
}

func (f *fakeStore) GetCapability(_ context.Context, id string) (domain.Capability, error) {
	if f.err != nil {
		return domain.Capability{}, f.err
	}
	for _, c := range f.capList {
		if c.ID == id {
			return c, nil
		}
	}
	return domain.Capability{}, httpx.NewProblem(http.StatusNotFound, store.CodeResNotFound, "capability not found")
}

func (f *fakeStore) GetCalendar(_ context.Context, id string) (store.Calendar, error) {
	if f.err != nil {
		return store.Calendar{}, f.err
	}
	for _, c := range f.calList {
		if c.ID == id {
			return c, nil
		}
	}
	return store.Calendar{}, httpx.NewProblem(http.StatusNotFound, store.CodeCalNotFound, "calendar not found")
}

func (f *fakeStore) ListCalendars(_ context.Context, after string, limit int) (store.Page[store.Calendar], error) {
	if f.err != nil {
		return store.Page[store.Calendar]{}, f.err
	}
	return pageOf(f.calList, after, limit, func(c store.Calendar) string { return c.ID }), nil
}

func TestDetailReads_ReturnTheRow(t *testing.T) {
	r, _ := newRouter(detailFixtures(), approves)
	tests := []struct{ path, want string }{
		{groupsPath + "/" + groupG, `"code":"A"`},
		{capabilitiesPath + "/" + weld, `"code":"WELD"`},
		{calendarsPath + "/" + rid(900), `"generatedTo":"2026-12-31"`},
	}
	for _, tc := range tests {
		rec := do(r, http.MethodGet, tc.path, "", readOnly, true)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("GET %s = %d %s; want 200 with %s", tc.path, rec.Code, rec.Body, tc.want)
		}
	}
}

func TestDetailReads_NotFoundBadIDAndAuthz(t *testing.T) {
	r, _ := newRouter(detailFixtures(), approves)
	for _, base := range []string{groupsPath, capabilitiesPath, calendarsPath} {
		if rec := do(r, http.MethodGet, base+"/"+rid(777), "", readOnly, true); rec.Code != http.StatusNotFound {
			t.Fatalf("%s missing = %d %s; want 404", base, rec.Code, rec.Body)
		}
		if rec := do(r, http.MethodGet, base+"/nope", "", readOnly, true); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s bad id = %d %s; want 400", base, rec.Code, rec.Body)
		}
		if rec := do(r, http.MethodGet, base+"/"+rid(900), "", "", false); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s no tenant = %d; want 401", base, rec.Code)
		}
		if rec := do(r, http.MethodGet, base+"/"+rid(900), "", writeOnly, true); rec.Code != http.StatusForbidden {
			t.Fatalf("%s no read duty = %d; want 403", base, rec.Code)
		}
	}
}

func TestListCalendars_PagesValidatesAndHidesStoreErrors(t *testing.T) {
	r, _ := newRouter(detailFixtures(), approves)
	if got := listAll(t, r, calendarsPath, "limit=1"); !slices.Equal(got, []string{rid(900), rid(902)}) {
		t.Fatalf("calendars = %v", got)
	}
	if rec := do(r, http.MethodGet, calendarsPath+"?limit=0", "", readOnly, true); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("limit=0 = %d; want 422", rec.Code)
	}
	if rec := do(r, http.MethodGet, calendarsPath, "", writeOnly, true); rec.Code != http.StatusForbidden {
		t.Fatalf("no read duty = %d; want 403", rec.Code)
	}
	r, _ = newRouter(&fakeStore{err: errors.New("pg secret detail")}, approves)
	for _, path := range []string{calendarsPath, calendarsPath + "/" + rid(900), groupsPath + "/" + rid(900), capabilitiesPath + "/" + rid(900)} {
		if rec := do(r, http.MethodGet, path, "", readOnly, true); rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret") {
			t.Fatalf("%s store error = %d %s; want hidden 500", path, rec.Code, rec.Body)
		}
	}
}
