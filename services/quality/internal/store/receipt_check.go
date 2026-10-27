package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
)

// ReceiptCheckFilter narrows the receipt check list.
type ReceiptCheckFilter struct {
	POLineID string
	Batch    string
	Limit    int
}

const receiptCheckColumns = "id, po_line_id, batch, receipt_line_id, received_qty::text, accepted_qty::text, covered_before::text, " +
	"covered_qty::text, normal_qty::text, skip, checked_by, checked_at"

func scanReceiptCheck(row pgx.Row) (domain.ReceiptCheck, error) {
	var c domain.ReceiptCheck
	var received, accepted, before, covered, normal string
	if err := row.Scan(&c.ID, &c.POLineID, &c.Batch, &c.ReceiptLineID, &received, &accepted, &before, &covered, &normal,
		&c.Skip, &c.CheckedBy, &c.CheckedAt); err != nil {
		return c, err
	}
	c.ReceivedQty, c.AcceptedQty, c.CoveredBefore = decimal.RequireFromString(received), decimal.RequireFromString(accepted), decimal.RequireFromString(before)
	c.CoveredQty, c.NormalQty = decimal.RequireFromString(covered), decimal.RequireFromString(normal)
	// The message and code are derived, so a stored check reads the same as when it was made.
	derived, err := domain.CheckReceipt(domain.ReceiptCheckInput{POLineID: c.POLineID, Batch: c.Batch, ReceiptLineID: c.ReceiptLineID,
		ReceivedQty: c.ReceivedQty}, c.AcceptedQty, c.CoveredBefore, c.CheckedBy, c.CheckedAt)
	if err != nil {
		return c, err
	}
	c.Code, c.Message = derived.Code, derived.Message
	return c, nil
}

// sourceAcceptedQty sums what closed SOURCE_INSPECTION orders of the PO line and
// batch released (accepted plus accepted with deviation).
func sourceAcceptedQty(ctx context.Context, tx pgx.Tx, poLineID, batch string) (decimal.Decimal, error) {
	var q string
	err := tx.QueryRow(ctx, `select coalesce(sum(d.accept_qty + d.deviation_qty), 0)::text
		from quality_usage_decision d
		join quality_order o on o.tenant_id = d.tenant_id and o.id = d.quality_order_id
		where d.tenant_id = current_setting('passage.tenant_id', true)::uuid
		and o.trigger = 'SOURCE_INSPECTION' and o.status = 'CLOSED'
		and o.source_ref->>'id' = $1 and o.dims->>'batch' = $2`, poLineID, batch).Scan(&q)
	if err != nil {
		return decimal.Zero, err
	}
	return decimal.NewFromString(q)
}

// CheckReceipt records the source-inspection check of one receipt line (BR-33.16).
// Receipts of the same PO line and batch serialise on an advisory lock, so the
// accepted quantity is used up once. check receives what source inspection
// accepted and what earlier receipts already covered. A replayed key, or the same
// receipt line with the same values, answers the stored check; the same receipt
// line with other values is refused (QM-0185).
func (s *Postgres) CheckReceipt(ctx context.Context, in domain.ReceiptCheckInput, key string,
	check func(accepted, coveredBefore decimal.Decimal) (domain.ReceiptCheck, error)) (domain.ReceiptCheck, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.ReceiptCheck
	var inserted bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != "" {
			prior, err := scanReceiptCheck(tx.QueryRow(ctx, `select `+receiptCheckColumns+` from quality_receipt_check where `+ownTenant+` and idempotency_key = $1`, key))
			if err == nil {
				if prior.ReceiptLineID != in.ReceiptLineID {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeKeyReuse, "Idempotency-Key was already used for another receipt line")
				}
				out = prior
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, p.TenantID+"|receipt|"+in.POLineID+"|"+in.Batch); err != nil {
			return err
		}
		prior, err := scanReceiptCheck(tx.QueryRow(ctx, `select `+receiptCheckColumns+` from quality_receipt_check where `+ownTenant+` and receipt_line_id = $1`, in.ReceiptLineID))
		if err == nil {
			if !prior.SameReceipt(in) {
				return httpx.NewProblem(http.StatusConflict, domain.CodeReceiptCheckDupe, "this receipt line was already checked with another PO line, batch or quantity")
			}
			out = prior
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		accepted, err := sourceAcceptedQty(ctx, tx, in.POLineID, in.Batch)
		if err != nil {
			return err
		}
		var before string
		if err := tx.QueryRow(ctx, `select coalesce(sum(covered_qty), 0)::text from quality_receipt_check where `+ownTenant+`
			and po_line_id = $1 and batch = $2`, in.POLineID, in.Batch).Scan(&before); err != nil {
			return err
		}
		c, err := check(accepted, decimal.RequireFromString(before))
		if err != nil {
			return err
		}
		c.ID = ids.New()
		saved, err := scanReceiptCheck(tx.QueryRow(ctx, `insert into quality_receipt_check (tenant_id, id, po_line_id, batch, receipt_line_id,
			received_qty, accepted_qty, covered_before, covered_qty, normal_qty, skip, checked_by, checked_at, idempotency_key)
			values ($1, $2, $3, $4, $5, $6::numeric, $7::numeric, $8::numeric, $9::numeric, $10::numeric, $11, $12, $13, $14)
			returning `+receiptCheckColumns,
			p.TenantID, c.ID, c.POLineID, c.Batch, c.ReceiptLineID, c.ReceivedQty.String(), c.AcceptedQty.String(), c.CoveredBefore.String(),
			c.CoveredQty.String(), c.NormalQty.String(), c.Skip, c.CheckedBy, c.CheckedAt, keyArg(key)))
		if isUnique(err) {
			return httpx.NewProblem(http.StatusConflict, domain.CodeWriteRace, "this receipt line or Idempotency-Key is being checked concurrently; retry")
		}
		if err != nil {
			return err
		}
		out, inserted = saved, true
		return nil
	})
	return out, inserted, err
}

// ListReceiptChecks returns receipt checks, newest first.
func (s *Postgres) ListReceiptChecks(ctx context.Context, f ReceiptCheckFilter) ([]domain.ReceiptCheck, error) {
	out := []domain.ReceiptCheck{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+receiptCheckColumns+` from quality_receipt_check where `+ownTenant+`
			and ($1 = '' or po_line_id = nullif($1, '')::uuid) and ($2 = '' or batch = $2)
			order by checked_at desc, id desc limit $3`, f.POLineID, f.Batch, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanReceiptCheck(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}
