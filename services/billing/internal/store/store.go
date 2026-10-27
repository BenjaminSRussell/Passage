// Package store persists invoices with Postgres under row-level security. A
// post runs in one transaction: lock the proposal, draw the next gap-free
// number, update the invoice and write invoice.posted to the outbox.
package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	billingevents "github.com/BenjaminSRussell/passage/services/billing/internal/events"
)

const (
	eventSource     = "billing"
	sequenceID      = "INV"
	dateLayout      = "2006-01-02"
	uniqueViolation = "23505"
	// CodeNotFound is returned for an unknown invoice id.
	CodeNotFound = "BIL-0100"
	// CodeNotProposal is returned when a post targets an invoice that is not a proposal.
	CodeNotProposal = "BIL-0101"
	// CodeKeyInFlight is returned when a concurrent request holds the same Idempotency-Key.
	CodeKeyInFlight = "idempotency_in_progress"
	// CodeVersion is returned when If-Match names a version the invoice no longer has.
	CodeVersion = "BIL-0103"
)

// ListFilter narrows ListInvoices; empty fields do not filter.
type ListFilter struct {
	Status     string
	CustomerID string
	Limit      int
}

// Line is a stored invoice line with its rounded net and tax.
type Line struct {
	domain.Line
	Net decimal.Decimal
	Tax decimal.Decimal
}

// Invoice is a stored proposal or posted invoice.
type Invoice struct {
	ID               string
	LegalEntityID    string
	CustomerID       string
	InvoiceAccountID string
	Type             string
	Status           string
	Currency         string
	Number           string
	FiscalYear       int
	InvoiceDate      time.Time
	DueDate          time.Time
	PostingDate      time.Time
	PaymentTermDays  int
	Totals           domain.Totals
	Lines            []Line
	IdempotencyKey   string
	RequestHash      string
	Version          int
}

// Postgres is the billing repository.
type Postgres struct{ pool *pgxpool.Pool }

// New returns a repository over pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

const invoiceColumns = `id::text, legal_entity_id::text, customer_id::text, invoice_account_id::text, invoice_type, status,
  currency::text, coalesce(invoice_number, ''), coalesce(fiscal_year, 0), invoice_date::text, due_date::text,
  coalesce(posting_date::text, ''), payment_term_days, net_amount::text, charge_amount::text, tax_amount::text,
  gross_amount::text, idempotency_key, request_hash, version`

func loadInvoice(ctx context.Context, tx pgx.Tx, where string, args ...any) (Invoice, error) {
	inv, err := scanInvoice(tx.QueryRow(ctx, `select `+invoiceColumns+` from invoice `+where, args...))
	if err != nil {
		return Invoice{}, err
	}
	inv.Lines, err = loadLines(ctx, tx, inv.ID)
	return inv, err
}

func scanInvoice(row pgx.Row) (Invoice, error) {
	var inv Invoice
	var invoiceDate, dueDate, postingDate, net, charge, tax, gross string
	err := row.Scan(
		&inv.ID, &inv.LegalEntityID, &inv.CustomerID, &inv.InvoiceAccountID, &inv.Type, &inv.Status,
		&inv.Currency, &inv.Number, &inv.FiscalYear, &invoiceDate, &dueDate, &postingDate, &inv.PaymentTermDays,
		&net, &charge, &tax, &gross, &inv.IdempotencyKey, &inv.RequestHash, &inv.Version)
	if err != nil {
		return Invoice{}, err
	}
	if inv.InvoiceDate, err = time.Parse(dateLayout, invoiceDate); err != nil {
		return Invoice{}, err
	}
	if inv.DueDate, err = time.Parse(dateLayout, dueDate); err != nil {
		return Invoice{}, err
	}
	if postingDate != "" {
		if inv.PostingDate, err = time.Parse(dateLayout, postingDate); err != nil {
			return Invoice{}, err
		}
	}
	amounts, err := parseDecimals(net, charge, tax, gross)
	if err != nil {
		return Invoice{}, err
	}
	inv.Totals = domain.Totals{Net: amounts[0], Charges: amounts[1], Tax: amounts[2], Gross: amounts[3]}
	return inv, nil
}

func loadLines(ctx context.Context, tx pgx.Tx, invoiceID string) ([]Line, error) {
	rows, err := tx.Query(ctx, `select line_no, description, qty::text, unit_price::text, disc_pct::text, line_net::text,
		tax_amount::text, revenue_account, tax_code from invoice_line where invoice_id = $1 order by line_no`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Line
	for rows.Next() {
		var l Line
		var qty, price, disc, net, tax string
		if err := rows.Scan(&l.No, &l.Description, &qty, &price, &disc, &net, &tax, &l.RevenueAccount, &l.TaxCode); err != nil {
			return nil, err
		}
		v, err := parseDecimals(qty, price, disc, net, tax)
		if err != nil {
			return nil, err
		}
		l.Qty, l.UnitPrice, l.DiscountPct, l.Net, l.Tax = v[0], v[1], v[2], v[3], v[4]
		out = append(out, l)
	}
	return out, rows.Err()
}

// parseDecimals reads numeric columns selected as text.
func parseDecimals(vals ...string) ([]decimal.Decimal, error) {
	out := make([]decimal.Decimal, len(vals))
	for i, v := range vals {
		d, err := decimal.NewFromString(v)
		if err != nil {
			return nil, err
		}
		out[i] = d
	}
	return out, nil
}

// FindByIdempotencyKey returns the invoice created under key, if any.
func (s *Postgres) FindByIdempotencyKey(ctx context.Context, key string) (Invoice, bool, error) {
	var inv Invoice
	found := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := loadInvoice(ctx, tx, `where idempotency_key = $1`, key)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		inv, found = row, err == nil
		return err
	})
	return inv, found, err
}

// CreateProposal stores a proposal with its lines. A concurrent request with
// the same Idempotency-Key fails the unique constraint and gets 409.
func (s *Postgres) CreateProposal(ctx context.Context, inv Invoice) (Invoice, error) {
	p, _ := tenant.FromContext(ctx)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into invoice (tenant_id, id, legal_entity_id, customer_id, invoice_account_id, invoice_type, status,
			invoice_date, due_date, payment_term_days, currency, net_amount, charge_amount, tax_amount, gross_amount, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5, $6, 'PROPOSAL', $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
			p.TenantID, inv.ID, inv.LegalEntityID, inv.CustomerID, inv.InvoiceAccountID, inv.Type,
			inv.InvoiceDate.Format(dateLayout), inv.DueDate.Format(dateLayout), inv.PaymentTermDays, inv.Currency,
			inv.Totals.Net.String(), inv.Totals.Charges.String(), inv.Totals.Tax.String(), inv.Totals.Gross.String(),
			inv.IdempotencyKey, inv.RequestHash)
		if err != nil {
			return err
		}
		for _, l := range inv.Lines {
			if _, err := tx.Exec(ctx, `insert into invoice_line (tenant_id, invoice_id, line_no, description, qty, unit_price, disc_pct,
				line_net, tax_amount, revenue_account, tax_code) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
				p.TenantID, inv.ID, l.No, l.Description, l.Qty.String(), l.UnitPrice.String(), l.DiscountPct.String(),
				l.Net.String(), l.Tax.String(), l.RevenueAccount, l.TaxCode); err != nil {
				return err
			}
		}
		return nil
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return Invoice{}, httpx.NewProblem(http.StatusConflict, CodeKeyInFlight, "a request with this Idempotency-Key is already in progress")
	}
	if err != nil {
		return Invoice{}, err
	}
	inv.Version = 1
	inv.Status = domain.StatusProposal
	return inv, nil
}

// GetInvoice returns one invoice with its lines.
func (s *Postgres) GetInvoice(ctx context.Context, id string) (Invoice, error) {
	var inv Invoice
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		inv, err = loadInvoice(ctx, tx, `where id = $1`, id)
		return err
	})
	return inv, notFoundOr(err)
}

// ListInvoices returns invoice headers (without lines), newest first.
func (s *Postgres) ListInvoices(ctx context.Context, f ListFilter) ([]Invoice, error) {
	var out []Invoice
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+invoiceColumns+` from invoice
			where ($1 = '' or status = $1) and ($2 = '' or customer_id::text = $2)
			order by created_at desc, id desc limit $3`, f.Status, f.CustomerID, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			inv, err := scanInvoice(rows)
			if err != nil {
				return err
			}
			out = append(out, inv)
		}
		return rows.Err()
	})
	return out, err
}

// PostProposal turns a proposal into a posted invoice. It draws the next number
// of the (entity, fiscal year) sequence last, so a failed post consumes none
// (BR-29.9), and enqueues invoice.posted in the same transaction. A non-zero
// expectedVersion must match the locked row (optimistic concurrency).
func (s *Postgres) PostProposal(ctx context.Context, id string, postingDate time.Time, expectedVersion int) (Invoice, error) {
	p, _ := tenant.FromContext(ctx)
	var out Invoice
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		inv, err := loadInvoice(ctx, tx, `where id = $1 for update`, id)
		if err != nil {
			return notFoundOr(err)
		}
		if inv.Status != domain.StatusProposal {
			return httpx.NewProblem(http.StatusConflict, CodeNotProposal, "invoice is not a proposal and cannot be posted")
		}
		if expectedVersion != 0 && expectedVersion != inv.Version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersion, "the invoice changed since it was read; reload and review it")
		}
		fiscalYear := postingDate.Year()
		var seq int64
		if err := tx.QueryRow(ctx, `insert into number_sequence_counter (tenant_id, sequence_id, legal_entity_id, fiscal_year, last_value)
			values ($1, $2, $3, $4, 1)
			on conflict (tenant_id, sequence_id, legal_entity_id, fiscal_year)
			do update set last_value = number_sequence_counter.last_value + 1
			returning last_value`, p.TenantID, sequenceID, inv.LegalEntityID, fiscalYear).Scan(&seq); err != nil {
			return err
		}
		inv.Number = fmt.Sprintf("INV-%d-%07d", fiscalYear, seq)
		inv.FiscalYear = fiscalYear
		inv.PostingDate = postingDate
		inv.Status = domain.StatusPosted
		inv.Version++
		if _, err := tx.Exec(ctx, `update invoice set status = 'POSTED', invoice_number = $2, fiscal_year = $3, posting_date = $4,
			version = version + 1 where id = $1`, inv.ID, inv.Number, fiscalYear, postingDate.Format(dateLayout)); err != nil {
			return err
		}
		evt, err := events.New(billingevents.InvoicePosted, eventSource, inv.ID, p.TenantID, postedData(inv))
		if err != nil {
			return err
		}
		if err := outbox.Enqueue(ctx, tx, "invoice", inv.ID, evt); err != nil {
			return err
		}
		out = inv
		return nil
	})
	return out, err
}

func postedData(inv Invoice) billingevents.InvoicePostedData {
	exp := money.MinorUnits(inv.Currency)
	lines := make([]billingevents.InvoicePostedLine, len(inv.Lines))
	for i, l := range inv.Lines {
		lines[i] = billingevents.InvoicePostedLine{LineNo: l.No, Description: l.Description, Qty: l.Qty.String(),
			Net: l.Net.StringFixed(exp), Tax: l.Tax.StringFixed(exp), TaxCode: l.TaxCode, RevenueAccount: l.RevenueAccount}
	}
	return billingevents.InvoicePostedData{
		InvoiceID: inv.ID, Number: inv.Number, Type: inv.Type, LegalEntityID: inv.LegalEntityID, Customer: inv.CustomerID,
		InvoiceAccount: inv.InvoiceAccountID, Currency: inv.Currency, InvoiceDate: inv.InvoiceDate.Format(dateLayout),
		PostingDate: inv.PostingDate.Format(dateLayout), DueDate: inv.DueDate.Format(dateLayout), Lines: lines,
		Totals: billingevents.InvoicePostedTotals{Net: inv.Totals.Net.StringFixed(exp), Charges: inv.Totals.Charges.StringFixed(exp),
			Tax: inv.Totals.Tax.StringFixed(exp), Gross: inv.Totals.Gross.StringFixed(exp)},
	}
}

func notFoundOr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NewProblem(http.StatusNotFound, CodeNotFound, "invoice not found")
	}
	return err
}
