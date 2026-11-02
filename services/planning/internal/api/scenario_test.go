package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

const (
	scenarioPath = "/v1/planning/scenarios"
	dutySim      = "planning.simulate"
)

func (f *fakeStore) ListScenarios(_ context.Context, q store.ScenarioQuery) ([]store.Scenario, error) {
	f.lastLimit = q.Limit
	out := []store.Scenario{}
	for _, sc := range f.scenarios {
		if q.PlanCode == "" || sc.Spec.PlanCode == q.PlanCode {
			out = append(out, sc)
		}
	}
	return out, nil
}

func (f *fakeStore) GetScenario(_ context.Context, code string) (store.Scenario, error) {
	for _, sc := range f.scenarios {
		if sc.Spec.Code == code {
			return sc, nil
		}
	}
	return store.Scenario{}, httpx.NewProblem(http.StatusNotFound, store.CodeScenarioNotFound, "planning scenario not found")
}

func (f *fakeStore) CreateScenario(_ context.Context, req store.ScenarioCreate) (store.Scenario, bool, error) {
	if hash, ok := f.scenarioKey[req.IdempotencyKey]; ok && req.IdempotencyKey != "" {
		if hash != req.InputHash {
			return store.Scenario{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reuse")
		}
		sc, err := f.GetScenario(context.Background(), req.Spec.Code)
		return sc, true, err
	}
	if _, err := f.GetScenario(context.Background(), req.Spec.Code); err == nil {
		return store.Scenario{}, false, httpx.NewProblem(http.StatusConflict, store.CodeScenarioExists, "exists")
	}
	if req.IdempotencyKey != "" {
		if f.scenarioKey == nil {
			f.scenarioKey = map[string]string{}
		}
		f.scenarioKey[req.IdempotencyKey] = req.InputHash
	}
	sc := store.Scenario{ID: testkit.TenantID(len(f.scenarios) + 60), Version: 1, Spec: req.Spec}
	f.scenarios = append(f.scenarios, sc)
	return sc, false, nil
}

func (f *fakeStore) UpdateScenario(_ context.Context, expected int, spec domain.ScenarioSpec) (store.Scenario, error) {
	for i, sc := range f.scenarios {
		if sc.Spec.Code != spec.Code {
			continue
		}
		if sc.Version != expected {
			return store.Scenario{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
		}
		f.scenarios[i] = store.Scenario{ID: sc.ID, Version: sc.Version + 1, Spec: spec}
		return f.scenarios[i], nil
	}
	return store.Scenario{}, httpx.NewProblem(http.StatusNotFound, store.CodeScenarioNotFound, "planning scenario not found")
}

func (f *fakeStore) RunSimulation(_ context.Context, req store.SimulationRequest) (store.Version, bool, error) {
	f.lastSim = req
	f.simulations++
	return store.Version{ID: testkit.TenantID(70), PlanCode: "MAIN", Number: 2, Status: "complete", Kind: "simulation", ScenarioCode: req.ScenarioCode,
		AsOf: req.AsOf.Format("2006-01-02"), ItemsPlanned: req.ItemsPlanned, PlannedOrders: len(req.Result.Orders), PlanHash: req.Result.Hash}, false, nil
}

const upBody = `{"code":"UP20","planCode":"MAIN","name":"Demand +20%","demandFactor":"1.2"}`

func TestScenario_CreateGetUpdateAndDuties(t *testing.T) {
	st := &fakeStore{}
	if rec := do(t, st, http.MethodPost, scenarioPath, upBody, "planning.read", true, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("create without duty: %d", rec.Code)
	}
	if rec := do(t, st, http.MethodPost, scenarioPath, upBody, dutySim, false, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("create without tenant: %d", rec.Code)
	}
	rec := do(t, st, http.MethodPost, scenarioPath, upBody, dutySim, true, "k1")
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %s etag %q", rec.Code, rec.Body.String(), rec.Header().Get("ETag"))
	}
	var dto scenarioDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if !dto.Active || !dto.DemandFactor.Equal(decimal.RequireFromString("1.2")) || dto.ItemFactors == nil {
		t.Errorf("dto = %+v", dto)
	}
	if rec := do(t, st, http.MethodPost, scenarioPath, upBody, dutySim, true, "k1"); rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Errorf("replay: %d %s", rec.Code, rec.Header().Get("Idempotent-Replayed"))
	}
	other := strings.Replace(upBody, "1.2", "1.3", 1)
	if rec := do(t, st, http.MethodPost, scenarioPath, other, dutySim, true, "k1"); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("key reuse: %d", rec.Code)
	}
	if rec := do(t, st, http.MethodPost, scenarioPath, upBody, dutySim, true, "k2"); rec.Code != http.StatusConflict {
		t.Errorf("duplicate code: %d", rec.Code)
	}
	if rec := do(t, st, http.MethodGet, scenarioPath+"/UP20", "", "planning.read", true, ""); rec.Code != http.StatusOK {
		t.Errorf("get: %d", rec.Code)
	}
	if rec := do(t, st, http.MethodGet, scenarioPath+"/NOPE", "", "planning.read", true, ""); rec.Code != http.StatusNotFound {
		t.Errorf("get unknown: %d", rec.Code)
	}
	if rec := do(t, st, http.MethodGet, scenarioPath+"?planCode=MAIN&limit=5", "", "planning.read", true, ""); rec.Code != http.StatusOK || st.lastLimit != 5 {
		t.Errorf("list: %d limit %d", rec.Code, st.lastLimit)
	}
}

func TestScenario_ValidationAndUpdate(t *testing.T) {
	st := &fakeStore{}
	rec := do(t, st, http.MethodPost, scenarioPath, `{"code":"x","planCode":"MAIN","name":"","demandFactor":"-1"}`, dutySim, true, "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "demandFactor") {
		t.Fatalf("invalid create: %d %s", rec.Code, rec.Body.String())
	}
	do(t, st, http.MethodPost, scenarioPath, upBody, dutySim, true, "")
	if rec := doIfMatch(t, st, http.MethodPut, scenarioPath+"/UP20", `{"name":"Up","demandFactor":"1.5","active":false}`, "", dutySim); rec.Code != http.StatusPreconditionRequired {
		t.Errorf("update without If-Match: %d", rec.Code)
	}
	rec = doIfMatch(t, st, http.MethodPut, scenarioPath+"/UP20", `{"name":"Up","demandFactor":"1.5","active":false}`, `"1"`, dutySim)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if st.scenarios[0].Spec.PlanCode != "MAIN" || st.scenarios[0].Spec.Active {
		t.Errorf("stored = %+v", st.scenarios[0].Spec)
	}
	if rec := doIfMatch(t, st, http.MethodPut, scenarioPath+"/UP20", `{"name":"Up"}`, `"1"`, dutySim); rec.Code != http.StatusPreconditionFailed {
		t.Errorf("stale update: %d", rec.Code)
	}
}

func TestScenarioRun_AppliesScenarioAndStoresSimulation(t *testing.T) {
	st := &fakeStore{}
	do(t, st, http.MethodPost, scenarioPath, upBody, dutySim, true, "")
	if rec := do(t, st, http.MethodPost, scenarioPath+"/UP20:run", runBody, "planning.run", true, "r1"); rec.Code != http.StatusForbidden {
		t.Fatalf("run needs the simulate duty: %d", rec.Code)
	}
	if rec := do(t, st, http.MethodPost, scenarioPath+"/UP20:run", runBody, dutySim, true, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("run needs a key: %d", rec.Code)
	}
	rec := do(t, st, http.MethodPost, scenarioPath+"/UP20:run", runBody, dutySim, true, "r1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}
	var v store.Version
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.Kind != "simulation" || v.ScenarioCode != "UP20" || v.Status != "complete" {
		t.Errorf("version = %+v", v)
	}
	// Demand 120 x 1.2 = 144 against 10 net on hand: the order is 144 - 20 + 10 = 134.
	total := decimal.Zero
	for _, o := range st.lastSim.Result.Orders {
		total = total.Add(o.Qty)
	}
	if !total.Equal(decimal.NewFromInt(134)) {
		t.Errorf("simulated order qty = %s, want 134", total)
	}
	if st.last.PlanCode != "" {
		t.Error("a simulation must not go through RunPlan")
	}
	if rec := do(t, st, http.MethodPost, scenarioPath+"/NOPE:run", runBody, dutySim, true, "r2"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown scenario: %d", rec.Code)
	}
}

func TestScenarioRun_InactiveScenarioIsRefused(t *testing.T) {
	st := &fakeStore{}
	do(t, st, http.MethodPost, scenarioPath, `{"code":"OFF","planCode":"MAIN","name":"Off","active":false}`, dutySim, true, "")
	rec := do(t, st, http.MethodPost, scenarioPath+"/OFF:run", runBody, dutySim, true, "r1")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodeScenarioInactive) || st.simulations != 0 {
		t.Fatalf("inactive: %d %s", rec.Code, rec.Body.String())
	}
}

// A scenario's added sales consume the forecast of an assemble-to-order-final
// item like its other sales (BR-22.13): forecast 100 plus an added sale of 40
// plans 60 + 40 = 100, not 140, and the added line is not scaled by the factor.
func TestScenarioRun_AddedSalesConsumeAssembleToOrderForecast(t *testing.T) {
	st := &fakeStore{models: map[string]store.ForecastModel{"BASE": {Code: "BASE"}}, demand: novemberForecast(),
		runStrategies: map[string]domain.StrategySpec{fgID: domain.DefaultStrategy(domain.StrategyATOFinal)}}
	sc := `{"code":"ADD40","planCode":"MAIN","name":"Added sale","demandFactor":"1","addedDemand":[{"itemId":"` + fgID + `","date":"2026-11-20","qty":"40"}]}`
	if rec := do(t, st, http.MethodPost, scenarioPath, sc, dutySim, true, ""); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	body := strings.Replace(atoRunBody, `"demand":[{"ref":"SO-1","refType":"sales-line","date":"2026-11-20","qty":"40"}]`, `"demand":[]`, 1)
	if rec := do(t, st, http.MethodPost, scenarioPath+"/ADD40:run", body, dutySim, true, "s1"); rec.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}
	total := decimal.Zero
	for _, o := range st.lastSim.Result.Orders {
		total = total.Add(o.Qty)
	}
	if !total.Equal(decimal.NewFromInt(100)) {
		t.Errorf("simulated order qty = %s, want 100 (forecast 60 + added sale 40)", total)
	}
}

// With a factor the added lines consume the scaled forecast (BR-22.13 with
// BR-22.14): factor 2 makes the forecast 200 and the real sale of 40 80, an
// added sale of 40 (never scaled) takes 40 more, so 80 + 40 + 80 = 200 is
// planned, not the 2 × 60 + 40 of consuming before scaling. An added sale of
// 230 finds 120 of forecast left after the real sale, so PLAN-0015 reports 110
// for it, unscaled.
func TestScenarioRun_FactorScalesForecastBeforeAddedSalesConsumeIt(t *testing.T) {
	cases := []struct {
		added, wantTotal, wantOnTop string
	}{
		{"40", "200", ""},
		{"230", "310", "110"},
	}
	for _, c := range cases {
		t.Run(c.added, func(t *testing.T) {
			st := &fakeStore{models: map[string]store.ForecastModel{"BASE": {Code: "BASE"}}, demand: novemberForecast(),
				runStrategies: map[string]domain.StrategySpec{fgID: domain.DefaultStrategy(domain.StrategyATOFinal)}}
			sc := `{"code":"X2","planCode":"MAIN","name":"Doubled","demandFactor":"2","addedDemand":[{"itemId":"` + fgID + `","date":"2026-11-21","qty":"` + c.added + `"}]}`
			if rec := do(t, st, http.MethodPost, scenarioPath, sc, dutySim, true, ""); rec.Code != http.StatusCreated {
				t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
			}
			if rec := do(t, st, http.MethodPost, scenarioPath+"/X2:run", atoRunBody, dutySim, true, "s-"+c.added); rec.Code != http.StatusCreated {
				t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
			}
			total := decimal.Zero
			for _, o := range st.lastSim.Result.Orders {
				total = total.Add(o.Qty)
			}
			if !total.Equal(decimal.RequireFromString(c.wantTotal)) {
				t.Errorf("simulated order qty = %s, want %s", total, c.wantTotal)
			}
			var onTop []domain.Message
			for _, m := range st.lastSim.Result.Messages {
				if m.Action == domain.ActionSalesOnTop {
					onTop = append(onTop, m)
				}
			}
			switch {
			case c.wantOnTop == "" && len(onTop) != 0:
				t.Errorf("unexpected sales-on-top %+v", onTop)
			case c.wantOnTop != "" && (len(onTop) != 1 || onTop[0].DemandRef != "SC:X2:1" || !onTop[0].Qty.Equal(decimal.RequireFromString(c.wantOnTop))):
				t.Errorf("sales-on-top = %+v; want one for SC:X2:1 of %s", onTop, c.wantOnTop)
			}
		})
	}
}
