package store

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
)

// ActivateForecastModel designates the model whose lines after a period are the project's estimate to complete for GL
// revenue recognition (D4c); any other model of the project stops being active. If-Match carries the model version.
func (s *Postgres) ActivateForecastModel(ctx context.Context, projectID, modelID string, expected int) (domain.ForecastModel, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.ForecastModel
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, _, err := forecastProject(ctx, tx, p.TenantID, projectID, true); err != nil {
			return err
		}
		cur, err := readForecast(ctx, tx, p.TenantID, projectID, modelID, true)
		if err != nil {
			return err
		}
		if cur.Version != expected {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the forecast model changed since it was read; re-read and retry")
		}
		if _, err := tx.Exec(ctx, `update project_forecast_model set is_active = false, version = version + 1, updated_at = now()
			where tenant_id = $1 and project_id = $2 and is_active and id <> $3::uuid`, p.TenantID, projectID, modelID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update project_forecast_model set is_active = true, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2::uuid and not is_active`, p.TenantID, modelID); err != nil {
			return err
		}
		out, err = readForecast(ctx, tx, p.TenantID, projectID, modelID, false)
		return err
	})
	return out, err
}
