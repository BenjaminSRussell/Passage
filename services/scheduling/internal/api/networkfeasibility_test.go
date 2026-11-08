package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func (f *fakeStore) NetworkFeasibility(_ context.Context, nf store.NetworkFeasibilityFilter) (store.NetworkFeasibility, error) {
	f.lastNetFind = nf
	if f.err != nil {
		return store.NetworkFeasibility{}, f.err
	}
	return store.NetworkFeasibility{Items: []store.NetworkFinding{{RunID: "r1", Code: "SCHED-0009", Flag: "NETWORK_INFEASIBLE", OperationID: "o1"}},
		Truncated: true, Infeasible: 4, InfeasibleOrders: 3, WaitViolations: 2}, nil
}

func TestNetworkFeasibility_DefaultsPassFiltersAndRefusesBadInput(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	rec := do(h, http.MethodGet, "/v1/scheduling/network-feasibility", "", "scheduling.read", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"flag":"NETWORK_INFEASIBLE"`) || !strings.Contains(rec.Body.String(), `"waitViolations":2`) || !strings.Contains(rec.Body.String(), `"infeasibleOrders":3`) ||
		!strings.Contains(rec.Body.String(), `"truncated":true`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if f.lastNetFind.Limit != 100 || f.lastNetFind.NetworkID != "" || f.lastNetFind.From != nil || f.lastNetFind.To != nil {
		t.Fatalf("defaults = %+v", f.lastNetFind)
	}
	do(h, http.MethodGet, "/v1/scheduling/network-feasibility?networkId="+strings.ToUpper(resID)+"&from=2027-01-04T00:00:00Z&to=2027-01-11T00:00:00Z&limit=5", "", "scheduling.read", "")
	n := f.lastNetFind
	if n.NetworkID != resID || n.Limit != 5 || n.From == nil || n.To == nil || n.To.Sub(*n.From).Hours() != 168 {
		t.Fatalf("filter = %+v", n)
	}
	for _, q := range []string{"?networkId=x", "?from=yesterday", "?to=2027-13-01", "?limit=0", "?limit=501", "?limit=x",
		"?from=2027-01-11T00:00:00Z&to=2027-01-04T00:00:00Z"} {
		if rec := do(h, http.MethodGet, "/v1/scheduling/network-feasibility"+q, "", "scheduling.read", ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", q, rec.Code)
		}
	}
	if rec := do(h, http.MethodGet, "/v1/scheduling/network-feasibility", "", "other.duty", ""); rec.Code != http.StatusForbidden {
		t.Errorf("without the read duty: status %d", rec.Code)
	}
}
