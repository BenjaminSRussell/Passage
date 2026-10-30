package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// workbenchCalls records what the workbench handlers passed to the fake store.
type workbenchCalls struct {
	waveReq     store.WaveRequest
	waveFilter  store.WaveFilter
	tracked     []store.TrackingEvent
	duplicate   bool
	cancelCalls int
}

func (f *fakeStore) CancelShipment(_ context.Context, id string, version int, idem store.Idem) (store.Shipment, error) {
	f.lastIdem = idem
	f.workbench.cancelCalls++
	if version != 3 {
		return store.Shipment{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersion, "shipment version changed")
	}
	return store.Shipment{ID: id, Version: 4, Status: domain.StatusCancelled}, nil
}

func (f *fakeStore) CloseRemainder(_ context.Context, id string, version int, idem store.Idem) (store.Shipment, error) {
	f.lastIdem = idem
	if version != 3 {
		return store.Shipment{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersion, "shipment version changed")
	}
	return store.Shipment{ID: id, Version: 4, Status: domain.StatusShipped}, nil
}

func (f *fakeStore) UnpackContainer(_ context.Context, id string, version int, _ string, idem store.Idem) (store.Shipment, error) {
	f.lastIdem = idem
	if version != 3 {
		return store.Shipment{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersion, "shipment version changed")
	}
	return store.Shipment{ID: id, Version: 4, Status: domain.StatusPicked}, nil
}

func (f *fakeStore) PreviewWaves(_ context.Context, req store.WaveRequest) ([]store.WavePlan, error) {
	f.workbench.waveReq = req
	return []store.WavePlan{
		{GroupKey: "C1", LineCount: 2, OrderCount: 1, Shipments: []store.WaveShipment{{ID: ids.New(), ShipmentNumber: "SH-0000001", LineCount: 2}}},
		{GroupKey: "C2", LineCount: 3, OrderCount: 2, Shipments: []store.WaveShipment{{ID: ids.New(), LineCount: 1}, {ID: ids.New(), LineCount: 2}}},
	}, nil
}

func (f *fakeStore) CreateWaves(_ context.Context, req store.WaveRequest, idem store.Idem) ([]store.Wave, error) {
	f.lastIdem = idem
	f.workbench.waveReq = req
	return []store.Wave{{WaveSummary: store.WaveSummary{ID: ids.New(), WaveNumber: "WV-0000001", WarehouseID: req.WarehouseID,
		GroupBy: req.GroupBy, GroupKey: "C1", Status: "OPEN", LineCount: 2, OrderCount: 1}, Shipments: []store.WaveShipment{}}}, nil
}

func (f *fakeStore) ListWaves(_ context.Context, filter store.WaveFilter) ([]store.WaveSummary, error) {
	f.workbench.waveFilter = filter
	return []store.WaveSummary{{ID: ids.New(), WaveNumber: "WV-0000001", Status: "OPEN"}}, nil
}

func (f *fakeStore) GetWave(_ context.Context, id string) (store.Wave, error) {
	if f.notFound {
		return store.Wave{}, httpx.NewProblem(http.StatusNotFound, store.CodeWaveNotFound, "wave not found")
	}
	return store.Wave{WaveSummary: store.WaveSummary{ID: id, WaveNumber: "WV-0000001"}, Shipments: []store.WaveShipment{}}, nil
}

func (f *fakeStore) RecordTracking(_ context.Context, _ string, ev store.TrackingEvent) (store.TrackingEvent, bool, error) {
	if f.workbench.duplicate {
		return ev, false, nil
	}
	f.workbench.tracked = append(f.workbench.tracked, ev)
	return ev, true, nil
}

func (f *fakeStore) ListTracking(context.Context, string) ([]store.TrackingEvent, error) {
	return f.workbench.tracked, nil
}

const wbWarehouse = "00000000-0000-0000-0000-0000000000a1"

func TestCancelShipment_RequiresKeyAndVersion(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/v1/fulfillment/shipments/" + ids.New() + ":cancel"
	if rec := do(t, h, http.MethodPost, path, "", map[string]string{"Idempotency-Key": "c-1"}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: status = %d, want 428", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, "", map[string]string{"If-Match": `"3"`}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "FUL-0051") {
		t.Fatalf("no key: status = %d body = %s, want 400 FUL-0051", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, path, "", map[string]string{"Idempotency-Key": "c-2", "If-Match": `"2"`}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale version: status = %d, want 412", rec.Code)
	}
	rec := do(t, h, http.MethodPost, path, "", map[string]string{"Idempotency-Key": "c-3", "If-Match": `"3"`})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"CANCELLED"`) || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("status = %d etag = %q body = %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	// Cancel is a WarehouseRelease duty, not a picker's.
	rec = do(t, h, http.MethodPost, path, "", map[string]string{"Idempotency-Key": "c-4", "If-Match": `"3"`, "X-Passage-Duties": "WarehousePick"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("picker cancel: status = %d, want 403", rec.Code)
	}
}

func TestCloseRemainder_RequiresKeyVersionAndDuty(t *testing.T) {
	h := newServer(t, &fakeStore{})
	path := "/v1/fulfillment/shipments/" + ids.New() + ":close-remainder"
	if rec := do(t, h, http.MethodPost, path, "", map[string]string{"Idempotency-Key": "r-1"}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: status = %d, want 428", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, "", map[string]string{"If-Match": `"3"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: status = %d, want 400", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, "", map[string]string{"Idempotency-Key": "r-2", "If-Match": `"2"`}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale version: status = %d, want 412", rec.Code)
	}
	rec := do(t, h, http.MethodPost, path, "", map[string]string{"Idempotency-Key": "r-3", "If-Match": `"3"`})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"SHIPPED"`) || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("status = %d etag = %q body = %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, path, "", map[string]string{"Idempotency-Key": "r-4", "If-Match": `"3"`, "X-Passage-Duties": "WarehousePick"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("picker close: status = %d, want 403", rec.Code)
	}
}

func TestUnpackContainer_RequiresKeyVersionAndDuty(t *testing.T) {
	h := newServer(t, &fakeStore{})
	path := "/v1/fulfillment/shipments/" + ids.New() + "/containers/" + ids.New() + ":unpack"
	if rec := do(t, h, http.MethodPost, path, "", map[string]string{"Idempotency-Key": "u-1"}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: status = %d, want 428", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, "", map[string]string{"If-Match": `"3"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: status = %d, want 400", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, "", map[string]string{"Idempotency-Key": "u-2", "If-Match": `"2"`}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale version: status = %d, want 412", rec.Code)
	}
	rec := do(t, h, http.MethodPost, path, "", map[string]string{"Idempotency-Key": "u-3", "If-Match": `"3"`})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"PICKED"`) || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("status = %d etag = %q body = %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	bad := "/v1/fulfillment/shipments/" + ids.New() + "/containers/not-a-uuid:unpack"
	if rec := do(t, h, http.MethodPost, bad, "", map[string]string{"Idempotency-Key": "u-4", "If-Match": `"3"`}); rec.Code != http.StatusNotFound {
		t.Fatalf("bad container id: status = %d, want 404", rec.Code)
	}
	rec = do(t, h, http.MethodPost, path, "", map[string]string{"Idempotency-Key": "u-5", "If-Match": `"3"`, "X-Passage-Duties": "WarehousePick"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("picker unpack: status = %d, want 403", rec.Code)
	}
}

func TestPreviewWaves_DefaultsTotalsAndValidation(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/v1/fulfillment/waves:preview"
	body := `{"warehouseId":"` + wbWarehouse + `","shipFrom":"2026-10-01","shipTo":"2026-10-31","carrier":"UPS"}`
	rec := do(t, h, http.MethodPost, path, body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out wavesResponse[store.WavePlan]
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.WaveCount != 2 || out.LineCount != 5 || out.GroupBy != "CUSTOMER" || out.Message != "Preview: 2 waves from 5 lines" {
		t.Fatalf("preview = %+v", out)
	}
	req := fs.workbench.waveReq
	if req.Limits != (domain.WaveLimits{MaxLines: 200, MaxOrders: 50}) || req.Carrier != "UPS" ||
		!req.ShipFrom.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) || !req.ShipTo.Equal(time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("store request = %+v", req)
	}

	bad := map[string]struct{ body, field string }{
		"unknown group":     {`{"warehouseId":"` + wbWarehouse + `","groupBy":"COLOR"}`, "groupBy"},
		"oversize wave":     {`{"warehouseId":"` + wbWarehouse + `","maxLines":5000}`, "maxLines"},
		"negative orders":   {`{"warehouseId":"` + wbWarehouse + `","maxOrders":-1}`, "maxOrders"},
		"inverted dates":    {`{"warehouseId":"` + wbWarehouse + `","shipFrom":"2026-10-31","shipTo":"2026-10-01"}`, "shipTo"},
		"bad carrier":       {`{"warehouseId":"` + wbWarehouse + `","carrier":"U P S"}`, "carrier"},
		"bad customer":      {`{"warehouseId":"` + wbWarehouse + `","customerId":"c1"}`, "customerId"},
		"missing warehouse": {`{"groupBy":"CUSTOMER"}`, "warehouseId"},
	}
	for name, c := range bad {
		rec := do(t, h, http.MethodPost, path, c.body, nil)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+c.field+`"`) {
			t.Errorf("%s: status = %d body = %s, want 400 naming %s", name, rec.Code, rec.Body.String(), c.field)
		}
	}
	if rec := do(t, h, http.MethodPost, path, `{"warehouseId":"`+wbWarehouse+`","colour":"red"}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field: status = %d, want 400", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("empty body: status = %d, want 400", rec.Code)
	}
}

func TestCreateWaves_RequiresKeyAndReportsFUL0016(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	body := `{"warehouseId":"` + wbWarehouse + `","groupBy":"CARRIER","maxLines":20}`
	if rec := do(t, h, http.MethodPost, "/v1/fulfillment/waves", body, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: status = %d, want 400", rec.Code)
	}
	rec := do(t, h, http.MethodPost, "/v1/fulfillment/waves", body, map[string]string{"Idempotency-Key": "w-1"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out wavesResponse[store.Wave]
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.WaveCount != 1 || out.Waves[0].WaveNumber != "WV-0000001" || out.Message != "FUL-0016: Workbench created 1 wave from 2 lines" {
		t.Fatalf("response = %+v", out)
	}
	if fs.lastIdem.Key != "w-1" || fs.workbench.waveReq.GroupBy != "CARRIER" || fs.workbench.waveReq.Limits.MaxLines != 20 {
		t.Fatalf("idem = %+v req = %+v", fs.lastIdem, fs.workbench.waveReq)
	}
	if rec := do(t, h, http.MethodPost, "/v1/fulfillment/waves", body, map[string]string{"Idempotency-Key": "w-2", "X-Passage-Duties": "WarehouseView"}); rec.Code != http.StatusForbidden {
		t.Fatalf("view-only create: status = %d, want 403", rec.Code)
	}
}

func TestWaves_ListAndGet(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	rec := do(t, h, http.MethodGet, "/v1/fulfillment/waves?warehouseId="+wbWarehouse+"&limit=10", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items"`) {
		t.Fatalf("list status = %d body = %s", rec.Code, rec.Body.String())
	}
	if fs.workbench.waveFilter != (store.WaveFilter{WarehouseID: wbWarehouse, Limit: 10}) {
		t.Fatalf("filter = %+v", fs.workbench.waveFilter)
	}
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/waves?warehouseId=w1", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad warehouse: status = %d, want 400", rec.Code)
	}
	id := ids.New()
	rec = do(t, h, http.MethodGet, "/v1/fulfillment/waves/"+id, "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), id) {
		t.Fatalf("get status = %d body = %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/waves/not-a-uuid", "", nil); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeWaveNotFound) {
		t.Fatalf("bad id: status = %d body = %s, want 404 %s", rec.Code, rec.Body.String(), store.CodeWaveNotFound)
	}
}

func TestTracking_RecordsOnceAndValidates(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/v1/fulfillment/shipments/" + ids.New() + "/tracking"
	ship := map[string]string{"X-Passage-Duties": "WarehouseShip"}
	good := `{"provider":"easypost","eventKey":"evt_1","trackingNumber":"1Z999AA1","status":"IN_TRANSIT","description":"at hub","eventTime":"2026-10-21T12:00:00+02:00"}`
	rec := do(t, h, http.MethodPost, path, good, ship)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"created":true`) {
		t.Fatalf("first delivery: status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := fs.workbench.tracked[0].EventTime; !got.Equal(time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)) || got.Location() != time.UTC {
		t.Fatalf("event time = %v, want 10:00 UTC", got)
	}
	fs.workbench.duplicate = true
	rec = do(t, h, http.MethodPost, path, good, ship)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"created":false`) {
		t.Fatalf("redelivery: status = %d body = %s", rec.Code, rec.Body.String())
	}
	bad := map[string]struct{ body, field string }{
		"status":    {`{"provider":"easypost","eventKey":"e","trackingNumber":"1Z","status":"TELEPORTED","eventTime":"2026-10-21T12:00:00Z"}`, "status"},
		"time":      {`{"provider":"easypost","eventKey":"e","trackingNumber":"1Z","status":"DELIVERED","eventTime":"yesterday"}`, "eventTime"},
		"provider":  {`{"provider":"Easy Post","eventKey":"e","trackingNumber":"1Z","status":"DELIVERED","eventTime":"2026-10-21T12:00:00Z"}`, "provider"},
		"event key": {`{"provider":"easypost","eventKey":" ","trackingNumber":"1Z","status":"DELIVERED","eventTime":"2026-10-21T12:00:00Z"}`, "eventKey"},
		"tracking":  {`{"provider":"easypost","eventKey":"e","trackingNumber":"","status":"DELIVERED","eventTime":"2026-10-21T12:00:00Z"}`, "trackingNumber"},
	}
	for name, c := range bad {
		rec := do(t, h, http.MethodPost, path, c.body, ship)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+c.field+`"`) {
			t.Errorf("%s: status = %d body = %s, want 400 naming %s", name, rec.Code, rec.Body.String(), c.field)
		}
	}
	if rec := do(t, h, http.MethodPost, path, good, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("without WarehouseShip: status = %d, want 403", rec.Code)
	}
}

func TestTracking_ListNeedsViewDuty(t *testing.T) {
	fs := &fakeStore{workbench: workbenchCalls{tracked: []store.TrackingEvent{{Provider: "shippo", EventKey: "e1", Status: "DELIVERED"}}}}
	h := newServer(t, fs)
	path := "/v1/fulfillment/shipments/" + ids.New() + "/tracking"
	rec := do(t, h, http.MethodGet, path, "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"eventKey":"e1"`) {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, path, "", map[string]string{"X-Passage-Duties": "PackingSlipPost"}); rec.Code != http.StatusForbidden {
		t.Fatalf("without WarehouseView: status = %d, want 403", rec.Code)
	}
}

func TestContainerLabel_RendersZPLForKnownContainer(t *testing.T) {
	sscc, err := domain.SSCC(0, "0614141", 12345678)
	if err != nil {
		t.Fatal(err)
	}
	cid, badID := ids.New(), ids.New()
	fs := &fakeStore{shipment: store.Shipment{ShipmentNumber: "SH-0000007", Containers: []store.Container{
		{ID: cid, SSCC: sscc, ContainerType: "PALLET"},
		{ID: badID, SSCC: sscc[:17] + "0", ContainerType: "PALLET"},
	}}}
	h := newServer(t, fs)
	sid := ids.New()
	base := "/v1/fulfillment/shipments/" + sid + "/containers/"
	rec := do(t, h, http.MethodGet, base+cid+"/label", "", nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/zpl") {
		t.Fatalf("status = %d type = %q body = %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), ">;>800"+sscc) || !strings.HasPrefix(rec.Body.String(), "^XA") {
		t.Fatalf("label = %s", rec.Body.String())
	}
	if sscc[17] != '0' {
		if rec := do(t, h, http.MethodGet, base+badID+"/label", "", nil); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodeSSCCInvalid) {
			t.Fatalf("bad check digit: status = %d body = %s, want 422 %s", rec.Code, rec.Body.String(), store.CodeSSCCInvalid)
		}
	}
	if rec := do(t, h, http.MethodGet, base+ids.New()+"/label", "", nil); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeContainerNotFound) {
		t.Fatalf("unknown container: status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestRelease_CarrierAndZonePassThroughAndValidate(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	order, customer, le := ids.New(), ids.New(), ids.New()
	today := time.Now().UTC().Format("2006-01-02")
	line := func(carrier, zone string) string {
		return `{"orderId":"` + order + `","orderLineId":"` + ids.New() + `","customerId":"` + customer + `","legalEntityId":"` + le +
			`","productId":"` + ids.New() + `","unit":"EA","qty":"5","confirmedShipDate":"` + today + `","atpOk":true,"carrierCode":"` + carrier + `","zone":"` + zone + `"}`
	}
	body := `{"warehouseId":"` + wbWarehouse + `","lines":[` + line("UPS", "A") + `,` + line("UPS", "B") + `]}`
	rec := do(t, h, http.MethodPost, "/v1/fulfillment/releases:run", body, map[string]string{"Idempotency-Key": "rel-cz"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	got := fs.released[0].Lines
	if got[0].CarrierCode != "UPS" || got[0].Zone != "A" || got[1].Zone != "B" {
		t.Fatalf("released lines = %+v", got)
	}
	mixed := `{"warehouseId":"` + wbWarehouse + `","lines":[` + line("UPS", "A") + `,` + line("DHL", "A") + `]}`
	rec = do(t, h, http.MethodPost, "/v1/fulfillment/releases:run", mixed, map[string]string{"Idempotency-Key": "rel-mixed"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "lines[1].carrierCode") {
		t.Fatalf("mixed carriers: status = %d body = %s", rec.Code, rec.Body.String())
	}
	badZone := `{"warehouseId":"` + wbWarehouse + `","lines":[` + line("UPS", "zone a") + `]}`
	rec = do(t, h, http.MethodPost, "/v1/fulfillment/releases:run", badZone, map[string]string{"Idempotency-Key": "rel-zone"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "lines[0].zone") {
		t.Fatalf("bad zone: status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestListShipments_AcceptsPartiallyShipped(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/shipments?status=PARTIALLY_SHIPPED", "", nil); rec.Code != http.StatusOK || fs.shipmentFilter.Status != "PARTIALLY_SHIPPED" {
		t.Fatalf("status = %d filter = %+v", rec.Code, fs.shipmentFilter)
	}
}

func TestPostPackingSlip_PassesAsnFlag(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/v1/fulfillment/shipments/" + ids.New() + ":post-packing-slip"
	body := `{"postingDate":"2026-10-21","currency":"USD","asnEnabled":true,"costs":[{"shipmentLineId":"` + ids.New() + `","unitCost":"1","costSnapshotId":"s"}]}`
	rec := do(t, h, http.MethodPost, path, body, map[string]string{"Idempotency-Key": "post-asn", "If-Match": `"3"`})
	if rec.Code != http.StatusCreated || !fs.lastPost.EmitASN {
		t.Fatalf("status = %d emitASN = %v body = %s", rec.Code, fs.lastPost.EmitASN, rec.Body.String())
	}
}

func (f *fakeStore) ConfirmWavePick(_ context.Context, waveID string, picks []store.PickInput, pickRest bool, idem store.Idem) (store.WavePickResult, error) {
	f.lastIdem = idem
	f.picks = picks
	return store.WavePickResult{WaveID: waveID, LinesConfirmed: len(picks), Shipments: []store.WavePickShipment{}}, nil
}

func TestConfirmWavePick_ValidationKeyAndDuty(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/v1/fulfillment/waves/" + ids.New() + ":confirm-pick"
	line := ids.New()
	good := `{"picks":[{"shipmentLineId":"` + line + `","picked":"0","reason":"OUT_OF_STOCK"}],"pickRest":true}`
	if rec := do(t, h, http.MethodPost, path, good, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: status = %d, want 400", rec.Code)
	}
	for name, body := range map[string]string{
		"empty":      `{}`,
		"bad reason": `{"picks":[{"shipmentLineId":"` + line + `","picked":"0","reason":"LOST"}]}`,
		"bad qty":    `{"picks":[{"shipmentLineId":"` + line + `","picked":"-1"}]}`,
		"duplicate":  `{"picks":[{"shipmentLineId":"` + line + `","picked":"1"},{"shipmentLineId":"` + line + `","picked":"1"}]}`,
		"bad id":     `{"picks":[{"shipmentLineId":"x","picked":"1"}]}`,
	} {
		if rec := do(t, h, http.MethodPost, path, body, map[string]string{"Idempotency-Key": "w-" + name}); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (%s)", name, rec.Code, rec.Body.String())
		}
	}
	rec := do(t, h, http.MethodPost, path, good, map[string]string{"Idempotency-Key": "w-ok"})
	if rec.Code != http.StatusOK || len(fs.picks) != 1 || fs.picks[0].Reason != "OUT_OF_STOCK" || fs.lastIdem.Key != "w-ok" {
		t.Fatalf("status = %d picks = %+v body = %s", rec.Code, fs.picks, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/v1/fulfillment/waves/nope:confirm-pick", good, map[string]string{"Idempotency-Key": "w-404"}); rec.Code != http.StatusNotFound {
		t.Fatalf("bad wave id: status = %d, want 404", rec.Code)
	}
	rec = do(t, h, http.MethodPost, path, good, map[string]string{"Idempotency-Key": "w-403", "X-Passage-Duties": "WarehouseView"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer: status = %d, want 403", rec.Code)
	}
}
