package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func (f *fakeStore) AssignWave(_ context.Context, id string, version int, picker string, idem store.Idem) (store.Wave, error) {
	f.lastIdem = idem
	f.assigned = picker
	f.assignedVersion = version
	p := picker
	return store.Wave{WaveSummary: store.WaveSummary{ID: id, WaveNumber: "WV-0000001", Status: "OPEN", Version: version + 1, Picker: &p}, Shipments: []store.WaveShipment{}}, nil
}

func (f *fakeStore) CloseWave(_ context.Context, id string, version int, idem store.Idem) (store.Wave, error) {
	f.lastIdem = idem
	f.closedVersion = version
	return store.Wave{WaveSummary: store.WaveSummary{ID: id, WaveNumber: "WV-0000001", Status: "CLOSED", Version: version + 1}, Shipments: []store.WaveShipment{}}, nil
}

func TestAssignWave_ValidationKeyVersionAndDuty(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/v1/fulfillment/waves/" + ids.New() + ":assign"
	hdr := func(key string) map[string]string {
		return map[string]string{"Idempotency-Key": key, "If-Match": `"2"`}
	}
	if rec := do(t, h, http.MethodPost, path, `{"picker":"anna"}`, map[string]string{"Idempotency-Key": "a-1"}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: status = %d, want 428", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, `{"picker":"anna"}`, map[string]string{"If-Match": "2"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: status = %d, want 400", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, `{"picker":"anna"}`, map[string]string{"Idempotency-Key": "a-2", "If-Match": "x"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad If-Match: status = %d, want 400", rec.Code)
	}
	for name, body := range map[string]string{"empty": `{}`, "blank": `{"picker":"  "}`, "long": `{"picker":"` + string(make([]byte, 0)) + `x123456789012345678901234567890123456789012345678901234567890123456789"}`,
		"control": `{"picker":"a\u0007b"}`, "unknown": `{"picker":"a","x":1}`} {
		if rec := do(t, h, http.MethodPost, path, body, hdr("a-"+name)); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (%s)", name, rec.Code, rec.Body.String())
		}
	}
	rec := do(t, h, http.MethodPost, path, `{"picker":" anna "}`, hdr("a-ok"))
	if rec.Code != http.StatusOK || fs.assigned != "anna" || fs.assignedVersion != 2 || fs.lastIdem.Key != "a-ok" || rec.Header().Get("ETag") == "" {
		t.Fatalf("status = %d picker = %q version = %d etag = %q", rec.Code, fs.assigned, fs.assignedVersion, rec.Header().Get("ETag"))
	}
	if rec := do(t, h, http.MethodPost, "/v1/fulfillment/waves/nope:assign", `{"picker":"anna"}`, hdr("a-404")); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: status = %d, want 404", rec.Code)
	}
	h2 := hdr("a-403")
	h2["X-Passage-Duties"] = "WarehousePick"
	if rec := do(t, h, http.MethodPost, path, `{"picker":"anna"}`, h2); rec.Code != http.StatusForbidden {
		t.Fatalf("picker duty: status = %d, want 403", rec.Code)
	}
}

func TestCloseWave_KeyVersionAndDuty(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/v1/fulfillment/waves/" + ids.New() + ":close"
	if rec := do(t, h, http.MethodPost, path, ``, map[string]string{"Idempotency-Key": "c-1"}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: status = %d, want 428", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, ``, map[string]string{"If-Match": `"3"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: status = %d, want 400", rec.Code)
	}
	rec := do(t, h, http.MethodPost, path, ``, map[string]string{"Idempotency-Key": "c-ok", "If-Match": `"3"`})
	if rec.Code != http.StatusOK || fs.closedVersion != 3 || fs.lastIdem.Key != "c-ok" {
		t.Fatalf("status = %d version = %d body = %s", rec.Code, fs.closedVersion, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, path, ``, map[string]string{"Idempotency-Key": "c-403", "If-Match": `"3"`, "X-Passage-Duties": "WarehouseView"}); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer: status = %d, want 403", rec.Code)
	}
}

func TestListWaves_FiltersPickerStatusUnassigned(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/waves?status=OPEN&picker=anna", "", nil); rec.Code != http.StatusOK ||
		fs.workbench.waveFilter.Status != "OPEN" || fs.workbench.waveFilter.Picker != "anna" {
		t.Fatalf("status = %d filter = %+v", rec.Code, fs.workbench.waveFilter)
	}
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/waves?unassigned=true", "", nil); rec.Code != http.StatusOK || !fs.workbench.waveFilter.Unassigned {
		t.Fatalf("unassigned: status = %d filter = %+v", rec.Code, fs.workbench.waveFilter)
	}
	for _, q := range []string{"status=DONE", "unassigned=true&picker=a"} {
		if rec := do(t, h, http.MethodGet, "/v1/fulfillment/waves?"+q, "", nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", q, rec.Code)
		}
	}
}
