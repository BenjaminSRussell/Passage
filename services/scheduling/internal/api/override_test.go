package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func (f *fakeStore) CreateCapacityOverride(_ context.Context, in store.CapacityOverride, idem store.Idem) (store.OverrideResult, bool, error) {
	f.lastOverride = in
	f.lastIdem = idem
	if f.err != nil {
		return store.OverrideResult{}, false, f.err
	}
	in.ID = ids.New()
	return store.OverrideResult{CapacityOverride: in, AffectedJobs: []store.AffectedJob{}}, !f.replay, nil
}

func (f *fakeStore) ListCapacityOverrides(_ context.Context, rid string, _, _ time.Time) ([]store.CapacityOverride, error) {
	f.lastOverride = store.CapacityOverride{ResourceID: rid}
	return []store.CapacityOverride{}, f.err
}

func (f *fakeStore) DeleteCapacityOverride(_ context.Context, rid, id string) error {
	f.lastOverride = store.CapacityOverride{ResourceID: rid, ID: id}
	return f.err
}

const overridePath = "/v1/scheduling/resources/" + parentOrder + "/capacity-overrides"

func TestCreateCapacityOverride_ValidatesAndRequiresKeyAndDuty(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	ok := `{"kind":"downtime","start":"2026-01-05T10:00:00Z","end":"2026-01-05T12:00:00Z","reason":"press repair"}`
	if rec := do(h, http.MethodPost, overridePath, ok, allDuties, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("without a key: status %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, overridePath, ok, "scheduling.read", "k0"); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty only: status %d, want 403", rec.Code)
	}
	bad := map[string]string{
		"kind":   `{"kind":"holiday","start":"2026-01-05T10:00:00Z","end":"2026-01-05T12:00:00Z"}`,
		"start":  `{"kind":"extra"}`,
		"end":    `{"kind":"extra","start":"2026-01-05T10:00:00Z","end":"2026-01-05T10:00:00Z"}`,
		"reason": `{"kind":"extra","start":"2026-01-05T10:00:00Z","end":"2026-01-05T12:00:00Z","reason":"` + strings.Repeat("x", 201) + `"}`,
	}
	for field, body := range bad {
		rec := do(h, http.MethodPost, overridePath, body, allDuties, "k-"+field)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Errorf("%s: status %d body %s", field, rec.Code, rec.Body)
		}
	}
	span := `{"kind":"extra","start":"2026-01-05T10:00:00Z","end":"2026-03-05T12:00:00Z"}`
	if rec := do(h, http.MethodPost, overridePath, span, allDuties, "k-span"); rec.Code != http.StatusBadRequest {
		t.Errorf("span over 31 days: status %d", rec.Code)
	}
	rec := do(h, http.MethodPost, overridePath, ok, allDuties, "k1")
	if rec.Code != http.StatusCreated || f.lastOverride.ResourceID != parentOrder || f.lastOverride.Kind != "downtime" {
		t.Fatalf("status %d, override %+v", rec.Code, f.lastOverride)
	}
	// The same key and body on another resource must not replay the first
	// resource's override: the path is part of the fingerprint.
	first := f.lastIdem.Fingerprint
	other := "/v1/scheduling/resources/" + dependentOrder + "/capacity-overrides"
	if rec := do(h, http.MethodPost, other, ok, allDuties, "k1"); rec.Code != http.StatusCreated || f.lastIdem.Fingerprint == first {
		t.Fatalf("other resource: status %d, fingerprint reused %v", rec.Code, f.lastIdem.Fingerprint == first)
	}
	if rec := do(newRouter(&fakeStore{replay: true}), http.MethodPost, overridePath, ok, allDuties, "k1"); rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: status %d", rec.Code)
	}
	missing := httpx.NewProblem(http.StatusNotFound, store.CodeResourceNotFound, "resource not found")
	if rec := do(newRouter(&fakeStore{err: missing}), http.MethodPost, overridePath, ok, allDuties, "k2"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown resource: status %d", rec.Code)
	}
}

func TestListAndDeleteCapacityOverride(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	rec := do(h, http.MethodGet, overridePath+"?from=2026-01-01T00:00:00Z", "", "scheduling.read", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) || f.lastOverride.ResourceID != parentOrder {
		t.Fatalf("list: status %d body %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodGet, overridePath+"?from=yesterday", "", "scheduling.read", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad from: status %d", rec.Code)
	}
	if rec := do(h, http.MethodDelete, overridePath+"/"+dependentOrder, "", "scheduling.read", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("delete with read duty: status %d", rec.Code)
	}
	if rec := do(h, http.MethodDelete, overridePath+"/"+dependentOrder, "", allDuties, ""); rec.Code != http.StatusNoContent || f.lastOverride.ID != dependentOrder {
		t.Fatalf("delete: status %d, %+v", rec.Code, f.lastOverride)
	}
	if rec := do(h, http.MethodDelete, overridePath+"/nope", "", allDuties, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: status %d", rec.Code)
	}
}
