package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
)

// Default names of the forecast model's UNIQUE constraints.
const (
	forecastKeyConstraint  = "project_forecast_model_tenant_id_idempotency_key_key"
	forecastNameConstraint = "project_forecast_model_tenant_id_project_id_name_key"
)

// ForecastInput is the body of a forecast model create or replace: its name and
// the whole set of lines (FR-41.20).
type ForecastInput struct {
	Name  string
	Lines []domain.ForecastLine
}

// ForecastComparison is a forecast model with its lines and the forecast-vs-actual
// rows by period and transaction type (§8.41.14 "Forecast vs actual").
type ForecastComparison struct {
	Model domain.ForecastModel
	Rows  []domain.ForecastRow
}

const forecastColumns = `m.id::text, m.project_id, m.name, m.currency, m.version, m.created_by, m.created_at, m.updated_at, m.is_active,
	(select count(*) from project_forecast_line l where l.tenant_id = m.tenant_id and l.model_id = m.id),
	(select coalesce(sum(l.cost_amount), 0)::text from project_forecast_line l where l.tenant_id = m.tenant_id and l.model_id = m.id),
	(select coalesce(sum(l.sales_amount), 0)::text from project_forecast_line l where l.tenant_id = m.tenant_id and l.model_id = m.id)`

func scanForecast(row pgx.Row) (domain.ForecastModel, error) {
	var m domain.ForecastModel
	err := row.Scan(&m.ID, &m.ProjectID, &m.Name, &m.Currency, &m.Version, &m.CreatedBy, &m.CreatedAt, &m.UpdatedAt, &m.Active,
		&m.LineCount, &m.TotalCost, &m.TotalSales)
	if err != nil {
		return m, err
	}
	if m.TotalCost, err = amount(m.TotalCost, m.Currency); err != nil {
		return m, err
	}
	m.TotalSales, err = amount(m.TotalSales, m.Currency)
	return m, err
}

// forecastProject returns the project's currency and the WBS codes of its
// WORKING plan (the nodes a forecast line may name), or 404. forUpdate locks
// the project row, which serialises creates on the project so that concurrent
// retries of one Idempotency-Key replay instead of colliding on the name.
func forecastProject(ctx context.Context, tx pgx.Tx, tenantID, projectID string, forUpdate bool) (string, map[string]bool, error) {
	q := `select currency from project where tenant_id = $1 and id = $2`
	if forUpdate {
		q += ` for update`
	}
	var currency string
	err := tx.QueryRow(ctx, q, tenantID, projectID).Scan(&currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, notFound("project")
	}
	if err != nil {
		return "", nil, err
	}
	rows, err := tx.Query(ctx, `select n.wbs_code from wbs_node n join plan_version v on v.tenant_id = n.tenant_id and v.id = n.plan_version_id
		where n.tenant_id = $1 and v.project_id = $2 and v.kind = 'WORKING'`, tenantID, projectID)
	if err != nil {
		return "", nil, err
	}
	codes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", nil, err
	}
	set := make(map[string]bool, len(codes))
	for _, c := range codes {
		set[c] = true
	}
	return currency, set, nil
}

// normalizeForecast validates a forecast body against the project (400 with
// field errors) and returns its numbered, rounded lines.
func normalizeForecast(in ForecastInput, currency string, codes map[string]bool) ([]domain.ForecastLine, error) {
	lines, fields := domain.NormalizeForecastLines(in.Lines, currency, codes)
	if !domain.ValidForecastName(in.Name) {
		if fields == nil {
			fields = map[string]string{}
		}
		fields["name"] = "must be 1 to 80 characters"
	}
	if len(fields) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, CodeValidation, "request failed validation")
		p.Errors = fields
		return nil, p
	}
	return lines, nil
}

func insertForecastLines(ctx context.Context, tx pgx.Tx, tenantID, modelID string, lines []domain.ForecastLine) error {
	b := &pgx.Batch{}
	for _, l := range lines {
		b.Queue(`insert into project_forecast_line (tenant_id, model_id, line_no, period, wbs_code, txn_type, quantity, cost_amount, sales_amount)
			values ($1, $2::uuid, $3, $4, $5, $6, $7::numeric, $8::numeric, $9::numeric)`,
			tenantID, modelID, l.LineNo, l.Period, l.WBSCode, string(l.TxnType), l.Quantity, l.CostAmount, l.SalesAmount)
	}
	return runBatch(ctx, tx, b)
}

func readForecastLines(ctx context.Context, tx pgx.Tx, tenantID string, m domain.ForecastModel) ([]domain.ForecastLine, error) {
	rows, err := tx.Query(ctx, `select line_no, period, wbs_code, txn_type, quantity::text, cost_amount::text, sales_amount::text
		from project_forecast_line where tenant_id = $1 and model_id = $2::uuid order by line_no`, tenantID, m.ID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.ForecastLine, error) {
		var l domain.ForecastLine
		var typ string
		if err := r.Scan(&l.LineNo, &l.Period, &l.WBSCode, &typ, &l.Quantity, &l.CostAmount, &l.SalesAmount); err != nil {
			return l, err
		}
		l.TxnType = domain.TxnType(typ)
		var err error
		if l.CostAmount, err = amount(l.CostAmount, m.Currency); err != nil {
			return l, err
		}
		l.SalesAmount, err = amount(l.SalesAmount, m.Currency)
		return l, err
	})
}

func readForecast(ctx context.Context, tx pgx.Tx, tenantID, projectID, modelID string, forUpdate bool) (domain.ForecastModel, error) {
	q := `select ` + forecastColumns + ` from project_forecast_model m where m.tenant_id = $1 and m.project_id = $2 and m.id = $3::uuid`
	if forUpdate {
		q += ` for update`
	}
	m, err := scanForecast(tx.QueryRow(ctx, q, tenantID, projectID, modelID))
	if errors.Is(err, pgx.ErrNoRows) {
		return m, notFound("forecast model")
	}
	if err != nil {
		return m, err
	}
	m.Lines, err = readForecastLines(ctx, tx, tenantID, m)
	return m, err
}

// sameForecast reports whether a replayed Idempotency-Key carries the same
// request as the stored model (both sides normalized).
func sameForecast(stored domain.ForecastModel, projectID, name string, lines []domain.ForecastLine) bool {
	if stored.ProjectID != projectID || stored.Name != name || len(stored.Lines) != len(lines) {
		return false
	}
	for i, l := range lines {
		s := stored.Lines[i]
		if s.Period != l.Period || s.TxnType != l.TxnType || !eqPtr(s.WBSCode, l.WBSCode) || s.Quantity != l.Quantity ||
			s.CostAmount != l.CostAmount || s.SalesAmount != l.SalesAmount {
			return false
		}
	}
	return true
}

func forecastNameTaken(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == forecastNameConstraint {
		return &domain.Error{Code: domain.CodeForecastDuplicate, Detail: "the project already has a forecast model with this name"}
	}
	return err
}

// CreateForecastModel adds a forecast model with its lines to a project
// (FR-41.20). Lines are validated against the project currency and its WORKING
// plan's WBS codes, under the project row lock. A replayed Idempotency-Key
// returns the stored model with created=false; a different body under the same key is idempotency_key_reuse.
// A second model with the same name is PROJ-FC-001.
func (s *Postgres) CreateForecastModel(ctx context.Context, projectID string, in ForecastInput, idempotencyKey string) (domain.ForecastModel, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.ForecastModel
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		currency, codes, err := forecastProject(ctx, tx, p.TenantID, projectID, true)
		if err != nil {
			return err
		}
		lines, err := normalizeForecast(in, currency, codes)
		if err != nil {
			return err
		}
		var replayID, replayProject string
		err = tx.QueryRow(ctx, `select id::text, project_id from project_forecast_model where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, idempotencyKey).Scan(&replayID, &replayProject)
		switch {
		case err == nil:
			stored, err := readForecast(ctx, tx, p.TenantID, replayProject, replayID, false)
			if err != nil {
				return err
			}
			if !sameForecast(stored, projectID, in.Name, lines) {
				return idempotencyReuse()
			}
			out = stored
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into project_forecast_model (tenant_id, id, project_id, name, currency, created_by, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7)`, p.TenantID, id, projectID, in.Name, currency, p.UserID, idempotencyKey); err != nil {
			return err
		}
		if err := insertForecastLines(ctx, tx, p.TenantID, id, lines); err != nil {
			return err
		}
		created = true
		out, err = readForecast(ctx, tx, p.TenantID, projectID, id, false)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == forecastKeyConstraint {
		return domain.ForecastModel{}, false, idempotencyReuse()
	}
	return out, created, forecastNameTaken(err)
}

// ListForecastModels returns the forecast models of a project by name, with
// line counts and totals but without lines. A project that is not visible is 404.
func (s *Postgres) ListForecastModels(ctx context.Context, projectID string) ([]domain.ForecastModel, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.ForecastModel{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockProject(ctx, tx, p.TenantID, projectID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+forecastColumns+` from project_forecast_model m
			where m.tenant_id = $1 and m.project_id = $2 order by m.name`, p.TenantID, projectID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.ForecastModel, error) { return scanForecast(r) })
		return err
	})
	return out, err
}

// GetForecast returns a forecast model with its lines and the forecast-vs-actual
// comparison (FR-41.20, `GET /{id}/forecasts/{modelId}`). Actuals are the
// recorded project transactions summed by calendar month and type; they are not
// yet ledger-posted (D2b is a handoff).
func (s *Postgres) GetForecast(ctx context.Context, projectID, modelID string) (ForecastComparison, error) {
	p, _ := tenant.FromContext(ctx)
	var out ForecastComparison
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		m, err := readForecast(ctx, tx, p.TenantID, projectID, modelID, false)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select to_char(txn_date, 'YYYY-MM'), txn_type, sum(quantity)::text, sum(cost_amount)::text, sum(sales_amount)::text
			from project_transaction where tenant_id = $1 and project_id = $2 group by 1, 2`, p.TenantID, projectID)
		if err != nil {
			return err
		}
		actuals, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.ActualTotal, error) {
			var a domain.ActualTotal
			var typ, q, c, sa string
			if err := r.Scan(&a.Period, &typ, &q, &c, &sa); err != nil {
				return a, err
			}
			a.TxnType = domain.TxnType(typ)
			a.Quantity, a.CostAmount, a.SalesAmount = decimal.RequireFromString(q), decimal.RequireFromString(c), decimal.RequireFromString(sa)
			return a, nil
		})
		if err != nil {
			return err
		}
		out = ForecastComparison{Model: m, Rows: domain.CompareForecast(m.Lines, actuals, m.Currency)}
		return nil
	})
	return out, err
}

// ReplaceForecast renames a forecast model and replaces all of its lines under
// If-Match (expected; 412 when stale), bumping its version. A name taken by
// another model of the project is PROJ-FC-001.
func (s *Postgres) ReplaceForecast(ctx context.Context, projectID, modelID string, expected int, in ForecastInput) (domain.ForecastModel, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.ForecastModel
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := readForecast(ctx, tx, p.TenantID, projectID, modelID, true)
		if err != nil {
			return err
		}
		if cur.Version != expected {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the forecast model changed since it was read; re-read and retry")
		}
		_, codes, err := forecastProject(ctx, tx, p.TenantID, projectID, false)
		if err != nil {
			return err
		}
		lines, err := normalizeForecast(in, cur.Currency, codes)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from project_forecast_line where tenant_id = $1 and model_id = $2::uuid`, p.TenantID, modelID); err != nil {
			return err
		}
		if err := insertForecastLines(ctx, tx, p.TenantID, modelID, lines); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update project_forecast_model set name = $3, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2::uuid`, p.TenantID, modelID, in.Name); err != nil {
			return err
		}
		out, err = readForecast(ctx, tx, p.TenantID, projectID, modelID, false)
		return err
	})
	return out, forecastNameTaken(err)
}
