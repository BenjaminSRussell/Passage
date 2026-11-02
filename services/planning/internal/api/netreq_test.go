package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
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

// ReadProjectionSource serves plan MAIN at as-of 2026-11-02 with FG on hand 20:
// 120 due on 2026-11-30 against a planned receipt of 110 that day.
func (f *fakeStore) ReadProjectionSource(_ context.Context, code string, itemIDs []string, windowDays int) (store.ProjectionSource, error) {
	f.lastWindow = windowDays
	if code != "MAIN" {
		return store.ProjectionSource{}, httpx.NewProblem(http.StatusNotFound, store.CodeVersionNotFound, "no current version")
	}
	src := store.ProjectionSource{Version: 3, AsOf: "2026-11-02", SiteID: siteID, Rows: map[string][]store.NetRequirement{}}
	if f.asOf != "" {
		src.AsOf = f.asOf
		return src, nil
	}
	for _, id := range itemIDs {
		if id == fgID {
			src.Rows[id] = []store.NetRequirement{
				{ItemID: id, Date: "2026-11-02", GrossRequirements: "10", ScheduledReceipts: "0", PlannedReceipts: "0", PAB: "10", NetRequirement: "0"},
				{ItemID: id, Date: "2026-11-30", GrossRequirements: "120", ScheduledReceipts: "0", PlannedReceipts: "110", PAB: "0", NetRequirement: "110"},
			}
		}
	}
	return src, nil
}

func TestPlanProjection_BucketsTheStoredPlan(t *testing.T) {
	st := &fakeStore{}
	path := "/v1/planning/plans/MAIN/projection?itemId=" + fgID + "&itemId=" + siteID + "&bucket=week&horizonDays=35&unitCost=2.5&currency=EUR"
	rec := do(t, st, http.MethodGet, path, "", "planning.read", true, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Version int    `json:"version"`
		AsOf    string `json:"asOf"`
		SiteID  string `json:"siteId"`
		Items   []struct {
			ItemID        string `json:"itemId"`
			Planned       bool   `json:"planned"`
			OpeningOnHand string `json:"openingOnHand"`
			Periods       []struct {
				StartDate     string  `json:"startDate"`
				Demand        string  `json:"demand"`
				Planned       string  `json:"planned"`
				ClosingOnHand string  `json:"closingOnHand"`
				Value         *string `json:"value"`
				Shortage      bool    `json:"shortage"`
			} `json:"periods"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Version != 3 || out.AsOf != "2026-11-02" || out.SiteID != siteID || len(out.Items) != 2 {
		t.Fatalf("body %s", rec.Body.String())
	}
	fg, other := out.Items[0], out.Items[1]
	// FG: opening 20 (PAB 10 + gross 10), week 1 demand 10 -> 10, week 4 (day 28) demand 120 and planned 110 -> 0.
	if fg.ItemID != fgID || !fg.Planned || fg.OpeningOnHand != "20" || len(fg.Periods) != 5 {
		t.Fatalf("FG %+v", fg)
	}
	if w := fg.Periods[0]; w.StartDate != "2026-11-02" || w.Demand != "10" || w.ClosingOnHand != "10" || w.Value == nil || *w.Value != "25.00" {
		t.Fatalf("week 1 %+v", w)
	}
	if w := fg.Periods[4]; w.StartDate != "2026-11-30" || w.Demand != "120" || w.Planned != "110" || w.ClosingOnHand != "0" || w.Shortage {
		t.Fatalf("week 5 %+v", w)
	}
	if other.Planned || other.OpeningOnHand != "0" {
		t.Fatalf("an item the plan did not plan: %+v", other)
	}
	if st.lastWindow != 35+30 {
		t.Fatalf("store window %d, want horizon plus 30 days of look-ahead", st.lastWindow)
	}
}

// TestPlanProjection_MonthBucketsFollowTheCalendar: from as-of 2 November a
// month bucket runs to 30 November, then the whole of December, then January
// up to the horizon, so the planned receipt of 30 November is in the first.
func TestPlanProjection_MonthBucketsFollowTheCalendar(t *testing.T) {
	rec := do(t, &fakeStore{}, http.MethodGet, "/v1/planning/plans/MAIN/projection?bucket=month&horizonDays=84&itemId="+fgID, "", "planning.read", true, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Items []struct {
			Periods []struct {
				StartDate string `json:"startDate"`
				EndDate   string `json:"endDate"`
				Planned   string `json:"planned"`
			} `json:"periods"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, p := range out.Items[0].Periods {
		got = append(got, p.StartDate+".."+p.EndDate)
	}
	want := "2026-11-02..2026-11-30 2026-12-01..2026-12-31 2027-01-01..2027-01-24"
	if strings.Join(got, " ") != want || out.Items[0].Periods[0].Planned != "110" {
		t.Fatalf("months %v, want %s; body %s", got, want, rec.Body.String())
	}
}

// TestPlanProjection_WeekBucketsAreISOWeeks: weeks run Monday to Sunday. From
// as-of Sunday 27 December 2026 the first bucket is that one day, then ISO week
// 53 of 2026 (28 December to 3 January 2027) across the year end, then week 1
// of 2027 cut at the horizon.
func TestPlanProjection_WeekBucketsAreISOWeeks(t *testing.T) {
	st := &fakeStore{asOf: "2026-12-27"}
	rec := do(t, st, http.MethodGet, "/v1/planning/plans/MAIN/projection?bucket=week&horizonDays=12&itemId="+fgID, "", "planning.read", true, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Items []struct {
			Periods []struct {
				StartDate string `json:"startDate"`
				EndDate   string `json:"endDate"`
			} `json:"periods"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, p := range out.Items[0].Periods {
		got = append(got, p.StartDate+".."+p.EndDate)
	}
	want := "2026-12-27..2026-12-27 2026-12-28..2027-01-03 2027-01-04..2027-01-07"
	if strings.Join(got, " ") != want {
		t.Fatalf("weeks %v, want %s", got, want)
	}
}

func TestPlanProjection_WithoutCostHasNoValue(t *testing.T) {
	rec := do(t, &fakeStore{}, http.MethodGet, "/v1/planning/plans/MAIN/projection?itemId="+fgID, "", "planning.read", true, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"value":null`) || !strings.Contains(rec.Body.String(), `"currency":null`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestPlanProjection_UnknownPlanIs404(t *testing.T) {
	rec := do(t, &fakeStore{}, http.MethodGet, "/v1/planning/plans/NOPE/projection?itemId="+fgID, "", "planning.read", true, "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeVersionNotFound) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestPlanProjection_ValidatesQuery(t *testing.T) {
	base := "/v1/planning/plans/MAIN/projection?"
	cases := map[string]struct{ query, field string }{
		"no item":          {"bucket=week", "itemId"},
		"bad item":         {"itemId=nope", "itemId"},
		"bad bucket":       {"itemId=" + fgID + "&bucket=year", "bucket"},
		"horizon too long": {"itemId=" + fgID + "&horizonDays=731", "horizonDays"},
		"negative cost":    {"itemId=" + fgID + "&unitCost=-1&currency=EUR", "unitCost"},
		"cost no currency": {"itemId=" + fgID + "&unitCost=1", "currency"},
		"bad currency":     {"itemId=" + fgID + "&unitCost=1&currency=euro", "currency"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rec := do(t, &fakeStore{}, http.MethodGet, base+c.query, "", "planning.read", true, "")
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+c.field+`"`) {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
		})
	}
	many := ""
	for i := 0; i < 21; i++ {
		many += fmt.Sprintf("&itemId=00000000-0000-0000-0000-%012d", i)
	}
	rec := do(t, &fakeStore{}, http.MethodGet, base+"bucket=week"+many, "", "planning.read", true, "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "at most 20") {
		t.Fatalf("21 items: status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestPlanProjection_RequiresTenantAndReadDuty(t *testing.T) {
	path := "/v1/planning/plans/MAIN/projection?itemId=" + fgID
	if rec := do(t, &fakeStore{}, http.MethodGet, path, "", "planning.run", true, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("without planning.read: status %d", rec.Code)
	}
	if rec := do(t, &fakeStore{}, http.MethodGet, path, "", "planning.read", false, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without tenant: status %d", rec.Code)
	}
}
