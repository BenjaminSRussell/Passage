package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

var (
	manifestIn      store.ManifestInput
	manifestFilter  store.ManifestFilter
	candidateFilter store.ManifestCandidateFilter
	cancelledAt     int
)

func (f *fakeStore) ManifestCandidates(_ context.Context, c store.ManifestCandidateFilter) ([]store.ManifestCandidate, error) {
	candidateFilter = c
	return []store.ManifestCandidate{{ShipmentID: ids.New(), ShipmentNumber: "SH-0000001", Carrier: "ups", TrackingNumber: "1Z1", Ready: true}}, nil
}

func (f *fakeStore) CloseManifest(_ context.Context, in store.ManifestInput, idem store.Idem) (store.Manifest, error) {
	f.lastIdem = idem
	manifestIn = in
	if in.Carrier == "dhl" {
		return store.Manifest{}, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeManifestEmpty, "nothing to manifest")
	}
	return store.Manifest{ID: ids.New(), Number: "MF-0000001", Status: "CLOSED", Carrier: in.Carrier, WarehouseID: in.WarehouseID, ParcelCount: 1, ShipmentCount: 1, Version: 1,
		Parcels: []store.ManifestParcel{}}, nil
}

func (f *fakeStore) ListManifests(_ context.Context, m store.ManifestFilter) ([]store.Manifest, error) {
	manifestFilter = m
	return []store.Manifest{{ID: ids.New(), Number: "MF-0000001", Status: "CLOSED", Version: 1}}, nil
}

func (f *fakeStore) GetManifest(_ context.Context, id string) (store.Manifest, error) {
	return store.Manifest{ID: id, Number: "MF-0000001", Status: "CLOSED", Version: 3, Parcels: []store.ManifestParcel{}}, nil
}

func (f *fakeStore) CancelManifest(_ context.Context, id string, version int, idem store.Idem) (store.Manifest, error) {
	f.lastIdem = idem
	cancelledAt = version
	if version != 3 {
		return store.Manifest{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersion, "stale")
	}
	return store.Manifest{ID: id, Status: "CANCELLED", Version: 4, Parcels: []store.ManifestParcel{}}, nil
}

func TestManifest_CloseValidatesAndPassesTheInput(t *testing.T) {
	h := newServer(t, &fakeStore{})
	wh, le := ids.New(), ids.New()
	today := time.Now().UTC().Format("2006-01-02")
	body := func(carrier, date, extra string) string {
		return `{"warehouseId":"` + wh + `","legalEntityId":"` + le + `","carrier":"` + carrier + `","pickupDate":"` + date + `"` + extra + `}`
	}
	ship := map[string]string{"Idempotency-Key": "m1", "X-Passage-Duties": "fulfillment.warehouse.ship"}
	rec := do(t, h, http.MethodPost, "/v1/fulfillment/manifests", body("ups", today, `,"providerManifestId":" PM-1 "`), ship)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` || manifestIn.Carrier != "ups" || manifestIn.ProviderManifestID != "PM-1" ||
		manifestIn.WarehouseID != wh || manifestIn.LegalEntityID != le || manifestIn.PickupDate.Format("2006-01-02") != today {
		t.Fatalf("close: %d %s %+v", rec.Code, rec.Body.String(), manifestIn)
	}
	if rec := do(t, h, http.MethodPost, "/v1/fulfillment/manifests", body("dhl", today, ""), ship); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "FUL-0076") {
		t.Errorf("empty manifest: %d %s", rec.Code, rec.Body.String())
	}
	far := time.Now().UTC().AddDate(0, 0, 9).Format("2006-01-02")
	for name, b := range map[string]string{
		"carrier": body("UPS", today, ""),
		"date":    body("ups", "tomorrow", ""),
		"window":  body("ups", far, ""),
		"ref":     body("ups", today, `,"providerManifestId":"`+strings.Repeat("x", 129)+`"`),
		"entity":  strings.Replace(body("ups", today, ""), le, "nope", 1),
		"unknown": body("ups", today, `,"extra":1`),
	} {
		if rec := do(t, h, http.MethodPost, "/v1/fulfillment/manifests", b, ship); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	if rec := do(t, h, http.MethodPost, "/v1/fulfillment/manifests", body("ups", today, ""), map[string]string{"X-Passage-Duties": "fulfillment.warehouse.ship"}); rec.Code != http.StatusBadRequest {
		t.Errorf("missing Idempotency-Key: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/v1/fulfillment/manifests", body("ups", today, ""), map[string]string{"Idempotency-Key": "m2", "X-Passage-Duties": "fulfillment.warehouse.view"}); rec.Code != http.StatusForbidden {
		t.Errorf("without the ship duty: %d, want 403", rec.Code)
	}
}

func TestManifest_ReadListAndCancel(t *testing.T) {
	h := newServer(t, &fakeStore{})
	wh, id := ids.New(), ids.New()
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/manifest-candidates?warehouseId="+wh+"&carrier=ups", "", nil); rec.Code != http.StatusOK || candidateFilter.WarehouseID != wh || candidateFilter.Carrier != "ups" || !strings.Contains(rec.Body.String(), `"ready":true`) {
		t.Fatalf("candidates: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/manifest-candidates?carrier=UPS", "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad carrier: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/manifests?status=CLOSED&pickupFrom=2026-11-01&pickupTo=2026-11-30&limit=20", "", nil); rec.Code != http.StatusOK ||
		manifestFilter.Status != "CLOSED" || manifestFilter.PickupFrom == nil || manifestFilter.PickupTo == nil || manifestFilter.Limit != 20 {
		t.Fatalf("list: %d %+v", rec.Code, manifestFilter)
	}
	for _, q := range []string{"status=OPEN", "pickupFrom=soon", "warehouseId=x", "limit=0"} {
		if rec := do(t, h, http.MethodGet, "/v1/fulfillment/manifests?"+q, "", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", q, rec.Code)
		}
	}
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/manifests/"+id, "", nil); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("get: %d %q", rec.Code, rec.Header().Get("ETag"))
	}
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/manifests/nope", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: %d", rec.Code)
	}
	ship := func(version string) map[string]string {
		m := map[string]string{"Idempotency-Key": "c1", "X-Passage-Duties": "fulfillment.warehouse.ship"}
		if version != "" {
			m["If-Match"] = version
		}
		return m
	}
	if rec := do(t, h, http.MethodPost, "/v1/fulfillment/manifests/"+id+":cancel", "", ship(`"3"`)); rec.Code != http.StatusOK || cancelledAt != 3 || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/v1/fulfillment/manifests/"+id+":cancel", "", ship("")); rec.Code != http.StatusPreconditionRequired {
		t.Errorf("missing If-Match: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/v1/fulfillment/manifests/"+id+":cancel", "", ship(`"2"`)); rec.Code != http.StatusPreconditionFailed {
		t.Errorf("stale version: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/v1/fulfillment/manifests/"+id+":cancel", "", ship("abc")); rec.Code != http.StatusBadRequest {
		t.Errorf("bad version: %d", rec.Code)
	}
}
