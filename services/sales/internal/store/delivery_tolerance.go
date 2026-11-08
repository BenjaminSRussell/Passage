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

// Problem codes of the delivery tolerances (BR-27.9, BR-28.7).
const (
	CodeToleranceExists   = "SLS-0126"
	CodeToleranceNotFound = "SLS-0127"
)

// NewDeliveryTolerance is a validated rule for one product, for one customer
// (or every customer of the legal entity when CustomerID is nil).
type NewDeliveryTolerance struct {
	LegalEntityID  string
	ProductID      string
	CustomerID     *string
	Tolerance      domain.DeliveryTolerance
	IdempotencyKey string
	RequestHash    string
	Actor          string
}

// DeliveryTolerance is a stored rule.
type DeliveryTolerance struct {
	ID               string          `json:"id"`
	LegalEntityID    string          `json:"legalEntityId"`
	ProductID        string          `json:"productId"`
	CustomerID       *string         `json:"customerId,omitempty"`
	OverdeliveryPct  decimal.Decimal `json:"overdeliveryPct"`
	UnderdeliveryPct decimal.Decimal `json:"underdeliveryPct"`
	PartialDelivery  string          `json:"partialDelivery"`
	Version          int             `json:"version"`
	UpdatedBy        string          `json:"updatedBy"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
}

func (t DeliveryTolerance) values() domain.DeliveryTolerance {
	return domain.DeliveryTolerance{OverdeliveryPct: t.OverdeliveryPct, UnderdeliveryPct: t.UnderdeliveryPct, PartialDelivery: t.PartialDelivery}
}

// DeliveryToleranceFilter narrows the list; empty fields match everything.
type DeliveryToleranceFilter struct {
	LegalEntityID string
	ProductID     string
	CustomerID    string
	Limit         int
}

// ResolvedTolerance is the tolerance that governs a product for a customer:
// the stored rule that won (ToleranceID empty for the default) and its source.
type ResolvedTolerance struct {
	ToleranceID string
	Source      string
	Tolerance   domain.DeliveryTolerance
}

const toleranceColumns = `id::text, legal_entity_id::text, product_id::text, customer_id::text, overdelivery_pct::text, underdelivery_pct::text,
	partial_delivery, version, updated_by, created_at, updated_at`

func scanTolerance(row pgx.Row) (DeliveryTolerance, error) {
	var t DeliveryTolerance
	var over, under string
	if err := row.Scan(&t.ID, &t.LegalEntityID, &t.ProductID, &t.CustomerID, &over, &under, &t.PartialDelivery, &t.Version, &t.UpdatedBy,
		&t.CreatedAt, &t.UpdatedAt); err != nil {
		return DeliveryTolerance{}, err
	}
	t.OverdeliveryPct, t.UnderdeliveryPct = decimal.RequireFromString(over), decimal.RequireFromString(under)
	return t, nil
}

func toleranceNotFound() error {
	return problem(http.StatusNotFound, CodeToleranceNotFound, "delivery tolerance not found")
}

func toleranceEvent(t DeliveryTolerance, previous *domain.DeliveryTolerance, actor string) salesevents.DeliveryToleranceData {
	d := salesevents.DeliveryToleranceData{ToleranceID: t.ID, LegalEntityID: t.LegalEntityID, ProductID: t.ProductID, CustomerID: t.CustomerID,
		OverdeliveryPct: t.OverdeliveryPct.String(), UnderdeliveryPct: t.UnderdeliveryPct.String(), PartialDelivery: t.PartialDelivery,
		Actor: actor, Version: t.Version}
	if previous != nil {
		d.PreviousOverdeliveryPct, d.PreviousUnderdeliveryPct, d.PreviousPartialDelivery =
			previous.OverdeliveryPct.String(), previous.UnderdeliveryPct.String(), previous.PartialDelivery
	}
	return d
}

func loadTolerance(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (DeliveryTolerance, error) {
	q := `select ` + toleranceColumns + ` from delivery_tolerance where tenant_id = $1 and id = $2`
	if lock {
		q += " for update"
	}
	out, err := scanTolerance(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryTolerance{}, toleranceNotFound()
	}
	return out, err
}

// CreateDeliveryTolerance stores a rule. A repeat of the same Idempotency-Key
// with the same body returns the stored rule with created=false; the key with
// another body is refused. A product that already has a rule for the customer
// (or for every customer) in the legal entity is 409 SLS-0126.
func (s *Postgres) CreateDeliveryTolerance(ctx context.Context, in NewDeliveryTolerance) (DeliveryTolerance, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out DeliveryTolerance
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var existing, hash string
		err := tx.QueryRow(ctx, `select id::text, request_hash from delivery_tolerance where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, in.IdempotencyKey).Scan(&existing, &hash)
		if err == nil {
			if hash != in.RequestHash {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			out, err = loadTolerance(ctx, tx, p.TenantID, existing, false)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		id := ids.New()
		t := in.Tolerance
		if _, err := tx.Exec(ctx, `insert into delivery_tolerance (tenant_id, id, legal_entity_id, product_id, customer_id, overdelivery_pct,
				underdelivery_pct, partial_delivery, idempotency_key, request_hash, created_by, updated_by)
			values ($1, $2, $3, $4, $5, $6::numeric, $7::numeric, $8, $9, $10, $11, $11)`,
			p.TenantID, id, in.LegalEntityID, in.ProductID, in.CustomerID, t.OverdeliveryPct.String(), t.UnderdeliveryPct.String(),
			t.PartialDelivery, in.IdempotencyKey, in.RequestHash, in.Actor); err != nil {
			return err
		}
		created = true
		out, err = loadTolerance(ctx, tx, p.TenantID, id, false)
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, "deliverytolerance", id, salesevents.DeliveryToleranceCreated, p.TenantID, toleranceEvent(out, nil, in.Actor))
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return DeliveryTolerance{}, false, problem(http.StatusConflict, CodeToleranceExists,
			"this product already has a delivery tolerance for the customer (or every customer) in the legal entity; change it with PUT, or a concurrent request stored it first")
	}
	if err != nil {
		return DeliveryTolerance{}, false, err
	}
	return out, created, nil
}

// ListDeliveryTolerances returns the rules in the caller's scope by product and
// customer (the all-customers rule first).
func (s *Postgres) ListDeliveryTolerances(ctx context.Context, f DeliveryToleranceFilter) ([]DeliveryTolerance, error) {
	p, _ := tenant.FromContext(ctx)
	out := []DeliveryTolerance{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+toleranceColumns+` from delivery_tolerance
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) and ($3 = '' or product_id::text = $3) and ($4 = '' or customer_id::text = $4)
			order by legal_entity_id, product_id, customer_id nulls first, id limit $5`, p.TenantID, f.LegalEntityID, f.ProductID, f.CustomerID, f.Limit)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (DeliveryTolerance, error) { return scanTolerance(row) })
		return err
	})
	return out, err
}

// GetDeliveryTolerance returns one rule.
func (s *Postgres) GetDeliveryTolerance(ctx context.Context, id string) (DeliveryTolerance, error) {
	p, _ := tenant.FromContext(ctx)
	var out DeliveryTolerance
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadTolerance(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

// ReplaceDeliveryTolerance sets new figures under If-Match; the resolve call
// answers with them from now on. Unchanged figures return the stored row
// without a new version.
func (s *Postgres) ReplaceDeliveryTolerance(ctx context.Context, id string, version int, t domain.DeliveryTolerance, actor string, now time.Time) (DeliveryTolerance, error) {
	p, _ := tenant.FromContext(ctx)
	var out DeliveryTolerance
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := loadTolerance(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return versionConflict()
		}
		if cur.OverdeliveryPct.Equal(t.OverdeliveryPct) && cur.UnderdeliveryPct.Equal(t.UnderdeliveryPct) && cur.PartialDelivery == t.PartialDelivery {
			out = cur
			return nil
		}
		if _, err := tx.Exec(ctx, `update delivery_tolerance set overdelivery_pct = $3::numeric, underdelivery_pct = $4::numeric, partial_delivery = $5,
				version = version + 1, updated_by = $6, updated_at = $7 where tenant_id = $1 and id = $2`,
			p.TenantID, id, t.OverdeliveryPct.String(), t.UnderdeliveryPct.String(), t.PartialDelivery, actor, now); err != nil {
			return err
		}
		out, err = loadTolerance(ctx, tx, p.TenantID, id, false)
		if err != nil {
			return err
		}
		prev := cur.values()
		return enqueue(ctx, tx, "deliverytolerance", id, salesevents.DeliveryToleranceChanged, p.TenantID, toleranceEvent(out, &prev, actor))
	})
	return out, err
}

// DeleteDeliveryTolerance removes a rule; the product and customer fall back to
// the all-customers rule or the default. deliverytolerance.removed carries the
// figures the rule had.
func (s *Postgres) DeleteDeliveryTolerance(ctx context.Context, id, actor string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := loadTolerance(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from delivery_tolerance where tenant_id = $1 and id = $2`, p.TenantID, id); err != nil {
			return err
		}
		return enqueue(ctx, tx, "deliverytolerance", id, salesevents.DeliveryToleranceRemoved, p.TenantID, toleranceEvent(cur, nil, actor))
	})
}

// ResolveDeliveryTolerance returns the tolerance of productID for customerID in
// a legal entity: the customer's own rule, else the all-customers rule, else
// the default (exact quantities, partial delivery allowed).
func (s *Postgres) ResolveDeliveryTolerance(ctx context.Context, legalEntityID, productID, customerID string) (ResolvedTolerance, error) {
	p, _ := tenant.FromContext(ctx)
	var out ResolvedTolerance
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = resolveToleranceTx(ctx, tx, p.TenantID, legalEntityID, productID, customerID)
		return err
	})
	return out, err
}

// resolveToleranceTx is ResolveDeliveryTolerance inside a transaction, for the
// flows that apply the tolerance (a delivery report).
func resolveToleranceTx(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, productID, customerID string) (ResolvedTolerance, error) {
	var out ResolvedTolerance
	rows, err := tx.Query(ctx, `select `+toleranceColumns+` from delivery_tolerance
		where tenant_id = $1 and legal_entity_id = $2 and product_id = $3 and (customer_id is null or customer_id = $4)`,
		tenantID, legalEntityID, productID, customerID)
	if err != nil {
		return out, err
	}
	stored, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (DeliveryTolerance, error) { return scanTolerance(row) })
	if err != nil {
		return out, err
	}
	rules := make([]domain.ToleranceRule, len(stored))
	for i, t := range stored {
		rules[i] = domain.ToleranceRule{ID: t.ID, CustomerID: t.CustomerID, DeliveryTolerance: t.values()}
	}
	out.Tolerance, out.ToleranceID, out.Source = domain.ResolveTolerance(rules, customerID)
	return out, nil
}
