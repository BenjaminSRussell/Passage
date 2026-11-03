package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/forecastclient"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

const (
	generatePath = "/v1/planning/forecasts/BASE:generate"
	genItem      = "00000000-0000-0000-0000-0000000000c1"
	genSite      = "00000000-0000-0000-0000-0000000000d1"
)

type fakeGenerator struct {
	got   []forecastclient.Series
	err   error
	value float64 // when set, every period of every path
}

func (g *fakeGenerator) Generate(_ context.Context, series []forecastclient.Series) ([]forecastclient.Result, error) {
	g.got = series
	if g.err != nil {
		return nil, g.err
	}
	mape := 0.25
	out := make([]forecastclient.Result, len(series))
	for i, s := range series {
		path := make([]float64, s.HorizonPeriods)
		for j := range path {
			path[j] = 10.5 + float64(j)
		}
		path[0] = -4 // a model path below zero is stored as 0
		if g.value != 0 {
			for j := range path {
				path[j] = g.value
			}
		}
		out[i] = forecastclient.Result{ItemID: s.ItemID, SiteID: s.SiteID, Model: "ets", SeasonLength: 12, Forecast: path, MAPE: &mape, Warnings: nil}
	}
	return out, nil
}

func generateBody(history string) string {
	return fmt.Sprintf(`{"firstPeriodStart":"2026-12-01","horizonPeriods":3,"series":[{"itemId":%q,"siteId":%q,"history":%s,"autoSeason":true}]}`, genItem, genSite, history)
}

func genDo(t *testing.T, a *API, body, duties, ifMatch string) *httptest.ResponseRecorder {
	t.Helper()
	router := httpx.NewRouter()
	a.Register(router)
	req := httptest.NewRequest(http.MethodPost, generatePath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", duties)
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func codeOf(rec *httptest.ResponseRecorder) string {
	var p struct{ Code string }
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	return p.Code
}

func TestGenerateForecastWritesMonthlyStatisticalCells(t *testing.T) {
	st := &fakeStore{}
	gen := &fakeGenerator{}
	rec := genDo(t, New(st).WithStatisticalForecast(gen), generateBody("[10,12.5,0,8]"), dutyForecasts, `"0"`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("ETag") != `"1"` {
		t.Errorf("ETag = %s", rec.Header().Get("ETag"))
	}
	var body struct {
		Generated    []generatedSeries
		CellsWritten int
		CellsKept    int
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.CellsWritten != 3 || body.CellsKept != 0 || len(body.Generated) != 1 || body.Generated[0].Model != "ets" || *body.Generated[0].MAPE != 0.25 || body.Generated[0].Warnings == nil {
		t.Fatalf("body = %+v", body)
	}
	if len(gen.got) != 1 || !gen.got[0].AutoSeason || gen.got[0].HorizonPeriods != 3 || gen.got[0].HistoryMonths != 36 || gen.got[0].Model != "auto" || len(gen.got[0].History) != 4 || gen.got[0].History[1] != 12.5 {
		t.Fatalf("sent to forecast service: %+v", gen.got)
	}
	want := [][3]string{{"2026-12-01", "2026-12-31", "0"}, {"2027-01-01", "2027-01-31", "11.5"}, {"2027-02-01", "2027-02-28", "12.5"}}
	if len(st.generated) != 3 {
		t.Fatalf("cells = %+v", st.generated)
	}
	for i, c := range st.generated {
		if c.PeriodStart != want[i][0] || c.PeriodEnd != want[i][1] || c.Qty.String() != want[i][2] || c.Source != "statistical" {
			t.Errorf("cell %d = %+v, want %v", i, c, want[i])
		}
	}
}

func TestGenerateForecastRefusals(t *testing.T) {
	gen := &fakeGenerator{}
	on := New(&fakeStore{}).WithStatisticalForecast(gen)
	if rec := genDo(t, New(&fakeStore{}), generateBody("[1]"), dutyForecasts, `"0"`); rec.Code != http.StatusConflict || codeOf(rec) != CodeFeatureOff {
		t.Errorf("flag off: %d %s", rec.Code, rec.Body)
	}
	if rec := genDo(t, on, generateBody("[1]"), dutyRead, `"0"`); rec.Code != http.StatusForbidden {
		t.Errorf("without the maintain duty: %d", rec.Code)
	}
	if rec := genDo(t, on, generateBody("[1]"), dutyForecasts, ""); rec.Code != http.StatusPreconditionRequired {
		t.Errorf("without If-Match: %d", rec.Code)
	}
	if rec := genDo(t, on, generateBody("[1]"), dutyForecasts, `"4"`); rec.Code != http.StatusPreconditionFailed {
		t.Errorf("stale version: %d %s", rec.Code, rec.Body)
	}
	bad := []string{
		strings.Replace(generateBody("[1]"), "2026-12-01", "2026-12-02", 1),
		strings.Replace(generateBody("[1]"), `"horizonPeriods":3`, `"horizonPeriods":25`, 1),
		strings.Replace(generateBody("[1]"), `"horizonPeriods":3`, `"historyMonths":6`, 1),
		generateBody("[]"),
		generateBody("[-1]"),
		generateBody("[1.0000001]"),
		strings.Replace(generateBody("[1]"), genItem, "nope", 1),
		strings.Replace(generateBody("[1]"), `"autoSeason":true`, `"model":"arima"`, 1),
		`{"firstPeriodStart":"2026-12-01","series":[]}`,
	}
	for i, b := range bad {
		if rec := genDo(t, on, b, dutyForecasts, `"0"`); rec.Code != http.StatusBadRequest {
			t.Errorf("bad body %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	if len(gen.got) != 0 {
		t.Error("a refused request reached the forecast service")
	}
}

func TestGenerateForecastServiceDown(t *testing.T) {
	gen := &fakeGenerator{err: fmt.Errorf("%w: boom", forecastclient.ErrUnavailable)}
	rec := genDo(t, New(&fakeStore{}).WithStatisticalForecast(gen), generateBody("[1,2]"), dutyForecasts, `"0"`)
	if rec.Code != http.StatusBadGateway || codeOf(rec) != CodeForecastUnavailable {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestGenerateForecastLockedModelAndKeptCells(t *testing.T) {
	st := &fakeStore{keptCells: 1}
	a := New(st).WithStatisticalForecast(&fakeGenerator{})
	rec := genDo(t, a, generateBody("[1,2]"), dutyForecasts, `"0"`)
	var body struct{ CellsWritten, CellsKept int }
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != 200 || body.CellsWritten != 2 || body.CellsKept != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	m := st.models["BASE"]
	m.Status = "submitted"
	st.models["BASE"] = m
	if rec := genDo(t, a, generateBody("[1,2]"), dutyForecasts, `"1"`); rec.Code != http.StatusConflict || codeOf(rec) != store.CodeForecastLocked {
		t.Errorf("submitted model: %d %s", rec.Code, rec.Body)
	}
}

// A stale If-Match or a locked model is refused before the forecast service
// runs; the store checks both again under its lock after the call.
func TestGenerateForecastChecksTheModelBeforeCallingTheService(t *testing.T) {
	st := &fakeStore{models: map[string]store.ForecastModel{
		"BASE": {Code: "BASE", Version: 3, Status: domain.ConsensusDraft},
		"SENT": {Code: "SENT", Version: 2, Status: domain.ConsensusSubmitted},
	}}
	gen := &fakeGenerator{}
	a := New(st).WithStatisticalForecast(gen)
	if rec := genDo(t, a, generateBody("[1,2]"), dutyForecasts, `"2"`); rec.Code != http.StatusPreconditionFailed || codeOf(rec) != store.CodeVersionConflict {
		t.Errorf("stale version: %d %s", rec.Code, rec.Body)
	}
	if rec := genDo(t, a, generateBody("[1,2]"), dutyForecasts, `"1"`); rec.Code != http.StatusPreconditionFailed {
		t.Errorf("If-Match 1 on a missing model: %d %s", rec.Code, rec.Body)
	}
	router := httpx.NewRouter()
	a.Register(router)
	req := httptest.NewRequest(http.MethodPost, "/v1/planning/forecasts/SENT:generate", strings.NewReader(generateBody("[1,2]")))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", dutyForecasts)
	req.Header.Set("If-Match", `"2"`)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || codeOf(rec) != store.CodeForecastLocked {
		t.Errorf("submitted model: %d %s", rec.Code, rec.Body)
	}
	if gen.got != nil {
		t.Fatalf("the forecast service ran for a refused request: %+v", gen.got)
	}
	if rec := genDo(t, a, generateBody("[1,2]"), dutyForecasts, `"3"`); rec.Code != http.StatusOK || gen.got == nil {
		t.Errorf("current version: %d %s", rec.Code, rec.Body)
	}
}

func TestGenerateForecastRefusesAnUnstorableQuantity(t *testing.T) {
	st := &fakeStore{}
	rec := genDo(t, New(st).WithStatisticalForecast(&fakeGenerator{value: 1e12}), generateBody("[1,2]"), dutyForecasts, `"0"`)
	if rec.Code != http.StatusUnprocessableEntity || codeOf(rec) != CodeForecastRange {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if st.generated != nil {
		t.Errorf("cells were written: %+v", st.generated)
	}
}
