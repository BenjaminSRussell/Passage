package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/store"
)

func (f *fakeStore) ConfirmShipped(_ context.Context, idem store.Idem, c store.ShipConfirmation) (store.ShipConfirmation, bool, error) {
	f.calls++
	f.idem, f.ship = idem, c
	return c, f.replayed, f.err
}

func (f *fakeStore) GetShipConfirmation(_ context.Context, id string) (store.ShipConfirmation, error) {
	f.calls++
	return store.ShipConfirmation{ShipmentID: id}, f.err
}

const shipLE = "0190a5f2-7b1c-7d3e-8a4f-000000000e11"

func ship(h *httptest.ResponseRecorder, api *API, path, duties, le, body string) *httptest.ResponseRecorder {
	router := httpx.NewRouter()
	api.Register(router)
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	r.Header.Set("X-Passage-User", "u1")
	r.Header.Set("X-Passage-Duties", duties)
	if le != "" {
		r.Header.Set(tenant.HeaderLegalEntity, le)
	}
	r.Header.Set("Idempotency-Key", "k1")
	router.ServeHTTP(h, r)
	return h
}

func TestConfirmShipped_RecordsTheConfirmationInTheCallersLegalEntity(t *testing.T) {
	fs := &fakeStore{}
	sh, wh := ids.New(), ids.New()
	body := `{"warehouseId":"` + wh + `","carrierCode":"UPSN","shippedAt":"2026-11-10T08:30:00Z","ssccs":["376104250000000017"]}`
	rec := ship(httptest.NewRecorder(), NewWithStore(fs, time.Minute), "/v1/warehouse/shipments/"+sh+":ship", dutyShipConfirm, shipLE, body)
	if rec.Code != 201 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if fs.ship.ShipmentID != sh || fs.ship.LegalEntityID != shipLE || fs.ship.WarehouseID != wh || fs.ship.CarrierCode != "UPSN" ||
		!fs.ship.ShippedAt.Equal(time.Date(2026, 11, 10, 8, 30, 0, 0, time.UTC)) || len(fs.ship.SSCCs) != 1 || fs.ship.ConfirmedBy != "u1" {
		t.Fatalf("store got %+v", fs.ship)
	}
}

func TestConfirmShipped_Refusals(t *testing.T) {
	sh, wh := ids.New(), ids.New()
	path := "/v1/warehouse/shipments/" + sh + ":ship"
	future := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	cases := []struct {
		name, path, duties, le, body string
		status                       int
		field                        string
	}{
		{"needs the ship-confirm duty", path, dutyReceive, shipLE, `{"warehouseId":"` + wh + `"}`, 403, ""},
		{"shipment must be a uuid", "/v1/warehouse/shipments/nope:ship", dutyShipConfirm, shipLE, `{"warehouseId":"` + wh + `"}`, 400, "id"},
		{"warehouse must be a uuid", path, dutyShipConfirm, shipLE, `{"warehouseId":"w1"}`, 400, "warehouseId"},
		{"legal entity outside the caller's scope", path, dutyShipConfirm, shipLE, `{"warehouseId":"` + wh + `","legalEntityId":"` + ids.New() + `"}`, 400, "legalEntityId"},
		{"a caller with no legal entity must name one", path, dutyShipConfirm, "", `{"warehouseId":"` + wh + `"}`, 400, "legalEntityId"},
		{"carrier is a SCAC", path, dutyShipConfirm, shipLE, `{"warehouseId":"` + wh + `","carrierCode":"ups"}`, 400, "carrierCode"},
		{"not in the future", path, dutyShipConfirm, shipLE, `{"warehouseId":"` + wh + `","shippedAt":"` + future + `"}`, 400, "shippedAt"},
		{"SSCC check digit", path, dutyShipConfirm, shipLE, `{"warehouseId":"` + wh + `","ssccs":["376104250000000016"]}`, 400, "ssccs[0]"},
		{"SSCC listed twice", path, dutyShipConfirm, shipLE, `{"warehouseId":"` + wh + `","ssccs":["376104250000000017","376104250000000017"]}`, 400, "ssccs[1]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := &fakeStore{}
			rec := ship(httptest.NewRecorder(), NewWithStore(fs, time.Minute), c.path, c.duties, c.le, c.body)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.status, rec.Body)
			}
			if fs.calls != 0 {
				t.Fatal("a refused request reached the store")
			}
			if c.field != "" {
				var p struct {
					Errors map[string]string `json:"errors"`
				}
				_ = json.Unmarshal(rec.Body.Bytes(), &p)
				if _, ok := p.Errors[c.field]; !ok {
					t.Fatalf("no error for %s: %s", c.field, rec.Body)
				}
			}
		})
	}
}

func TestConfirmShipped_SecondConfirmationIsADuplicate(t *testing.T) {
	fs := &fakeStore{err: store.ErrConflict}
	rec := ship(httptest.NewRecorder(), NewWithStore(fs, time.Minute), "/v1/warehouse/shipments/"+ids.New()+":ship", dutyShipConfirm, shipLE, `{"warehouseId":"`+ids.New()+`"}`)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), codeDuplicate) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}
