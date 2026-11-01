package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func (f *fakeStore) ImportProductionOrder(_ context.Context, _ store.OrderImportRequest) (store.OrderImport, error) {
	return store.OrderImport{}, f.err
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
