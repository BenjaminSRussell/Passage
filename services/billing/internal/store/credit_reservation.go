package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
)

const (
	StatusReserved = "RESERVED"
	StatusConsumed = "CONSUMED"
	StatusReleased = "RELEASED"

	CodeKeyReuse         = "idempotency_key_reuse"
	CodeReservationState = "reservation_state"
)

// CreditReservation is a credit taken from an original invoice outside a
// billing credit note, held until released. It counts against the invoice
// exactly like a posted credit note while its status is RESERVED.
type CreditReservation struct {
	ID             string
	LegalEntityID  string
	InvoiceID      string
	Reference      string
	Status         string
	Currency       string
	Totals         domain.Totals
	Lines          []domain.CreditLine
	IdempotencyKey string
	RequestHash    string
	CreatedAt      time.Time
	ReleasedAt     time.Time
}

func addReservations(ctx context.Context, tx pgx.Tx, invoiceID string, u domain.CreditUsage) (domain.CreditUsage, error) {
	var charges, gross string
	if err := tx.QueryRow(ctx, `select coalesce(sum(charge_amount), 0)::text, coalesce(sum(gross_amount), 0)::text
		from credit_reservation where invoice_id = $1 and status <> 'RELEASED'`, invoiceID).Scan(&charges, &gross); err != nil {
		return u, err
	}
	v, err := parseDecimals(charges, gross)
	if err != nil {
		return u, err
	}
	u.Charges, u.Gross, u.Reserved = u.Charges.Add(v[0]), u.Gross.Add(v[1]), v[1]
	rows, err := tx.Query(ctx, `select l.original_line_no, sum(l.qty)::text, sum(l.line_net)::text, sum(l.tax_amount)::text
		from credit_reservation r join credit_reservation_line l on l.tenant_id = r.tenant_id and l.reservation_id = r.id
		where r.invoice_id = $1 and r.status <> 'RELEASED' group by l.original_line_no`, invoiceID)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var no int
		var qty, net, tax string
		if err := rows.Scan(&no, &qty, &net, &tax); err != nil {
			return u, err
		}
		v, err := parseDecimals(qty, net, tax)
		if err != nil {
			return u, err
		}
		c := u.Lines[no]
		u.Lines[no] = domain.Credited{Qty: c.Qty.Add(v[0]), Net: c.Net.Add(v[1]), Tax: c.Tax.Add(v[2]), ReservedQty: v[0], ReservedTax: v[2]}
	}
	return u, rows.Err()
}

// ReserveCredit takes req (quantities of the original's lines) and charges from
// the invoice res.InvoiceID under the invoice's row lock, the same lock a credit
// note post takes, so reservations and credit notes can never add up to more
// than was invoiced (BIL-0005). The key is checked under the lock too: the same
// key and body return the stored reservation (replayed), the same key with
// another body is 422.
func (s *Postgres) ReserveCredit(ctx context.Context, res CreditReservation, req []domain.CreditQty, charges *decimal.Decimal) (CreditReservation, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out CreditReservation
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		orig, err := loadInvoice(ctx, tx, `where id = $1 for update`, res.InvoiceID)
		if err != nil {
			return notFoundOr(err)
		}
		existing, err := loadReservation(ctx, tx, `where idempotency_key = $1`, res.IdempotencyKey)
		found := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if found && (existing.RequestHash != res.RequestHash || existing.InvoiceID != res.InvoiceID) {
			return keyReuse()
		}
		if found && existing.Status != StatusReleased {
			out, replayed = existing, true
			return nil
		}
		if err := domain.CanBeCredited(orig.Status, orig.Type); err != nil {
			return ruleProblem(err)
		}
		original := make([]domain.PostedLine, len(orig.Lines))
		for i, l := range orig.Lines {
			original[i] = domain.PostedLine{Line: l.Line, Net: l.Net, Tax: l.Tax}
		}
		planned, totals, err := domain.PlanCredit(orig.Currency, original, orig.Totals, orig.Credited, req, charges)
		if err != nil {
			return ruleProblem(err)
		}
		if found {
			res.ID = existing.ID
			if _, err := tx.Exec(ctx, `update credit_reservation set status = 'RESERVED', released_at = null, net_amount = $2, charge_amount = $3,
				tax_amount = $4, gross_amount = $5 where id = $1`, res.ID, totals.Net.String(), totals.Charges.String(), totals.Tax.String(), totals.Gross.String()); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `delete from credit_reservation_line where reservation_id = $1`, res.ID); err != nil {
				return err
			}
			replayed = true
		}
		res.LegalEntityID, res.Currency, res.Status, res.Totals, res.Lines = orig.LegalEntityID, orig.Currency, StatusReserved, totals, planned
		if !found {
			if err := tx.QueryRow(ctx, `insert into credit_reservation (tenant_id, id, legal_entity_id, invoice_id, reference, net_amount,
				charge_amount, tax_amount, gross_amount, idempotency_key, request_hash)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) returning created_at`,
				p.TenantID, res.ID, res.LegalEntityID, res.InvoiceID, res.Reference, totals.Net.String(), totals.Charges.String(),
				totals.Tax.String(), totals.Gross.String(), res.IdempotencyKey, res.RequestHash).Scan(&res.CreatedAt); err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
					return keyReuse()
				}
				return err
			}
		} else {
			res.CreatedAt = existing.CreatedAt
		}
		for _, l := range planned {
			if _, err := tx.Exec(ctx, `insert into credit_reservation_line (tenant_id, reservation_id, original_line_no, qty, line_net, tax_amount)
				values ($1, $2, $3, $4, $5, $6)`, p.TenantID, res.ID, l.OriginalLineNo, l.Line.Qty.String(), l.Net.String(), l.Tax.String()); err != nil {
				return err
			}
		}
		out = res
		return nil
	})
	return out, replayed, err
}

func keyReuse() error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeKeyReuse, "Idempotency-Key was already used with a different request")
}

// ReleaseCredit frees a RESERVED reservation so its amount can be credited
// again; changed reports whether this call did it. Releasing a released
// reservation returns it unchanged; a CONSUMED one cannot be released (409).
func (s *Postgres) ReleaseCredit(ctx context.Context, id string) (CreditReservation, bool, error) {
	return s.transition(ctx, id, StatusReleased, "status = 'RELEASED', released_at = now()", StatusConsumed)
}

// CommitCredit marks a RESERVED reservation CONSUMED: the credit it held was
// issued, so it counts against the invoice for good. Committing a consumed
// reservation returns it unchanged; a released one cannot be committed (409).
func (s *Postgres) CommitCredit(ctx context.Context, id string) (CreditReservation, error) {
	out, _, err := s.transition(ctx, id, StatusConsumed, "status = 'CONSUMED'", StatusReleased)
	return out, err
}

func (s *Postgres) transition(ctx context.Context, id, target, set, blocking string) (CreditReservation, bool, error) {
	var out CreditReservation
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update credit_reservation set `+set+` where id = $1 and status = 'RESERVED'`, id)
		if err != nil {
			return err
		}
		changed = tag.RowsAffected() == 1
		out, err = loadReservation(ctx, tx, `where id = $1`, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeNotFound, "credit reservation not found")
		}
		if err == nil && out.Status == blocking {
			return httpx.NewProblem(http.StatusConflict, CodeReservationState, "the credit reservation is "+out.Status+" and cannot become "+target)
		}
		return err
	})
	return out, changed && err == nil, err
}

func loadReservation(ctx context.Context, tx pgx.Tx, where string, args ...any) (CreditReservation, error) {
	var r CreditReservation
	var net, charges, tax, gross string
	var released *time.Time
	if err := tx.QueryRow(ctx, `select id::text, legal_entity_id::text, invoice_id::text, reference, status,
		(select i.currency::text from invoice i where i.tenant_id = credit_reservation.tenant_id and i.id = credit_reservation.invoice_id),
		net_amount::text, charge_amount::text, tax_amount::text, gross_amount::text, idempotency_key, request_hash, created_at, released_at
		from credit_reservation `+where, args...).Scan(&r.ID, &r.LegalEntityID, &r.InvoiceID, &r.Reference, &r.Status, &r.Currency, &net, &charges,
		&tax, &gross, &r.IdempotencyKey, &r.RequestHash, &r.CreatedAt, &released); err != nil {
		return CreditReservation{}, err
	}
	if released != nil {
		r.ReleasedAt = *released
	}
	v, err := parseDecimals(net, charges, tax, gross)
	if err != nil {
		return CreditReservation{}, err
	}
	r.Totals = domain.Totals{Net: v[0], Charges: v[1], Tax: v[2], Gross: v[3]}
	rows, err := tx.Query(ctx, `select original_line_no, qty::text, line_net::text, tax_amount::text
		from credit_reservation_line where reservation_id = $1 order by original_line_no`, r.ID)
	if err != nil {
		return CreditReservation{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var no int
		var qty, lineNet, lineTax string
		if err := rows.Scan(&no, &qty, &lineNet, &lineTax); err != nil {
			return CreditReservation{}, err
		}
		v, err := parseDecimals(qty, lineNet, lineTax)
		if err != nil {
			return CreditReservation{}, err
		}
		r.Lines = append(r.Lines, domain.CreditLine{OriginalLineNo: no, Line: domain.Line{No: len(r.Lines) + 1, Qty: v[0]}, Net: v[1], Tax: v[2]})
	}
	return r, rows.Err()
}
