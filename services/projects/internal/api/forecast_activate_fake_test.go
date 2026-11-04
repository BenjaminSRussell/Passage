package api

import (
	"context"
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
	"github.com/BenjaminSRussell/passage/services/projects/internal/store"
)

func (f *fakeStore) ActivateForecastModel(_ context.Context, projectID, modelID string, expected int) (domain.ForecastModel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.forecasts[modelID]
	if !ok || m.ProjectID != projectID {
		return domain.ForecastModel{}, notFoundProblem("forecast model")
	}
	if m.Version != expected {
		return domain.ForecastModel{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale version")
	}
	for id, other := range f.forecasts {
		if other.ProjectID == projectID && other.Active {
			other.Active, other.Version = false, other.Version+1
			f.forecasts[id] = other
		}
	}
	m = f.forecasts[modelID]
	m.Active, m.Version = true, m.Version+1
	f.forecasts[modelID] = m
	return m, nil
}
