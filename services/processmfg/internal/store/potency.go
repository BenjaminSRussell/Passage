package store

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
)

// CodePotencyState is a potency change on an order whose recipe is fixed: reported or ended, or with its control
// recipe already made. 409.
const CodePotencyState = "PMF-0507"

// SetPotencyAdjustments replaces the potency adjustments of an order (BR-24.2) after checking them against its lines.
// They apply to the control recipe the order's release generates, so an order whose recipe is made, or that is
// reported or ended, refuses them.
func (s *Postgres) SetPotencyAdjustments(ctx context.Context, orderID string, adjs []domain.PotencyAdjustment, user string) (BatchOrder, error) {
	p, _ := tenant.FromContext(ctx)
	var out BatchOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		o, err := loadOrder(ctx, tx, orderID, " for update")
		if err != nil {
			return err
		}
		var recipe *string
		if err := tx.QueryRow(ctx, `select control_recipe_id::text from batch_order where id = $1`, orderID).Scan(&recipe); err != nil {
			return err
		}
		if o.Status == domain.StatusReported || o.Status == domain.StatusEnded || recipe != nil {
			return httpx.NewProblem(http.StatusConflict, CodePotencyState, "the order's control recipe is fixed (made already, or the order is reported or ended); potency can no longer change it")
		}
		if _, err := domain.ApplyPotency(potencyLines(o), adjs); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from batch_order_potency where order_id = $1`, orderID); err != nil {
			return err
		}
		for i, a := range adjs {
			if _, err := tx.Exec(ctx, `insert into batch_order_potency (tenant_id, order_id, line_number, standard_potency, batch_potency, compensating_line, batch_ref, seq, set_by)
				values ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, p.TenantID, orderID, a.LineNumber, a.StandardPotency, a.BatchPotency, a.CompensatingLine, a.BatchRef, i+1, user); err != nil {
				return err
			}
		}
		out, err = loadOrder(ctx, tx, orderID, "")
		return err
	})
	return out, err
}

func potencyLines(o BatchOrder) []domain.PotencyLine {
	out := make([]domain.PotencyLine, 0, len(o.Lines))
	for _, l := range o.Lines {
		out = append(out, domain.PotencyLine{LineNumber: l.LineNumber, Type: l.Type, Qty: l.Qty})
	}
	return out
}

// PotencyAdjusted returns the order's line quantities after its potency adjustments (only the lines they touch).
func PotencyAdjusted(o BatchOrder) (map[int]decimal.Decimal, error) {
	return domain.ApplyPotency(potencyLines(o), o.PotencyAdjustments)
}

func loadPotency(ctx context.Context, tx pgx.Tx, orderID string) ([]domain.PotencyAdjustment, error) {
	rows, err := tx.Query(ctx, `select line_number, standard_potency, batch_potency, compensating_line, batch_ref from batch_order_potency
		where order_id = $1 order by seq`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.PotencyAdjustment{}
	for rows.Next() {
		var a domain.PotencyAdjustment
		if err := rows.Scan(&a.LineNumber, &a.StandardPotency, &a.BatchPotency, &a.CompensatingLine, &a.BatchRef); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
