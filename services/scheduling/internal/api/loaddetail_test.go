package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func (f *fakeStore) DailyCapacityLoad(_ context.Context, f2 store.DailyLoadFilter) ([]domain.DayLoad, error) {
	f.lastDaily = f2
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	y, m, d := f2.From.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, tokyo)
	return []domain.DayLoad{{ResourceID: resID, Day: day, Start: day, End: day.AddDate(0, 0, 1), OpenMinutes: 480, BookedMinutes: 240, LoadPct: 50}}, f.err
}

func (f *fakeStore) LoadJobs(_ context.Context, id string, from, to time.Time) ([]store.LoadJob, bool, error) {
	f.lastUseID, f.lastUseFrom, f.lastUseTo = id, from, to
	if f.err != nil {
		return nil, false, f.err
	}
	return []store.LoadJob{{JobID: "j1", OrderID: "o1", OperNumber: 10, Segment: 1, Role: "primary", Start: from, End: from.Add(time.Hour),
		MinutesInRange: 60, Finite: true}}, true, nil
}

func TestCapacityLoad_DayPeriodReturnsDaysNeverFlagged(t *testing.T) {
	f := &fakeStore{}
	rec := do(newRouter(f), http.MethodGet, "/v1/scheduling/capacity-load?period=day&from=2027-01-05&days=7&resourceIds="+strings.ToUpper(resID), "", allDuties, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0]["period"] != "day" || body.Items[0]["periodStart"] != "2027-01-05" || body.Items[0]["loadPct"] != float64(50) {
		t.Fatalf("items = %+v", body.Items)
	}
	if body.Items[0]["start"] != "2027-01-04T15:00:00Z" || body.Items[0]["end"] != "2027-01-05T15:00:00Z" {
		t.Fatalf("a day item carries the UTC instants of its local day, the drill-down range: %+v", body.Items[0])
	}
	if _, has := body.Items[0]["weekStart"]; has {
		t.Fatalf("a day item must not carry weekStart: %+v", body.Items[0])
	}
	if body.Items[0]["bottleneck"] != false {
		t.Fatalf("a day item is never flagged a bottleneck: %+v", body.Items[0])
	}
	if f.lastDaily.Days != 7 || len(f.lastDaily.ResourceIDs) != 1 || f.lastDaily.ResourceIDs[0] != resID {
		t.Fatalf("filter = %+v", f.lastDaily)
	}
}

func TestCapacityLoad_WeekItemsCarryPeriodFields(t *testing.T) {
	rec := do(newRouter(&fakeStore{}), http.MethodGet, "/v1/scheduling/capacity-load?from=2027-01-05", "", allDuties, "")
	if !strings.Contains(rec.Body.String(), `"period":"week"`) || !strings.Contains(rec.Body.String(), `"periodStart":"2027-01-05"`) ||
		!strings.Contains(rec.Body.String(), `"weekStart":"2027-01-05"`) || !strings.Contains(rec.Body.String(), `"start":"2027-01-05T00:00:00Z"`) ||
		!strings.Contains(rec.Body.String(), `"end":"2027-01-12T00:00:00Z"`) {
		t.Fatalf("body %s", rec.Body)
	}
}

func TestCapacityLoad_RefusesBadPeriodAndDays(t *testing.T) {
	h := newRouter(&fakeStore{})
	for name, q := range map[string]string{
		"period":  "?period=month",
		"days 0":  "?period=day&days=0",
		"days 94": "?period=day&days=94",
	} {
		if rec := do(h, http.MethodGet, "/v1/scheduling/capacity-load"+q, "", allDuties, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d body %s", name, rec.Code, rec.Body)
		}
	}
}

func TestCapacityLoadJobs_ReturnsJobsInUTCAndRefusesBadInput(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	path := "/v1/scheduling/capacity-load/jobs?resourceId=" + strings.ToUpper(resID) + "&from=2027-01-04T09:00:00%2B01:00&to=2027-01-05T00:00:00Z"
	rec := do(h, http.MethodGet, path, "", "scheduling.read", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"truncated":true`) || !strings.Contains(rec.Body.String(), `"minutesInRange":60`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if f.lastUseID != resID || !f.lastUseFrom.Equal(time.Date(2027, 1, 4, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("store got %q %v", f.lastUseID, f.lastUseFrom)
	}
	for name, q := range map[string]string{
		"no resource":    "?from=2027-01-04T00:00:00Z&to=2027-01-05T00:00:00Z",
		"bad resource":   "?resourceId=x&from=2027-01-04T00:00:00Z&to=2027-01-05T00:00:00Z",
		"to before from": "?resourceId=" + resID + "&from=2027-01-05T00:00:00Z&to=2027-01-04T00:00:00Z",
		"over 93 days":   "?resourceId=" + resID + "&from=2027-01-01T00:00:00Z&to=2027-05-05T00:00:00Z",
	} {
		if rec := do(h, http.MethodGet, "/v1/scheduling/capacity-load/jobs"+q, "", "scheduling.read", ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d body %s", name, rec.Code, rec.Body)
		}
	}
	if rec := do(h, http.MethodGet, path, "", "other.duty", ""); rec.Code != http.StatusForbidden {
		t.Errorf("without the read duty: status %d", rec.Code)
	}
}
