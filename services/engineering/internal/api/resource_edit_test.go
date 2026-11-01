package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

func (f *fakeStore) UpdateResource(_ context.Context, r domain.Resource, expected int, _ store.Idem) (store.ResourceRecord, error) {
	s := f.state()
	stored, ok := s.resources[r.ID]
	if !ok {
		return store.ResourceRecord{}, httpx.NewProblem(http.StatusNotFound, store.CodeResNotFound, "resource not found")
	}
	if stored.Version != expected {
		return store.ResourceRecord{}, &domain.Error{Code: domain.CodeResConflict, Detail: "stale"}
	}
	r.Code, r.SiteID, r.Version = stored.Code, stored.SiteID, expected+1
	s.resources[r.ID] = r
	return s.record(r), nil
}

func (f *fakeStore) DeleteResource(_ context.Context, id string) error {
	s := f.state()
	if _, ok := s.resources[id]; !ok {
		return httpx.NewProblem(http.StatusNotFound, store.CodeResNotFound, "resource not found")
	}
	if f.inUse {
		return &domain.Error{Code: domain.CodeResInUse, Detail: "required by an active route"}
	}
	delete(s.resources, id)
	return nil
}

func TestUpdateResource_ReplacesFieldsAndRefusesIdentityChangeAndStaleVersion(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f, approves)
	_, id := seedResource(t, r)
	f.res.resources[id] = withVersion(f.res.resources[id], 1)
	path := resourcesPath + "/" + id
	rec := do(r, http.MethodPut, path, `{"name":"Mill 2","type":"machine","exclusive":true,"version":1}`, writeOnly, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"Mill 2"`) || !strings.Contains(rec.Body.String(), `"version":2`) || !strings.Contains(rec.Body.String(), `"code":"M1"`) {
		t.Fatalf("update = %d %s; want 200 with the new name, version 2 and the code kept", rec.Code, rec.Body)
	}
	for body, want := range map[string]string{
		`{"name":"Mill","type":"machine","code":"OTHER","version":2}`:      "code",
		`{"name":"Mill","type":"machine","siteId":"S2","version":2}`:       "siteId",
		`{"name":"Mill","type":"machine"}`:                                 "version",
		`{"name":"Mill","type":"robot","version":2}`:                       "type",
		`{"name":"Mill","type":"machine","efficiencyPct":"0","version":2}`: "efficiencyPct",
	} {
		if rec := do(r, http.MethodPut, path, body, writeOnly, true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("PUT %s = %d %s; want 400 naming %s", body, rec.Code, rec.Body, want)
		}
	}
	if rec := do(r, http.MethodPut, path, `{"name":"Mill","type":"machine","version":1}`, writeOnly, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ENG-RES-VERSION-CONFLICT") {
		t.Fatalf("stale = %d %s; want 409 ENG-RES-VERSION-CONFLICT", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPut, path, `{"name":"Mill","type":"machine","version":2}`, readOnly, true); rec.Code != http.StatusForbidden {
		t.Fatalf("read-only update = %d; want 403", rec.Code)
	}
	if rec := do(r, http.MethodPut, resourcesPath+"/"+unknownID, `{"name":"Mill","type":"machine","version":1}`, writeOnly, true); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown = %d; want 404", rec.Code)
	}
}

func withVersion(r domain.Resource, v int) domain.Resource { r.Version = v; return r }

func TestDeleteResource_RefusedWhileInUse(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f, approves)
	_, id := seedResource(t, r)
	path := resourcesPath + "/" + id
	if rec := do(r, http.MethodDelete, path, "", readOnly, true); rec.Code != http.StatusForbidden {
		t.Fatalf("read-only delete = %d; want 403", rec.Code)
	}
	f.inUse = true
	if rec := do(r, http.MethodDelete, path, "", writeOnly, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ENG-RES-IN-USE") {
		t.Fatalf("in use = %d %s; want 409 ENG-RES-IN-USE", rec.Code, rec.Body)
	}
	f.inUse = false
	if rec := do(r, http.MethodDelete, path, "", writeOnly, true); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s; want 204", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodDelete, path, "", writeOnly, true); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d; want 404", rec.Code)
	}
	if rec := do(r, http.MethodDelete, resourcesPath+"/not-a-uuid", "", writeOnly, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id = %d; want 400", rec.Code)
	}
}
