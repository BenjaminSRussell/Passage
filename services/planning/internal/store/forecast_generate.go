package store

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	planevents "github.com/BenjaminSRussell/passage/services/planning/internal/events"
)

// GenerateOutcome reports a statistical generation: the cells it wrote and the
// cells it left alone because a planner's manual value or the consensus holds
// the period.
type GenerateOutcome struct {
	Model        ForecastModel
	CellsWritten int
	CellsKept    int
}

// GenerateStatisticalCells writes statistical cells under If-Match on the model
// (FR-22.9): a cell replaces the stored cell of its item, site and period only
// when that cell is itself statistical, so a manual override survives the next
// generation. It recomputes the reduced quantities of every touched series,
// bumps the model version and writes forecast.generated.v1, all in one
// transaction. A submitted or published model is locked (409).
func (s *Postgres) GenerateStatisticalCells(ctx context.Context, modelCode, legalEntityID string, expectedVersion int, cells []ForecastCellInput) (GenerateOutcome, error) {
	p, _ := tenant.FromContext(ctx)
	var out GenerateOutcome
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		m, err := lockForecastModel(ctx, tx, p.TenantID, modelCode, legalEntityID, expectedVersion)
		if err != nil {
			return err
		}
		if !domain.ConsensusWritable(m.Status) {
			return httpx.NewProblem(http.StatusConflict, CodeForecastLocked, "forecast model "+m.Code+" is "+m.Status+"; its cells are locked")
		}
		if err := tx.QueryRow(ctx, `update forecast_model set version = version + 1 where tenant_id = $1 and id = $2::uuid returning version`,
			p.TenantID, m.id).Scan(&m.Version); err != nil {
			return err
		}
		batch := &pgx.Batch{}
		type seriesKey struct{ item, site string }
		var series []seriesKey
		seen := map[seriesKey]bool{}
		from, to := "", ""
		for _, c := range cells {
			batch.Queue(`insert into forecast_line (tenant_id, id, model_id, item_id, site_id, period_start, period_end, qty, source, sales_qty, reduced_qty, updated_by)
				values ($1, $2, $3::uuid, $4::uuid, $5::uuid, $6::date, $7::date, $8::numeric, 'statistical', 0, $8::numeric, $9)
				on conflict (tenant_id, model_id, item_id, site_id, period_start) do update set period_end = excluded.period_end,
				qty = excluded.qty, reduced_qty = excluded.reduced_qty, updated_by = excluded.updated_by, updated_at = now()
				where forecast_line.source = 'statistical'`,
				p.TenantID, ids.New(), m.id, c.ItemID, c.SiteID, c.PeriodStart, c.PeriodEnd, c.Qty.String(), nullableText(p.UserID))
			k := seriesKey{c.ItemID, c.SiteID}
			if !seen[k] {
				seen[k] = true
				series = append(series, k)
			}
			if from == "" || c.PeriodStart < from {
				from = c.PeriodStart
			}
			if c.PeriodEnd > to {
				to = c.PeriodEnd
			}
		}
		br := tx.SendBatch(ctx, batch)
		written := 0
		for range cells {
			tag, err := br.Exec()
			if err != nil {
				_ = br.Close()
				return err
			}
			written += int(tag.RowsAffected())
		}
		if err := br.Close(); err != nil {
			return err
		}
		for _, k := range series {
			if err := reduceSeries(ctx, tx, p.TenantID, m, k.item, k.site); err != nil {
				return err
			}
		}
		evt, err := events.New(planevents.ForecastGenerated, eventSource, m.id, p.TenantID, planevents.ForecastGeneratedData{
			ModelID: m.id, ModelCode: m.Code, Version: m.Version, Series: len(series), CellsWritten: written,
			CellsKept: len(cells) - written, PeriodFrom: from, PeriodTo: to, GeneratedBy: p.UserID,
		})
		if err != nil {
			return err
		}
		if err := outbox.Enqueue(ctx, tx, "forecast-model", m.id, evt); err != nil {
			return err
		}
		out = GenerateOutcome{Model: m, CellsWritten: written, CellsKept: len(cells) - written}
		return nil
	})
	return out, err
}
