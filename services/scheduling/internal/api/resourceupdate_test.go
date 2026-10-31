package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func (f *fakeStore) UpdateResource(_ context.Context, id string, version int, patch store.ResourcePatch, _ time.Time, idem store.Idem) (store.ResourceUpdate, bool, error) {
	f.lastIdem = idem
	f.lastPatch, f.lastPatchVersion = patch, version
	if f.err != nil {
		return store.ResourceUpdate{}, false, f.err
	}
	r := store.Resource{ID: id, Code: "MIX-1", Name: "Mixer", TimeZone: "UTC", Version: version + 1}
	if patch.Name != nil {
		r.Name = *patch.Name
	}
	return store.ResourceUpdate{Resource: r, AffectedJobs: []store.AffectedJob{}}, f.replay, nil
}

func patchResource(h http.Handler, path, body, duties, key, ifMatch string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", tenantID)
	req.Header.Set("X-Passage-Duties", duties)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const resourcePath = "/v1/scheduling/resources/" + parentOrder

func TestUpdateResource_ValidatesAndNeedsKeyVersionAndDuty(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	ok := `{"name":" Mixer 2 ","shifts":[{"weekday":1,"startMinute":360,"endMinute":840}]}`
	if rec := patchResource(h, resourcePath, ok, allDuties, "", `"1"`); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := patchResource(h, resourcePath, ok, allDuties, "k", ""); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	if rec := patchResource(h, resourcePath, ok, allDuties, "k", `"x"`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad If-Match: %d", rec.Code)
	}
	if rec := patchResource(h, resourcePath, ok, "scheduling.read", "k", `"1"`); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty only: %d", rec.Code)
	}
	bad := map[string]string{
		"body":     `{}`,
		"name":     `{"name":"  "}`,
		"timeZone": `{"timeZone":"Mars/Olympus"}`,
		"shifts":   `{"shifts":[{"weekday":9,"startMinute":0,"endMinute":60}]}`,
	}
	for field, body := range bad {
		rec := patchResource(h, resourcePath, body, allDuties, "k-"+field, `"1"`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Errorf("%s: %d %s", field, rec.Code, rec.Body)
		}
	}
	rec := patchResource(h, resourcePath, ok, allDuties, "k1", `"3"`)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` || f.lastPatchVersion != 3 ||
		f.lastPatch.Name == nil || *f.lastPatch.Name != "Mixer 2" || len(f.lastPatch.Shifts) != 1 {
		t.Fatalf("status %d etag %s patch %+v", rec.Code, rec.Header().Get("ETag"), f.lastPatch)
	}
	first := f.lastIdem.Fingerprint
	patchResource(h, resourcePath, ok, allDuties, "k1", `"4"`)
	if f.lastIdem.Fingerprint == first {
		t.Fatal("the version is part of the fingerprint")
	}
	if rec := patchResource(newRouter(&fakeStore{replay: true}), resourcePath, ok, allDuties, "k1", `"3"`); rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay header missing: %d", rec.Code)
	}
	stale := httpx.NewProblem(http.StatusPreconditionFailed, store.CodeResourceVersion, "changed")
	if rec := patchResource(newRouter(&fakeStore{err: stale}), resourcePath, ok, allDuties, "k2", `"1"`); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale: %d", rec.Code)
	}
	if rec := patchResource(h, "/v1/scheduling/resources/nope", ok, allDuties, "k3", `"1"`); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
}
