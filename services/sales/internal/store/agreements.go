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
	// CommitmentType is QUANTITY (default) or AMOUNT; Commitment is a quantity
	// of Unit or an amount in Currency accordingly.
	CommitmentType string
	Commitment     decimal.Decimal
	ValidFrom      *string
	ValidTo        *string
	CumTolerance   decimal.Decimal
	// OverdeliveryPct is how far above the commitment releases may go (BR-27.9).
	OverdeliveryPct decimal.Decimal
	IdempotencyKey  string
	RequestHash     string
	Actor           string
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
	// CommitmentType is QUANTITY or AMOUNT. The commitment, released, ceiling and
	// remaining figures below are in CommitmentUnit: the agreement unit for a
	// QUANTITY agreement, the currency for an AMOUNT one (each release then draws
	// the net value of its line).
	CommitmentType string          `json:"commitmentType"`
	CommitmentUnit string          `json:"commitmentUnit"`
	Commitment     decimal.Decimal `json:"commitmentQty"`
	Released       decimal.Decimal `json:"releasedQty"`
	// OverdeliveryPct lets releases go above the commitment by that percent;
	// CeilingQty is the commitment with it and RemainingQty what can still be
	// released (the ceiling less the released quantity).
	OverdeliveryPct decimal.Decimal `json:"overdeliveryPct"`
	CeilingQty      decimal.Decimal `json:"ceilingQty"`
	Remaining       decimal.Decimal `json:"remainingQty"`
	// UsedPct is the released figure as a percent of the commitment to two
	// places (it passes 100 when the over-delivery tolerance is drawn on); absent
	// for a commitment of 0.
	UsedPct   *decimal.Decimal `json:"usedPct,omitempty"`
	Status    string           `json:"status"`
	ValidFrom *string          `json:"validFrom,omitempty"`
	ValidTo   *string          `json:"validTo,omitempty"`
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
	ID       string          `json:"id"`
	Quantity decimal.Decimal `json:"quantity"`
	// Units is what the release drew from the commitment, in the commitment unit
	// (the quantity, or the line net value of an AMOUNT agreement).
	Units decimal.Decimal `json:"units"`
	// DrawnQty is drawn after the release by raising its order line,
	// ReturnedQty what went back by lowering or cancelling it, both in the
	// commitment unit; the release is outstanding for Units + DrawnQty - ReturnedQty.
	DrawnQty     decimal.Decimal `json:"drawnQty"`
	ReturnedQty  decimal.Decimal `json:"returnedQty"`
	SalesOrderID string          `json:"salesOrderId"`
	OrderNumber  string          `json:"orderNumber"`
	Actor        string          `json:"actor"`
	ReleasedAt   time.Time       `json:"releasedAt"`
	// ReturnedAt is the time of the last return of quantity to the commitment
	// (an order cancelled, a line lowered or removed).
	ReturnedAt *time.Time `json:"returnedAt,omitempty"`
}

// AgreementFilter narrows the agreement list. Empty fields match everything;
// an empty LegalEntityID means every legal entity in the caller's scope.
type AgreementFilter struct {
	LegalEntityID string
	CustomerID    string
	Status        string
	// ExpiringBy (YYYY-MM-DD) keeps agreements whose validTo is on or before the
	// day, soonest first; an open-ended agreement never matches.
	ExpiringBy string
	// MinUsedPct keeps agreements whose released figure is at least that percent
	// of the commitment (a decimal string); empty keeps all.
	MinUsedPct string
	Limit      int
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
	// CustomerGroup is the customer's group read from the party service; the
	// release order's lines are checked against its GROUP listings (BR-27.13).
	CustomerGroup *string
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
	created_by, created_at, updated_at, cum_tolerance::text, cum_shipped::text, cum_reset_date::text, close_reason, closed_by, closed_at, overdelivery_pct::text,
	commitment_type`

func scanAgreement(row pgx.Row) (Agreement, error) {
	var a Agreement
	var price, commitment, released, tol, shipped, pct string
	if err := row.Scan(&a.ID, &a.LegalEntityID, &a.AgreementNumber, &a.CustomerID, &a.InvoiceAccountID, &a.ProductID, &a.Unit, &price,
		&a.Currency, &commitment, &released, &a.Status, &a.ValidFrom, &a.ValidTo, &a.Version, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt, &tol, &shipped, &a.CumResetDate, &a.CloseReason, &a.ClosedBy, &a.ClosedAt, &pct, &a.CommitmentType); err != nil {
		return Agreement{}, err
	}
	a.CommitmentUnit = a.Unit
	if a.CommitmentType == domain.CommitmentAmount {
		a.CommitmentUnit = a.Currency
	}
	a.CumTolerance = decimal.RequireFromString(tol)
	a.CumShipped = decimal.RequireFromString(shipped)
	a.UnitPrice = decimal.RequireFromString(price)
	a.Commitment = decimal.RequireFromString(commitment)
	a.Released = decimal.RequireFromString(released)
	a.OverdeliveryPct = decimal.RequireFromString(pct)
	a.CeilingQty = domain.AgreementCeiling(a.Commitment, a.OverdeliveryPct)
	a.Remaining = a.CeilingQty.Sub(a.Released)
	if a.Commitment.IsPositive() {
		used := a.Released.Mul(decimal.NewFromInt(100)).Div(a.Commitment).RoundBank(2)
		a.UsedPct = &used
	}
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
	if in.CommitmentType == "" {
		in.CommitmentType = domain.CommitmentQuantity
	}
	if !domain.ValidCommitmentType(in.CommitmentType) {
		return Agreement{}, false, problem(http.StatusBadRequest, "COM-0001", domain.ErrInvalidCommitmentType.Error())
	}
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
				product_id, unit, unit_price, currency, commitment_qty, valid_from, valid_to, idempotency_key, request_hash, created_by, cum_tolerance, overdelivery_pct, commitment_type)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10, $11::numeric, $12::date, $13::date, $14, $15, $16, $17::numeric, $18::numeric, $19)`,
			p.TenantID, id, in.LegalEntityID, domain.FormatAgreementNumber(seq), in.CustomerID, in.InvoiceAccountID, in.ProductID, in.Unit,
			in.UnitPrice.String(), in.Currency, in.Commitment.String(), in.ValidFrom, in.ValidTo, in.IdempotencyKey, in.RequestHash, in.Actor, in.CumTolerance.String(), in.OverdeliveryPct.String(), in.CommitmentType); err != nil {
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
				and ($6::date is null or valid_to <= $6::date)
				and ($7::numeric is null or (commitment_qty > 0 and released_qty * 100 >= $7::numeric * commitment_qty))
			order by case when $6::date is null then null else valid_to end, created_at desc, id desc limit $5`,
			p.TenantID, f.LegalEntityID, f.CustomerID, f.Status, f.Limit, nullIfEmpty(f.ExpiringBy), nullIfEmpty(f.MinUsedPct))
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
	rows, err := tx.Query(ctx, `select r.id::text, r.quantity::text, r.units::text, r.sales_order_id::text, o.order_number, r.actor, r.released_at,
			(select max(x.returned_at) from agreement_return x where x.tenant_id = r.tenant_id and x.release_id = r.id),
			(select coalesce(sum(d.quantity), 0)::text from agreement_draw d where d.tenant_id = r.tenant_id and d.release_id = r.id),
			(select coalesce(sum(x.quantity), 0)::text from agreement_return x where x.tenant_id = r.tenant_id and x.release_id = r.id)
		from agreement_release r join sales_order o on o.tenant_id = r.tenant_id and o.id = r.sales_order_id
		where r.tenant_id = $1 and r.agreement_id = $2 order by r.released_at desc, r.id desc`, tenantID, agreementID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgreementReleaseRecord{}
	for rows.Next() {
		var r AgreementReleaseRecord
		var qty, units, drawn, returned string
		if err := rows.Scan(&r.ID, &qty, &units, &r.SalesOrderID, &r.OrderNumber, &r.Actor, &r.ReleasedAt, &r.ReturnedAt, &drawn, &returned); err != nil {
			return nil, err
		}
		r.Quantity = decimal.RequireFromString(qty)
		r.Units = decimal.RequireFromString(units)
		r.DrawnQty = decimal.RequireFromString(drawn)
		r.ReturnedQty = decimal.RequireFromString(returned)
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
		units, err := domain.CommitmentUnits(a.CommitmentType, in.Quantity, a.UnitPrice, decimal.Zero)
		if err != nil {
			return problem(http.StatusBadRequest, "COM-0001", err.Error())
		}
		next, status, err := domain.ReleaseAgreement(a.Commitment, a.OverdeliveryPct, a.Released, units)
		if errors.Is(err, domain.ErrAgreementExhausted) {
			return problem(http.StatusUnprocessableEntity, CodeAgreementExhausted,
				"AGREEMENT_EXHAUSTED: agreement "+a.AgreementNumber+" has "+a.Remaining.String()+" "+a.CommitmentUnit+" remaining; the release asks for "+units.String())
		}
		if err != nil {
			return problem(http.StatusBadRequest, "COM-0001", err.Error())
		}
		lineNet, err := domain.LineNet(in.Quantity, a.UnitPrice, decimal.Zero)
		if err != nil {
			return problem(http.StatusBadRequest, "COM-0001", err.Error())
		}
		order, err := createOrderTx(ctx, tx, p.TenantID, NewOrder{LegalEntityID: a.LegalEntityID, CustomerID: a.CustomerID,
			InvoiceAccountID: a.InvoiceAccountID, CustomerGroup: in.CustomerGroup, OrderType: "ORDER", Currency: a.Currency, RequestedShipDate: in.RequestedShipDate,
			Channel: agreementChannel, RequestHash: in.RequestHash, OrderDate: now, Actor: in.Actor,
			Lines: []Line{{LineNo: 1, ProductID: a.ProductID, Quantity: in.Quantity, Unit: a.Unit, SiteID: in.SiteID, WarehouseID: in.WarehouseID,
				UnitPrice: a.UnitPrice, DiscPct: decimal.Zero, LineNet: lineNet, RequestedShipDate: in.RequestedShipDate}}})
		if err != nil {
			return err
		}
		// A converted or released order is entered like any other (FR-27.7).
		if err := checkAtEntryTx(ctx, tx, p.TenantID, &order); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update sales_agreement set released_qty = $3::numeric, status = $4, version = version + 1, updated_at = $5
			where tenant_id = $1 and id = $2`, p.TenantID, id, next.String(), status, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into agreement_release (tenant_id, id, legal_entity_id, agreement_id, quantity, sales_order_id,
				idempotency_key, request_hash, actor, released_at, units, unit_price)
			values ($1, $2, $3, $4, $5::numeric, $6, $7, $8, $9, $10, $11::numeric, $12::numeric)`,
			p.TenantID, ids.New(), a.LegalEntityID, id, in.Quantity.String(), order.ID, in.IdempotencyKey, in.RequestHash, in.Actor, now,
			units.String(), a.UnitPrice.String()); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "agreement", id, salesevents.AgreementReleased, p.TenantID, salesevents.AgreementReleasedData{
			AgreementID: id, AgreementNumber: a.AgreementNumber, CustomerID: a.CustomerID, ProductID: a.ProductID, SalesOrderID: order.ID,
			Quantity: in.Quantity.String(), CommitmentType: a.CommitmentType, DrawnUnits: units.String(), Released: next.String(), Remaining: a.CeilingQty.Sub(next).String(), Status: status,
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
