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
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// agreementChannel is the channel of orders created by an agreement release.
const agreementChannel = "AGREEMENT"

// NewAgreement is a validated blanket quantity commitment for one product of a
// customer at an agreed unit price (FR-27.10).
type NewAgreement struct {
	LegalEntityID    string
	CustomerID       string
	InvoiceAccountID string
	ProductID        string
	Unit             string
	UnitPrice        decimal.Decimal
	Currency         string
	Commitment       decimal.Decimal
	ValidFrom        *string
	ValidTo          *string
	CumTolerance     decimal.Decimal
	IdempotencyKey   string
	RequestHash      string
	Actor            string
}

// Agreement is a sales agreement with its commitment and released quantity.
type Agreement struct {
	ID               string          `json:"id"`
	LegalEntityID    string          `json:"legalEntityId"`
	AgreementNumber  string          `json:"agreementNumber"`
	CustomerID       string          `json:"customerId"`
	InvoiceAccountID string          `json:"invoiceAccountId"`
	ProductID        string          `json:"productId"`
	Unit             string          `json:"unit"`
	UnitPrice        decimal.Decimal `json:"unitPrice"`
	Currency         string          `json:"currency"`
	Commitment       decimal.Decimal `json:"commitmentQty"`
	Released         decimal.Decimal `json:"releasedQty"`
	Remaining        decimal.Decimal `json:"remainingQty"`
	Status           string          `json:"status"`
	ValidFrom        *string         `json:"validFrom,omitempty"`
	ValidTo          *string         `json:"validTo,omitempty"`
	// CumTolerance, CumShipped and CumResetDate are the cumulative quantity
	// settings of scheduling-agreement releases (BR-27.12): the tolerance a
	// release's cumulative received quantity may differ from CumShipped, the
	// quantity shipped since CumResetDate.
	CumTolerance decimal.Decimal `json:"cumTolerance"`
	CumShipped   decimal.Decimal `json:"cumShipped"`
	CumResetDate *string         `json:"cumResetDate,omitempty"`
	// CloseReason, ClosedBy and ClosedAt are set when the agreement was closed early.
	CloseReason *string    `json:"closeReason,omitempty"`
	ClosedBy    *string    `json:"closedBy,omitempty"`
	ClosedAt    *time.Time `json:"closedAt,omitempty"`
	Version     int        `json:"version"`
	CreatedBy   string     `json:"createdBy"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	// Releases is the release history, newest first; filled only on a single read.
	Releases []AgreementReleaseRecord `json:"releases,omitempty"`
}

// AgreementReleaseRecord is one stored release and the order it created.
type AgreementReleaseRecord struct {
	ID           string          `json:"id"`
	Quantity     decimal.Decimal `json:"quantity"`
	SalesOrderID string          `json:"salesOrderId"`
	OrderNumber  string          `json:"orderNumber"`
	Actor        string          `json:"actor"`
	ReleasedAt   time.Time       `json:"releasedAt"`
	// ReturnedAt is set when the order was cancelled and the quantity went back
	// to the commitment.
	ReturnedAt *time.Time `json:"returnedAt,omitempty"`
}

// AgreementFilter narrows the agreement list. Empty fields match everything;
// an empty LegalEntityID means every legal entity in the caller's scope.
type AgreementFilter struct {
	LegalEntityID string
	CustomerID    string
	Status        string
	Limit         int
}

// AgreementRelease is one release request: the quantity to draw and the
// delivery data of the order line it creates.
type AgreementRelease struct {
	Quantity          decimal.Decimal
	SiteID            string
	WarehouseID       string
	RequestedShipDate string
	IdempotencyKey    string
	RequestHash       string
	Actor             string
}

// ReleaseResult is the agreement after a release and the DRAFT order the
// release created.
type ReleaseResult struct {
	Agreement Agreement `json:"agreement"`
	Order     Order     `json:"order"`
}

func agreementNotFound() error {
	return problem(http.StatusNotFound, CodeAgreementNotFound, "sales agreement not found")
}

const agreementColumns = `id::text, legal_entity_id::text, agreement_number, customer_id::text, invoice_account_id::text, product_id::text,
	unit, unit_price::text, currency::text, commitment_qty::text, released_qty::text, status, valid_from::text, valid_to::text, version,
	created_by, created_at, updated_at, cum_tolerance::text, cum_shipped::text, cum_reset_date::text, close_reason, closed_by, closed_at`

func scanAgreement(row pgx.Row) (Agreement, error) {
	var a Agreement
	var price, commitment, released, tol, shipped string
	if err := row.Scan(&a.ID, &a.LegalEntityID, &a.AgreementNumber, &a.CustomerID, &a.InvoiceAccountID, &a.ProductID, &a.Unit, &price,
		&a.Currency, &commitment, &released, &a.Status, &a.ValidFrom, &a.ValidTo, &a.Version, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt, &tol, &shipped, &a.CumResetDate, &a.CloseReason, &a.ClosedBy, &a.ClosedAt); err != nil {
		return Agreement{}, err
	}
	a.CumTolerance = decimal.RequireFromString(tol)
	a.CumShipped = decimal.RequireFromString(shipped)
	a.UnitPrice = decimal.RequireFromString(price)
	a.Commitment = decimal.RequireFromString(commitment)
	a.Released = decimal.RequireFromString(released)
	a.Remaining = a.Commitment.Sub(a.Released)
	return a, nil
}

func loadAgreement(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (Agreement, error) {
	q := `select ` + agreementColumns + ` from sales_agreement where tenant_id = $1 and id = $2`
	if lock {
		q += " for update"
	}
	a, err := scanAgreement(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Agreement{}, agreementNotFound()
	}
	return a, err
}

// CreateAgreement stores a new ACTIVE agreement numbered SA-. A repeat of the
// same Idempotency-Key with the same body returns the stored agreement with
// created=false; the key with another body is refused.
func (s *Postgres) CreateAgreement(ctx context.Context, in NewAgreement) (Agreement, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out Agreement
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var existing, hash string
		err := tx.QueryRow(ctx, `select id::text, request_hash from sales_agreement where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, in.IdempotencyKey).Scan(&existing, &hash)
		if err == nil {
			if hash != in.RequestHash {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			out, err = loadAgreement(ctx, tx, p.TenantID, existing, false)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		seq, err := nextSequence(ctx, tx, p.TenantID, in.LegalEntityID, "SA")
		if err != nil {
			return err
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into sales_agreement (tenant_id, id, legal_entity_id, agreement_number, customer_id, invoice_account_id,
				product_id, unit, unit_price, currency, commitment_qty, valid_from, valid_to, idempotency_key, request_hash, created_by, cum_tolerance)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10, $11::numeric, $12::date, $13::date, $14, $15, $16, $17::numeric)`,
			p.TenantID, id, in.LegalEntityID, domain.FormatAgreementNumber(seq), in.CustomerID, in.InvoiceAccountID, in.ProductID, in.Unit,
			in.UnitPrice.String(), in.Currency, in.Commitment.String(), in.ValidFrom, in.ValidTo, in.IdempotencyKey, in.RequestHash, in.Actor, in.CumTolerance.String()); err != nil {
			return err
		}
		created = true
		out, err = loadAgreement(ctx, tx, p.TenantID, id, false)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return Agreement{}, false, problem(http.StatusConflict, CodeDuplicateIntake, "a concurrent request stored this agreement first; retry to receive it")
	}
	if err != nil {
		return Agreement{}, false, err
	}
	return out, created, nil
}

// ListAgreements returns agreements in the caller's legal-entity scope, newest
// first.
func (s *Postgres) ListAgreements(ctx context.Context, f AgreementFilter) ([]Agreement, error) {
	p, _ := tenant.FromContext(ctx)
	out := []Agreement{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+agreementColumns+` from sales_agreement
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) and ($3 = '' or customer_id::text = $3) and ($4 = '' or status = $4)
			order by created_at desc, id desc limit $5`, p.TenantID, f.LegalEntityID, f.CustomerID, f.Status, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAgreement(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// GetAgreement returns one agreement with its remaining commitment and its
// release history.
func (s *Postgres) GetAgreement(ctx context.Context, id string) (Agreement, error) {
	p, _ := tenant.FromContext(ctx)
	var a Agreement
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if a, err = loadAgreement(ctx, tx, p.TenantID, id, false); err != nil {
			return err
		}
		a.Releases, err = loadReleases(ctx, tx, p.TenantID, id)
		return err
	})
	return a, err
}

func loadReleases(ctx context.Context, tx pgx.Tx, tenantID, agreementID string) ([]AgreementReleaseRecord, error) {
	rows, err := tx.Query(ctx, `select r.id::text, r.quantity::text, r.sales_order_id::text, o.order_number, r.actor, r.released_at, x.returned_at
		from agreement_release r join sales_order o on o.tenant_id = r.tenant_id and o.id = r.sales_order_id
		left join agreement_return x on x.tenant_id = r.tenant_id and x.release_id = r.id
		where r.tenant_id = $1 and r.agreement_id = $2 order by r.released_at desc, r.id desc`, tenantID, agreementID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgreementReleaseRecord{}
	for rows.Next() {
		var r AgreementReleaseRecord
		var qty string
		if err := rows.Scan(&r.ID, &qty, &r.SalesOrderID, &r.OrderNumber, &r.Actor, &r.ReleasedAt, &r.ReturnedAt); err != nil {
			return nil, err
		}
		r.Quantity = decimal.RequireFromString(qty)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReleaseAgreement draws quantity against the commitment and creates a DRAFT
// order of one line at the agreement's price, all in one transaction under the
// agreement's row lock, so concurrent releases cannot overdraw (FR-27.10,
// BR-27.9). A release beyond the remaining commitment is refused with 422
// AGREEMENT_EXHAUSTED (SLS-0008, AC-27.9) and a release outside the validity
// period with SLS-0029; neither changes anything. The order passes the product
// listing check like any other order. A repeat of the same Idempotency-Key and
// body returns the stored result with created=false.
func (s *Postgres) ReleaseAgreement(ctx context.Context, id string, in AgreementRelease, now time.Time) (ReleaseResult, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out ReleaseResult
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var priorAgreement, priorOrder, hash string
		err := tx.QueryRow(ctx, `select agreement_id::text, sales_order_id::text, request_hash from agreement_release
			where tenant_id = $1 and idempotency_key = $2`, p.TenantID, in.IdempotencyKey).Scan(&priorAgreement, &priorOrder, &hash)
		if err == nil {
			if priorAgreement != id || hash != in.RequestHash {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request")
			}
			if out.Agreement, err = loadAgreement(ctx, tx, p.TenantID, id, false); err != nil {
				return err
			}
			out.Order, err = loadOrder(ctx, tx, p.TenantID, priorOrder, false)
			out.Order.Warnings = supplementaryWarnings(out.Order)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		a, err := loadAgreement(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if a.Status == domain.AgreementClosed {
			return agreementClosed(a)
		}
		from, err := parseDay(a.ValidFrom)
		if err != nil {
			return err
		}
		to, err := parseDay(a.ValidTo)
		if err != nil {
			return err
		}
		if !domain.AgreementValidOn(from, to, now.UTC().Truncate(24*time.Hour)) {
			return problem(http.StatusUnprocessableEntity, CodeAgreementNotValid, "agreement "+a.AgreementNumber+" is not valid on "+now.UTC().Format(dayLayout))
		}
		next, status, err := domain.ReleaseAgreement(a.Commitment, a.Released, in.Quantity)
		if errors.Is(err, domain.ErrAgreementExhausted) {
			return problem(http.StatusUnprocessableEntity, CodeAgreementExhausted,
				"AGREEMENT_EXHAUSTED: agreement "+a.AgreementNumber+" has "+a.Remaining.String()+" "+a.Unit+" remaining; the release asks for "+in.Quantity.String())
		}
		if err != nil {
			return problem(http.StatusBadRequest, "COM-0001", err.Error())
		}
		lineNet, err := domain.LineNet(in.Quantity, a.UnitPrice, decimal.Zero)
		if err != nil {
			return problem(http.StatusBadRequest, "COM-0001", err.Error())
		}
		order, err := createOrderTx(ctx, tx, p.TenantID, NewOrder{LegalEntityID: a.LegalEntityID, CustomerID: a.CustomerID,
			InvoiceAccountID: a.InvoiceAccountID, OrderType: "ORDER", Currency: a.Currency, RequestedShipDate: in.RequestedShipDate,
			Channel: agreementChannel, RequestHash: in.RequestHash, OrderDate: now, Actor: in.Actor,
			Lines: []Line{{LineNo: 1, ProductID: a.ProductID, Quantity: in.Quantity, Unit: a.Unit, SiteID: in.SiteID, WarehouseID: in.WarehouseID,
				UnitPrice: a.UnitPrice, DiscPct: decimal.Zero, LineNet: lineNet, RequestedShipDate: in.RequestedShipDate}}})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update sales_agreement set released_qty = $3::numeric, status = $4, version = version + 1, updated_at = $5
			where tenant_id = $1 and id = $2`, p.TenantID, id, next.String(), status, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into agreement_release (tenant_id, id, legal_entity_id, agreement_id, quantity, sales_order_id,
				idempotency_key, request_hash, actor, released_at)
			values ($1, $2, $3, $4, $5::numeric, $6, $7, $8, $9, $10)`,
			p.TenantID, ids.New(), a.LegalEntityID, id, in.Quantity.String(), order.ID, in.IdempotencyKey, in.RequestHash, in.Actor, now); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "agreement", id, salesevents.AgreementReleased, p.TenantID, salesevents.AgreementReleasedData{
			AgreementID: id, AgreementNumber: a.AgreementNumber, CustomerID: a.CustomerID, ProductID: a.ProductID, SalesOrderID: order.ID,
			Quantity: in.Quantity.String(), Released: next.String(), Remaining: a.Commitment.Sub(next).String(), Status: status,
			Version: a.Version + 1, Actor: in.Actor}); err != nil {
			return err
		}
		created = true
		out.Order = order
		out.Agreement, err = loadAgreement(ctx, tx, p.TenantID, id, false)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return ReleaseResult{}, false, problem(http.StatusConflict, CodeDuplicateIntake, "a concurrent request stored this release first; retry to receive it")
	}
	if err != nil {
		return ReleaseResult{}, false, err
	}
	return out, created, nil
}
