package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func (f *fakeStore) ListWarehouseSettings(context.Context) ([]store.WarehouseSetting, error) {
	return []store.WarehouseSetting{{WarehouseID: ids.New(), ShortPickAction: domain.ActionKeepOpen}}, nil
}

func (f *fakeStore) GetWarehouseSetting(_ context.Context, id string) (store.WarehouseSetting, error) {
	return store.WarehouseSetting{WarehouseID: id, ShortPickAction: domain.ActionBackorder, Version: 2}, nil
}

var savedLegalEntity string

func (f *fakeStore) SaveWarehouseSetting(_ context.Context, id, legalEntityID string, version int, action domain.ShortPickAction, idem store.Idem) (store.WarehouseSetting, error) {
	f.lastIdem = idem
	savedLegalEntity = legalEntityID
	if version != 2 {
		return store.WarehouseSetting{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeSettingVersion, "stale")
	}
	return store.WarehouseSetting{WarehouseID: id, LegalEntityID: &legalEntityID, ShortPickAction: action, Version: 3}, nil
}

var backorderDate *time.Time

func (f *fakeStore) BackorderRemainder(_ context.Context, id string, version int, date *time.Time, idem store.Idem) (store.BackorderResult, error) {
	f.lastIdem = idem
	backorderDate = date
	if version != 3 {
		return store.BackorderResult{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersion, "shipment version changed")
	}
	return store.BackorderResult{Shipment: store.Shipment{ID: id, Version: 4, Status: domain.StatusShipped},
		Backorder: store.Shipment{ID: ids.New(), Version: 1, Status: domain.StatusReleased, BackorderOfID: &id}}, nil
}

func TestBackorderRemainder_RequiresKeyVersionBodyAndDuty(t *testing.T) {
	h := newServer(t, &fakeStore{})
	path := "/v1/fulfillment/shipments/" + ids.New() + ":backorder-remainder"
	if rec := do(t, h, http.MethodPost, path, "{}", map[string]string{"Idempotency-Key": "b-1"}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: status = %d, want 428", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, "{}", map[string]string{"If-Match": `"3"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: status = %d, want 400", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, "{}", map[string]string{"Idempotency-Key": "b-2", "If-Match": `"2"`}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale version: status = %d, want 412", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, `{"plannedShipDate":"next week"}`, map[string]string{"Idempotency-Key": "b-3", "If-Match": `"3"`}); rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
		t.Fatalf("bad date: status = %d, want a validation error", rec.Code)
	}
	rec := do(t, h, http.MethodPost, path, `{"plannedShipDate":"2026-12-01"}`, map[string]string{"Idempotency-Key": "b-4", "If-Match": `"3"`})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"backorder"`) || !strings.Contains(rec.Body.String(), `"backorderOfId"`) || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("status = %d etag = %q body = %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if backorderDate == nil || backorderDate.Format(dateLayout) != "2026-12-01" {
		t.Fatalf("planned ship date = %v", backorderDate)
	}
	rec = do(t, h, http.MethodPost, path, "{}", map[string]string{"Idempotency-Key": "b-5", "If-Match": `"3"`, "X-Passage-Duties": "WarehousePick"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("picker backorder: status = %d, want 403", rec.Code)
	}
	rec = do(t, h, http.MethodPost, path, "{}", map[string]string{"Idempotency-Key": "b-6", "If-Match": `"3"`})
	if rec.Code != http.StatusOK || backorderDate != nil {
		t.Fatalf("no date: status = %d date = %v", rec.Code, backorderDate)
	}
}

func TestWarehouseSetting_ReadSaveValidateAndDuty(t *testing.T) {
	h := newServer(t, &fakeStore{})
	wh := ids.New()
	path := "/v1/fulfillment/warehouse-settings/" + wh
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/warehouse-settings", "", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "KEEP_OPEN") {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, path, "", nil); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` || !strings.Contains(rec.Body.String(), "REMAINDER_BACKORDER") {
		t.Fatalf("get: %d %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/warehouse-settings/nope", "", nil); rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}
	le := ids.New()
	body := `{"shortPickAction":"CANCEL_REMAINDER","legalEntityId":"` + le + `"}`
	if rec := do(t, h, http.MethodPut, path, body, map[string]string{"Idempotency-Key": "s-1"}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPut, path, body, map[string]string{"If-Match": `"x"`, "Idempotency-Key": "s-1"}); rec.Code == http.StatusOK {
		t.Fatalf("bad If-Match accepted")
	}
	if rec := do(t, h, http.MethodPut, path, body, map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPut, path, `{"shortPickAction":"SHRUG","legalEntityId":"`+le+`"}`, map[string]string{"If-Match": `"2"`, "Idempotency-Key": "s-2"}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "shortPickAction") {
		t.Fatalf("bad action: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPut, path, `{"shortPickAction":"BACKORDER","legalEntityId":"`+le+`"}`, map[string]string{"If-Match": `"2"`, "Idempotency-Key": "s-2b"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("pre-PRD code BACKORDER accepted: %d", rec.Code)
	}
	for _, bad := range []string{`{"shortPickAction":"CANCEL_REMAINDER"}`, `{"shortPickAction":"CANCEL_REMAINDER","legalEntityId":"le-1"}`} {
		if rec := do(t, h, http.MethodPut, path, bad, map[string]string{"If-Match": `"2"`, "Idempotency-Key": "s-le"}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "legalEntityId") {
			t.Fatalf("%s: %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	if rec := do(t, h, http.MethodPut, path, body, map[string]string{"If-Match": `"1"`, "Idempotency-Key": "s-3"}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale: %d", rec.Code)
	}
	rec := do(t, h, http.MethodPut, path, body, map[string]string{"If-Match": `"2"`, "Idempotency-Key": "s-4"})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` || !strings.Contains(rec.Body.String(), "CANCEL_REMAINDER") {
		t.Fatalf("save: %d %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if savedLegalEntity != le {
		t.Fatalf("legal entity passed to the store = %q, want %s", savedLegalEntity, le)
	}
	if rec := do(t, h, http.MethodPut, path, body, map[string]string{"If-Match": `"2"`, "Idempotency-Key": "s-5", "X-Passage-Duties": "WarehouseView"}); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer save: %d", rec.Code)
	}
}
