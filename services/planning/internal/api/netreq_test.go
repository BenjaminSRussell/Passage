package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// ListNetRequirements serves two days of FG in plan MAIN and nothing else.
func (f *fakeStore) ListNetRequirements(_ context.Context, code, itemID string, limit int) ([]store.NetRequirement, error) {
	f.lastLimit = limit
	if code != "MAIN" || itemID != fgID {
		return []store.NetRequirement{}, nil
	}
	return []store.NetRequirement{
		{ItemID: fgID, Date: "2026-11-02", GrossRequirements: "10", ScheduledReceipts: "0", PlannedReceipts: "0", PAB: "10", NetRequirement: "0"},
		{ItemID: fgID, Date: "2026-11-30", GrossRequirements: "120", ScheduledReceipts: "0", PlannedReceipts: "110", PAB: "0", NetRequirement: "110"},
	}, nil
}

func TestNetRequirements_ReturnsTimePhasedRows(t *testing.T) {
	st := &fakeStore{}
	rec := do(t, st, http.MethodGet, "/v1/planning/net-requirements?planCode=MAIN&itemId="+fgID+"&limit=50", "", "planning.read", true, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Items []map[string]string `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Items) != 2 {
		t.Fatalf("items %+v, %v", out, err)
	}
	// The wire names are the OpenAPI NetRequirement fields.
	got := out.Items[1]
	if got["date"] != "2026-11-30" || got["grossRequirements"] != "120" || got["plannedReceipts"] != "110" ||
		got["projectedAvailableBalance"] != "0" || got["netRequirement"] != "110" {
		t.Fatalf("row %+v", got)
	}
	if st.lastLimit != 50 {
		t.Fatalf("limit %d reached the store; want 50", st.lastLimit)
	}
}

func TestNetRequirements_UnknownItemIsAnEmptyList(t *testing.T) {
	rec := do(t, &fakeStore{}, http.MethodGet, "/v1/planning/net-requirements?planCode=MAIN&itemId="+siteID, "", "planning.read", true, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestNetRequirements_ValidatesQuery(t *testing.T) {
	rec := do(t, &fakeStore{}, http.MethodGet, "/v1/planning/net-requirements?itemId=nope", "", "planning.read", true, "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"planCode"`) || !strings.Contains(rec.Body.String(), `"itemId"`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	rec = do(t, &fakeStore{}, http.MethodGet, "/v1/planning/net-requirements?planCode=MAIN&itemId="+fgID+"&limit=501", "", "planning.read", true, "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"limit"`) {
		t.Fatalf("limit 501: status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestNetRequirements_RequiresTenantAndReadDuty(t *testing.T) {
	path := "/v1/planning/net-requirements?planCode=MAIN&itemId=" + fgID
	if rec := do(t, &fakeStore{}, http.MethodGet, path, "", "planning.run", true, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("without planning.read: status %d", rec.Code)
	}
	if rec := do(t, &fakeStore{}, http.MethodGet, path, "", "planning.read", false, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without tenant: status %d", rec.Code)
	}
}

// TestRun_PlansInWorkingDays: with Saturday, Sunday and 2026-11-25 closed, the
// 7-day lead time of SO-1's order (due Monday 2026-11-30, day 28) counts
// working days back to Wednesday 2026-11-18 (day 16) instead of day 21.
func TestRun_PlansInWorkingDays(t *testing.T) {
	st := &fakeStore{}
	body := strings.Replace(runBody, `"asOf":"2026-11-02",`, `"asOf":"2026-11-02","calendar":{"closedWeekdays":[0,6],"closedDates":["2026-11-25"]},`, 1)
	rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", body, "planning.run", true, "cal-1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if o := st.last.Result.Orders; len(o) != 1 || o[0].DeliveryDate != 28 || o[0].OrderDate != 16 {
		t.Fatalf("orders %+v; want delivery day 28, order day 16", o)
	}
	// The calendar is part of the input: the same key without it is a reuse.
	if rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", runBody, "planning.run", true, "cal-1"); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("same key, other calendar: status %d", rec.Code)
	}
}

func TestRun_RejectsInvalidCalendar(t *testing.T) {
	cases := map[string]string{
		"weekday out of range": `"calendar":{"closedWeekdays":[9]}`,
		"every weekday closed": `"calendar":{"closedWeekdays":[0,1,2,3,4,5,6]}`,
		"bad closed date":      `"calendar":{"closedDates":["25/12/2026"]}`,
	}
	for name, cal := range cases {
		t.Run(name, func(t *testing.T) {
			st := &fakeStore{}
			body := strings.Replace(runBody, `"asOf":"2026-11-02",`, `"asOf":"2026-11-02",`+cal+`,`, 1)
			rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", body, "planning.run", true, "cal-x")
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"calendar"`) {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
			if st.last.IdempotencyKey != "" {
				t.Fatal("a refused run must not reach the store")
			}
		})
	}
}
