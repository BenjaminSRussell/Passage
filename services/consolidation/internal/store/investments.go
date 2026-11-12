package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/domain"
)

// MaxLayers bounds the IC receipt layers of one unit and period.
const MaxLayers = 5000

// SetLayers replaces the IC receipt layers a unit's goods still sit in at the end of the period (FR-40.16). The layers are what E3
// of a run over a collection of that period takes its unrealized profit from.
func (s *Postgres) SetLayers(ctx context.Context, groupID, unitID, period string, layers []domain.ICLayer) error {
	tenantID, by, err := creator(ctx)
	if err != nil {
		return err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var one int
		if err := tx.QueryRow(ctx, `select 1 from consolidation_unit where tenant_id = $1 and group_id = $2::uuid and id = $3::uuid`, tenantID, groupID, unitID).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
			return &domain.Error{Code: domain.CodeGroupNotFound, Detail: "no unit " + unitID + " in group " + groupID}
		} else if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from consolidation_ic_layer where tenant_id = $1 and unit_id = $2::uuid and period = $3`, tenantID, unitID, period); err != nil {
			return err
		}
		for _, l := range layers {
			group := any(nil)
			if l.GroupCostKnown {
				group = l.GroupUnitCost.String()
			}
			if _, err := tx.Exec(ctx, `insert into consolidation_ic_layer (tenant_id, id, unit_id, period, ref, quantity, legal_unit_cost, group_unit_cost, updated_by)
				values ($1, $2, $3::uuid, $4, $5, $6::numeric, $7::numeric, $8::numeric, $9)`, tenantID, ids.New(), unitID, period, l.ID, l.Quantity.String(), l.LegalUnitCost.String(), group, by); err != nil {
				return err
			}
		}
		return nil
	})
	return wrap("set consolidation layers", err)
}

// GetLayers returns the layers of every unit of the group for the period, by unit id.
func (s *Postgres) GetLayers(ctx context.Context, groupID, period string) (map[string][]domain.ICLayer, error) {
	pr, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]domain.ICLayer{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadGroupHeader(ctx, tx, pr.TenantID, groupID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select l.unit_id::text, l.ref, l.quantity::text, l.legal_unit_cost::text, l.group_unit_cost::text
			from consolidation_ic_layer l join consolidation_unit u on u.tenant_id = l.tenant_id and u.id = l.unit_id
			where l.tenant_id = $1 and u.group_id = $2::uuid and l.period = $3 order by l.unit_id, l.ref`, pr.TenantID, groupID, period)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var unit, qty, legal string
			var group *string
			var l domain.ICLayer
			if err := rows.Scan(&unit, &l.ID, &qty, &legal, &group); err != nil {
				return err
			}
			if l.Quantity, err = decimal.NewFromString(qty); err != nil {
				return fmt.Errorf("store: layer quantity %q: %w", qty, err)
			}
			if l.LegalUnitCost, err = decimal.NewFromString(legal); err != nil {
				return fmt.Errorf("store: layer cost %q: %w", legal, err)
			}
			if group != nil {
				if l.GroupUnitCost, err = decimal.NewFromString(*group); err != nil {
					return fmt.Errorf("store: layer group cost %q: %w", *group, err)
				}
				l.GroupCostKnown = true
			}
			out[unit] = append(out[unit], l)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, wrap("get consolidation layers", err)
	}
	return out, nil
}

// InvestmentRecord is one line of a run's investments register.
type InvestmentRecord struct {
	RunID string
	domain.InvestmentLine
}

func insertInvestments(ctx context.Context, tx pgx.Tx, tenantID, runID string, lines []domain.InvestmentLine) error {
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `insert into consolidation_investment (tenant_id, run_id, unit_id, method, ownership_pct, cost, opening_carrying, share_of_profit, dividends, closing_carrying, unrecognized_loss)
			values ($1, $2, $3, $4, $5::numeric, $6::numeric, $7::numeric, $8::numeric, $9::numeric, $10::numeric, $11::numeric)`,
			tenantID, runID, l.UnitID, l.Method, l.OwnershipPct.String(), l.Cost.String(), l.OpeningCarrying.String(), l.ShareOfProfit.String(), l.Dividends.String(), l.ClosingCarrying.String(), l.UnrecognizedLoss.String()); err != nil {
			return err
		}
	}
	return nil
}

func scanInvestments(rows pgx.Rows) ([]InvestmentRecord, error) {
	var out []InvestmentRecord
	for rows.Next() {
		var r InvestmentRecord
		var pct, cost, open, share, div, closing, loss string
		if err := rows.Scan(&r.RunID, &r.UnitID, &r.Method, &pct, &cost, &open, &share, &div, &closing, &loss); err != nil {
			return nil, err
		}
		for _, f := range []struct {
			dst *decimal.Decimal
			raw string
		}{{&r.OwnershipPct, pct}, {&r.Cost, cost}, {&r.OpeningCarrying, open}, {&r.ShareOfProfit, share}, {&r.Dividends, div}, {&r.ClosingCarrying, closing}, {&r.UnrecognizedLoss, loss}} {
			var err error
			if *f.dst, err = decimal.NewFromString(f.raw); err != nil {
				return nil, fmt.Errorf("store: investment amount %q: %w", f.raw, err)
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const investmentColumns = `i.run_id::text, i.unit_id, i.method, i.ownership_pct::text, i.cost::text, i.opening_carrying::text, i.share_of_profit::text, i.dividends::text, i.closing_carrying::text, i.unrecognized_loss::text`

// RunInvestments returns the investments register of a run, ordered by unit.
func (s *Postgres) RunInvestments(ctx context.Context, runID string) ([]InvestmentRecord, error) {
	pr, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	var out []InvestmentRecord
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var one int
		if err := tx.QueryRow(ctx, `select 1 from consolidation_run where tenant_id = $1 and id = $2::uuid`, pr.TenantID, runID).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
			return &domain.Error{Code: domain.CodeRunNotFound, Detail: "no consolidation run with id " + runID}
		} else if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+investmentColumns+` from consolidation_investment i where i.tenant_id = $1 and i.run_id = $2::uuid order by i.unit_id`, pr.TenantID, runID)
		if err != nil {
			return err
		}
		defer rows.Close()
		out, err = scanInvestments(rows)
		return err
	})
	if err != nil {
		return nil, wrap("get consolidation investments", err)
	}
	return out, nil
}

// LatestInvestments returns the investments register of the newest run of the group for the period, by unit id; ok is false when the
// group has no run for it. A run is the group's own when its group id matches.
func (s *Postgres) LatestInvestments(ctx context.Context, groupID, period string) (map[string]InvestmentRecord, bool, error) {
	pr, err := principal(ctx)
	if err != nil {
		return nil, false, err
	}
	out := map[string]InvestmentRecord{}
	found := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var runID string
		err := tx.QueryRow(ctx, `select id::text from consolidation_run where tenant_id = $1 and group_id = $2 and period = $3 order by version desc limit 1`, pr.TenantID, groupID, period).Scan(&runID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		rows, err := tx.Query(ctx, `select `+investmentColumns+` from consolidation_investment i where i.tenant_id = $1 and i.run_id = $2::uuid`, pr.TenantID, runID)
		if err != nil {
			return err
		}
		defer rows.Close()
		recs, err := scanInvestments(rows)
		for _, r := range recs {
			out[r.UnitID] = r
		}
		return err
	})
	if err != nil {
		return nil, false, wrap("get latest consolidation investments", err)
	}
	return out, found, nil
}
