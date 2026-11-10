// Package store persists invoices and credit notes with Postgres under
// row-level security. A post runs in one transaction: lock the proposal, draw
// the next gap-free number, update the document and write its posted event to
// the outbox. A credit note post also locks its original invoice and re-checks
// the remaining creditable amount (BIL-0005) under that lock.
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
	invoiceSequence = "INV"
	creditSequence  = "CN"
	dateLayout      = "2006-01-02"
	uniqueViolation = "23505"
	// CodeNotFound is returned for an unknown invoice id.
	CodeNotFound = "BIL-0100"
	// CodeNotProposal is returned when a post targets an invoice that is not a
	// proposal, or a document through the other kind's post endpoint.
	CodeNotProposal = "BIL-0101"
	// CodeKeyInFlight is returned when a concurrent request holds the same Idempotency-Key.
	CodeKeyInFlight = "idempotency_in_progress"
	// CodeVersion is returned when If-Match names a version the invoice no longer has.
	CodeVersion = "BIL-0103"
	// CodeSlipLineInvoiced is returned when a packing slip line is already on an
	// invoice or proposal (BR-29.5: a slip belongs to at most one invoice).
	CodeSlipLineInvoiced = "BIL-0104"
	// slipLineIndex is the unique index that enforces BR-29.5.
	slipLineIndex = "invoice_line_slip_line_unique"
)

// postedStatuses are the states in which a credit note counts against its
// original's remaining creditable amount.
const postedStatuses = `('POSTED', 'PARTIALLY_SETTLED', 'SETTLED')`

// ListFilter narrows ListInvoices; empty fields do not filter. After is the id
// of the last invoice of the previous page (keyset cursor).
type ListFilter struct {
	Status            string
	Type              string
	CustomerID        string
	OriginalInvoiceID string
	After             string
	Limit             int
}

// InvoicePage is one page of invoice headers; NextAfter is the id to pass as
// After for the next page, empty on the last page.
type InvoicePage struct {
	Items     []Invoice
	NextAfter string
}

// Line is a stored invoice line with its rounded net and tax. OriginalLineNo
// is the credited line of the original invoice on a credit note, else zero.
// Source is set on shipment and consolidated invoice lines only (FR-29.12).
type Line struct {
	domain.Line
	Net            decimal.Decimal
	Tax            decimal.Decimal
	OriginalLineNo int
	Source         domain.Source
}

// Installment is one stored open item of an installment plan with the term
// that produced it (FR-29.18).
type Installment struct {
	domain.Installment
	Term domain.InstallmentTerm
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
	Installments     []Installment
	// OriginalInvoiceID, OriginalNumber and Reason are set on a credit note.
	OriginalInvoiceID string
	OriginalNumber    string
	Reason            string
	// Credited is what posted credit notes took from this invoice; it is
	// loaded on single reads of an invoice that is not itself a credit note.
	Credited       domain.CreditUsage
	IdempotencyKey string
	RequestHash    string
	Version        int
	// ProposalBatchID and ProposalBatchSeq place a shipment proposal in the
	// POST /proposals request that created it; empty on every other invoice.
	ProposalBatchID  string
	ProposalBatchSeq int
}

// ProposalBatch is one POST /v1/billing/proposals request: its key, the hash of
// its body and the legal entity it invoices.
type ProposalBatch struct {
	ID             string
	LegalEntityID  string
	IdempotencyKey string
	RequestHash    string
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
  gross_amount::text, idempotency_key, request_hash, version, coalesce(original_invoice_id::text, ''),
  coalesce((select o.invoice_number from invoice o where o.tenant_id = invoice.tenant_id and o.id = invoice.original_invoice_id), ''),
  coalesce(credit_reason, '')`

func loadInvoice(ctx context.Context, tx pgx.Tx, where string, args ...any) (Invoice, error) {
	inv, err := scanInvoice(tx.QueryRow(ctx, `select `+invoiceColumns+` from invoice `+where, args...))
	if err != nil {
		return Invoice{}, err
	}
	if inv.Lines, err = loadLines(ctx, tx, inv.ID); err != nil {
		return Invoice{}, err
	}
	if inv.Installments, err = loadInstallments(ctx, tx, inv.ID); err != nil {
		return Invoice{}, err
	}
	if inv.Type != domain.TypeCreditNote {
		inv.Credited, err = creditUsage(ctx, tx, inv.ID)
	}
	return inv, err
}

func scanInvoice(row pgx.Row) (Invoice, error) {
	var inv Invoice
	var invoiceDate, dueDate, postingDate, net, charge, tax, gross string
	err := row.Scan(
		&inv.ID, &inv.LegalEntityID, &inv.CustomerID, &inv.InvoiceAccountID, &inv.Type, &inv.Status,
		&inv.Currency, &inv.Number, &inv.FiscalYear, &invoiceDate, &dueDate, &postingDate, &inv.PaymentTermDays,
		&net, &charge, &tax, &gross, &inv.IdempotencyKey, &inv.RequestHash, &inv.Version, &inv.OriginalInvoiceID,
		&inv.OriginalNumber, &inv.Reason)
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
		tax_amount::text, revenue_account, tax_code, coalesce(original_line_no, 0), coalesce(packing_slip_id::text, ''),
		coalesce(packing_slip_line_id::text, ''), coalesce(packing_slip_date::text, ''), coalesce(order_id::text, ''),
		coalesce(order_line_id::text, ''), coalesce(product_id::text, ''), coalesce(price_explain_ref, '')
		from invoice_line where invoice_id = $1 order by line_no`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Line
	for rows.Next() {
		var l Line
		var qty, price, disc, net, tax, slipDate string
		src := &l.Source
		if err := rows.Scan(&l.No, &l.Description, &qty, &price, &disc, &net, &tax, &l.RevenueAccount, &l.TaxCode, &l.OriginalLineNo,
			&src.PackingSlipID, &src.PackingSlipLineID, &slipDate, &src.OrderID, &src.OrderLineID, &src.ProductID, &src.PriceExplainRef); err != nil {
			return nil, err
		}
		if slipDate != "" {
			if src.PackingSlipDate, err = time.Parse(dateLayout, slipDate); err != nil {
				return nil, err
			}
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

func loadInstallments(ctx context.Context, tx pgx.Tx, invoiceID string) ([]Installment, error) {
	rows, err := tx.Query(ctx, `select installment_no, percent::text, offset_days, amount::text, due_date::text
		from invoice_installment where invoice_id = $1 order by installment_no`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Installment
	for rows.Next() {
		var in Installment
		var pct, amount, due string
		if err := rows.Scan(&in.No, &pct, &in.Term.OffsetDays, &amount, &due); err != nil {
			return nil, err
		}
		v, err := parseDecimals(pct, amount)
		if err != nil {
			return nil, err
		}
		if in.DueDate, err = time.Parse(dateLayout, due); err != nil {
			return nil, err
		}
		in.Term.Percent, in.Amount = v[0], v[1]
		out = append(out, in)
	}
	return out, rows.Err()
}

// creditUsage sums what the posted credit notes of an invoice took from it,
// per original line and in total.
func creditUsage(ctx context.Context, tx pgx.Tx, invoiceID string) (domain.CreditUsage, error) {
	u := domain.CreditUsage{Lines: map[int]domain.Credited{}, Charges: decimal.Zero, Gross: decimal.Zero}
	var charges, gross string
	if err := tx.QueryRow(ctx, `select coalesce(sum(charge_amount), 0)::text, coalesce(sum(gross_amount), 0)::text
		from invoice where original_invoice_id = $1 and status in `+postedStatuses, invoiceID).Scan(&charges, &gross); err != nil {
		return u, err
	}
	v, err := parseDecimals(charges, gross)
	if err != nil {
		return u, err
	}
	u.Charges, u.Gross = v[0], v[1]
	rows, err := tx.Query(ctx, `select l.original_line_no, sum(l.qty)::text, sum(l.line_net)::text, sum(l.tax_amount)::text
		from invoice c join invoice_line l on l.tenant_id = c.tenant_id and l.invoice_id = c.id
		where c.original_invoice_id = $1 and c.status in `+postedStatuses+`
		group by l.original_line_no`, invoiceID)
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
		u.Lines[no] = domain.Credited{Qty: v[0], Net: v[1], Tax: v[2]}
	}
	if err := rows.Err(); err != nil {
		return u, err
	}
	rows.Close()
	return addReservations(ctx, tx, invoiceID, u)
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

// CreateProposal stores an invoice proposal with its lines and installments.
// A concurrent request with the same Idempotency-Key fails the unique
// constraint and gets 409.
func (s *Postgres) CreateProposal(ctx context.Context, inv Invoice) (Invoice, error) {
	return s.insertProposal(ctx, inv, nil)
}

// CreateCreditNote stores a credit note proposal against inv.OriginalInvoiceID.
// In the same transaction it locks the original and checks that it can still
// be credited by this much (BIL-0005), so a proposal never outruns the
// original; the post repeats the check because other credit notes may post first.
func (s *Postgres) CreateCreditNote(ctx context.Context, cn Invoice) (Invoice, error) {
	if cn.Type != domain.TypeCreditNote || cn.OriginalInvoiceID == "" {
		return Invoice{}, errors.New("store: CreateCreditNote needs a CREDIT_NOTE with its original invoice")
	}
	return s.insertProposal(ctx, cn, func(tx pgx.Tx) error {
		_, err := lockOriginal(ctx, tx, cn)
		return err
	})
}

func (s *Postgres) insertProposal(ctx context.Context, inv Invoice, before func(pgx.Tx) error) (Invoice, error) {
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if before != nil {
			if err := before(tx); err != nil {
				return err
			}
		}
		return writeProposal(ctx, tx, inv)
	})
	if err != nil {
		return Invoice{}, writeProblem(err)
	}
	inv.Version = 1
	inv.Status = domain.StatusProposal
	return inv, nil
}

// writeProposal inserts a proposal header, its lines with their source
// references, and its installments inside tx.
func writeProposal(ctx context.Context, tx pgx.Tx, inv Invoice) error {
	p, _ := tenant.FromContext(ctx)
	var batchSeq any
	if inv.ProposalBatchSeq > 0 {
		batchSeq = inv.ProposalBatchSeq
	}
	_, err := tx.Exec(ctx, `insert into invoice (tenant_id, id, legal_entity_id, customer_id, invoice_account_id, invoice_type, status,
		invoice_date, due_date, payment_term_days, currency, net_amount, charge_amount, tax_amount, gross_amount, idempotency_key, request_hash,
		original_invoice_id, credit_reason, proposal_batch_id, proposal_batch_seq)
		values ($1, $2, $3, $4, $5, $6, 'PROPOSAL', $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)`,
		p.TenantID, inv.ID, inv.LegalEntityID, inv.CustomerID, inv.InvoiceAccountID, inv.Type,
		inv.InvoiceDate.Format(dateLayout), inv.DueDate.Format(dateLayout), inv.PaymentTermDays, inv.Currency,
		inv.Totals.Net.String(), inv.Totals.Charges.String(), inv.Totals.Tax.String(), inv.Totals.Gross.String(),
		inv.IdempotencyKey, inv.RequestHash, nullable(inv.OriginalInvoiceID), nullable(inv.Reason),
		nullable(inv.ProposalBatchID), batchSeq)
	if err != nil {
		return err
	}
	for _, l := range inv.Lines {
		var originalLine, slipDate any
		if l.OriginalLineNo > 0 {
			originalLine = l.OriginalLineNo
		}
		src := l.Source
		if !src.PackingSlipDate.IsZero() {
			slipDate = src.PackingSlipDate.Format(dateLayout)
		}
		if _, err := tx.Exec(ctx, `insert into invoice_line (tenant_id, invoice_id, line_no, description, qty, unit_price, disc_pct,
			line_net, tax_amount, revenue_account, tax_code, original_line_no, packing_slip_id, packing_slip_line_id, packing_slip_date,
			order_id, order_line_id, product_id, price_explain_ref)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`,
			p.TenantID, inv.ID, l.No, l.Description, l.Qty.String(), l.UnitPrice.String(), l.DiscountPct.String(),
			l.Net.String(), l.Tax.String(), l.RevenueAccount, l.TaxCode, originalLine, nullable(src.PackingSlipID),
			nullable(src.PackingSlipLineID), slipDate, nullable(src.OrderID), nullable(src.OrderLineID), nullable(src.ProductID),
			nullable(src.PriceExplainRef)); err != nil {
			return err
		}
	}
	for _, in := range inv.Installments {
		if _, err := tx.Exec(ctx, `insert into invoice_installment (tenant_id, invoice_id, installment_no, percent, offset_days, amount, due_date)
			values ($1, $2, $3, $4, $5, $6, $7)`, p.TenantID, inv.ID, in.No, in.Term.Percent.String(), in.Term.OffsetDays,
			in.Amount.String(), in.DueDate.Format(dateLayout)); err != nil {
			return err
		}
	}
	return nil
}

// writeProblem maps a failed proposal write: a packing slip line already on an
// invoice is 409 BIL-0104 (BR-29.5); any other duplicate is a concurrent
// request holding the same Idempotency-Key.
func writeProblem(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != uniqueViolation {
		return err
	}
	if pgErr.ConstraintName == slipLineIndex {
		return httpx.NewProblem(http.StatusConflict, CodeSlipLineInvoiced,
			"a packing slip line of this request is already on an invoice or proposal; a slip line is invoiced once")
	}
	return httpx.NewProblem(http.StatusConflict, CodeKeyInFlight, "a request with this Idempotency-Key is already in progress")
}

// CreateShipmentProposals stores one POST /proposals request (FR-29.1, FR-29.2):
// the batch row and every invoice it planned, in one transaction, so either all
// invoices exist or none does. Each invoice is numbered in the batch in the
// given order. A packing slip line already on an invoice fails the whole batch
// with 409 BIL-0104 (BR-29.5).
func (s *Postgres) CreateShipmentProposals(ctx context.Context, b ProposalBatch, invs []Invoice) ([]Invoice, error) {
	if len(invs) == 0 {
		return nil, errors.New("store: a proposal batch needs at least one invoice")
	}
	p, _ := tenant.FromContext(ctx)
	out := make([]Invoice, len(invs))
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into proposal_batch (tenant_id, id, legal_entity_id, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5)`, p.TenantID, b.ID, b.LegalEntityID, b.IdempotencyKey, b.RequestHash); err != nil {
			return err
		}
		for i, inv := range invs {
			if inv.Type != domain.TypeShipment && inv.Type != domain.TypeConsolidated {
				return fmt.Errorf("store: a proposal batch holds shipment invoices only, got %s", inv.Type)
			}
			inv.ProposalBatchID, inv.ProposalBatchSeq = b.ID, i+1
			if err := writeProposal(ctx, tx, inv); err != nil {
				return err
			}
			inv.Version, inv.Status = 1, domain.StatusProposal
			out[i] = inv
		}
		return nil
	})
	if err != nil {
		return nil, writeProblem(err)
	}
	return out, nil
}

// FindProposalBatch returns the batch created under key with its invoices in
// batch order, if any.
func (s *Postgres) FindProposalBatch(ctx context.Context, key string) (ProposalBatch, []Invoice, bool, error) {
	var b ProposalBatch
	var invs []Invoice
	found := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select id::text, legal_entity_id::text, idempotency_key, request_hash from proposal_batch
			where idempotency_key = $1`, key).Scan(&b.ID, &b.LegalEntityID, &b.IdempotencyKey, &b.RequestHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		rows, err := tx.Query(ctx, `select id::text from invoice where proposal_batch_id = $1 order by proposal_batch_seq`, b.ID)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			inv, err := loadInvoice(ctx, tx, `where id = $1`, id)
			if err != nil {
				return err
			}
			invs = append(invs, inv)
		}
		return nil
	})
	return b, invs, found, err
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

// ListInvoices returns one page of invoice headers (without lines), newest
// first. The page is keyed on (created_at, id) of the After invoice, so rows
// created while the caller pages are neither repeated nor skipped; an After the
// caller cannot see yields an empty page.
func (s *Postgres) ListInvoices(ctx context.Context, f ListFilter) (InvoicePage, error) {
	if f.Limit < 1 {
		return InvoicePage{}, errors.New("store: list limit must be at least 1")
	}
	var out []Invoice
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+invoiceColumns+` from invoice
			where ($1 = '' or status = $1) and ($2 = '' or customer_id::text = $2)
			  and ($4 = '' or invoice_type = $4) and ($5 = '' or original_invoice_id::text = $5)
			  and ($6 = '' or (created_at, id) < (select a.created_at, a.id from invoice a where a.id = nullif($6, '')::uuid))
			order by created_at desc, id desc limit $3`, f.Status, f.CustomerID, f.Limit+1, f.Type, f.OriginalInvoiceID, f.After)
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
	if err != nil {
		return InvoicePage{}, err
	}
	page := InvoicePage{Items: out}
	if len(out) > f.Limit {
		page.Items = out[:f.Limit]
		page.NextAfter = page.Items[f.Limit-1].ID
	}
	return page, nil
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
		if inv.Type == domain.TypeCreditNote {
			return httpx.NewProblem(http.StatusConflict, CodeNotProposal, "a credit note posts through /v1/billing/credit-notes/{id}:post")
		}
		if err := checkPostable(inv, expectedVersion); err != nil {
			return err
		}
		if err := markPosted(ctx, tx, &inv, p.TenantID, invoiceSequence, postingDate); err != nil {
			return err
		}
		evt, err := events.New(billingevents.InvoicePosted, eventSource, inv.ID, p.TenantID, postedData(inv))
		if err != nil {
			return err
		}
		if err := outbox.Enqueue(ctx, tx, "invoice", inv.ID, evt); err != nil {
			return err
		}
		if err := queueSalesReports(ctx, tx, p.TenantID, inv); err != nil {
			return err
		}
		out = inv
		return nil
	})
	return out, err
}

// PostCreditNote posts a credit note proposal (FR-29.5). It locks the credit
// note, then its original, re-checks the remaining creditable amount against
// the credit notes posted so far (BIL-0005), draws the next CN- number last and
// enqueues creditnote.posted in the same transaction.
func (s *Postgres) PostCreditNote(ctx context.Context, id string, postingDate time.Time, expectedVersion int) (Invoice, error) {
	p, _ := tenant.FromContext(ctx)
	var out Invoice
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cn, err := loadInvoice(ctx, tx, `where id = $1 for update`, id)
		if err != nil {
			return notFoundOr(err)
		}
		if cn.Type != domain.TypeCreditNote {
			return httpx.NewProblem(http.StatusConflict, CodeNotProposal, "not a credit note; an invoice posts through /v1/billing/invoices/{id}:post")
		}
		if err := checkPostable(cn, expectedVersion); err != nil {
			return err
		}
		if _, err := lockOriginal(ctx, tx, cn); err != nil {
			return err
		}
		if err := markPosted(ctx, tx, &cn, p.TenantID, creditSequence, postingDate); err != nil {
			return err
		}
		evt, err := events.New(billingevents.CreditNotePosted, eventSource, cn.ID, p.TenantID, creditNoteData(cn))
		if err != nil {
			return err
		}
		if err := outbox.Enqueue(ctx, tx, "invoice", cn.ID, evt); err != nil {
			return err
		}
		out = cn
		return nil
	})
	return out, err
}

// lockOriginal locks the original invoice of a credit note and checks that the
// note fits what it can still be credited (BIL-0005). The parties and currency
// must be the original's.
func lockOriginal(ctx context.Context, tx pgx.Tx, cn Invoice) (Invoice, error) {
	orig, err := loadInvoice(ctx, tx, `where id = $1 for update`, cn.OriginalInvoiceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invoice{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeCreditExceeded, "the original invoice does not exist")
	}
	if err != nil {
		return Invoice{}, err
	}
	if err := domain.CanBeCredited(orig.Status, orig.Type); err != nil {
		return Invoice{}, ruleProblem(err)
	}
	if orig.LegalEntityID != cn.LegalEntityID || orig.CustomerID != cn.CustomerID || orig.InvoiceAccountID != cn.InvoiceAccountID || orig.Currency != cn.Currency {
		return Invoice{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeCreditExceeded,
			"a credit note must carry the original's legal entity, customer, invoice account and currency")
	}
	original := make([]domain.PostedLine, len(orig.Lines))
	for i, l := range orig.Lines {
		original[i] = domain.PostedLine{Line: l.Line, Net: l.Net, Tax: l.Tax}
	}
	credit := make([]domain.CreditLine, len(cn.Lines))
	for i, l := range cn.Lines {
		credit[i] = domain.CreditLine{OriginalLineNo: l.OriginalLineNo, Line: l.Line, Net: l.Net, Tax: l.Tax}
	}
	if err := domain.VerifyCredit(cn.Currency, original, orig.Totals, orig.Credited, credit, cn.Totals); err != nil {
		return Invoice{}, ruleProblem(err)
	}
	return orig, nil
}

// checkPostable refuses a document that is not a proposal (BIL-0101) or whose
// version moved since the caller read it (BIL-0103). A zero expectedVersion
// means the caller sent no If-Match.
func checkPostable(inv Invoice, expectedVersion int) error {
	if inv.Status != domain.StatusProposal {
		return httpx.NewProblem(http.StatusConflict, CodeNotProposal, "the document is not a proposal and cannot be posted")
	}
	if expectedVersion != 0 && expectedVersion != inv.Version {
		return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersion, "the document changed since it was read; reload and review it")
	}
	return nil
}

// markPosted draws the next number of sequence for the entity and posting
// year and writes the posted state. The caller holds the document's row lock.
func markPosted(ctx context.Context, tx pgx.Tx, inv *Invoice, tenantID, sequence string, postingDate time.Time) error {
	fiscalYear := postingDate.Year()
	var seq int64
	if err := tx.QueryRow(ctx, `insert into number_sequence_counter (tenant_id, sequence_id, legal_entity_id, fiscal_year, last_value)
		values ($1, $2, $3, $4, 1)
		on conflict (tenant_id, sequence_id, legal_entity_id, fiscal_year)
		do update set last_value = number_sequence_counter.last_value + 1
		returning last_value`, tenantID, sequence, inv.LegalEntityID, fiscalYear).Scan(&seq); err != nil {
		return err
	}
	inv.Number = fmt.Sprintf("%s-%d-%07d", sequence, fiscalYear, seq)
	inv.FiscalYear = fiscalYear
	inv.PostingDate = postingDate
	inv.Status = domain.StatusPosted
	inv.Version++
	_, err := tx.Exec(ctx, `update invoice set status = 'POSTED', invoice_number = $2, fiscal_year = $3, posting_date = $4,
		version = version + 1 where id = $1`, inv.ID, inv.Number, fiscalYear, postingDate.Format(dateLayout))
	return err
}

func postedLines(currency string, in []Line) []billingevents.InvoicePostedLine {
	exp := money.MinorUnits(currency)
	lines := make([]billingevents.InvoicePostedLine, len(in))
	for i, l := range in {
		lines[i] = billingevents.InvoicePostedLine{LineNo: l.No, Description: l.Description, Qty: l.Qty.String(),
			Net: l.Net.StringFixed(exp), Tax: l.Tax.StringFixed(exp), TaxCode: l.TaxCode, RevenueAccount: l.RevenueAccount,
			OrderLineID: l.Source.OrderLineID, Product: l.Source.ProductID, PackingSlipLineID: l.Source.PackingSlipLineID}
	}
	return lines
}

func postedTotals(currency string, t domain.Totals) billingevents.InvoicePostedTotals {
	exp := money.MinorUnits(currency)
	return billingevents.InvoicePostedTotals{Net: t.Net.StringFixed(exp), Charges: t.Charges.StringFixed(exp),
		Tax: t.Tax.StringFixed(exp), Gross: t.Gross.StringFixed(exp)}
}

func postedData(inv Invoice) billingevents.InvoicePostedData {
	exp := money.MinorUnits(inv.Currency)
	installments := make([]billingevents.InvoicePostedInstallment, len(inv.Installments))
	for i, in := range inv.Installments {
		installments[i] = billingevents.InvoicePostedInstallment{No: in.No, Amount: in.Amount.StringFixed(exp), DueDate: in.DueDate.Format(dateLayout)}
	}
	return billingevents.InvoicePostedData{
		InvoiceID: inv.ID, Number: inv.Number, Type: inv.Type, LegalEntityID: inv.LegalEntityID, Customer: inv.CustomerID,
		InvoiceAccount: inv.InvoiceAccountID, Currency: inv.Currency, InvoiceDate: inv.InvoiceDate.Format(dateLayout),
		PostingDate: inv.PostingDate.Format(dateLayout), DueDate: inv.DueDate.Format(dateLayout), Lines: postedLines(inv.Currency, inv.Lines),
		Totals: postedTotals(inv.Currency, inv.Totals), Installments: installments,
	}
}

func creditNoteData(cn Invoice) billingevents.CreditNotePostedData {
	base := postedLines(cn.Currency, cn.Lines)
	lines := make([]billingevents.CreditNotePostedLine, len(cn.Lines))
	for i, l := range cn.Lines {
		lines[i] = billingevents.CreditNotePostedLine{InvoicePostedLine: base[i], OriginalLineNo: l.OriginalLineNo}
	}
	return billingevents.CreditNotePostedData{
		CreditNoteID: cn.ID, Number: cn.Number, OriginalInvoiceID: cn.OriginalInvoiceID, OriginalNumber: cn.OriginalNumber,
		Reason: cn.Reason, LegalEntityID: cn.LegalEntityID, Customer: cn.CustomerID, InvoiceAccount: cn.InvoiceAccountID,
		Currency: cn.Currency, CreditDate: cn.InvoiceDate.Format(dateLayout), PostingDate: cn.PostingDate.Format(dateLayout),
		Lines: lines, Totals: postedTotals(cn.Currency, cn.Totals),
	}
}

// ruleProblem maps a domain rule violation to a problem with its code: 422 for
// a BIL rule, 400 for a request that is malformed (COM-0001).
func ruleProblem(err error) error {
	var de *domain.Error
	if !errors.As(err, &de) {
		return err
	}
	status := http.StatusUnprocessableEntity
	if de.Code == "COM-0001" {
		status = http.StatusBadRequest
	}
	return httpx.NewProblem(status, de.Code, de.Detail)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func notFoundOr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NewProblem(http.StatusNotFound, CodeNotFound, "invoice not found")
	}
	return err
}
