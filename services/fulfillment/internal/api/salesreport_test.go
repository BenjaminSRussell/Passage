package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

type salesReportCalls struct {
	filter  store.SalesReportFilter
	retried string
	idem    store.Idem
}

func (f *fakeStore) ListSalesReports(_ context.Context, fl store.SalesReportFilter) ([]store.SalesReport, error) {
	f.salesReports.filter = fl
	return []store.SalesReport{{ID: "r-1", Status: store.SalesReportParked, CreatedAt: time.Now()}}, nil
}

func (f *fakeStore) RetrySalesReport(_ context.Context, id string, idem store.Idem) (store.SalesReport, error) {
	f.salesReports.retried, f.salesReports.idem = id, idem
	return store.SalesReport{ID: id, Status: store.SalesReportPending, CreatedAt: time.Now()}, nil
}

// The report list filters by status and slip; :retry needs an Idempotency-Key and
// fulfillment.packing-slip.post, the list fulfillment.warehouse.view (#496).
func TestSalesReports_ListAndRetry(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	slip := ids.New()
	rec := do(t, h, http.MethodGet, "/v1/fulfillment/sales-delivery-reports?status=PARKED&packingSlipId="+slip+"&limit=5", "", nil)
	if got := fs.salesReports.filter; rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "r-1") || got.Status != "PARKED" || got.PackingSlipID != slip || got.Limit != 5 {
		t.Fatalf("list: %d %s %+v", rec.Code, rec.Body.String(), got)
	}
	for _, q := range []string{"status=SENT", "packingSlipId=PS-1", "limit=0"} {
		if rec := do(t, h, http.MethodGet, "/v1/fulfillment/sales-delivery-reports?"+q, "", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/sales-delivery-reports", "", map[string]string{"X-Passage-Duties": "fulfillment.warehouse.pick"}); rec.Code != http.StatusForbidden {
		t.Fatalf("list without fulfillment.warehouse.view: %d", rec.Code)
	}

	id := ids.New()
	path := "/v1/fulfillment/sales-delivery-reports/" + id + ":retry"
	if rec := do(t, h, http.MethodPost, path, "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("retry without a key: %d", rec.Code)
	}
	rec = do(t, h, http.MethodPost, path, "", map[string]string{"Idempotency-Key": "retry-1"})
	if rec.Code != http.StatusOK || fs.salesReports.retried != id || fs.salesReports.idem.Key != "retry-1" || !strings.Contains(rec.Body.String(), "PENDING") {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/v1/fulfillment/sales-delivery-reports/nope:retry", "", map[string]string{"Idempotency-Key": "retry-2"}); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, "", map[string]string{"Idempotency-Key": "retry-3", "X-Passage-Duties": "fulfillment.warehouse.view"}); rec.Code != http.StatusForbidden {
		t.Fatalf("retry without fulfillment.packing-slip.post: %d", rec.Code)
	}
}
