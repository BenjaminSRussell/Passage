package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/carrier"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func fakeTracked(provider, trackingNumber string) store.TrackingEvent {
	return store.TrackingEvent{Provider: provider, EventKey: "label:" + trackingNumber, TrackingNumber: trackingNumber, Status: "LABELLED", EventTime: time.Now().UTC()}
}

// fakeGateway stands in for carrier-gateway: one label per key, a fixed tracking timeline.
type fakeGateway struct {
	err      error
	le       string
	keys     []string
	orders   []carrier.LabelOrder
	labels   map[string]carrier.Label
	tracked  []string
	timeline []carrier.TrackingEvent
}

func (g *fakeGateway) Rates(_ context.Context, le string, q carrier.RateQuery) (carrier.Rates, error) {
	g.le = le
	if g.err != nil {
		return carrier.Rates{}, g.err
	}
	return carrier.Rates{Items: []carrier.Rate{{Carrier: "sandbox", Service: "GROUND", Amount: "8.63", Currency: "USD", TransitDays: 3}}, Failures: []carrier.Failure{}}, nil
}

func (g *fakeGateway) Label(_ context.Context, le, key string, o carrier.LabelOrder) (carrier.Label, error) {
	g.le = le
	if g.err != nil {
		return carrier.Label{}, g.err
	}
	g.keys, g.orders = append(g.keys, key), append(g.orders, o)
	if l, ok := g.labels[key]; ok {
		l.Created = false
		return l, nil
	}
	l := carrier.Label{Carrier: o.Carrier, Service: o.Service, TrackingNumber: fmt.Sprintf("SBX%d", len(g.labels)+1), Amount: "8.63", Currency: "USD",
		Format: "ZPL", Document: "XlhB", CreatedAt: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC), Created: true}
	if g.labels == nil {
		g.labels = map[string]carrier.Label{}
	}
	g.labels[key] = l
	return l, nil
}

func (g *fakeGateway) Track(_ context.Context, le, code, tn string) (carrier.Tracking, error) {
	g.le = le
	g.tracked = append(g.tracked, code+"/"+tn)
	if g.err != nil {
		return carrier.Tracking{}, g.err
	}
	return carrier.Tracking{Carrier: code, TrackingNumber: tn, Status: g.timeline[len(g.timeline)-1].Status, Events: g.timeline}, nil
}

const carrierBody = `"shipFrom":{"street1":"1 Dock Rd","city":"Columbus","postalCode":"43004","country":"US"},` +
	`"shipTo":{"street1":"200 Main St","city":"Austin","postalCode":"73301","country":"US"},` +
	`"parcels":[{"weightKg":"2.5","lengthCm":"30","widthCm":"20","heightCm":"10"}]`

func carrierServer(t *testing.T, fs *fakeStore, g carrier.Gateway) http.Handler {
	t.Helper()
	r := httpx.NewRouter()
	New(fs, Options{Carrier: g}).Routes(r)
	return r
}

var shipDuty = map[string]string{"X-Passage-Duties": "WarehouseShip"}

func shipKey(key string) map[string]string {
	return map[string]string{"X-Passage-Duties": "WarehouseShip", "Idempotency-Key": key}
}

func TestCarrierLabel_BuysOnceAndRecordsLabelled(t *testing.T) {
	le, code := ids.New(), "sandbox"
	fs := &fakeStore{}
	fs.shipment.LegalEntityID, fs.shipment.ShipmentNumber, fs.shipment.CarrierCode, fs.shipment.Status = le, "SH-000123", &code, domain.StatusPacked
	g := &fakeGateway{}
	h := carrierServer(t, fs, g)
	id := ids.New()
	path := "/v1/fulfillment/shipments/" + id + ":carrier-label"

	if rec := do(t, h, http.MethodPost, path, `{"service":"GROUND",`+carrierBody+`}`, shipDuty); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), CodeIdemMissing) {
		t.Fatalf("no key: %d %s", rec.Code, rec.Body)
	}
	rec := do(t, h, http.MethodPost, path, `{"service":"GROUND",`+carrierBody+`}`, shipKey("k1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("label: %d %s", rec.Code, rec.Body)
	}
	var out carrierLabelResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.TrackingNumber != "SBX1" || out.TrackingEvent.Status != "LABELLED" || out.TrackingEvent.EventKey != "label:SBX1" || out.TrackingEvent.Provider != "sandbox" {
		t.Fatalf("response %+v", out)
	}
	if g.le != le || g.keys[0] != id+":k1" || g.orders[0].Carrier != "sandbox" || g.orders[0].Reference != "SH-000123" || g.orders[0].ShipTo.City != "Austin" {
		t.Fatalf("gateway saw le=%s keys=%v orders=%+v", g.le, g.keys, g.orders)
	}
	if rec := do(t, h, http.MethodPost, path, `{"service":"GROUND",`+carrierBody+`}`, shipKey("k1")); rec.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body)
	}
	if len(g.labels) != 1 || len(fs.workbench.tracked) != 2 || fs.workbench.tracked[1].EventKey != "label:SBX1" {
		t.Fatalf("labels %d tracked %+v", len(g.labels), fs.workbench.tracked)
	}

	fs.shipment.CarrierCode = nil
	if rec := do(t, h, http.MethodPost, path, `{"service":"GROUND",`+carrierBody+`}`, shipKey("k2")); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"carrier"`) {
		t.Fatalf("no carrier: %d %s", rec.Code, rec.Body)
	}
	fs.shipment.Status = domain.StatusCancelled
	if rec := do(t, h, http.MethodPost, path, `{"carrier":"sandbox","service":"GROUND",`+carrierBody+`}`, shipKey("k3")); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), CodeCarrierShipment) {
		t.Fatalf("cancelled: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, http.MethodPost, path, `{"service":"GROUND","bogus":1,`+carrierBody+`}`, shipKey("k4")); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
}

func TestCarrierRates_PassesTheShipmentsEntityAndMapsErrors(t *testing.T) {
	le := ids.New()
	fs := &fakeStore{}
	fs.shipment.LegalEntityID = le
	g := &fakeGateway{}
	h := carrierServer(t, fs, g)
	path := "/v1/fulfillment/shipments/" + ids.New() + ":carrier-rates"
	rec := do(t, h, http.MethodPost, path, `{"services":["GROUND"],`+carrierBody+`}`, shipDuty)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"amount":"8.63"`) || g.le != le {
		t.Fatalf("rates: %d %s le=%s", rec.Code, rec.Body, g.le)
	}
	for name, c := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"field errors": {&carrier.Error{Status: 400, Code: "CGW-0400", Errors: map[string]string{"parcels[0].weightKg": "bad"}}, 400, "parcels[0].weightKg"},
		"not served":   {&carrier.Error{Status: 422, Code: "CGW-0103", Detail: "sandbox does not serve BR"}, 422, "CGW-0103"},
		"auth fault":   {&carrier.Error{Status: 401, Code: "CGW-0401"}, 502, CodeCarrierUnavailable},
		"outage":       {fmt.Errorf("%w: status 503", carrier.ErrUnavailable), 503, CodeCarrierUnavailable},
	} {
		g.err = c.err
		rec := do(t, h, http.MethodPost, path, `{`+carrierBody+`}`, shipDuty)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.code) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := do(t, carrierServer(t, fs, nil), http.MethodPost, path, `{`+carrierBody+`}`, shipDuty); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), CodeCarrierUnconfigured) {
		t.Fatalf("unconfigured: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, http.MethodPost, path, `{`+carrierBody+`}`, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("without WarehouseShip: %d", rec.Code)
	}
}

func TestCarrierTrack_RecordsNewEventsOfTheNewestLabel(t *testing.T) {
	fs := &fakeStore{}
	fs.shipment.LegalEntityID = ids.New()
	t0 := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	g := &fakeGateway{timeline: []carrier.TrackingEvent{
		{EventKey: "SBX1:1", Status: "LABELLED", Description: "Label created", EventTime: t0},
		{EventKey: "SBX1:2", Status: "IN_TRANSIT", Description: "Picked up", EventTime: t0.Add(4 * time.Hour)},
		{EventKey: "SBX1:x", Status: "WEIRD", Description: "unknown status is skipped", EventTime: t0.Add(5 * time.Hour)},
		{EventKey: "SBX1:3", Status: "DELIVERED", Description: "Delivered", EventTime: t0.Add(72 * time.Hour)},
	}}
	h := carrierServer(t, fs, g)
	path := "/v1/fulfillment/shipments/" + ids.New() + ":carrier-track"
	if rec := do(t, h, http.MethodPost, path, `{}`, shipDuty); rec.Code != http.StatusConflict {
		t.Fatalf("nothing to track: %d %s", rec.Code, rec.Body)
	}
	fs.workbench.tracked = append(fs.workbench.tracked, fakeTracked("sandbox", "SBX1"))
	rec := do(t, h, http.MethodPost, path, `{}`, shipDuty)
	if rec.Code != http.StatusOK {
		t.Fatalf("track: %d %s", rec.Code, rec.Body)
	}
	var out carrierTrackResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Recorded != 3 || len(out.Events) != 3 || out.Status != "DELIVERED" || g.tracked[0] != "sandbox/SBX1" {
		t.Fatalf("out %+v tracked %v", out, g.tracked)
	}
	fs.workbench.duplicate = true
	rec = do(t, h, http.MethodPost, path, `{"carrier":"sandbox","trackingNumber":"SBX1"}`, shipDuty)
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Recorded != 0 {
		t.Fatalf("second pull recorded %d (%v)", out.Recorded, err)
	}
	if rec := do(t, h, http.MethodPost, path, `{"trackingNumber":"SBX1"}`, shipDuty); rec.Code != http.StatusBadRequest {
		t.Fatalf("number without carrier: %d", rec.Code)
	}
}
