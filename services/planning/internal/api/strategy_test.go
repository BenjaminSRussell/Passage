package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

func (f *fakeStore) RunStrategies(context.Context, string, string, []string) (map[string]domain.StrategySpec, error) {
	return f.runStrategies, nil
}

func (f *fakeStore) ListStrategies(context.Context) ([]store.Strategy, error) {
	return append([]store.Strategy{}, f.strategies...), nil
}

func (f *fakeStore) GetStrategy(_ context.Context, code string) (store.Strategy, error) {
	for _, s := range f.strategies {
		if s.Spec.Code == code {
			return s, nil
		}
	}
	return store.Strategy{}, httpx.NewProblem(http.StatusNotFound, store.CodeStrategyNotFound, "no setup")
}

func (f *fakeStore) CreateStrategy(_ context.Context, req store.StrategyCreate) (store.Strategy, bool, error) {
	if req.IdempotencyKey != "" {
		if hash, ok := f.strategyKeys[req.IdempotencyKey]; ok {
			if hash != req.InputHash {
				return store.Strategy{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reuse")
			}
			st, err := f.GetStrategy(context.Background(), req.Spec.Code)
			return st, true, err
		}
	}
	if _, err := f.GetStrategy(context.Background(), req.Spec.Code); err == nil {
		return store.Strategy{}, false, httpx.NewProblem(http.StatusConflict, store.CodeStrategyExists, "exists")
	}
	if req.IdempotencyKey != "" {
		if f.strategyKeys == nil {
			f.strategyKeys = map[string]string{}
		}
		f.strategyKeys[req.IdempotencyKey] = req.InputHash
	}
	st := store.Strategy{ID: testkit.TenantID(len(f.strategies) + 30), Version: 1, Spec: req.Spec}
	f.strategies = append(f.strategies, st)
	return st, false, nil
}

func (f *fakeStore) UpdateStrategy(_ context.Context, expected int, spec domain.StrategySpec) (store.Strategy, error) {
	for i, s := range f.strategies {
		if s.Spec.Code != spec.Code {
			continue
		}
		if s.Version != expected {
			return store.Strategy{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
		}
		f.strategies[i] = store.Strategy{ID: s.ID, Version: s.Version + 1, Spec: spec}
		return f.strategies[i], nil
	}
	return store.Strategy{}, httpx.NewProblem(http.StatusNotFound, store.CodeStrategyNotFound, "no setup")
}

const strategyPath = "/v1/planning/strategies"

func decodeStrategy(t *testing.T, rec *httptest.ResponseRecorder) strategyDTO {
	t.Helper()
	var s strategyDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	return s
}

// FR-22.19: the list always shows the six strategies, with the PRD defaults for
// those that have no stored setup.
func TestStrategies_ListShowsSixWithDefaults(t *testing.T) {
	st := &fakeStore{}
	rec := do(t, st, http.MethodGet, strategyPath, "", dutyRead, true, "")
	var page struct{ Items []strategyDTO }
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != http.StatusOK || len(page.Items) != 6 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	mto := page.Items[2]
	if mto.Code != domain.StrategyMTO || mto.Configured || !mto.UsesMarking || mto.ConsumptionMode != "backward" || mto.BackwardDays != 30 || mto.ForwardDays != 0 {
		t.Fatalf("mto default %+v", mto)
	}
	if page.Items[0].UsesMarking {
		t.Fatalf("mts-net default marks: %+v", page.Items[0])
	}
}

func TestStrategies_CreateGetUpdate(t *testing.T) {
	st := &fakeStore{}
	body := `{"code":"assemble-to-order-final","consumptionMode":"backward-forward","backwardDays":14,"forwardDays":7}`
	rec := do(t, st, http.MethodPost, strategyPath, body, dutySetup, true, "k-1")
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: status %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if s := decodeStrategy(t, rec); !s.Configured || s.BackwardDays != 14 || s.ForwardDays != 7 || s.UsesMarking {
		t.Fatalf("created %+v", s)
	}
	replay := do(t, st, http.MethodPost, strategyPath, body, dutySetup, true, "k-1")
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" || len(st.strategies) != 1 {
		t.Fatalf("replay: status %d strategies %d", replay.Code, len(st.strategies))
	}
	if reuse := do(t, st, http.MethodPost, strategyPath, strings.Replace(body, "14", "15", 1), dutySetup, true, "k-1"); reuse.Code != http.StatusUnprocessableEntity {
		t.Fatalf("key reuse: status %d", reuse.Code)
	}
	if dup := do(t, st, http.MethodPost, strategyPath, body, dutySetup, true, ""); dup.Code != http.StatusConflict || problemOf(t, dup).Code != store.CodeStrategyExists {
		t.Fatalf("duplicate: status %d body %s", dup.Code, dup.Body.String())
	}
	get := do(t, st, http.MethodGet, strategyPath+"/assemble-to-order-final", "", dutyRead, true, "")
	if s := decodeStrategy(t, get); get.Code != http.StatusOK || !s.Configured || s.Version != 1 {
		t.Fatalf("get: status %d %+v", get.Code, s)
	}
	unset := do(t, st, http.MethodGet, strategyPath+"/mto", "", dutyRead, true, "")
	if s := decodeStrategy(t, unset); unset.Code != http.StatusOK || s.Configured || !s.UsesMarking {
		t.Fatalf("unset get: status %d %+v", unset.Code, s)
	}
	if bad := do(t, st, http.MethodGet, strategyPath+"/nope", "", dutyRead, true, ""); bad.Code != http.StatusNotFound {
		t.Fatalf("unknown strategy: status %d", bad.Code)
	}
	upd := doIfMatch(t, st, http.MethodPut, strategyPath+"/assemble-to-order-final", `{"consumptionMode":"backward","backwardDays":45}`, `"1"`, dutySetup)
	if s := decodeStrategy(t, upd); upd.Code != http.StatusOK || s.Version != 2 || s.BackwardDays != 45 || s.ForwardDays != 0 {
		t.Fatalf("update: status %d %+v", upd.Code, s)
	}
	if stale := doIfMatch(t, st, http.MethodPut, strategyPath+"/assemble-to-order-final", `{}`, `"1"`, dutySetup); stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale: status %d", stale.Code)
	}
	if none := doIfMatch(t, st, http.MethodPut, strategyPath+"/mto", `{}`, `"1"`, dutySetup); none.Code != http.StatusNotFound {
		t.Fatalf("update without setup: status %d", none.Code)
	}
	if mismatch := doIfMatch(t, st, http.MethodPut, strategyPath+"/assemble-to-order-final", `{"code":"mto"}`, `"2"`, dutySetup); mismatch.Code != http.StatusBadRequest {
		t.Fatalf("code mismatch: status %d", mismatch.Code)
	}
}

func TestStrategies_RefusalsAndDuties(t *testing.T) {
	st := &fakeStore{}
	for name, body := range map[string]string{
		"unknown code":     `{"code":"nope"}`,
		"bad mode":         `{"code":"mto","consumptionMode":"sideways"}`,
		"window too long":  `{"code":"mto","backwardDays":366}`,
		"negative forward": `{"code":"mto","forwardDays":-1}`,
		"empty window":     `{"code":"assemble-to-order-final","consumptionMode":"backward-forward","backwardDays":0,"forwardDays":0}`,
	} {
		if rec := do(t, st, http.MethodPost, strategyPath, body, dutySetup, true, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d body %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := do(t, st, http.MethodPost, strategyPath, `{"code":"mto"}`, dutyRead, true, ""); rec.Code != http.StatusForbidden {
		t.Errorf("create with read duty: status %d", rec.Code)
	}
	if rec := do(t, st, http.MethodGet, strategyPath, "", "planning.run", true, ""); rec.Code != http.StatusForbidden {
		t.Errorf("list without read duty: status %d", rec.Code)
	}
	if rec := do(t, st, http.MethodGet, strategyPath, "", dutyRead, false, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("list without tenant: status %d", rec.Code)
	}
}

// atoRunBody plans an item with one sales line of 40 on 2026-11-20 and nothing else.
const atoRunBody = `{"asOf":"2026-11-02","siteId":"` + siteID + `","forecastModel":"BASE","items":[
 {"itemId":"` + fgID + `","coverage":{"code":"requirement","marginDays":0},"onHand":"0","safetyStock":"0",
  "orderType":"production","leadTimeDays":7,"lot":{},
  "demand":[{"ref":"SO-1","refType":"sales-line","date":"2026-11-20","qty":"40"}],
  "receipts":[],"components":[]}]}`

// novemberForecast is the AC-22.14 forecast: 100 in the period ending 2026-11-30.
func novemberForecast() []store.ForecastDemandLine {
	return []store.ForecastDemandLine{{ItemID: fgID, PeriodStart: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		PeriodEnd: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC), Qty: decimal.RequireFromString("100"), ReducedQty: decimal.RequireFromString("100")}}
}

func demandByType(req store.RunRequest) map[string]decimal.Decimal {
	out := map[string]decimal.Decimal{}
	for _, p := range req.Result.Pegging {
		out[p.DemandRefType] = out[p.DemandRefType].Add(p.Qty)
	}
	return out
}

// AC-22.14 through the run: under assemble-to-order-final the sales line of 40
// consumes 40 of the forecast of 100, so the item's gross requirement is
// 60 + 40 = 100, not 140. Under mts-net the same data plans 140.
func TestRun_AssembleToOrderConsumesForecast(t *testing.T) {
	ato := domain.DefaultStrategy(domain.StrategyATOFinal)
	st := &fakeStore{models: map[string]store.ForecastModel{"BASE": {Code: "BASE"}}, demand: novemberForecast(),
		runStrategies: map[string]domain.StrategySpec{fgID: ato}}
	atoVersion, req := runVersion(t, st, atoRunBody, "run-ato")
	got := demandByType(req)
	if !got[domain.RefForecast].Equal(decimal.RequireFromString("60")) || !got[domain.RefSales].Equal(decimal.RequireFromString("40")) {
		t.Fatalf("pegged demand %v; want forecast 60 and sales 40", got)
	}

	st = &fakeStore{models: map[string]store.ForecastModel{"BASE": {Code: "BASE"}}, demand: novemberForecast()}
	netVersion, req := runVersion(t, st, atoRunBody, "run-net")
	got = demandByType(req)
	if !got[domain.RefForecast].Equal(decimal.RequireFromString("100")) || !got[domain.RefSales].Equal(decimal.RequireFromString("40")) {
		t.Fatalf("mts-net pegged demand %v; want forecast 100 and sales 40", got)
	}
	// The plan hash fingerprints the result, so the same request under another
	// strategy setup is not reported as an identical plan (FR-22.14 diff).
	if atoVersion.PlanHash == "" || atoVersion.PlanHash == netVersion.PlanHash {
		t.Fatalf("plan hash %q under ato and %q under mts-net; want them to differ", atoVersion.PlanHash, netVersion.PlanHash)
	}
}

// Only the item's own sales lines consume its forecast: a sales line of
// another item in the same run leaves the assemble-to-order forecast whole.
func TestRun_AssembleToOrderConsumesOnlyTheItemsOwnSales(t *testing.T) {
	ato := domain.DefaultStrategy(domain.StrategyATOFinal)
	st := &fakeStore{models: map[string]store.ForecastModel{"BASE": {Code: "BASE"}}, demand: novemberForecast(),
		runStrategies: map[string]domain.StrategySpec{fgID: ato, otherItemID: ato}}
	body := `{"asOf":"2026-11-02","siteId":"` + siteID + `","forecastModel":"BASE","items":[
 {"itemId":"` + fgID + `","coverage":{"code":"requirement","marginDays":0},"onHand":"0","safetyStock":"0",
  "orderType":"production","leadTimeDays":7,"lot":{},"demand":[],"receipts":[],"components":[]},
 {"itemId":"` + otherItemID + `","coverage":{"code":"requirement","marginDays":0},"onHand":"0","safetyStock":"0",
  "orderType":"production","leadTimeDays":7,"lot":{},
  "demand":[{"ref":"SO-9","refType":"sales-line","date":"2026-11-20","qty":"40"}],"receipts":[],"components":[]}]}`
	_, req := runVersion(t, st, body, "run-ato-other-item")
	if got := demandByType(req); !got[domain.RefForecast].Equal(decimal.RequireFromString("100")) || !got[domain.RefSales].Equal(decimal.RequireFromString("40")) {
		t.Fatalf("pegged demand %v; want the whole forecast of 100 and the other item's sale of 40", got)
	}
}

// A consumption window that does not reach the sale leaves the forecast whole
// (the sale is added on top of it).
func TestRun_AssembleToOrderWindowLimitsConsumption(t *testing.T) {
	ato := domain.DefaultStrategy(domain.StrategyATOFinal)
	ato.BackwardDays = 0
	st := &fakeStore{models: map[string]store.ForecastModel{"BASE": {Code: "BASE"}}, demand: novemberForecast(),
		runStrategies: map[string]domain.StrategySpec{fgID: ato}}
	// Bucket 11-01..11-30 contains the sale day, so a zero backward window still consumes it
	// (the bucket overlaps [d, d]); move the sale after the period to leave it out.
	body := strings.Replace(atoRunBody, "2026-11-20", "2026-12-20", 1)
	_, req := runVersion(t, st, body, "run-ato-window")
	if got := demandByType(req); !got[domain.RefForecast].Equal(decimal.RequireFromString("100")) {
		t.Fatalf("pegged demand %v; want the whole forecast of 100", got)
	}
}

// mto plans no forecast; mts-gross plans the forecast as entered.
func TestRun_MakeToOrderDropsAndGrossKeepsForecast(t *testing.T) {
	cells := novemberForecast()
	cells[0].ReducedQty = decimal.RequireFromString("70")
	st := &fakeStore{models: map[string]store.ForecastModel{"BASE": {Code: "BASE"}}, demand: cells,
		runStrategies: map[string]domain.StrategySpec{fgID: domain.DefaultStrategy(domain.StrategyMTO)}}
	_, req := runVersion(t, st, atoRunBody, "run-mto")
	if got := demandByType(req); !got[domain.RefForecast].IsZero() || !got[domain.RefSales].Equal(decimal.RequireFromString("40")) {
		t.Fatalf("mto pegged demand %v; want sales only", got)
	}
	st.runStrategies = map[string]domain.StrategySpec{fgID: domain.DefaultStrategy(domain.StrategyMTSGross)}
	_, req = runVersion(t, st, atoRunBody, "run-gross")
	if got := demandByType(req); !got[domain.RefForecast].Equal(decimal.RequireFromString("100")) {
		t.Fatalf("mts-gross pegged demand %v; want the forecast as entered, 100", got)
	}
	st.runStrategies = nil
	_, req = runVersion(t, st, atoRunBody, "run-reduced")
	if got := demandByType(req); !got[domain.RefForecast].Equal(decimal.RequireFromString("70")) {
		t.Fatalf("mts-net pegged demand %v; want the reduced 70", got)
	}
}

// onHandRunBody is atoRunBody for an item with 150 on hand.
var onHandRunBody = strings.Replace(atoRunBody, `"onHand":"0"`, `"onHand":"150"`, 1)

func totalOrdered(req store.RunRequest) decimal.Decimal {
	total := decimal.Zero
	for _, o := range req.Result.Orders {
		total = total.Add(o.Qty)
	}
	return total
}

// mts-gross plans the forecast whatever is on hand: forecast 100 and a sale of
// 40 with 150 on hand plan 100 under mts-gross (the sale takes on-hand), and
// nothing under mts-net, which nets the forecast against the stock too.
func TestRun_GrossForecastIgnoresOnHand(t *testing.T) {
	models := map[string]store.ForecastModel{"BASE": {Code: "BASE"}}
	st := &fakeStore{models: models, demand: novemberForecast(),
		runStrategies: map[string]domain.StrategySpec{fgID: domain.DefaultStrategy(domain.StrategyMTSGross)}}
	_, req := runVersion(t, st, onHandRunBody, "run-gross-stock")
	if got := totalOrdered(req); !got.Equal(decimal.RequireFromString("100")) {
		t.Fatalf("mts-gross ordered %s; want the whole forecast of 100", got)
	}
	st = &fakeStore{models: models, demand: novemberForecast()}
	_, req = runVersion(t, st, onHandRunBody, "run-net-stock")
	if len(req.Result.Orders) != 0 {
		t.Fatalf("mts-net orders = %+v; want none, on-hand covers 140", req.Result.Orders)
	}
}

// A sale larger than the forecast it can consume raises PLAN-0015 for the rest.
func TestRun_AssembleToOrderRaisesSalesOnTop(t *testing.T) {
	st := &fakeStore{models: map[string]store.ForecastModel{"BASE": {Code: "BASE"}}, demand: novemberForecast(),
		runStrategies: map[string]domain.StrategySpec{fgID: domain.DefaultStrategy(domain.StrategyATOFinal)}}
	body := strings.Replace(atoRunBody, `"qty":"40"`, `"qty":"130"`, 1)
	_, req := runVersion(t, st, body, "run-ato-on-top")
	var on []domain.Message
	for _, m := range req.Result.Messages {
		if m.Action == domain.ActionSalesOnTop {
			on = append(on, m)
		}
	}
	if len(on) != 1 || on[0].DemandRef != "SO-1" || !on[0].Qty.Equal(decimal.RequireFromString("30")) || on[0].ItemID != fgID {
		t.Fatalf("messages = %+v; want one sales-on-top for SO-1 of 30", req.Result.Messages)
	}
	// Forecast 100 is used up; the sale of 130 is the whole demand.
	if got := totalOrdered(req); !got.Equal(decimal.RequireFromString("130")) {
		t.Fatalf("ordered %s; want 130", got)
	}
	// A sale the forecast covers raises nothing.
	_, req = runVersion(t, st, atoRunBody, "run-ato-covered")
	for _, m := range req.Result.Messages {
		if m.Action == domain.ActionSalesOnTop {
			t.Fatalf("unexpected %+v", m)
		}
	}
}

// A strategy sent on the coverage block wins and works for an item with no
// stored coverage; an unknown code is a 400.
func TestRun_InlineStrategyOnCoverage(t *testing.T) {
	st := &fakeStore{models: map[string]store.ForecastModel{"BASE": {Code: "BASE"}}, demand: novemberForecast()}
	body := strings.Replace(atoRunBody, `"marginDays":0}`, `"marginDays":0,"strategy":"assemble-to-order-final"}`, 1)
	_, req := runVersion(t, st, body, "run-inline-ato")
	if got := demandByType(req); !got[domain.RefForecast].Equal(decimal.RequireFromString("60")) {
		t.Fatalf("pegged demand %v; want forecast 60 under the inline strategy", got)
	}
	// Inline wins over the stored coverage's strategy (here mto).
	st.runStrategies = map[string]domain.StrategySpec{fgID: domain.DefaultStrategy(domain.StrategyMTO)}
	_, req = runVersion(t, st, body, "run-inline-wins")
	if got := demandByType(req); !got[domain.RefForecast].Equal(decimal.RequireFromString("60")) {
		t.Fatalf("pegged demand %v; want the inline strategy to win", got)
	}
	bad := strings.Replace(atoRunBody, `"marginDays":0}`, `"marginDays":0,"strategy":"nope"}`, 1)
	if rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", bad, "planning.run", true, "run-bad"); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown strategy: %d %s", rec.Code, rec.Body.String())
	}
}
