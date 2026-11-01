package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func (f *fakeStore) RunKPIs(_ context.Context, id string) (store.RunKPIs, error) {
	if f.err != nil {
		return store.RunKPIs{}, f.err
	}
	return store.RunKPIs{RunID: id, PlacedJobs: 3, LateOrders: 1, Resources: []store.ResourceKPI{{ResourceID: resID, ResourceCode: "M1", UtilizationPct: 44.4}}}, nil
}

func (f *fakeStore) LateOrders(_ context.Context, lf store.LateOrderFilter) ([]store.LateOrder, bool, error) {
	f.lastLate = lf
	if f.err != nil {
		return nil, false, f.err
	}
	return []store.LateOrder{{OrderID: "o1", LateMinutes: 90, LateOperations: 1, Operations: 2}}, true, nil
}

func TestRunKPIs_ReturnsMeasuresAndNeedsReadDuty(t *testing.T) {
	h := newRouter(&fakeStore{})
	rec := do(h, http.MethodGet, "/v1/scheduling/runs/"+strings.ToUpper(resID)+"/kpis", "", "scheduling.read", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"placedJobs":3`) || !strings.Contains(rec.Body.String(), `"utilizationPct":44.4`) ||
		!strings.Contains(rec.Body.String(), `"runId":"`+resID+`"`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodGet, "/v1/scheduling/runs/"+resID+"/kpis", "", "other.duty", ""); rec.Code != http.StatusForbidden {
		t.Errorf("without the read duty: status %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/v1/scheduling/runs/not-a-uuid/kpis", "", "scheduling.read", ""); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: status %d", rec.Code)
	}
}

func TestLateOrders_DefaultsPassFiltersAndRefusesBadInput(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	rec := do(h, http.MethodGet, "/v1/scheduling/late-orders", "", "scheduling.read", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"lateMinutes":90`) || !strings.Contains(rec.Body.String(), `"truncated":true`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if f.lastLate.MinLateMinutes != 1 || f.lastLate.Limit != 100 {
		t.Fatalf("defaults = %+v", f.lastLate)
	}
	do(h, http.MethodGet, "/v1/scheduling/late-orders?minLateMinutes=60&limit=5", "", "scheduling.read", "")
	if f.lastLate.MinLateMinutes != 60 || f.lastLate.Limit != 5 {
		t.Fatalf("filter = %+v", f.lastLate)
	}
	for _, q := range []string{"?minLateMinutes=0", "?minLateMinutes=x", "?limit=0", "?limit=501"} {
		if rec := do(h, http.MethodGet, "/v1/scheduling/late-orders"+q, "", "scheduling.read", ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", q, rec.Code)
		}
	}
	if rec := do(h, http.MethodGet, "/v1/scheduling/late-orders", "", "other.duty", ""); rec.Code != http.StatusForbidden {
		t.Errorf("without the read duty: status %d", rec.Code)
	}
}
