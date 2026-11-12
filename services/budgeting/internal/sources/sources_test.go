package sources

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const le = "00000000-0000-0000-0000-0000000000e1"

func principalCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: "00000000-0000-0000-0000-000000000001", UserID: "u", LegalEntityID: le})
}

func testReader(t *testing.T, h http.Handler) *Reader {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	signer, err := tenant.ServiceSignerFromEnv(func(k string) string {
		return map[string]string{"PASSAGE_SERVICE_NAME": "budgeting", "PASSAGE_SERVICE_SIGNING_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}[k]
	})
	if err != nil || signer == nil {
		t.Fatalf("signer: %v", err)
	}
	r, err := New(srv.URL, srv.URL, srv.URL, srv.URL, signer, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDepreciation_SumsTheOpenBooksSchedulesOfTheYear(t *testing.T) {
	var asked []string
	r := testReader(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		asked = append(asked, req.URL.Path+"?"+req.URL.RawQuery)
		switch {
		case req.URL.Path == "/v1/assets" && req.URL.Query().Get("after") == "":
			_, _ = w.Write([]byte(`{"items":[{"id":"a1"},{"id":"a2"}],"nextCursor":"a2"}`))
		case req.URL.Path == "/v1/assets":
			_, _ = w.Write([]byte(`{"items":[{"id":"a3"}]}`))
		case req.URL.Path == "/v1/assets/a1", req.URL.Path == "/v1/assets/a3":
			_, _ = w.Write([]byte(`{"books":[{"id":"b-` + strings.TrimPrefix(req.URL.Path, "/v1/assets/") + `","bookCode":"IFRS","currency":"EUR","status":"Open"}]}`))
		case req.URL.Path == "/v1/assets/a2":
			_, _ = w.Write([]byte(`{"books":[{"id":"b-a2","bookCode":"TAX","currency":"EUR","status":"Open"}]}`)) // not the book asked for
		case strings.HasSuffix(req.URL.Path, "/schedule") && strings.Contains(req.URL.Path, "a1"):
			_, _ = w.Write([]byte(`{"lines":[{"period":"2026-12","amount":"99"},{"period":"2027-01","amount":"100.10"},{"period":"2027-02","amount":"100.10"}]}`))
		case strings.HasSuffix(req.URL.Path, "/schedule"):
			_, _ = w.Write([]byte(`{"lines":[{"period":"2027","amount":"1200"}]}`)) // an annual line
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	m, used, err := r.Depreciation(principalCtx(), le, 2027, "EUR", "IFRS")
	if err != nil || used != 2 {
		t.Fatalf("depreciation = %v used %d err %v (asked %v)", m, used, err, asked)
	}
	if !m["2027-01"].Equal(decimal.RequireFromString("200.10")) || !m["2027-02"].Equal(decimal.RequireFromString("200.10")) || !m["2027-12"].Equal(decimal.NewFromInt(100)) || len(m) != 12 {
		t.Fatalf("months = %v", m)
	}
	if _, ok := m["2026-12"]; ok {
		t.Fatal("a period of another year was included")
	}
	if _, _, err := r.Depreciation(principalCtx(), le, 2027, "USD", "IFRS"); err == nil || !strings.Contains(err.Error(), "in EUR and the plan in USD") {
		t.Fatalf("currency mismatch: %v", err)
	}
}

func TestPositions_CostTheFTEByJobInEveryMonthTheyAreValid(t *testing.T) {
	r := testReader(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte(`{"items":[
		 {"jobCode":"ENG","departmentId":"d1","legalEntityId":"` + le + `","fte":"1.0","validFrom":"2026-01-01T00:00:00Z"},
		 {"jobCode":"ENG","departmentId":"d1","legalEntityId":"` + le + `","fte":"0.5","validFrom":"2027-03-15T00:00:00Z","validTo":"2027-06-01T00:00:00Z"},
		 {"jobCode":"OPS","departmentId":"d2","legalEntityId":"` + le + `","fte":"2","validFrom":"2028-01-01T00:00:00Z"},
		 {"jobCode":"ENG","departmentId":"d9","legalEntityId":"00000000-0000-0000-0000-0000000000ff","fte":"1","validFrom":"2026-01-01T00:00:00Z"}]}`))
	}))
	costs, err := r.Positions(principalCtx(), le, 2027, []PositionRate{{JobCode: "ENG", MonthlyCost: decimal.NewFromInt(6000)}, {JobCode: "OPS", MonthlyCost: decimal.NewFromInt(4000)}})
	if err != nil || len(costs) != 1 || costs[0].DepartmentID != "d1" || costs[0].Positions != 2 {
		t.Fatalf("costs = %+v, %v", costs, err)
	}
	d := costs[0].Months
	// The full position costs 6000 every month; the half position adds 3000 in March to May (valid from March 15 until June 1).
	if !d["2027-01"].Equal(decimal.NewFromInt(6000)) || !d["2027-03"].Equal(decimal.NewFromInt(9000)) || !d["2027-05"].Equal(decimal.NewFromInt(9000)) || !d["2027-06"].Equal(decimal.NewFromInt(6000)) {
		t.Fatalf("months = %v", d)
	}
	if _, err := r.Positions(principalCtx(), le, 2027, []PositionRate{{JobCode: "OPS", MonthlyCost: decimal.NewFromInt(1)}}); err == nil || !strings.Contains(err.Error(), "job ENG") {
		t.Fatalf("missing rate: %v", err)
	}
}

func TestProjectForecastAndDemand(t *testing.T) {
	r := testReader(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasPrefix(req.URL.Path, "/v1/projects/"):
			_, _ = w.Write([]byte(`{"currency":"EUR","lines":[{"period":"2027-03","costAmount":"100","salesAmount":"150"},{"period":"2027-03","costAmount":"20","salesAmount":"30"},{"period":"2028-01","costAmount":"9","salesAmount":"9"}]}`))
		case strings.HasPrefix(req.URL.Path, "/v1/planning/forecasts/FC/lines"):
			if req.URL.Query().Get("itemId") == "i1" {
				_, _ = w.Write([]byte(`{"items":[{"periodStart":"2027-01-01","qty":"10"},{"periodStart":"2027-02-01","qty":"20.5"},{"periodStart":"2026-12-01","qty":"99"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"items":[{"periodStart":"2027-01-01","qty":"4"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	cost, err := r.ProjectForecast(principalCtx(), le, "p1", "m1", 2027, "EUR", false)
	if err != nil || !cost["2027-03"].Equal(decimal.NewFromInt(120)) || len(cost) != 1 {
		t.Fatalf("cost = %v, %v", cost, err)
	}
	sales, _ := r.ProjectForecast(principalCtx(), le, "p1", "m1", 2027, "EUR", true)
	if !sales["2027-03"].Equal(decimal.NewFromInt(180)) {
		t.Fatalf("sales = %v", sales)
	}
	if _, err := r.ProjectForecast(principalCtx(), le, "p1", "m1", 2027, "USD", false); err == nil {
		t.Fatal("a forecast in another currency must be refused")
	}
	d, err := r.Demand(principalCtx(), le, "FC", 2027, []ItemPrice{{ItemID: "i1", Price: decimal.RequireFromString("2.5")}, {ItemID: "i2", Price: decimal.NewFromInt(10)}})
	if err != nil || !d["2027-01"].Equal(decimal.NewFromInt(65)) || !d["2027-02"].Equal(decimal.RequireFromString("51.25")) || len(d) != 2 {
		t.Fatalf("demand = %v, %v", d, err)
	}
}

func TestReader_ErrorsAndConfiguration(t *testing.T) {
	r := testReader(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	var se *Error
	if _, err := r.ProjectForecast(principalCtx(), le, "p", "m", 2027, "EUR", false); !errors.As(err, &se) || !strings.Contains(se.Detail, "status 403") {
		t.Fatalf("403: %v", err)
	}
	off := &Reader{http: r.http, signer: r.signer}
	if _, err := off.ProjectForecast(principalCtx(), le, "p", "m", 2027, "EUR", false); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("not configured: %v", err)
	}
	if rd, err := FromEnv(func(string) string { return "" }); rd != nil || err != nil {
		t.Fatalf("FromEnv unset: %v %v", rd, err)
	}
	if _, err := FromEnv(func(k string) string { return map[string]string{"HR_URL": "http://hr"}[k] }); err == nil {
		t.Fatal("a URL without a signing key must be refused")
	}
}
