package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/billingclient"
)

const MaxRMAInvoiceLines = 2000

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
		return rmaInvalid("name the invoice lines this credit returns (invoiceLines) so billing can hold them")
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

func (s *Postgres) reserve(ctx context.Context, legalEntityID, invoiceID, rmaID, rmaNumber string, lines []RMAInvoiceLine) (string, error) {
	wire := make([]billingclient.Line, 0, len(lines))
	for _, l := range lines {
		wire = append(wire, billingclient.Line{LineNo: l.LineNo, Qty: l.Qty})
	}
	res, err := s.reserver.Reserve(ctx, legalEntityID, invoiceID, reservationKey(legalEntityID, rmaID), rmaNumber, wire)
	return res.ID, err
}

func reservationRefused(err error) bool {
	return errors.Is(err, billingclient.ErrNoRoom) || errors.Is(err, billingclient.ErrNotFound)
}

func reserveProblem(ctx context.Context, rmaNumber string, err error) error {
	switch {
	case errors.Is(err, billingclient.ErrNoRoom):
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRMAInvoice,
			"the invoice has no room left for this return: other credits already cover it, so this credit was not recorded")
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
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "this RMA was already credited with different amounts or references")
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
	id, err := s.reserve(ctx, in.LegalEntityID, in.InvoiceID, in.RMAID, in.RMANumber, in.InvoiceLines)
	if err != nil {
		return "", reserveProblem(ctx, in.RMANumber, err)
	}
	return id, nil
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

func (s *Postgres) recordRMAFailure(ctx context.Context, tenantID string, row rmaCreditRow, next time.Time, parked bool) {
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `update rma_credit set gl_attempts = gl_attempts + 1, gl_next_attempt_at = $3, gl_parked = $4,
			billing_reservation_id = case when $4 then null else billing_reservation_id end
			where tenant_id = $1 and id = $2 and status = 'PENDING'`, tenantID, row.id, next, parked)
		return e
	}); err != nil {
		slog.ErrorContext(ctx, "rma credit attempt not recorded", "rma", row.rmaNumber, "err", err, "correlationId", middleware.GetReqID(ctx))
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
	if len(row.lines) == 0 || row.reservationID != "" {
		return row, nil
	}
	if s.reserver == nil {
		return row, httpx.NewProblem(http.StatusServiceUnavailable, CodeRMAUnavailable, "billing is not configured; the invoice lines of this credit cannot be reserved")
	}
	id, err := s.reserve(ctx, row.legalEntityID, row.billingInvoiceID, row.rmaID, row.rmaNumber, row.lines)
	if err != nil {
		s.recordRMAFailure(ctx, tenantID, row, rmaBackoff(row, now), reservationRefused(err))
		return row, reserveProblem(ctx, row.rmaNumber, err)
	}
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
	}
	return err
}

func (s *Postgres) commitHeld(ctx context.Context, row rmaCreditRow) error {
	if s.reserver == nil {
		return errors.New("billing reservations are not configured")
	}
	id := row.reservationID
	if id == "" {
		var err error
		if id, err = s.reserve(ctx, row.legalEntityID, row.billingInvoiceID, row.rmaID, row.rmaNumber, row.lines); err != nil {
			return err
		}
	}
	err := s.reserver.Commit(ctx, row.legalEntityID, id)
	if errors.Is(err, billingclient.ErrReleased) {
		if id, err = s.reserve(ctx, row.legalEntityID, row.billingInvoiceID, row.rmaID, row.rmaNumber, row.lines); err == nil {
			err = s.reserver.Commit(ctx, row.legalEntityID, id)
		}
	}
	return err
}

func (s *Postgres) uncommittedRMACredits(ctx context.Context, tenantID string, limit int) ([]rmaCreditRow, error) {
	var out []rmaCreditRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+rmaCreditColumns+` from rma_credit
			where tenant_id = $1 and status = 'POSTED' and billing_reservation_id is not null and not reservation_committed
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
