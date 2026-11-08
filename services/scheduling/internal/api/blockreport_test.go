package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func (f *fakeStore) BlockReport(_ context.Context, id string, from, to time.Time) (store.BlockReport, error) {
	f.lastReportID, f.lastUseFrom, f.lastUseTo = id, from, to
	if f.err != nil {
		return store.BlockReport{}, f.err
	}
	p := 50
	return store.BlockReport{Items: []store.BlockUsage{{ResourceID: resID, ResourceCode: "PAINT-1", BlockID: "b1", Code: "MON-WHITE", Windows: 1,
		BlockMinutes: 240, MatchedMinutes: 120, UnusedMinutes: 120, UtilizationPct: &p}}, BlockMinutes: 240, MatchedMinutes: 120, UnusedMinutes: 120, UtilizationPct: &p}, nil
}

func TestBlockReport_PassesTheFilterInUTCAndReturnsTotals(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	rec := do(h, http.MethodGet, "/v1/scheduling/block-utilization?from=2027-01-04T09:00:00%2B01:00&to=2027-01-11T00:00:00Z", "", "scheduling.read", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"utilizationPct":50`) || !strings.Contains(rec.Body.String(), `"resourceCode":"PAINT-1"`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if f.lastReportID != "" || !f.lastUseFrom.Equal(time.Date(2027, 1, 4, 8, 0, 0, 0, time.UTC)) || f.lastUseFrom.Location() != time.UTC {
		t.Fatalf("store got id %q from %v", f.lastReportID, f.lastUseFrom)
	}
	do(h, http.MethodGet, "/v1/scheduling/block-utilization?resourceId="+strings.ToUpper(resID)+"&from=2027-01-04T00:00:00Z&to=2027-01-05T00:00:00Z", "", "scheduling.read", "")
	if f.lastReportID != resID {
		t.Fatalf("resource filter = %q", f.lastReportID)
	}
}

func TestBlockReport_RefusesBadInputAndMapsStoreProblems(t *testing.T) {
	h := newRouter(&fakeStore{})
	base := "/v1/scheduling/block-utilization"
	for name, q := range map[string]string{
		"missing to":     "?from=2027-01-04T00:00:00Z",
		"not RFC 3339":   "?from=2027-01-04&to=2027-01-05",
		"to before from": "?from=2027-01-05T00:00:00Z&to=2027-01-04T00:00:00Z",
		"over 62 days":   "?from=2027-01-01T00:00:00Z&to=2027-03-05T00:00:00Z",
		"bad resource":   "?resourceId=x&from=2027-01-04T00:00:00Z&to=2027-01-05T00:00:00Z",
	} {
		if rec := do(h, http.MethodGet, base+q, "", "scheduling.read", ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d body %s", name, rec.Code, rec.Body)
		}
	}
	ok := "?from=2027-01-04T00:00:00Z&to=2027-01-05T00:00:00Z"
	missing := &fakeStore{err: httpx.NewProblem(http.StatusNotFound, store.CodeResourceNotFound, "resource not found")}
	if rec := do(newRouter(missing), http.MethodGet, base+ok+"&resourceId="+resID, "", "scheduling.read", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown resource: status %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, base+ok, "", "scheduling.run", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("without scheduling.read: status %d", rec.Code)
	}
}
