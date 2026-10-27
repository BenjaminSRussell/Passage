package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func (f *fakeStore) BlockUtilization(_ context.Context, id string, from, to time.Time) ([]store.BlockWindow, error) {
	f.lastUseID, f.lastUseFrom, f.lastUseTo = id, from, to
	if f.err != nil {
		return nil, f.err
	}
	return []store.BlockWindow{{BlockID: "b1", Code: "MON-WHITE", Priority: 10,
		Condition: []domain.Predicate{{Attribute: "color", Op: "=", Value: "white"}},
		Start:     from, End: from.Add(4 * time.Hour), BlockMinutes: 240, MatchedMinutes: 120, UnusedMinutes: 120}}, nil
}

func TestBlockUtilization_PassesTheRangeInUTCAndReturnsWindows(t *testing.T) {
	f := &fakeStore{}
	path := "/v1/scheduling/resources/" + strings.ToUpper(resID) + "/block-utilization?from=2027-01-04T09:00:00%2B01:00&to=2027-01-11T00:00:00Z"
	rec := do(newRouter(f), http.MethodGet, path, "", "scheduling.read", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if f.lastUseID != resID || !f.lastUseFrom.Equal(time.Date(2027, 1, 4, 8, 0, 0, 0, time.UTC)) || f.lastUseFrom.Location() != time.UTC {
		t.Fatalf("store got id %q from %v", f.lastUseID, f.lastUseFrom)
	}
	var body struct {
		Items []store.BlockWindow `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].Code != "MON-WHITE" || body.Items[0].UnusedMinutes != 120 || body.Items[0].Condition[0].Value != "white" {
		t.Fatalf("items = %+v", body.Items)
	}
}

func TestBlockUtilization_RefusesBadRangesAndIDs(t *testing.T) {
	h := newRouter(&fakeStore{})
	base := "/v1/scheduling/resources/" + resID + "/block-utilization"
	for name, q := range map[string]string{
		"missing to":     "?from=2027-01-04T00:00:00Z",
		"not RFC 3339":   "?from=2027-01-04&to=2027-01-05",
		"to before from": "?from=2027-01-05T00:00:00Z&to=2027-01-04T00:00:00Z",
		"over 62 days":   "?from=2027-01-01T00:00:00Z&to=2027-03-05T00:00:00Z",
	} {
		rec := do(h, http.MethodGet, base+q, "", "scheduling.read", "")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"from"`) {
			t.Errorf("%s: status %d body %s", name, rec.Code, rec.Body)
		}
	}
	rec := do(h, http.MethodGet, "/v1/scheduling/resources/not-a-uuid/block-utilization?from=2027-01-04T00:00:00Z&to=2027-01-05T00:00:00Z", "", "scheduling.read", "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeResourceNotFound) {
		t.Fatalf("bad id: status %d body %s", rec.Code, rec.Body)
	}
	missing := &fakeStore{err: httpx.NewProblem(http.StatusNotFound, store.CodeResourceNotFound, "resource not found")}
	rec = do(newRouter(missing), http.MethodGet, base+"?from=2027-01-04T00:00:00Z&to=2027-01-05T00:00:00Z", "", "scheduling.read", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown resource: status %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, base+"?from=2027-01-04T00:00:00Z&to=2027-01-05T00:00:00Z", "", "scheduling.run", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("without scheduling.read: status %d", rec.Code)
	}
}
