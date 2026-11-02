package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// Quotation problem codes (README lists the full catalogue).
const (
	CodeQuotationState    = "SLS-0030"
	CodeQuotationExpired  = "SLS-0031"
	CodeLineSelection     = "SLS-0032"
	CodeQuotationNotFound = "SLS-0105"

	quotationSequence = "SQ"
	// quoteChannel is the channel of orders created by a quotation conversion.
	quoteChannel = "QUOTE"
)

// NewQuotation is a validated quotation before storage. Lines carry LineNet.
type NewQuotation struct {
	LegalEntityID    string
	CustomerID       string
	InvoiceAccountID string
	Currency         string
	// ValidUntil is zero when the request gave none: the legal entity's
	// quotation validity from IssuedAt applies.
	ValidUntil time.Time
	IssuedAt   time.Time
	// ProbabilityPct is nil when the request gave none: the legal entity's
	// quotation_probability_default applies.
	ProbabilityPct *int
	IdempotencyKey string
	RequestHash    string
	Actor          string
	Lines          []Line
}

// QuotationLine is one quotation line. LineStatus is OPEN or CONVERTED; a
// converted line names the order it became.
type QuotationLine struct {
	Line
	ConvertedOrderID *string `json:"convertedOrderId,omitempty"`
}

// Quotation is a priced offer with its lines.
type Quotation struct {
	ID               string          `json:"id"`
	LegalEntityID    string          `json:"legalEntityId"`
	QuotationNumber  string          `json:"quotationNumber"`
	CustomerID       string          `json:"customerId"`
	InvoiceAccountID string          `json:"invoiceAccountId"`
	Currency         string          `json:"currency"`
	Status           string          `json:"status"`
	ValidUntil       string          `json:"validUntil"`
	ProbabilityPct   int             `json:"probabilityPct"`
	NetTotal         decimal.Decimal `json:"netTotal"`
	LostReason       *string         `json:"lostReason,omitempty"`
	Version          int             `json:"version"`
	CreatedBy        string          `json:"createdBy"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
	// Lines is filled on a single read and empty in the list.
	Lines []QuotationLine `json:"lines"`
	// OpenLines and LineCount summarise the lines in the list and on a read.
	LineCount int `json:"lineCount"`
	OpenLines int `json:"openLines"`
}

// QuotationFilter narrows the quotation list. Empty fields match everything;
// an empty LegalEntityID means every legal entity in the caller's scope.
type QuotationFilter struct {
	LegalEntityID string
	CustomerID    string
	Status        string
	Limit         int
}

// QuotationConversion is the outcome of :convert: the quotation after the
// conversion and the DRAFT order that holds the converted lines.
type QuotationConversion struct {
	Quotation Quotation `json:"quotation"`
	Order     Order     `json:"order"`
}

// ConvertRequest is one :convert call. LineNos empty converts every open line.
// CanOverrideListing and ListingOverrideReason pass the caller's listing
// override to the order, which is checked like any other (FR-27.15).
type ConvertRequest struct {
	Version               int
	LineNos               []int
	Actor                 string
	CanOverrideListing    bool
	ListingOverrideReason string
}

func quotationNotFound() error {
	return problem(http.StatusNotFound, CodeQuotationNotFound, "quotation not found")
}

// quotationProblem maps a domain refusal to its stable problem.
func quotationProblem(status string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrQuotationExpired):
		return problem(http.StatusUnprocessableEntity, CodeQuotationExpired, "the quotation's validity has passed")
	default:
		return problem(http.StatusUnprocessableEntity, CodeQuotationState, "a quotation in status "+status+" cannot take this action")
	}
}

// CreateQuotation stores a DRAFT quotation numbered SQ-. A repeat of the same
// Idempotency-Key and body returns the stored quotation with created=false;
// the key with another body is refused.
func (s *Postgres) CreateQuotation(ctx context.Context, in NewQuotation) (Quotation, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out Quotation
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var existing, hash string
		err := tx.QueryRow(ctx, `select id::text, request_hash from sales_quotation where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, in.IdempotencyKey).Scan(&existing, &hash)
		if err == nil {
			if hash != in.RequestHash {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			out, err = loadQuotation(ctx, tx, p.TenantID, existing, false)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if in.ValidUntil.IsZero() || in.ProbabilityPct == nil {
			params, err := loadParameters(ctx, tx, p.TenantID, in.LegalEntityID)
			if err != nil {
				return err
			}
			if in.ValidUntil.IsZero() {
				in.ValidUntil = domain.QuotationValidUntil(in.IssuedAt, params.QuotationExpiryDays)
			}
			if in.ProbabilityPct == nil {
				in.ProbabilityPct = &params.QuotationProbabilityDefault
			}
		}
		seq, err := nextSequence(ctx, tx, p.TenantID, in.LegalEntityID, quotationSequence)
		if err != nil {
			return err
		}
		id := ids.New()
		nets := make([]decimal.Decimal, 0, len(in.Lines))
		for _, l := range in.Lines {
			nets = append(nets, l.LineNet)
		}
		if _, err := tx.Exec(ctx, `insert into sales_quotation (tenant_id, id, legal_entity_id, quotation_number, customer_id, invoice_account_id,
				currency, status, probability_pct, valid_until, net_total, idempotency_key, request_hash, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, 'DRAFT', $8, $9::date, $10::numeric, $11, $12, $13)`,
			p.TenantID, id, in.LegalEntityID, domain.FormatQuotationNumber(seq), in.CustomerID, in.InvoiceAccountID, in.Currency,
			*in.ProbabilityPct, in.ValidUntil.Format(dayLayout), domain.SumNets(nets).String(), in.IdempotencyKey, in.RequestHash, in.Actor); err != nil {
			return err
		}
		for _, l := range in.Lines {
			if _, err := tx.Exec(ctx, `insert into sales_quotation_line (tenant_id, id, quotation_id, line_no, product_id, quantity, unit,
					site_id, warehouse_id, unit_price, disc_pct, line_net, requested_ship_date, line_status)
				values ($1, $2, $3, $4, $5, $6::numeric, $7, $8, $9, $10::numeric, $11::numeric, $12::numeric, $13::date, 'OPEN')`,
				p.TenantID, ids.New(), id, l.LineNo, l.ProductID, l.Quantity.String(), l.Unit, l.SiteID, l.WarehouseID,
				l.UnitPrice.String(), l.DiscPct.String(), l.LineNet.String(), l.RequestedShipDate); err != nil {
				return err
			}
		}
		created = true
		out, err = loadQuotation(ctx, tx, p.TenantID, id, false)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return Quotation{}, false, problem(http.StatusConflict, CodeDuplicateIntake, "a concurrent request stored this quotation first; retry to receive it")
	}
	if err != nil {
		return Quotation{}, false, err
	}
	return out, created, nil
}

const quotationColumns = `q.id::text, q.legal_entity_id::text, q.quotation_number, q.customer_id::text, q.invoice_account_id::text,
	q.currency::text, q.status, q.probability_pct, q.valid_until::text, q.net_total::text, q.lost_reason, q.version, q.created_by,
	q.created_at, q.updated_at,
	(select count(*) from sales_quotation_line l where l.tenant_id = q.tenant_id and l.quotation_id = q.id),
	(select count(*) from sales_quotation_line l where l.tenant_id = q.tenant_id and l.quotation_id = q.id and l.line_status = 'OPEN')`

func scanQuotation(row pgx.Row) (Quotation, error) {
	var q Quotation
	var net string
	if err := row.Scan(&q.ID, &q.LegalEntityID, &q.QuotationNumber, &q.CustomerID, &q.InvoiceAccountID, &q.Currency, &q.Status,
		&q.ProbabilityPct, &q.ValidUntil, &net, &q.LostReason, &q.Version, &q.CreatedBy, &q.CreatedAt, &q.UpdatedAt,
		&q.LineCount, &q.OpenLines); err != nil {
		return Quotation{}, err
	}
	q.NetTotal = decimal.RequireFromString(net)
	q.Lines = []QuotationLine{}
	return q, nil
}

// ListQuotations returns quotations in the caller's legal-entity scope, newest
// first, without their lines.
func (s *Postgres) ListQuotations(ctx context.Context, f QuotationFilter) ([]Quotation, error) {
	p, _ := tenant.FromContext(ctx)
	out := []Quotation{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+quotationColumns+` from sales_quotation q
			where q.tenant_id = $1 and ($2 = '' or q.legal_entity_id::text = $2) and ($3 = '' or q.customer_id::text = $3)
				and ($4 = '' or q.status = $4)
			order by q.created_at desc, q.id desc limit $5`, p.TenantID, f.LegalEntityID, f.CustomerID, f.Status, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			q, err := scanQuotation(rows)
			if err != nil {
				return err
			}
			out = append(out, q)
		}
		return rows.Err()
	})
	return out, err
}

// GetQuotation returns one quotation with its lines.
func (s *Postgres) GetQuotation(ctx context.Context, id string) (Quotation, error) {
	p, _ := tenant.FromContext(ctx)
	var q Quotation
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		q, err = loadQuotation(ctx, tx, p.TenantID, id, false)
		return err
	})
	return q, err
}

// SendQuotation moves a DRAFT quotation that is still valid to SENT.
func (s *Postgres) SendQuotation(ctx context.Context, id string, version int, now time.Time) (Quotation, error) {
	return s.transitionQuotation(ctx, id, version, func(q Quotation) error {
		validUntil, err := time.Parse(dayLayout, q.ValidUntil)
		if err != nil {
			return err
		}
		return quotationProblem(q.Status, domain.CheckSend(q.Status, validUntil, now))
	}, `update sales_quotation set status = 'SENT', version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`)
}

// LoseQuotation marks a DRAFT or SENT quotation LOST with a reason.
func (s *Postgres) LoseQuotation(ctx context.Context, id string, version int, reason string) (Quotation, error) {
	return s.transitionQuotation(ctx, id, version, func(q Quotation) error {
		return quotationProblem(q.Status, domain.CheckLose(q.Status))
	}, `update sales_quotation set status = 'LOST', lost_reason = $3, version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`, reason)
}

// transitionQuotation locks the quotation, checks the version and the
// transition, and applies update.
func (s *Postgres) transitionQuotation(ctx context.Context, id string, version int, check func(Quotation) error, update string, args ...any) (Quotation, error) {
	p, _ := tenant.FromContext(ctx)
	var out Quotation
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		q, err := loadQuotation(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if q.Version != version {
			return versionConflict()
		}
		if err := check(q); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, update, append([]any{p.TenantID, id}, args...)...); err != nil {
			return err
		}
		out, err = loadQuotation(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

// ConvertQuotation turns the selected open lines (every open line when
// LineNos is empty) into one DRAFT sales order at the quoted prices (FR-27.1,
// AC-27.6). Under the quotation's row lock and in one transaction it checks
// the version, status and validity, creates the order (which passes the
// product listing check like any order), marks the lines CONVERTED with the
// order id, moves the quotation to CONVERTED and writes quotation.converted.
// A failure leaves nothing behind; a retry with the old version is 412.
func (s *Postgres) ConvertQuotation(ctx context.Context, id string, in ConvertRequest, now time.Time) (QuotationConversion, error) {
	p, _ := tenant.FromContext(ctx)
	var out QuotationConversion
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		q, err := loadQuotation(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if q.Version != in.Version {
			return versionConflict()
		}
		validUntil, err := time.Parse(dayLayout, q.ValidUntil)
		if err != nil {
			return err
		}
		if err := quotationProblem(q.Status, domain.CheckConvertible(q.Status, validUntil, now)); err != nil {
			return err
		}
		selected, err := selectOpenLines(q, in.LineNos)
		if err != nil {
			return err
		}
		newOrder, err := orderFromQuotation(q, selected, in, now)
		if err != nil {
			return err
		}
		order, err := createOrderTx(ctx, tx, p.TenantID, newOrder)
		if err != nil {
			return err
		}
		// A converted or released order is entered like any other (FR-27.7).
		if err := checkAtEntryTx(ctx, tx, p.TenantID, &order); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update sales_quotation_line set line_status = 'CONVERTED', converted_order_id = $3
			where tenant_id = $1 and quotation_id = $2 and line_no = any($4::int[])`, p.TenantID, id, order.ID, selected); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update sales_quotation set status = 'CONVERTED', version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, id); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "quotation", id, salesevents.QuotationConverted, p.TenantID, salesevents.QuotationConvertedData{
			QuotationID: id, QuotationNumber: q.QuotationNumber, CustomerID: q.CustomerID, OrderID: order.ID, OrderNumber: order.OrderNumber,
			Lines: selected, Actor: in.Actor, Version: q.Version + 1}); err != nil {
			return err
		}
		out.Order = order
		out.Quotation, err = loadQuotation(ctx, tx, p.TenantID, id, false)
		return err
	})
	if err != nil {
		return QuotationConversion{}, err
	}
	return out, nil
}

// selectOpenLines applies domain.SelectLines to the quotation's OPEN lines.
func selectOpenLines(q Quotation, requested []int) ([]int, error) {
	var open []int
	for _, l := range q.Lines {
		if l.LineStatus == domain.QuoteLineOpen {
			open = append(open, l.LineNo)
		}
	}
	selected, err := domain.SelectLines(open, requested)
	if err != nil {
		return nil, problem(http.StatusUnprocessableEntity, CodeLineSelection, err.Error())
	}
	return selected, nil
}

// orderFromQuotation builds the DRAFT order for a conversion: the selected
// lines renumbered from 1 at the quoted price and discount, the header ship
// date the earliest of theirs. The request hash fingerprints the quotation,
// its version and the selection.
func orderFromQuotation(q Quotation, selected []int, in ConvertRequest, now time.Time) (NewOrder, error) {
	byNo := make(map[int]Line, len(q.Lines))
	for _, l := range q.Lines {
		byNo[l.LineNo] = l.Line
	}
	hash, err := domain.RequestHash(struct {
		Quotation string `json:"quotation"`
		Version   int    `json:"version"`
		Lines     []int  `json:"lines"`
	}{q.ID, q.Version, selected})
	if err != nil {
		return NewOrder{}, err
	}
	order := NewOrder{LegalEntityID: q.LegalEntityID, CustomerID: q.CustomerID, InvoiceAccountID: q.InvoiceAccountID,
		OrderType: "ORDER", Currency: q.Currency, Channel: quoteChannel, RequestHash: hash, OrderDate: now, Actor: in.Actor,
		CanOverrideListing: in.CanOverrideListing, ListingOverrideReason: in.ListingOverrideReason}
	for i, n := range selected {
		l := byNo[n]
		l.ID, l.LineNo, l.LineStatus, l.ConfirmedShipDate = "", i+1, "OPEN", nil
		order.Lines = append(order.Lines, l)
		if order.RequestedShipDate == "" || l.RequestedShipDate < order.RequestedShipDate {
			order.RequestedShipDate = l.RequestedShipDate
		}
	}
	if len(order.Lines) == 0 {
		return NewOrder{}, fmt.Errorf("conversion of quotation %s selected no lines", q.QuotationNumber)
	}
	return order, nil
}

func loadQuotation(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (Quotation, error) {
	query := `select ` + quotationColumns + ` from sales_quotation q where q.tenant_id = $1 and q.id = $2`
	if lock {
		query += ` for update`
	}
	q, err := scanQuotation(tx.QueryRow(ctx, query, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Quotation{}, quotationNotFound()
	}
	if err != nil {
		return Quotation{}, err
	}
	rows, err := tx.Query(ctx, `select id::text, line_no, product_id::text, quantity::text, unit, site_id::text, warehouse_id::text,
			unit_price::text, disc_pct::text, line_net::text, requested_ship_date::text, line_status, converted_order_id::text
		from sales_quotation_line where tenant_id = $1 and quotation_id = $2 order by line_no`, tenantID, id)
	if err != nil {
		return Quotation{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var l QuotationLine
		var qty, price, disc, lineNet string
		if err := rows.Scan(&l.ID, &l.LineNo, &l.ProductID, &qty, &l.Unit, &l.SiteID, &l.WarehouseID, &price, &disc, &lineNet,
			&l.RequestedShipDate, &l.LineStatus, &l.ConvertedOrderID); err != nil {
			return Quotation{}, err
		}
		l.Quantity = decimal.RequireFromString(qty)
		l.UnitPrice = decimal.RequireFromString(price)
		l.DiscPct = decimal.RequireFromString(disc)
		l.LineNet = decimal.RequireFromString(lineNet)
		q.Lines = append(q.Lines, l)
	}
	return q, rows.Err()
}
