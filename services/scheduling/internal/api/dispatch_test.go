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

func (f *fakeStore) DispatchRows(_ context.Context, id string, from, to time.Time) ([]domain.DispatchRow, bool, error) {
	f.lastUseID, f.lastUseFrom, f.lastUseTo = id, from, to
	if f.err != nil {
		return nil, false, f.err
	}
	return []domain.DispatchRow{
		{JobID: "j2", OrderID: "o2", OperNumber: 20, Segment: 1, Role: "primary", Start: from.Add(3 * time.Hour), End: from.Add(4 * time.Hour), Finite: true},
		{JobID: "j1", OrderID: "o1", OperNumber: 10, Segment: 1, Role: "primary", Start: from.Add(time.Hour), End: from.Add(2 * time.Hour), SetupGroup: "RED", ChangeoverMins: 15, Finite: true, Firmed: true},
	}, false, nil
}

func TestDispatchList_NumbersLinesAndUsesTheShiftWindow(t *testing.T) {
	f := &fakeStore{}
	rec := do(newRouter(f), http.MethodGet, "/v1/scheduling/dispatch-list?resourceId="+resID+"&date=2027-01-05&startMinute=1320&endMinute=1800", "", allDuties, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	var body struct {
		Resource     map[string]any   `json:"resource"`
		WindowStart  string           `json:"windowStart"`
		WindowEnd    string           `json:"windowEnd"`
		TotalMinutes int              `json:"totalMinutes"`
		Lines        []map[string]any `json:"lines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.WindowStart != "2027-01-05T22:00:00Z" || body.WindowEnd != "2027-01-06T06:00:00Z" {
		t.Fatalf("window = %s .. %s, want the night shift in UTC", body.WindowStart, body.WindowEnd)
	}
	if len(body.Lines) != 2 || body.Lines[0]["orderId"] != "o1" || body.Lines[0]["sequence"] != float64(1) || body.Lines[1]["sequence"] != float64(2) {
		t.Fatalf("lines = %+v", body.Lines)
	}
	if body.Lines[0]["setupGroup"] != "RED" || body.Lines[0]["changeoverMinutes"] != float64(15) || body.TotalMinutes != 120 || body.Resource["code"] != "MIX-1" {
		t.Fatalf("body = %+v", body)
	}
}

func TestDispatchList_RefusesBadInputAndNeedsTheReadDuty(t *testing.T) {
	base := "/v1/scheduling/dispatch-list?resourceId=" + resID + "&date=2027-01-05"
	for _, q := range []string{
		"/v1/scheduling/dispatch-list?date=2027-01-05",
		"/v1/scheduling/dispatch-list?resourceId=" + resID,
		base + "&days=8",
		base + "&startMinute=600&endMinute=600",
		base + "&startMinute=x",
	} {
		if rec := do(newRouter(&fakeStore{}), http.MethodGet, q, "", allDuties, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, rec.Code)
		}
	}
	if rec := do(newRouter(&fakeStore{}), http.MethodGet, base, "", "scheduling.run", ""); rec.Code != http.StatusForbidden {
		t.Errorf("without a duty: status %d, want 403", rec.Code)
	}
}

func TestDispatchList_UsesTheResourceZoneAcrossDaylightSaving(t *testing.T) {
	f := &fakeStore{resourceTZ: "America/New_York"}
	// The night of 2027-03-13: 22:00 EST to 06:00 EDT is seven hours.
	rec := do(newRouter(f), http.MethodGet, "/v1/scheduling/dispatch-list?resourceId="+resID+"&date=2027-03-13&startMinute=1320&endMinute=1800", "", allDuties, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if !f.lastUseFrom.Equal(time.Date(2027, 3, 14, 3, 0, 0, 0, time.UTC)) || !f.lastUseTo.Equal(time.Date(2027, 3, 14, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("store asked for %s .. %s, want 03:00Z .. 10:00Z", f.lastUseFrom, f.lastUseTo)
	}
	// 02:10 to 02:50 on the spring day never shows on the clock.
	rec = do(newRouter(f), http.MethodGet, "/v1/scheduling/dispatch-list?resourceId="+resID+"&date=2027-03-14&startMinute=130&endMinute=170", "", allDuties, "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "daylight-saving gap") {
		t.Fatalf("window in the gap: status %d body %s, want 400", rec.Code, rec.Body)
	}
	if rec := do(newRouter(&fakeStore{resourceTZ: "Mars/Olympus"}), http.MethodGet, "/v1/scheduling/dispatch-list?resourceId="+resID+"&date=2027-03-14", "", allDuties, ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("unknown zone: status %d, want 500 rather than a list in UTC", rec.Code)
	}
}

func TestDispatchList_ResourceOfAnotherTenantIsNotFound(t *testing.T) {
	// The store reads the resource under the caller's tenant (RLS), so another tenant's resource is not found.
	f := &fakeStore{err: httpx.NewProblem(http.StatusNotFound, store.CodeResourceNotFound, "resource not found")}
	rec := do(newRouter(f), http.MethodGet, "/v1/scheduling/dispatch-list?resourceId="+resID+"&date=2027-01-05", "", allDuties, "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeResourceNotFound) {
		t.Fatalf("status %d body %s, want 404 %s", rec.Code, rec.Body, store.CodeResourceNotFound)
	}
	if f.lastUseID != "" {
		t.Fatalf("bookings were read for a resource that was not found: %s", f.lastUseID)
	}
}
