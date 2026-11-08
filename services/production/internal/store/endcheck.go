package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

// EndCheck reports whether `:end` would be accepted for the order now and what it would
// accept with a warning (FR-21.8, FR-21.19; PROD-0012, PROD-0020, PROD-0021). It reads
// what `:end` reads, through the same guards and the same settlement plan, without the
// row lock and without writing, so the answer can differ by the time the command runs.
// zeroOutputDuty is the caller's own duty to end with zero output.
func (s *Postgres) EndCheck(ctx context.Context, orderID string, zeroOutputDuty bool) (domain.EndReport, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.EndReport
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		h, err := readOrderHead(ctx, tx, p.TenantID, orderID, false)
		if err != nil {
			return err
		}
		f := domain.EndFacts{Status: h.status, QtyRAF: h.qtyRAF, ZeroOutputDuty: zeroOutputDuty, Today: today()}
		if _, err := domain.Transition(h.status, domain.CmdEnd); err != nil {
			out = domain.EvaluateEnd(f)
			return nil
		}
		var lastPosted *string
		if err := tx.QueryRow(ctx, `select count(*) filter (where not posted), max(posting_date) filter (where posted)::text
			from production_journal where tenant_id = $1 and order_id = $2`, p.TenantID, orderID).Scan(&f.UnpostedJournals, &lastPosted); err != nil {
			return err
		}
		if lastPosted != nil {
			f.LastPostedOn = *lastPosted
		}
		prm, err := orderParameters(ctx, tx, p.TenantID, h, domain.Parameters{})
		if err != nil {
			return err
		}
		f.AsBuiltRequired = prm.AsBuiltRequired
		if f.AttachedAsBuilt, err = countAttachedAsBuilt(ctx, tx, p.TenantID, orderID); err != nil {
			return err
		}
		// The settlement `:end` would post: a missing posting profile or account refuses it.
		if _, err := planSettlement(ctx, tx, p.TenantID, h, prm.ReworkCapitalize); err != nil {
			var pr *httpx.Problem
			if !errors.As(err, &pr) || pr.Status >= http.StatusInternalServerError {
				return err
			}
			f.Settlement = &domain.EndFinding{Code: pr.Code, Message: pr.Detail}
		}
		// PROD-0020 counts the serials of components that serial as-built records name: item
		// tracking is wave 16, so a component no serial record names is taken as untracked.
		rows, err := tx.Query(ctx, `select c.consumed::text, a.assigned::text
			from (select item_id, sum(qty_consumed) as consumed from production_bom_line where tenant_id = $1 and order_id = $2 group by item_id) c
			join (select component_item_id, sum(qty) as assigned from production_as_built
				where tenant_id = $1 and order_id = $2 and detached_at is null and tracking = 'serial' group by component_item_id) a
				on a.component_item_id = c.item_id
			order by c.item_id`, p.TenantID, orderID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var consumed, assigned string
			if err := rows.Scan(&consumed, &assigned); err != nil {
				return err
			}
			f.Components = append(f.Components, domain.ComponentCoverage{Consumed: dec(consumed), Assigned: dec(assigned)})
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out = domain.EvaluateEnd(f)
		return nil
	})
	return out, err
}
