package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func (f *fakeStore) ImportProductionOrder(_ context.Context, _ store.OrderImportRequest) (store.OrderImport, error) {
	return store.OrderImport{}, f.err
}

func (f *fakeStore) ReimportProductionOrder(_ context.Context, id string) (store.OrderImport, error) {
	if f.err != nil {
		return store.OrderImport{}, f.err
	}
	f.reimported = id
	return store.OrderImport{OrderID: id, OrderNumber: "PO-7", Status: store.ImportImported, Skipped: []domain.SkippedOperation{}}, nil
}

func (f *fakeStore) WithdrawProductionOrder(_ context.Context, _ store.OrderWithdrawal) (store.OrderImport, bool, error) {
	return store.OrderImport{}, false, f.err
}

func (f *fakeStore) ListOrderImports(_ context.Context, lf store.OrderImportFilter) ([]store.OrderImport, error) {
	f.lastImports = lf
	if f.err != nil {
		return nil, f.err
	}
	return []store.OrderImport{{OrderID: resID, OrderNumber: "PO-7", Status: store.ImportPartial, Skipped: []domain.SkippedOperation{{OperNumber: 30, Code: domain.SkipUnknownResource, Detail: "x"}}}}, nil
}

func (f *fakeStore) GetOrderImport(_ context.Context, id string) (store.OrderImport, error) {
	if f.err != nil {
		return store.OrderImport{}, f.err
	}
	return store.OrderImport{OrderID: id, OrderNumber: "PO-7", Status: store.ImportImported, Skipped: []domain.SkippedOperation{}}, nil
}

func TestOrderImports_ListFiltersAndNeedsReadDuty(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	rec := do(h, http.MethodGet, "/v1/scheduling/order-imports", "", "scheduling.read", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"orderNumber":"PO-7"`) || !strings.Contains(rec.Body.String(), `"unknown-resource"`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if f.lastImports.Limit != 50 || f.lastImports.Status != "" {
		t.Fatalf("defaults = %+v", f.lastImports)
	}
	do(h, http.MethodGet, "/v1/scheduling/order-imports?status=rejected&limit=5", "", "scheduling.read", "")
	if f.lastImports.Status != "rejected" || f.lastImports.Limit != 5 {
		t.Fatalf("filter = %+v", f.lastImports)
	}
	for _, q := range []string{"?status=bogus", "?limit=0", "?limit=201", "?limit=x"} {
		if rec := do(h, http.MethodGet, "/v1/scheduling/order-imports"+q, "", "scheduling.read", ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", q, rec.Code)
		}
	}
	if rec := do(h, http.MethodGet, "/v1/scheduling/order-imports", "", "other.duty", ""); rec.Code != http.StatusForbidden {
		t.Errorf("without the read duty: status %d", rec.Code)
	}
}

func TestOrderImports_GetByOrderId(t *testing.T) {
	h := newRouter(&fakeStore{})
	rec := do(h, http.MethodGet, "/v1/scheduling/order-imports/"+strings.ToUpper(resID), "", "scheduling.read", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"orderId":"`+resID+`"`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodGet, "/v1/scheduling/order-imports/nope", "", "scheduling.read", ""); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: status %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/v1/scheduling/order-imports/"+resID, "", "other.duty", ""); rec.Code != http.StatusForbidden {
		t.Errorf("without the read duty: status %d", rec.Code)
	}
}

func TestOrderImports_ReimportNeedsScheduleDutyAndValidId(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	rec := do(h, http.MethodPost, "/v1/scheduling/order-imports/"+strings.ToUpper(resID)+":reimport", "", "scheduling.schedule.write", "")
	if rec.Code != http.StatusOK || f.reimported != resID || !strings.Contains(rec.Body.String(), `"orderId":"`+resID+`"`) {
		t.Fatalf("status %d reimported %q body %s", rec.Code, f.reimported, rec.Body)
	}
	if rec := do(h, http.MethodPost, "/v1/scheduling/order-imports/nope:reimport", "", "scheduling.schedule.write", ""); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: status %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, "/v1/scheduling/order-imports/"+resID+":reimport", "", "scheduling.read", ""); rec.Code != http.StatusForbidden {
		t.Errorf("read duty only: status %d", rec.Code)
	}
	f.err = httpx.NewProblem(http.StatusConflict, store.CodeOrderNotReimportable, "not kept")
	if rec := do(h, http.MethodPost, "/v1/scheduling/order-imports/"+resID+":reimport", "", "scheduling.schedule.write", ""); rec.Code != http.StatusConflict {
		t.Errorf("not kept: status %d", rec.Code)
	}
}

func TestResources_EngineeringMappingValidated(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	bad := `{"engineeringResourceId":"not-a-uuid"}`
	rec := patchResource(h, resourcePath, bad, allDuties, "k-eng", `"1"`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"engineeringResourceId"`) {
		t.Fatalf("bad id: %d %s", rec.Code, rec.Body)
	}
	rec = patchResource(h, resourcePath, `{"engineeringResourceId":"`+strings.ToUpper(resID)+`"}`, allDuties, "k-eng2", `"1"`)
	if rec.Code != http.StatusOK || f.lastPatch.EngineeringResourceID == nil || *f.lastPatch.EngineeringResourceID != resID {
		t.Fatalf("set: %d %+v", rec.Code, f.lastPatch)
	}
	rec = patchResource(h, resourcePath, `{"engineeringResourceId":""}`, allDuties, "k-eng3", `"1"`)
	if rec.Code != http.StatusOK || f.lastPatch.EngineeringResourceID == nil || *f.lastPatch.EngineeringResourceID != "" {
		t.Fatalf("clear: %d %+v", rec.Code, f.lastPatch)
	}
}
