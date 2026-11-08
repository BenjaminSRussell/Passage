package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

var (
	fakeOp      store.OperationRule
	savedOpIn   store.OperationRuleInput
	savedOpBase int
)

func (f *fakeStore) ListOperationRules(context.Context) ([]store.OperationRule, error) {
	return []store.OperationRule{{WarehouseID: ids.New(), PostingDateSource: "MANUAL", TimeZone: "UTC", AutoPackEnabled: true, WorkbenchGroupKeyDefault: "CUSTOMER"}}, nil
}

func (f *fakeStore) GetOperationRule(_ context.Context, id string) (store.OperationRule, error) {
	if fakeOp.Version > 0 {
		r := fakeOp
		r.WarehouseID = id
		return r, nil
	}
	return store.OperationRule{WarehouseID: id, PostingDateSource: "MANUAL", TimeZone: "UTC", AutoPackEnabled: true, WorkbenchGroupKeyDefault: "CUSTOMER"}, nil
}

func (f *fakeStore) SaveOperationRule(_ context.Context, id, le string, version int, in store.OperationRuleInput, idem store.Idem) (store.OperationRule, error) {
	f.lastIdem = idem
	savedOpIn, savedOpBase = in, version
	if version != 1 {
		return store.OperationRule{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeSettingVersion, "stale")
	}
	return store.OperationRule{WarehouseID: id, LegalEntityID: &le, PostingDateSource: in.PostingDateSource, TimeZone: in.TimeZone,
		AutoPackEnabled: in.AutoPackEnabled, WorkbenchGroupKeyDefault: in.WorkbenchGroupKeyDefault, Version: 2}, nil
}

func TestOperationRule_ReadSaveValidateAndDuty(t *testing.T) {
	h := newServer(t, &fakeStore{})
	wh, le := ids.New(), ids.New()
	path := "/v1/fulfillment/operation-rules/" + wh
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/operation-rules", "", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"postingDateSource":"MANUAL"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, path, "", nil); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"0"` {
		t.Fatalf("get: %d %q", rec.Code, rec.Header().Get("ETag"))
	}
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/operation-rules/nope", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}
	body := func(src, zone, extra string) string {
		return `{"legalEntityId":"` + le + `","postingDateSource":"` + src + `","timeZone":"` + zone + `","autoPackEnabled":false,"workbenchGroupKeyDefault":"ZONE"` + extra + `}`
	}
	headers := map[string]string{"If-Match": `"1"`, "Idempotency-Key": "k1"}
	rec := do(t, h, http.MethodPut, path, body("SHIP_TIMESTAMP", "Europe/Berlin", ""), headers)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` || savedOpBase != 1 ||
		savedOpIn != (store.OperationRuleInput{PostingDateSource: "SHIP_TIMESTAMP", TimeZone: "Europe/Berlin", AutoPackEnabled: false, WorkbenchGroupKeyDefault: "ZONE"}) {
		t.Fatalf("save: %d %s %+v", rec.Code, rec.Body.String(), savedOpIn)
	}
	if rec := do(t, h, http.MethodPut, path, body("SHIP_TIMESTAMP", "", ""), headers); rec.Code != http.StatusOK || savedOpIn.TimeZone != "UTC" {
		t.Fatalf("empty zone should save as UTC: %d %+v", rec.Code, savedOpIn)
	}
	for name, b := range map[string]string{
		"source":   body("SOMETIME", "UTC", ""),
		"zone":     body("TODAY", "Mars/Olympus", ""),
		"group":    strings.Replace(body("TODAY", "UTC", ""), "ZONE", "COLOUR", 1),
		"autopack": strings.Replace(body("TODAY", "UTC", ""), `"autoPackEnabled":false,`, "", 1),
		"entity":   strings.Replace(body("TODAY", "UTC", ""), le, "nope", 1),
	} {
		if rec := do(t, h, http.MethodPut, path, b, headers); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	if rec := do(t, h, http.MethodPut, path, body("TODAY", "UTC", ""), map[string]string{"Idempotency-Key": "k"}); rec.Code != http.StatusPreconditionRequired {
		t.Errorf("missing If-Match: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPut, path, body("TODAY", "UTC", ""), map[string]string{"If-Match": `"0"`, "Idempotency-Key": "k"}); rec.Code != http.StatusPreconditionFailed {
		t.Errorf("stale: %d", rec.Code)
	}
}

func TestOperationRule_Duties(t *testing.T) {
	h := newServer(t, &fakeStore{})
	path := "/v1/fulfillment/operation-rules/" + ids.New()
	b := `{"legalEntityId":"` + ids.New() + `","postingDateSource":"TODAY","autoPackEnabled":true,"workbenchGroupKeyDefault":"CARRIER"}`
	if rec := do(t, h, http.MethodPut, path, b, map[string]string{"If-Match": `"1"`, "Idempotency-Key": "d", "X-Passage-Duties": "fulfillment.warehouse.view"}); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer save: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, path, "", map[string]string{"X-Passage-Duties": "fulfillment.warehouse.pick"}); rec.Code != http.StatusForbidden {
		t.Fatalf("picker read: %d", rec.Code)
	}
}

// A workbench call that names no groupBy takes the warehouse's saved default; one that names it wins.
func TestWorkbench_GroupKeyDefaultFromOperationRule(t *testing.T) {
	defer func() { fakeOp = store.OperationRule{} }()
	fakeOp = store.OperationRule{Version: 1, PostingDateSource: "MANUAL", TimeZone: "UTC", AutoPackEnabled: true, WorkbenchGroupKeyDefault: "CARRIER"}
	fs := &fakeStore{}
	h := newServer(t, fs)
	path := "/v1/fulfillment/waves:preview"
	if rec := do(t, h, http.MethodPost, path, `{"warehouseId":"`+wbWarehouse+`"}`, nil); rec.Code != http.StatusOK || fs.workbench.waveReq.GroupBy != "CARRIER" {
		t.Fatalf("default group: %d %q", rec.Code, fs.workbench.waveReq.GroupBy)
	}
	if rec := do(t, h, http.MethodPost, path, `{"warehouseId":"`+wbWarehouse+`","groupBy":"PRODUCT"}`, nil); rec.Code != http.StatusOK || fs.workbench.waveReq.GroupBy != "PRODUCT" {
		t.Fatalf("named group: %d %q", rec.Code, fs.workbench.waveReq.GroupBy)
	}
}
