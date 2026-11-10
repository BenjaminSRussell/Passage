package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// CancelRMACredit drops a PENDING credit that will not be posted, releases the invoice room billing holds for it and
// frees its RMA to be credited again, for example with corrected amounts. A reservation never expires on its own, so
// this is how an abandoned RMA gives its room back. Only a credit the ledger refused (parked) or one never tried can
// go: after a lost ledger answer the journal may be booked, so such a credit is retried instead.
func (s *Postgres) CancelRMACredit(ctx context.Context, id string) (RMACreditView, error) {
	p, _ := tenant.FromContext(ctx)
	var row rmaCreditRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if row, err = loadRMACredit(ctx, tx, `id = $2`, p.TenantID, id); errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeRMACreditNotFound, "RMA credit not found")
		} else if err != nil {
			return err
		}
		if row.status != "PENDING" {
			return httpx.NewProblem(http.StatusConflict, CodeRMACreditState, "the credit is already posted and cannot be cancelled; reverse it with a billing credit note instead")
		}
		var leased bool
		if err := tx.QueryRow(ctx, `select coalesce(post_lease_until > now(), false) from rma_credit where tenant_id = $1 and id = $2`, p.TenantID, id).Scan(&leased); err != nil {
			return err
		}
		if leased {
			return httpx.NewProblem(http.StatusConflict, CodeIdempotencyBusy, "another attempt is posting this credit; retry")
		}
		if !row.parked && row.attempts > 0 {
			return httpx.NewProblem(http.StatusConflict, CodeRMACreditState, "the ledger did not answer the last post, so the journal may be booked; retry the credit instead of cancelling it")
		}
		return nil
	})
	if err != nil {
		return RMACreditView{}, err
	}
	if row.reservationID != "" && s.reserver != nil {
		if err := s.reserver.Release(ctx, row.legalEntityID, row.reservationID); err != nil {
			return RMACreditView{}, reserveProblem(ctx, row.rmaNumber, err, true)
		}
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `delete from rma_credit_tax where tenant_id = $1 and rma_credit_id = $2`, p.TenantID, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `delete from rma_credit where tenant_id = $1 and id = $2 and status = 'PENDING'`, p.TenantID, id)
		if err == nil && tag.RowsAffected() != 1 {
			return httpx.NewProblem(http.StatusConflict, CodeRMACreditState, "the credit was posted meanwhile and cannot be cancelled")
		}
		return err
	})
	if err != nil {
		return RMACreditView{}, mapWriteErr(err)
	}
	view := row.view()
	view.Status, view.ReservationID = "CANCELLED", ""
	return view, nil
}

// BackfillRMAReservation gives a credit posted before billing reservations existed the reservation it never took, from
// the invoice lines the caller names, so billing counts the credit against the invoice's limit. The reservation is
// committed at once, because the credit is already posted.
func (s *Postgres) BackfillRMAReservation(ctx context.Context, id, invoiceID string, lines []RMAInvoiceLine) (RMACreditView, error) {
	if err := ValidateRMAInvoiceLines(lines); err != nil {
		return RMACreditView{}, err
	}
	switch {
	case len(lines) == 0:
		return RMACreditView{}, rmaInvalid("name the invoiceLines the credit returned")
	case !ids.Valid(invoiceID):
		return RMACreditView{}, rmaInvalid("invoiceId must be the billing invoice's UUID")
	case s.reserver == nil:
		return RMACreditView{}, httpx.NewProblem(http.StatusServiceUnavailable, CodeRMAUnavailable, "billing is not configured; the invoice lines of this credit cannot be reserved")
	}
	p, _ := tenant.FromContext(ctx)
	var row rmaCreditRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if row, err = loadRMACredit(ctx, tx, `id = $2`, p.TenantID, id); errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeRMACreditNotFound, "RMA credit not found")
		} else if err != nil {
			return err
		}
		switch {
		case row.status != "POSTED":
			return httpx.NewProblem(http.StatusConflict, CodeRMACreditState, "only a posted credit takes a backfilled reservation; a pending one reserves when it posts")
		case len(row.lines) > 0:
			return httpx.NewProblem(http.StatusConflict, CodeRMACreditState, "the credit already holds a billing reservation")
		}
		return nil
	})
	if err != nil {
		return RMACreditView{}, err
	}
	res, err := s.reserve(ctx, row.legalEntityID, invoiceID, row.rmaID, row.rmaNumber, lines)
	if err != nil {
		return RMACreditView{}, reserveProblem(ctx, row.rmaNumber, err, false)
	}
	if short := reservedShort(res, row.gross); short != nil {
		_ = s.reserver.Release(ctx, row.legalEntityID, res.ID)
		return RMACreditView{}, short
	}
	if err := s.reserver.Commit(ctx, row.legalEntityID, res.ID); err != nil {
		return RMACreditView{}, reserveProblem(ctx, row.rmaNumber, err, true)
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `update rma_credit set billing_invoice_id = $3::uuid, reservation_lines = $4::jsonb, billing_reservation_id = $5::uuid, reservation_committed = true
			where tenant_id = $1 and id = $2 and status = 'POSTED' and reservation_lines is null`, p.TenantID, id, invoiceID, linesJSON(lines), res.ID)
		return e
	})
	if err != nil {
		return RMACreditView{}, err
	}
	row.billingInvoiceID, row.lines, row.reservationID, row.committed = invoiceID, lines, res.ID, true
	return row.view(), nil
}
