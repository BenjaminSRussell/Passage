package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
)

// ForecastModel is one forecast model, its reduction setup (FR-22.8) and its
// consensus status (FR-22.9). Version is the ETag: every write to the model,
// its lines or its status bumps it. Only a draft accepts cell writes.
type ForecastModel struct {
	id                 string
	Code               string  `json:"code"`
	Version            int     `json:"version"`
	ReductionPrinciple string  `json:"reductionPrinciple"`
	ReductionPercent   string  `json:"reductionPercent"`
	Status             string  `json:"status"`
	SubmittedBy        *string `json:"submittedBy"`
	DecidedBy          *string `json:"decidedBy"`
	PublishedAt        *string `json:"publishedAt"`
}

// ForecastPublication summarises a published consensus: the item and site
// series, the distinct periods and the first and last day they span.
type ForecastPublication struct {
	Items          int    `json:"items"`
	HorizonPeriods int    `json:"horizonPeriods"`
	PeriodFrom     string `json:"periodFrom"`
	PeriodTo       string `json:"periodTo"`
}

// Problem codes of the consensus workflow (FR-22.9).
const (
	CodeForecastLocked = "PLAN-FORECAST-LOCKED"
	CodeConsensusState = "PLAN-CONSENSUS-STATE"
	CodeConsensusSoD   = "PLAN-CONSENSUS-SOD"
	CodeConsensusEmpty = "PLAN-CONSENSUS-EMPTY"
)

// ModelSpec replaces the reduction setup of a model.
type ModelSpec struct {
	ReductionPrinciple string
	ReductionPercent   decimal.Decimal
}

// ForecastCellInput is one forecast cell written by a planner (FR-22.9 manual override).
type ForecastCellInput struct {
	ItemID, SiteID string
	PeriodStart    string
	PeriodEnd      string
	Source         string
	Qty, SalesQty  decimal.Decimal
}

// ForecastLine is one forecast cell with its reduced quantity (FR-22.8).
type ForecastLine struct {
	ItemID      string `json:"itemId"`
	SiteID      string `json:"siteId"`
	PeriodStart string `json:"periodStart"`
	PeriodEnd   string `json:"periodEnd"`
	Source      string `json:"source"`
	Qty         string `json:"qty"`
	SalesQty    string `json:"salesQty"`
	ReducedQty  string `json:"reducedQty"`
}

// ForecastDemandLine is a reduced forecast cell that a plan run nets as demand.
type ForecastDemandLine struct {
	ItemID      string
	PeriodStart time.Time
	ReducedQty  decimal.Decimal
}

const modelSelect = `select id::text, code, version, reduction_principle, reduction_percent::text, status, submitted_by, decided_by,
	to_char(published_at at time zone 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') from forecast_model`

func scanModel(row pgx.Row) (ForecastModel, error) {
	var m ForecastModel
	err := row.Scan(&m.id, &m.Code, &m.Version, &m.ReductionPrinciple, &m.ReductionPercent, &m.Status, &m.SubmittedBy, &m.DecidedBy, &m.PublishedAt)
	return m, err
}

func forecastNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeForecastNotFound, "forecast model not found")
}

// lockForecastModel returns the model locked for the write. A model that does
// not exist yet is created at version 0, so a first write sends If-Match "0".
func lockForecastModel(ctx context.Context, tx pgx.Tx, tenantID, code string, expectedVersion int) (ForecastModel, error) {
	if _, err := tx.Exec(ctx, `insert into forecast_model (tenant_id, id, code, version) values ($1, $2, $3, 0) on conflict (tenant_id, code) do nothing`,
		tenantID, ids.New(), code); err != nil {
		return ForecastModel{}, err
	}
	m, err := scanModel(tx.QueryRow(ctx, modelSelect+` where tenant_id = $1 and code = $2 for update`, tenantID, code))
	if err != nil {
		return ForecastModel{}, err
	}
	if m.Version != expectedVersion {
		return ForecastModel{}, versionConflict("forecast model", m.Version)
	}
	return m, nil
}

// UpsertForecastLines writes the given cells under If-Match on the model: each
// cell replaces the stored cell of the same item, site and period start. It
// then recomputes the reduced quantities of every touched series, or of every
// series of the model when the reduction setup changes, and bumps the model
// version. All of it is one transaction.
func (s *Postgres) UpsertForecastLines(ctx context.Context, modelCode string, expectedVersion int, spec *ModelSpec, cells []ForecastCellInput) (ForecastModel, error) {
	p, _ := tenant.FromContext(ctx)
	var out ForecastModel
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		m, err := lockForecastModel(ctx, tx, p.TenantID, modelCode, expectedVersion)
		if err != nil {
			return err
		}
		if !domain.ConsensusWritable(m.Status) {
			return httpx.NewProblem(http.StatusConflict, CodeForecastLocked, "forecast model "+m.Code+" is "+m.Status+"; its cells are locked")
		}
		if spec != nil {
			m.ReductionPrinciple = spec.ReductionPrinciple
			m.ReductionPercent = spec.ReductionPercent.String()
		}
		if err := tx.QueryRow(ctx, `update forecast_model set version = version + 1, reduction_principle = $3, reduction_percent = $4::numeric
			where tenant_id = $1 and id = $2::uuid returning version, reduction_percent::text`, p.TenantID, m.id, m.ReductionPrinciple, m.ReductionPercent).
			Scan(&m.Version, &m.ReductionPercent); err != nil {
			return err
		}
		batch := &pgx.Batch{}
		type seriesKey struct{ item, site string }
		var series []seriesKey
		seen := map[seriesKey]bool{}
		for _, c := range cells {
			batch.Queue(`insert into forecast_line (tenant_id, id, model_id, item_id, site_id, period_start, period_end, qty, source, sales_qty, reduced_qty, updated_by)
				values ($1, $2, $3::uuid, $4::uuid, $5::uuid, $6::date, $7::date, $8::numeric, $9, $10::numeric, $8::numeric, $11)
				on conflict (tenant_id, model_id, item_id, site_id, period_start) do update set period_end = excluded.period_end,
				qty = excluded.qty, source = excluded.source, sales_qty = excluded.sales_qty, reduced_qty = excluded.reduced_qty,
				updated_by = excluded.updated_by, updated_at = now()`,
				p.TenantID, ids.New(), m.id, c.ItemID, c.SiteID, c.PeriodStart, c.PeriodEnd, c.Qty.String(), c.Source, c.SalesQty.String(), nullableText(p.UserID))
			k := seriesKey{c.ItemID, c.SiteID}
			if !seen[k] {
				seen[k] = true
				series = append(series, k)
			}
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return err
		}
		if spec != nil {
			// A new reduction setup applies to every series of the model.
			series = series[:0]
			rows, err := tx.Query(ctx, `select distinct item_id::text, site_id::text from forecast_line where tenant_id = $1 and model_id = $2::uuid`, p.TenantID, m.id)
			if err != nil {
				return err
			}
			for rows.Next() {
				var k seriesKey
				if err := rows.Scan(&k.item, &k.site); err != nil {
					rows.Close()
					return err
				}
				series = append(series, k)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		for _, k := range series {
			if err := reduceSeries(ctx, tx, p.TenantID, m, k.item, k.site); err != nil {
				return err
			}
		}
		out = m
		return nil
	})
	return out, err
}

// reduceSeries recomputes reduced_qty for every period of one item at one site
// in period order (FR-22.8, BR-22.3).
func reduceSeries(ctx context.Context, tx pgx.Tx, tenantID string, m ForecastModel, itemID, siteID string) error {
	rows, err := tx.Query(ctx, `select id::text, qty::text, sales_qty::text from forecast_line
		where tenant_id = $1 and model_id = $2::uuid and item_id = $3::uuid and site_id = $4::uuid order by period_start`,
		tenantID, m.id, itemID, siteID)
	if err != nil {
		return err
	}
	var lineIDs []string
	var cells []domain.ForecastCell
	for rows.Next() {
		var id, qty, sales string
		if err := rows.Scan(&id, &qty, &sales); err != nil {
			rows.Close()
			return err
		}
		c := domain.ForecastCell{}
		if c.Qty, err = decimal.NewFromString(qty); err != nil {
			rows.Close()
			return err
		}
		if c.SalesQty, err = decimal.NewFromString(sales); err != nil {
			rows.Close()
			return err
		}
		lineIDs = append(lineIDs, id)
		cells = append(cells, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	percent, err := decimal.NewFromString(m.ReductionPercent)
	if err != nil {
		return err
	}
	reduced, err := domain.ReducePeriods(m.ReductionPrinciple, percent, cells)
	if err != nil {
		return err
	}
	batch := &pgx.Batch{}
	for i, id := range lineIDs {
		batch.Queue(`update forecast_line set reduced_qty = $3::numeric where tenant_id = $1 and id = $2::uuid`, tenantID, id, reduced[i].String())
	}
	return tx.SendBatch(ctx, batch).Close()
}

// nullableText turns an empty string into SQL null.
func nullableText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nullableUUID turns an empty filter into SQL null.
func nullableUUID(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ListForecastLines reads a model and its cells, optionally of one item and
// one site, ordered by item, site and period.
func (s *Postgres) ListForecastLines(ctx context.Context, modelCode, itemID, siteID string, limit int) (ForecastModel, []ForecastLine, error) {
	p, _ := tenant.FromContext(ctx)
	var m ForecastModel
	out := []ForecastLine{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		m, err = scanModel(tx.QueryRow(ctx, modelSelect+` where tenant_id = $1 and code = $2`, p.TenantID, modelCode))
		if errors.Is(err, pgx.ErrNoRows) {
			return forecastNotFound()
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select item_id::text, site_id::text, to_char(period_start, 'YYYY-MM-DD'), to_char(period_end, 'YYYY-MM-DD'),
			source, qty::text, sales_qty::text, reduced_qty::text from forecast_line
			where tenant_id = $1 and model_id = $2::uuid and ($3::uuid is null or item_id = $3::uuid) and ($4::uuid is null or site_id = $4::uuid)
			order by item_id, site_id, period_start limit $5`, p.TenantID, m.id, nullableUUID(itemID), nullableUUID(siteID), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l ForecastLine
			if err := rows.Scan(&l.ItemID, &l.SiteID, &l.PeriodStart, &l.PeriodEnd, &l.Source, &l.Qty, &l.SalesQty, &l.ReducedQty); err != nil {
				return err
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	return m, out, err
}

// ForecastDemand returns the reduced forecast of a model at one site for the
// periods that have not ended before from (FR-22.8): the forecast demand a
// plan run nets. Cells reduced to zero are left out.
func (s *Postgres) ForecastDemand(ctx context.Context, modelCode, siteID string, from time.Time) ([]ForecastDemandLine, error) {
	p, _ := tenant.FromContext(ctx)
	var out []ForecastDemandLine
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		m, err := scanModel(tx.QueryRow(ctx, modelSelect+` where tenant_id = $1 and code = $2`, p.TenantID, modelCode))
		if errors.Is(err, pgx.ErrNoRows) {
			return forecastNotFound()
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select item_id::text, period_start, reduced_qty::text from forecast_line
			where tenant_id = $1 and model_id = $2::uuid and site_id = $3::uuid and period_end >= $4::date and reduced_qty > 0
			order by item_id, period_start`, p.TenantID, m.id, siteID, from.Format(dateLayout))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l ForecastDemandLine
			var qty string
			if err := rows.Scan(&l.ItemID, &l.PeriodStart, &qty); err != nil {
				return err
			}
			if l.ReducedQty, err = decimal.NewFromString(qty); err != nil {
				return err
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	return out, err
}
