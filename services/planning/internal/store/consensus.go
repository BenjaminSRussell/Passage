package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	planevents "github.com/BenjaminSRussell/passage/services/planning/internal/events"
)

// consensusProblem maps a domain consensus error to its problem detail.
func consensusProblem(err error, m ForecastModel) error {
	switch {
	case errors.Is(err, domain.ErrConsensusSoD):
		return httpx.NewProblem(http.StatusForbidden, CodeConsensusSoD,
			"the consensus must be decided by an identified user who neither submitted nor edited this version of "+m.Code)
	case errors.Is(err, domain.ErrConsensusEmpty):
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeConsensusEmpty, "forecast model "+m.Code+" has no cells to submit")
	case errors.Is(err, domain.ErrConsensusState):
		return httpx.NewProblem(http.StatusConflict, CodeConsensusState, "forecast model "+m.Code+" is "+m.Status+"; the transition is not allowed")
	}
	return err
}

// lockExistingModel locks a model for a consensus transition: 404 when it does
// not exist, 412 when expectedVersion is stale. Unlike a cell write it never
// creates the model.
func lockExistingModel(ctx context.Context, tx pgx.Tx, tenantID, code string, expectedVersion int) (ForecastModel, error) {
	m, err := scanModel(tx.QueryRow(ctx, modelSelect+` where tenant_id = $1 and code = $2 for update`, tenantID, code))
	if errors.Is(err, pgx.ErrNoRows) {
		return ForecastModel{}, forecastNotFound()
	}
	if err != nil {
		return ForecastModel{}, err
	}
	if m.Version != expectedVersion {
		return ForecastModel{}, versionConflict("forecast model", m.Version)
	}
	return m, nil
}

// SubmitForecastConsensus submits a draft model with cells for approval
// (FR-22.9): the cells lock and the submitter is recorded for the
// segregation-of-duties check of the decision.
func (s *Postgres) SubmitForecastConsensus(ctx context.Context, modelCode string, expectedVersion int) (ForecastModel, error) {
	p, _ := tenant.FromContext(ctx)
	var out ForecastModel
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		m, err := lockExistingModel(ctx, tx, p.TenantID, modelCode, expectedVersion)
		if err != nil {
			return err
		}
		var cells int
		if err := tx.QueryRow(ctx, `select count(*) from forecast_line where tenant_id = $1 and model_id = $2::uuid`, p.TenantID, m.id).Scan(&cells); err != nil {
			return err
		}
		status, err := domain.SubmitConsensus(m.Status, p.UserID, cells)
		if err != nil {
			return consensusProblem(err, m)
		}
		out, err = scanModel(tx.QueryRow(ctx, `update forecast_model set status = $3, submitted_by = $4, submitted_at = now(),
			decided_by = null, version = version + 1 where tenant_id = $1 and id = $2::uuid
			returning id::text, code, version, reduction_principle, reduction_percent::text, status, submitted_by, decided_by, null::text`,
			p.TenantID, m.id, status, p.UserID))
		return err
	})
	return out, err
}

// DecideForecastConsensus approves (publishes) or rejects a submitted model.
// The approver must be neither the submitter nor a writer of any of its cells
// (4.22.17). Rejecting returns the model to draft. Publishing locks it for
// good, marks its cells as consensus and writes forecast.published.v1 to the
// outbox in the same transaction; the publication summary is returned.
func (s *Postgres) DecideForecastConsensus(ctx context.Context, modelCode string, expectedVersion int, approve bool) (ForecastModel, *ForecastPublication, error) {
	p, _ := tenant.FromContext(ctx)
	var out ForecastModel
	var pub *ForecastPublication
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		m, err := lockExistingModel(ctx, tx, p.TenantID, modelCode, expectedVersion)
		if err != nil {
			return err
		}
		var editors []string
		if err := tx.QueryRow(ctx, `select coalesce(array_agg(distinct updated_by), '{}') from forecast_line
			where tenant_id = $1 and model_id = $2::uuid and updated_by is not null`, p.TenantID, m.id).Scan(&editors); err != nil {
			return err
		}
		submitter := ""
		if m.SubmittedBy != nil {
			submitter = *m.SubmittedBy
		}
		status, err := domain.DecideConsensus(m.Status, submitter, editors, p.UserID, approve)
		if err != nil {
			return consensusProblem(err, m)
		}
		if !approve {
			out, err = scanModel(tx.QueryRow(ctx, `update forecast_model set status = $3, submitted_by = null, submitted_at = null,
				decided_by = $4, version = version + 1 where tenant_id = $1 and id = $2::uuid
				returning id::text, code, version, reduction_principle, reduction_percent::text, status, submitted_by, decided_by, null::text`,
				p.TenantID, m.id, status, p.UserID))
			return err
		}
		var summary ForecastPublication
		if err := tx.QueryRow(ctx, `select count(distinct (item_id, site_id)), count(distinct period_start),
			to_char(min(period_start), 'YYYY-MM-DD'), to_char(max(period_end), 'YYYY-MM-DD')
			from forecast_line where tenant_id = $1 and model_id = $2::uuid`, p.TenantID, m.id).
			Scan(&summary.Items, &summary.HorizonPeriods, &summary.PeriodFrom, &summary.PeriodTo); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update forecast_line set source = 'consensus' where tenant_id = $1 and model_id = $2::uuid`, p.TenantID, m.id); err != nil {
			return err
		}
		out, err = scanModel(tx.QueryRow(ctx, `update forecast_model set status = $3, decided_by = $4, published_at = now(),
			version = version + 1 where tenant_id = $1 and id = $2::uuid
			returning id::text, code, version, reduction_principle, reduction_percent::text, status, submitted_by, decided_by,
			to_char(published_at at time zone 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`, p.TenantID, m.id, status, p.UserID))
		if err != nil {
			return err
		}
		evt, err := events.New(planevents.ForecastPublished, eventSource, m.id, p.TenantID, planevents.ForecastPublishedData{
			ModelID: m.id, ModelCode: m.Code, Version: out.Version, Items: summary.Items, HorizonPeriods: summary.HorizonPeriods,
			PeriodFrom: summary.PeriodFrom, PeriodTo: summary.PeriodTo, SubmittedBy: submitter, ApprovedBy: p.UserID,
		})
		if err != nil {
			return err
		}
		if err := outbox.Enqueue(ctx, tx, "forecast-model", m.id, evt); err != nil {
			return err
		}
		pub = &summary
		return nil
	})
	if err != nil {
		return ForecastModel{}, nil, err
	}
	return out, pub, nil
}
