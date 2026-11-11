package store

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// ReservationBackfiller is the receivables call that gives an RMA credit posted before billing reservations existed the
// reservation it never took. The ARCreditor of a deployment that supports it implements this too.
type ReservationBackfiller interface {
	BackfillRMAReservation(ctx context.Context, legalEntityID, rmaID, invoiceID string, lines []ARInvoiceLine) error
}

// ReceivablesAlreadyReserved is returned by a backfiller when the credit already holds a reservation (AR-RMA-STATE).
var ErrReservationHeld = errors.New("the credit already holds a billing reservation")

// ReservationBackfillResult counts what one run of the driver did.
type ReservationBackfillResult struct {
	Backfilled  int                        `json:"backfilled"`
	AlreadyHeld int                        `json:"alreadyHeld"`
	Skipped     []ReservationBackfillIssue `json:"skipped"`
	Failed      []ReservationBackfillIssue `json:"failed"`
	More        bool                       `json:"more"`
}

// ReservationBackfillIssue names an RMA the driver could not finish and why.
type ReservationBackfillIssue struct {
	RMANumber string `json:"rmaNumber"`
	Reason    string `json:"reason"`
}

const maxBackfillBatch = 100

// BackfillReservations works through the credited return orders of a legal entity that were credited before billing
// reservations existed: for each it works out the invoice lines the credit returned (the stored ones, or those worked
// out for a credit stored before migration 0048) and asks receivables to reserve and commit them under billing, so
// billing's invoice limit counts the credit. An order is marked once receivables holds the reservation; a repeat run
// takes up the rest. limit bounds one run (default and maximum 100).
func (s *Postgres) BackfillReservations(ctx context.Context, legalEntityID string, limit int) (ReservationBackfillResult, error) {
	var out ReservationBackfillResult
	backfiller, ok := s.ar.(ReservationBackfiller)
	if !ok {
		return out, problem(503, CodeARUnavailable, "receivables is not configured; reservations cannot be backfilled")
	}
	if limit <= 0 || limit > maxBackfillBatch {
		limit = maxBackfillBatch
	}
	p, _ := tenant.FromContext(ctx)
	var orders []ReturnOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text from return_order where tenant_id = $1 and legal_entity_id = $2
			and credit_note_id is not null and reservation_backfilled_at is null order by created_at, id limit $3`, p.TenantID, legalEntityID, limit+1)
		if err != nil {
			return err
		}
		var idList []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			idList = append(idList, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(idList) > limit {
			out.More, idList = true, idList[:limit]
		}
		for _, id := range idList {
			o, err := loadReturn(ctx, tx, p.TenantID, id, false)
			if err != nil {
				return err
			}
			orders = append(orders, o)
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	for _, o := range orders {
		lines, err := s.creditInvoiceLines(ctx, o.ID)
		if err == nil && len(lines) == 0 {
			lines, err = s.legacyCreditLines(ctx, o)
		}
		if err != nil {
			slog.ErrorContext(ctx, "backfill invoice lines", "rma", o.RMANumber, "err", err)
			out.Failed = append(out.Failed, ReservationBackfillIssue{RMANumber: o.RMANumber, Reason: "the invoice lines could not be worked out; try again"})
			continue
		}
		if len(lines) == 0 || o.OriginalInvoiceID == "" {
			// Nothing to reserve: the credit has no invoice lines (a restocking-only or untaxed legacy credit).
			if err := s.markBackfilled(ctx, o.ID); err != nil {
				return out, err
			}
			out.Skipped = append(out.Skipped, ReservationBackfillIssue{RMANumber: o.RMANumber, Reason: "the credit returned no invoice lines to reserve"})
			continue
		}
		switch err := backfiller.BackfillRMAReservation(ctx, o.LegalEntityID, o.ID, o.OriginalInvoiceID, lines); {
		case err == nil:
			out.Backfilled++
		case errors.Is(err, ErrReservationHeld):
			out.AlreadyHeld++
		default:
			slog.ErrorContext(ctx, "backfill reservation", "rma", o.RMANumber, "err", err)
			out.Failed = append(out.Failed, ReservationBackfillIssue{RMANumber: o.RMANumber, Reason: err.Error()})
			continue
		}
		if err := s.markBackfilled(ctx, o.ID); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (s *Postgres) markBackfilled(ctx context.Context, id string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update return_order set reservation_backfilled_at = now() where tenant_id = $1 and id = $2`, p.TenantID, id)
		return err
	})
}
