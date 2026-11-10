package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/billingclient"
)

const (
	MaxRMAInvoiceLines = 2000
	postLease          = 2 * time.Minute
)

var ReservationAttention atomic.Int64

// RMAInvoiceLine is an invoice line an RMA credit returns and the quantity of it. Billing reserves these lines before the
// credit is stored, so credit notes and RMA credits together cannot credit a line past what was invoiced.
type RMAInvoiceLine struct {
	LineNo int             `json:"lineNo"`
	Qty    decimal.Decimal `json:"qty"`
}

func (s *Postgres) WithReservations(r billingclient.Reserver) *Postgres {
	s.reserver = r
	return s
}

func ValidateRMAInvoiceLines(lines []RMAInvoiceLine) error {
	if len(lines) > MaxRMAInvoiceLines {
		return rmaInvalid(fmt.Sprintf("a credit returns at most %d invoice lines", MaxRMAInvoiceLines))
	}
	seen := map[int]bool{}
	for _, l := range lines {
		switch {
		case l.LineNo < 1:
			return rmaInvalid("an invoice line number must be 1 or more")
		case seen[l.LineNo]:
			return rmaInvalid(fmt.Sprintf("invoice line %d is listed twice", l.LineNo))
		case !l.Qty.IsPositive():
			return rmaInvalid(fmt.Sprintf("the quantity returned on invoice line %d must be greater than 0", l.LineNo))
		}
		seen[l.LineNo] = true
	}
	return nil
}

func (s *Postgres) checkReservable(in RMACredit) error {
	if err := ValidateRMAInvoiceLines(in.InvoiceLines); err != nil {
		return err
	}
	switch {
	case s.reserver == nil && len(in.InvoiceLines) > 0:
		return httpx.NewProblem(http.StatusServiceUnavailable, CodeRMAUnavailable, "billing is not configured; the invoice lines of this credit cannot be reserved")
	case s.reserver != nil && len(in.InvoiceLines) == 0:
		return rmaInvalid("this credit names no invoiceLines, which billing needs to hold the returned lines; upgrade sales so it sends them before receivables uses billing reservations")
	case s.reserver != nil && !ids.Valid(in.InvoiceID):
		return rmaInvalid("invoiceId must be the billing invoice's UUID so billing can hold its lines")
	}
	return nil
}

func invoiceIDIfReserved(in RMACredit) string {
	if len(in.InvoiceLines) == 0 {
		return ""
	}
	return in.InvoiceID
}

func linesJSON(lines []RMAInvoiceLine) string {
	if len(lines) == 0 {
		return ""
	}
	raw, _ := json.Marshal(lines)
	return string(raw)
}

func reservationKey(legalEntityID, rmaID string) string {
	le, _ := ids.Canonical(legalEntityID)
	rma, _ := ids.Canonical(rmaID)
	return "rma-credit:" + le + ":" + rma
}

func (s *Postgres) reserve(ctx context.Context, legalEntityID, invoiceID, rmaID, rmaNumber string, lines []RMAInvoiceLine) (billingclient.Reservation, error) {
	wire := make([]billingclient.Line, 0, len(lines))
	for _, l := range lines {
		wire = append(wire, billingclient.Line{LineNo: l.LineNo, Qty: l.Qty})
	}
	return s.reserver.Reserve(ctx, legalEntityID, invoiceID, reservationKey(legalEntityID, rmaID), rmaNumber, wire)
}

func reservedShort(res billingclient.Reservation, gross decimal.Decimal) error {
	if res.Known && gross.GreaterThan(res.Net) {
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRMAInvoice,
			fmt.Sprintf("billing held %s of the invoice for these lines, less than the return value %s, so the credit was not posted", res.Net, gross))
	}
	return nil
}

func reservationRefused(err error) bool {
	return errors.Is(err, billingclient.ErrNoRoom) || errors.Is(err, billingclient.ErrNotFound) ||
		errors.Is(err, billingclient.ErrRejected) || errors.Is(err, billingclient.ErrKeyReuse)
}

func reserveProblem(ctx context.Context, rmaNumber string, err error, stored bool) error {
	switch {
	case errors.Is(err, billingclient.ErrNoRoom) && stored:
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRMAInvoice,
			"the invoice has no room left for this return, so the credit stays stored and parked and is not posted; once the room is free again (for example a billing credit note is reversed) retry it, or ask support to cancel it")
	case errors.Is(err, billingclient.ErrNoRoom):
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRMAInvoice,
			"the invoice has no room left for this return: other credits already cover it, so this credit was not recorded")
	case errors.Is(err, billingclient.ErrKeyReuse):
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRMAInvoice,
			"billing already holds a reservation for this RMA with other invoice lines, left by an earlier attempt; send the same lines as that attempt, or ask billing support to release it")
	case errors.Is(err, billingclient.ErrRejected):
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRMAInvoice,
			"billing refused the invoice lines of this credit; check the line numbers and quantities against the invoice")
	case errors.Is(err, billingclient.ErrNotFound):
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRMAInvoice, "billing has no posted invoice with this invoiceId")
	case errors.Is(err, billingclient.ErrBusy):
		return httpx.NewProblem(http.StatusConflict, CodeIdempotencyBusy, "a concurrent request is reserving this RMA's credit; retry")
	}
	slog.ErrorContext(ctx, "billing did not reserve the rma credit", "rma", rmaNumber, "err", err, "correlationId", middleware.GetReqID(ctx))
	return httpx.NewProblem(http.StatusServiceUnavailable, CodeRMAUnavailable, "billing could not hold the invoice amount for this credit, so it was not posted; retry later")
}

func (s *Postgres) reserveBeforeInsert(ctx context.Context, tenantID string, in RMACredit, hash string) (string, error) {
	if len(in.InvoiceLines) == 0 {
		return "", nil
	}
	var stored bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := loadRMACredit(ctx, tx, `legal_entity_id = $2 and rma_id = $3`, tenantID, in.LegalEntityID, in.RMAID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.requestHash != hash {
			legacy := in
			legacy.InvoiceLines = nil
			if row.status != "PENDING" || len(row.lines) > 0 || row.requestHash != rmaHash(legacy) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "this RMA was already credited with different amounts or references")
			}
			if _, err := tx.Exec(ctx, `update rma_credit set request_hash = $3, billing_invoice_id = $4::uuid, reservation_lines = $5::jsonb where tenant_id = $1 and id = $2`,
				tenantID, row.id, hash, in.InvoiceID, linesJSON(in.InvoiceLines)); err != nil {
				return err
			}
		}
		stored = true
		return nil
	})
	if err != nil {
		return "", mapWriteErr(err)
	}
	if stored {
		return "", nil
	}
	res, err := s.reserve(ctx, in.LegalEntityID, in.InvoiceID, in.RMAID, in.RMANumber, in.InvoiceLines)
	if err != nil {
		return "", reserveProblem(ctx, in.RMANumber, err, false)
	}
	if err := reservedShort(res, in.Gross); err != nil {
		s.releaseUnclaimed(ctx, tenantID, in, res.ID)
		return "", err
	}
	return res.ID, nil
}

func (s *Postgres) releaseUnclaimed(ctx context.Context, tenantID string, in RMACredit, reservationID string) {
	var claimed bool
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select exists (select 1 from rma_credit where tenant_id = $1 and legal_entity_id = $2 and rma_id = $3)`,
			tenantID, in.LegalEntityID, in.RMAID).Scan(&claimed)
	}); err != nil || claimed {
		if err != nil {
			slog.ErrorContext(ctx, "rma credit refused but its billing reservation was not released; a retry of the same RMA reuses it",
				"rma", in.RMANumber, "reservation", reservationID, "err", err, "correlationId", middleware.GetReqID(ctx))
		}
		return
	}
	if err := s.reserver.Release(ctx, in.LegalEntityID, reservationID); err != nil {
		slog.ErrorContext(ctx, "rma credit refused but its billing reservation was not released; a retry of the same RMA reuses it",
			"rma", in.RMANumber, "reservation", reservationID, "err", err, "correlationId", middleware.GetReqID(ctx))
	}
}

func rmaBackoff(row rmaCreditRow, now time.Time) time.Time {
	return now.Add(min(rmaGLBackoffBase<<min(row.attempts, 10), rmaGLBackoffMax))
}

func (s *Postgres) recordRMAFailure(ctx context.Context, tenantID string, row rmaCreditRow, next time.Time, parked bool) bool {
	updated := false
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `update rma_credit set gl_attempts = gl_attempts + 1, gl_next_attempt_at = $3, gl_parked = $4,
			billing_reservation_id = case when $4 then null else billing_reservation_id end
			where tenant_id = $1 and id = $2 and status = 'PENDING'`, tenantID, row.id, next, parked)
		updated = e == nil && tag.RowsAffected() == 1
		return e
	}); err != nil {
		slog.ErrorContext(ctx, "rma credit attempt not recorded", "rma", row.rmaNumber, "err", err, "correlationId", middleware.GetReqID(ctx))
	}
	return updated
}

func (s *Postgres) claimPost(ctx context.Context, tenantID string, row rmaCreditRow, now time.Time) (rmaCreditRow, bool, error) {
	var fresh rmaCreditRow
	claimed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update rma_credit set post_lease_until = $3 where tenant_id = $1 and id = $2 and status = 'PENDING' and not gl_parked
			and (post_lease_until is null or post_lease_until <= $4)`, tenantID, row.id, leaseEnd(now), now)
		if err != nil {
			return err
		}
		claimed = tag.RowsAffected() == 1
		fresh, err = loadRMACredit(ctx, tx, `id = $2`, tenantID, row.id)
		return err
	})
	switch {
	case err != nil:
		return row, false, err
	case claimed:
		return fresh, true, nil
	case fresh.status == "POSTED":
		return fresh, false, nil
	case fresh.parked:
		return fresh, false, rmaParked()
	}
	return fresh, false, httpx.NewProblem(http.StatusConflict, CodeIdempotencyBusy, "another attempt is posting this credit; retry")
}

// leaseEnd is when a lease taken at now ends, at the microsecond the database keeps, so the holder can find its own lease.
func leaseEnd(now time.Time) time.Time { return now.Add(postLease).Truncate(time.Microsecond) }

// releasePostLease clears the lease the caller took at now; a lease another attempt took since is left alone.
func (s *Postgres) releasePostLease(ctx context.Context, tenantID, id string, now time.Time) {
	detached := context.WithoutCancel(ctx)
	if err := dbx.WithTenantTx(detached, s.pool, func(tx pgx.Tx) error {
		_, e := tx.Exec(detached, `update rma_credit set post_lease_until = null where tenant_id = $1 and id = $2 and post_lease_until = $3`, tenantID, id, leaseEnd(now))
		return e
	}); err != nil {
		slog.ErrorContext(ctx, "rma credit post lease not released; it expires by itself", "err", err, "correlationId", middleware.GetReqID(ctx))
	}
}

func (s *Postgres) releaseReservation(ctx context.Context, row rmaCreditRow) {
	if row.reservationID == "" || s.reserver == nil {
		return
	}
	if err := s.reserver.Release(ctx, row.legalEntityID, row.reservationID); err != nil {
		slog.ErrorContext(ctx, "parked rma credit kept its billing reservation; it holds the invoice room until the credit is retried",
			"rma", row.rmaNumber, "reservation", row.reservationID, "err", err, "correlationId", middleware.GetReqID(ctx))
	}
}

func (s *Postgres) ensureReserved(ctx context.Context, tenantID string, row rmaCreditRow, now time.Time) (rmaCreditRow, error) {
	if len(row.lines) == 0 && s.reserver != nil {
		return row, rmaInvalid("this credit was stored before billing reservations existed; credit the return order again from sales so it sends the invoice lines")
	}
	if len(row.lines) == 0 || row.reservationID != "" {
		return row, nil
	}
	if s.reserver == nil {
		return row, httpx.NewProblem(http.StatusServiceUnavailable, CodeRMAUnavailable, "billing is not configured; the invoice lines of this credit cannot be reserved")
	}
	res, err := s.reserve(ctx, row.legalEntityID, row.billingInvoiceID, row.rmaID, row.rmaNumber, row.lines)
	if err != nil {
		s.recordRMAFailure(ctx, tenantID, row, rmaBackoff(row, now), reservationRefused(err))
		return row, reserveProblem(ctx, row.rmaNumber, err, true)
	}
	if short := reservedShort(res, row.gross); short != nil {
		s.recordRMAFailure(ctx, tenantID, row, rmaBackoff(row, now), true)
		row.reservationID = res.ID
		s.releaseReservation(ctx, row)
		return row, short
	}
	id := res.ID
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `update rma_credit set billing_reservation_id = $3 where tenant_id = $1 and id = $2 and status = 'PENDING'`, tenantID, row.id, id)
		return e
	}); err != nil {
		return row, err
	}
	row.reservationID = id
	return row, nil
}

// commitReservation turns a posted credit's reservation into a consumed one. Receivables releases a reservation only
// while its credit is PENDING, so a POSTED credit never has a releasable reservation it released itself; when the commit
// fails the credit stays POSTED with reservation_committed false, and a repeat of the same call or the sweep retries it.
func (s *Postgres) commitReservation(ctx context.Context, tenantID string, row rmaCreditRow) error {
	if len(row.lines) == 0 || row.committed {
		return nil
	}
	err := s.commitHeld(ctx, row)
	if err == nil {
		err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `update rma_credit set reservation_committed = true where tenant_id = $1 and id = $2`, tenantID, row.id)
			return e
		})
	}
	if err != nil {
		slog.ErrorContext(ctx, "rma credit is posted but its billing reservation is not committed; the sweep retries the commit",
			"rma", row.rmaNumber, "reservation", row.reservationID, "err", err, "correlationId", middleware.GetReqID(ctx))
		if reservationRefused(err) {
			s.flagAttention(ctx, tenantID, row, "the credit is posted but billing no longer holds its reservation ("+err.Error()+"); the invoice may be over-credited and needs review")
		}
	}
	return err
}

func (s *Postgres) flagAttention(ctx context.Context, tenantID string, row rmaCreditRow, reason string) {
	var flagged bool
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `update rma_credit set needs_attention = $3 where tenant_id = $1 and id = $2 and needs_attention is null`, tenantID, row.id, reason)
		flagged = e == nil && tag.RowsAffected() == 1
		return e
	}); err != nil {
		slog.ErrorContext(ctx, "rma credit needs attention but could not be flagged", "rma", row.rmaNumber, "reason", reason, "err", err, "correlationId", middleware.GetReqID(ctx))
		return
	}
	if flagged {
		ReservationAttention.Add(1)
		slog.ErrorContext(ctx, "rma credit needs attention", "rma", row.rmaNumber, "reason", reason, "correlationId", middleware.GetReqID(ctx))
	}
}

func (s *Postgres) commitHeld(ctx context.Context, row rmaCreditRow) error {
	if s.reserver == nil {
		return errors.New("billing reservations are not configured")
	}
	id := row.reservationID
	if id == "" {
		var err error
		res, err := s.reserve(ctx, row.legalEntityID, row.billingInvoiceID, row.rmaID, row.rmaNumber, row.lines)
		if err != nil {
			return err
		}
		id = res.ID
	}
	err := s.reserver.Commit(ctx, row.legalEntityID, id)
	if errors.Is(err, billingclient.ErrReleased) {
		var res billingclient.Reservation
		if res, err = s.reserve(ctx, row.legalEntityID, row.billingInvoiceID, row.rmaID, row.rmaNumber, row.lines); err == nil {
			err = s.reserver.Commit(ctx, row.legalEntityID, res.ID)
		}
	}
	return err
}

func (s *Postgres) uncommittedRMACredits(ctx context.Context, tenantID string, limit int) ([]rmaCreditRow, error) {
	var out []rmaCreditRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+rmaCreditColumns+` from rma_credit
			where tenant_id = $1 and status = 'POSTED' and billing_reservation_id is not null and not reservation_committed and needs_attention is null
			order by created_at, id limit $2`, tenantID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRMACredit(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
