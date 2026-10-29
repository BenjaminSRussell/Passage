package api

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
	"github.com/BenjaminSRussell/passage/services/projects/internal/store"
)

// This file gives fakeStore the simulation transfer and forecast methods of
// store.Store, built on the same domain functions as the Postgres store
// (simulation_test.go and forecast_test.go there check it against a database).

// transferState mirrors store.transferState (lock held).
func (f *fakeStore) transferState(projectID, simID string, resolutions map[string]string) (store.Transfer, error) {
	sim, err := f.wbsOf(projectID, simID)
	if err != nil {
		return store.Transfer{}, err
	}
	if sim.Plan.Kind != domain.KindSimulation {
		return store.Transfer{}, &domain.Error{Code: domain.CodePlanKind, Detail: "not a simulation"}
	}
	if sim.Plan.Locked {
		return store.Transfer{}, &domain.Error{Code: domain.CodePlanLocked, Detail: "locked"}
	}
	base, ok := f.planBase[simID]
	if !ok {
		return store.Transfer{}, &domain.Error{Code: domain.CodePlanKind, Detail: "no common ancestor"}
	}
	for _, pv := range f.plans {
		if pv.ProjectID == projectID && pv.Kind == domain.KindWorking {
			wk, _ := f.wbsOf(projectID, pv.ID)
			plan := domain.DiffSimulation(base, domain.ContentByCode(wk.Nodes, wk.Links), domain.ContentByCode(sim.Nodes, sim.Links), resolutions)
			return store.Transfer{Simulation: sim.Plan, Working: pv, Plan: plan}, nil
		}
	}
	return store.Transfer{}, &domain.Error{Code: domain.CodePlanKind, Detail: "no WORKING plan"}
}

func (f *fakeStore) PreviewTransfer(_ context.Context, projectID, simID string) (store.Transfer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.transferState(projectID, simID, nil)
}

func (f *fakeStore) TransferSimulation(_ context.Context, projectID, simID string, expected, workingVersion int, resolutions map[string]string) (store.Transfer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, err := f.transferState(projectID, simID, resolutions)
	if err != nil {
		return store.Transfer{}, err
	}
	if t.Simulation.Version != expected || (workingVersion != 0 && t.Working.Version != workingVersion) {
		return store.Transfer{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale version")
	}
	if err := domain.CheckTransfer(simID, t.Plan); err != nil {
		return store.Transfer{}, err
	}
	nodes := append([]domain.WBSNodeInput(nil), t.Plan.Nodes...)
	domain.NormalizeWBS(nodes)
	f.setWBS(t.Working.ID, nodes, t.Plan.Dependencies)
	now := time.Now().UTC()
	t.Working.Version++
	t.Simulation.Version++
	t.Simulation.Locked, t.Simulation.TransferredAt = true, &now
	f.plans[t.Working.ID], f.plans[simID] = t.Working, t.Simulation
	return t, nil
}

// forecastInput mirrors store.normalizeForecast (lock held).
func (f *fakeStore) forecastInput(projectID string, in store.ForecastInput) ([]domain.ForecastLine, error) {
	codes := map[string]bool{}
	for _, pv := range f.plans {
		if pv.ProjectID == projectID && pv.Kind == domain.KindWorking {
			for _, n := range f.nodes[pv.ID] {
				codes[n.WBSCode] = true
			}
		}
	}
	lines, fields := domain.NormalizeForecastLines(in.Lines, f.projects[projectID].Currency, codes)
	if !domain.ValidForecastName(in.Name) {
		if fields == nil {
			fields = map[string]string{}
		}
		fields["name"] = "must be 1 to 80 characters"
	}
	if len(fields) > 0 {
		return nil, validation(fields)
	}
	return lines, nil
}

func (f *fakeStore) nameTaken(projectID, name, except string) bool {
	for _, m := range f.forecasts {
		if m.ProjectID == projectID && m.Name == name && m.ID != except {
			return true
		}
	}
	return false
}

func (f *fakeStore) storeForecast(m domain.ForecastModel, lines []domain.ForecastLine) domain.ForecastModel {
	m.Lines, m.LineCount = lines, len(lines)
	m.TotalCost, m.TotalSales = domain.ForecastTotals(lines, m.Currency)
	f.forecasts[m.ID] = m
	return m
}

func (f *fakeStore) CreateForecastModel(ctx context.Context, projectID string, in store.ForecastInput, key string) (domain.ForecastModel, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	proj, ok := f.projects[projectID]
	if !ok {
		return domain.ForecastModel{}, false, notFoundProblem("project")
	}
	lines, err := f.forecastInput(projectID, in)
	if err != nil {
		return domain.ForecastModel{}, false, err
	}
	if id, ok := f.fcKeys[key]; ok {
		m := f.forecasts[id]
		if m.ProjectID != projectID || m.Name != in.Name || len(m.Lines) != len(lines) {
			return domain.ForecastModel{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "key reused")
		}
		return m, false, nil
	}
	if f.nameTaken(projectID, in.Name, "") {
		return domain.ForecastModel{}, false, &domain.Error{Code: domain.CodeForecastDuplicate, Detail: "name taken"}
	}
	p, _ := tenant.FromContext(ctx)
	now := time.Now().UTC()
	m := domain.ForecastModel{ID: ids.New(), ProjectID: projectID, Name: in.Name, Currency: proj.Currency, Version: 1,
		CreatedBy: p.UserID, CreatedAt: now, UpdatedAt: now}
	f.fcKeys[key] = m.ID
	return f.storeForecast(m, lines), true, nil
}

func (f *fakeStore) ListForecastModels(_ context.Context, projectID string) ([]domain.ForecastModel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.projects[projectID]; !ok {
		return nil, notFoundProblem("project")
	}
	out := []domain.ForecastModel{}
	for _, m := range f.forecasts {
		if m.ProjectID == projectID {
			m.Lines = nil
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeStore) GetForecast(_ context.Context, projectID, modelID string) (store.ForecastComparison, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.forecasts[modelID]
	if !ok || m.ProjectID != projectID {
		return store.ForecastComparison{}, notFoundProblem("forecast model")
	}
	var actuals []domain.ActualTotal
	for _, t := range f.txns {
		if t.ProjectID == projectID {
			actuals = append(actuals, domain.ActualTotal{Period: t.TxnDate[:7], TxnType: t.TxnType, Quantity: decimal.RequireFromString(t.Quantity),
				CostAmount: decimal.RequireFromString(t.CostAmount), SalesAmount: decimal.RequireFromString(t.SalesAmount)})
		}
	}
	return store.ForecastComparison{Model: m, Rows: domain.CompareForecast(m.Lines, actuals, m.Currency)}, nil
}

func (f *fakeStore) ReplaceForecast(_ context.Context, projectID, modelID string, expected int, in store.ForecastInput) (domain.ForecastModel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.forecasts[modelID]
	if !ok || m.ProjectID != projectID {
		return domain.ForecastModel{}, notFoundProblem("forecast model")
	}
	if m.Version != expected {
		return domain.ForecastModel{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale version")
	}
	lines, err := f.forecastInput(projectID, in)
	if err != nil {
		return domain.ForecastModel{}, err
	}
	if f.nameTaken(projectID, in.Name, modelID) {
		return domain.ForecastModel{}, &domain.Error{Code: domain.CodeForecastDuplicate, Detail: "name taken"}
	}
	m.Name, m.Version, m.UpdatedAt = in.Name, m.Version+1, time.Now().UTC()
	return f.storeForecast(m, lines), nil
}
