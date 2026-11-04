package store

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// CodeCostRateExists is SF-0170: the worker already has a cost rate effective on that date.
// Rates are append-only; a change takes effect on a later date (409).
const CodeCostRateExists = "SF-0170"

// CostRate is one dated cost rate of a worker (per hour, in the worker's currency).
type CostRate struct {
	ID            string          `json:"id"`
	WorkerID      string          `json:"workerId"`
	EffectiveFrom string          `json:"effectiveFrom"`
	CostRate      decimal.Decimal `json:"costRate"`
	Note          *string         `json:"note"`
	CreatedBy     string          `json:"createdBy"`
	CreatedAt     time.Time       `json:"createdAt"`
}

// CostRateInput is a rate to add. EffectiveFrom is a calendar date (YYYY-MM-DD).
type CostRateInput struct {
	EffectiveFrom string
	CostRate      decimal.Decimal
	Note          *string
}

const costRateColumns = `id::text, worker_id::text, to_char(effective_from, 'YYYY-MM-DD'), cost_rate::text, note, created_by, created_at`

func scanCostRate(row pgx.Row) (CostRate, error) {
	var c CostRate
	var rate string
	if err := row.Scan(&c.ID, &c.WorkerID, &c.EffectiveFrom, &rate, &c.Note, &c.CreatedBy, &c.CreatedAt); err != nil {
		return CostRate{}, err
	}
	var err error
	c.CostRate, err = decimal.NewFromString(rate)
	return c, err
}

// AddCostRate records that the worker costs in.CostRate an hour from in.EffectiveFrom. The worker
// row is locked, so rates and calculation of one worker serialize; an unknown worker is SF-0101
// (404), a second rate for the same date SF-0170 (409), and the worker's own user cannot set a
// rate (SF-0106, 403): the rate is the worker's pay. A date in the past is allowed: days already
// calculated or approved are not rewritten, and a later recalculation of an unapproved day (or a
// revision of an approved one) prices each day at the rate effective on its own date.
func (s *Postgres) AddCostRate(ctx context.Context, workerID string, in CostRateInput, savedBy string, idem Idem) (CostRate, error) {
	p, _ := tenant.FromContext(ctx)
	var out CostRate
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		w, err := lockWorkerRow(ctx, tx, p.TenantID, workerID)
		if err != nil {
			return err
		}
		if w.UserID != nil && *w.UserID != "" && *w.UserID == savedBy {
			return httpx.NewProblem(http.StatusForbidden, CodeOwnApproval, "you cannot set your own cost rate")
		}
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from worker_cost_rate where tenant_id = $1 and worker_id = $2 and effective_from = $3::date)`,
			p.TenantID, workerID, in.EffectiveFrom).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return httpx.NewProblem(http.StatusConflict, CodeCostRateExists, "the worker already has a cost rate effective on "+in.EffectiveFrom+"; set the change from a later date")
		}
		id := ids.New()
		out, err = scanCostRate(tx.QueryRow(ctx, `insert into worker_cost_rate (tenant_id, id, worker_id, effective_from, cost_rate, note, created_by)
			values ($1, $2, $3, $4::date, $5, $6, $7) returning `+costRateColumns,
			p.TenantID, id, workerID, in.EffectiveFrom, in.CostRate.String(), in.Note, savedBy))
		if err != nil {
			return err
		}
		if err := enqueue(ctx, tx, workerSource, workerID, sfevents.WorkerCostRateSet, p.TenantID, sfevents.WorkerCostRateSetData{
			WorkerID: workerID, CostRateID: id, EffectiveFrom: out.EffectiveFrom, CostRate: out.CostRate.String(), Currency: w.Currency, SetBy: savedBy,
		}); err != nil {
			return err
		}
		return remember(ctx, tx, idem, http.StatusCreated, out)
	})
	return out, err
}

// ListCostRates returns the worker's dated rates, newest effective date first. An unknown worker
// is SF-0101 (404).
func (s *Postgres) ListCostRates(ctx context.Context, workerID string) ([]CostRate, error) {
	p, _ := tenant.FromContext(ctx)
	out := []CostRate{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var found bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from worker where tenant_id = $1 and id = $2)`, p.TenantID, workerID).Scan(&found); err != nil {
			return err
		}
		if !found {
			return httpx.NewProblem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
		}
		rows, err := tx.Query(ctx, `select `+costRateColumns+` from worker_cost_rate where tenant_id = $1 and worker_id = $2
			order by effective_from desc, id`, p.TenantID, workerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCostRate(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// LoadRateSteps returns the worker's dated rates for the calculation (see domain.Calculation).
func (s *Postgres) LoadRateSteps(ctx context.Context, workerID string) ([]domain.RateStep, error) {
	p, _ := tenant.FromContext(ctx)
	var out []domain.RateStep
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select effective_from, cost_rate::text from worker_cost_rate where tenant_id = $1 and worker_id = $2
			order by effective_from`, p.TenantID, workerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var st domain.RateStep
			var rate string
			if err := rows.Scan(&st.From, &rate); err != nil {
				return err
			}
			if st.Rate, err = decimal.NewFromString(rate); err != nil {
				return err
			}
			out = append(out, st)
		}
		return rows.Err()
	})
	return out, err
}
