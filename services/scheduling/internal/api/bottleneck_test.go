package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func (f *fakeStore) RefreshCapacityLoad(context.Context) (store.RefreshOutcome, error) {
	if f.err != nil {
		return store.RefreshOutcome{}, f.err
	}
	return store.RefreshOutcome{Resources: 2, LoadRows: 5, OpenEpisodes: 1}, nil
}

func (f *fakeStore) ListBottlenecks(_ context.Context, flt store.BottleneckFilter) ([]store.BottleneckEpisode, error) {
	f.lastBottleneck = flt
	return []store.BottleneckEpisode{{ID: ids.New(), ResourceCode: "CNC-1", FirstWeek: "2027-01-04"}}, f.err
}

func (f *fakeStore) GetBottleneck(_ context.Context, id string) (store.BottleneckDetail, error) {
	if f.err != nil {
		return store.BottleneckDetail{}, f.err
	}
	return store.BottleneckDetail{BottleneckEpisode: store.BottleneckEpisode{ID: id}, Weeks: []store.LoadWeek{}}, nil
}

func TestBottleneckRoutes(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	call := func(method, target string) *httptest.ResponseRecorder {
		return do(h, method, target, "", allDuties, "")
	}
	if rec := call(http.MethodGet, "/v1/scheduling/bottlenecks?status=open&limit=5&resourceId="+ids.New()); rec.Code != 200 || f.lastBottleneck.Status != "open" || f.lastBottleneck.Limit != 5 {
		t.Fatalf("list: %d %s %+v", rec.Code, rec.Body, f.lastBottleneck)
	}
	for _, bad := range []string{"?status=weird", "?resourceId=nope", "?limit=0"} {
		if rec := call(http.MethodGet, "/v1/scheduling/bottlenecks"+bad); rec.Code != 400 {
			t.Fatalf("%s: %d, want 400", bad, rec.Code)
		}
	}
	if rec := call(http.MethodGet, "/v1/scheduling/bottlenecks/"+ids.New()); rec.Code != 200 {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodGet, "/v1/scheduling/bottlenecks/not-a-uuid"); rec.Code != 404 || !strings.Contains(rec.Body.String(), store.CodeBottleneckNotFound) {
		t.Fatalf("get bad id: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodPost, "/v1/scheduling/capacity-load:refresh"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"openEpisodes":1`) {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodPost, "/v1/scheduling/capacity-load:refresh", "", "scheduling.read", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("refresh without scheduling.run: %d, want 403", rec.Code)
	}
	f.err = httpx.NewProblem(http.StatusNotFound, store.CodeBottleneckNotFound, "bottleneck episode not found")
	if rec := call(http.MethodGet, "/v1/scheduling/bottlenecks/"+ids.New()); rec.Code != 404 {
		t.Fatalf("store 404 not passed on: %d", rec.Code)
	}
}
