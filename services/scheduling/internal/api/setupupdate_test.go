package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func (f *fakeStore) GetCapacityBlock(_ context.Context, id string) (store.CapacityBlock, error) {
	return store.CapacityBlock{ID: id, Code: "MON-WHITE", Condition: []domain.Predicate{}, Version: 2}, f.err
}

func (f *fakeStore) UpdateCapacityBlock(_ context.Context, id string, version int, patch store.BlockPatch, _ time.Time, idem store.Idem) (store.CapacityBlockUpdate, bool, error) {
	f.lastIdem, f.lastBlockPatch, f.lastPatchVersion = idem, patch, version
	if f.err != nil {
		return store.CapacityBlockUpdate{}, false, f.err
	}
	return store.CapacityBlockUpdate{
		CapacityBlock: store.CapacityBlock{ID: id, Code: "MON-WHITE", Condition: []domain.Predicate{}, Version: version + 1, Active: patch.Active == nil || *patch.Active},
		AffectedJobs:  []store.AffectedJob{},
	}, f.replay, nil
}

func (f *fakeStore) UpdateNetwork(_ context.Context, id string, version int, patch store.NetworkPatch, idem store.Idem) (store.Network, bool, error) {
	f.lastIdem, f.lastNetPatch, f.lastPatchVersion = idem, patch, version
	if f.err != nil {
		return store.Network{}, false, f.err
	}
	return store.Network{ID: id, Code: "MIXING", Edges: []domain.NetworkEdge{}, Version: version + 1}, f.replay, nil
}

const (
	blockPath   = "/v1/scheduling/capacity-blocks/" + parentOrder
	networkPath = "/v1/scheduling/networks/" + parentOrder
)

func TestUpdateCapacityBlock_ValidatesAndNeedsKeyVersionAndDuty(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	ok := `{"priority":20,"active":false}`
	if rec := patchResource(h, blockPath, ok, allDuties, "", `"1"`); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := patchResource(h, blockPath, ok, allDuties, "k", ""); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	if rec := patchResource(h, blockPath, ok, "scheduling.read", "k", `"1"`); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty only: %d", rec.Code)
	}
	bad := map[string]string{
		"body":      `{}`,
		"priority":  `{"priority":1001}`,
		"condition": `{"condition":[{"attribute":"","op":"=","value":"x"}]}`,
	}
	for field, body := range bad {
		rec := patchResource(h, blockPath, body, allDuties, "k-"+field, `"1"`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Errorf("%s: %d %s", field, rec.Code, rec.Body)
		}
	}
	rec := patchResource(h, blockPath, ok, allDuties, "k1", `"2"`)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` || f.lastPatchVersion != 2 || !strings.Contains(rec.Body.String(), `"affectedJobs":[]`) ||
		f.lastBlockPatch.Priority == nil || *f.lastBlockPatch.Priority != 20 || f.lastBlockPatch.Active == nil || *f.lastBlockPatch.Active {
		t.Fatalf("status %d etag %s patch %+v", rec.Code, rec.Header().Get("ETag"), f.lastBlockPatch)
	}
	first := f.lastIdem.Fingerprint
	patchResource(h, blockPath, ok, allDuties, "k1", `"3"`)
	if f.lastIdem.Fingerprint == first {
		t.Fatal("the version is part of the fingerprint")
	}
	if rec := patchResource(newRouter(&fakeStore{replay: true}), blockPath, ok, allDuties, "k1", `"2"`); rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay header missing: %d", rec.Code)
	}
	stale := httpx.NewProblem(http.StatusPreconditionFailed, store.CodeBlockVersion, "changed")
	if rec := patchResource(newRouter(&fakeStore{err: stale}), blockPath, ok, allDuties, "k2", `"1"`); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale: %d", rec.Code)
	}
	if rec := patchResource(h, "/v1/scheduling/capacity-blocks/nope", ok, allDuties, "k3", `"1"`); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func TestGetCapacityBlock_ReturnsETag(t *testing.T) {
	req := do(newRouter(&fakeStore{}), http.MethodGet, blockPath, "", "scheduling.read", "")
	if req.Code != http.StatusOK || req.Header().Get("ETag") != `"2"` {
		t.Fatalf("get: %d etag %s", req.Code, req.Header().Get("ETag"))
	}
	if r := do(newRouter(&fakeStore{}), http.MethodGet, "/v1/scheduling/capacity-blocks/nope", "", "scheduling.read", ""); r.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", r.Code)
	}
}

func TestUpdateNetwork_ValidatesAndNeedsKeyVersionAndDuty(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	ok := `{"name":" Mixing line 2 ","activeTo":"2026-12-31"}`
	if rec := patchResource(h, networkPath, ok, allDuties, "k", ""); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	if rec := patchResource(h, networkPath, ok, "scheduling.read,scheduling.capacity.write", "k", `"1"`); rec.Code != http.StatusForbidden {
		t.Fatalf("setup duty missing: %d", rec.Code)
	}
	bad := map[string]string{
		"body":     `{}`,
		"name":     `{"name":" "}`,
		"activeTo": `{"activeTo":"31/12/2026"}`,
	}
	// An end date cannot be cleared: an explicit null is refused, not ignored.
	for i, body := range []string{`{"activeTo":null}`, `{"name":"Line","activeTo":null}`} {
		rec := patchResource(h, networkPath, body, allDuties, "k-null-"+strconv.Itoa(i), `"1"`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not cleared") {
			t.Errorf("null activeTo %s: %d %s", body, rec.Code, rec.Body)
		}
	}
	for field, body := range bad {
		rec := patchResource(h, networkPath, body, allDuties, "k-"+field, `"1"`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Errorf("%s: %d %s", field, rec.Code, rec.Body)
		}
	}
	rec := patchResource(h, networkPath, ok, allDuties, "k1", `"1"`)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` || f.lastNetPatch.Name == nil || *f.lastNetPatch.Name != "Mixing line 2" ||
		f.lastNetPatch.ActiveTo == nil || *f.lastNetPatch.ActiveTo != "2026-12-31" {
		t.Fatalf("status %d patch %+v", rec.Code, f.lastNetPatch)
	}
	end := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeNetworkEnd, "before start")
	if rec := patchResource(newRouter(&fakeStore{err: end}), networkPath, ok, allDuties, "k2", `"1"`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("end before start: %d", rec.Code)
	}
}
