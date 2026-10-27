package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
)

const (
	// CodeItemCoverageExists is returned when the item already has a coverage
	// for the same site and planning scope (warehouse or subcontractor).
	CodeItemCoverageExists = "PLAN-ITEM-COVERAGE-EXISTS"
	// CodeItemCoverageNotFound is returned for an unknown item coverage id.
	CodeItemCoverageNotFound = "PLAN-ITEM-COVERAGE-NOT-FOUND"
	// CodeCoverageGroupUnknown is returned when an item coverage names a
	// coverage group the tenant does not have.
	CodeCoverageGroupUnknown = "PLAN-COVERAGE-GROUP-UNKNOWN"

	foreignKeyViolation               = "23503"
	itemCoverageScopeConstraint       = "item_coverage_scope_key"
	itemCoverageIdempotencyConstraint = "item_coverage_idempotency_key"
	itemCoverageGroupConstraint       = "item_coverage_group_fkey"
	itemCoverageProfileConstraint     = "item_coverage_buffer_profile_fkey"
)

// ItemCoverage is a stored item coverage. Version is its ETag;
// CoverageGroupCode is read from the referenced group for display.
type ItemCoverage struct {
	ID                string
	Version           int
	CoverageGroupCode string
	Spec              domain.ItemCoverageSpec
}

// ItemCoverageCreate is one create command; IdempotencyKey and InputHash are
// empty when the caller sent no Idempotency-Key.
type ItemCoverageCreate struct {
	IdempotencyKey string
	InputHash      string
	Spec           domain.ItemCoverageSpec
}

const itemCoverageSelect = `select c.id::text, c.version, g.code, c.item_id::text, c.site_id::text, coalesce(c.warehouse_id::text, ''),
	c.coverage_group_id::text, c.min_qty::text, c.max_qty::text, c.safety_margin_receipt_days, c.lead_time_days,
	coalesce(c.vendor_id::text, ''), c.planned_order_type, c.planning_strategy_code, c.planning_scope, c.decoupling_point,
	coalesce(c.buffer_profile_id::text, ''), c.required_shelf_life_days, c.priority_based
	from item_coverage c join coverage_group g on g.tenant_id = c.tenant_id and g.id = c.coverage_group_id`

func scanItemCoverage(row pgx.Row) (ItemCoverage, error) {
	var c ItemCoverage
	s := &c.Spec
	var minQty, maxQty string
	if err := row.Scan(&c.ID, &c.Version, &c.CoverageGroupCode, &s.ItemID, &s.SiteID, &s.WarehouseID, &s.CoverageGroupID,
		&minQty, &maxQty, &s.SafetyMarginReceiptDays, &s.LeadTimeDays, &s.VendorID, &s.PlannedOrderType, &s.PlanningStrategy,
		&s.PlanningScope, &s.DecouplingPoint, &s.BufferProfileID, &s.RequiredShelfLifeDays, &s.PriorityBased); err != nil {
		return ItemCoverage{}, err
	}
	var err error
	if s.MinQty, err = decimal.NewFromString(minQty); err != nil {
		return ItemCoverage{}, fmt.Errorf("item coverage %s: min_qty: %w", c.ID, err)
	}
	if s.MaxQty, err = decimal.NewFromString(maxQty); err != nil {
		return ItemCoverage{}, fmt.Errorf("item coverage %s: max_qty: %w", c.ID, err)
	}
	return c, nil
}

func itemCoverageNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeItemCoverageNotFound, "item coverage not found")
}

// itemCoverageWriteError maps the constraint violations of a create or update
// to their problems: the scope key is 409, an unknown coverage group or
// buffer profile 422.
func itemCoverageWriteError(err error, spec domain.ItemCoverageSpec) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch {
	case pgErr.Code == uniqueViolation && pgErr.ConstraintName == itemCoverageScopeConstraint:
		return httpx.NewProblem(http.StatusConflict, CodeItemCoverageExists,
			"item "+spec.ItemID+" already has a "+spec.PlanningScope+"-scope coverage at this site")
	case pgErr.Code == uniqueViolation && pgErr.ConstraintName == itemCoverageIdempotencyConstraint:
		// A concurrent create with the same key committed first.
		return idempotencyReuse()
	case pgErr.Code == foreignKeyViolation && pgErr.ConstraintName == itemCoverageGroupConstraint:
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeCoverageGroupUnknown,
			"coverage group "+spec.CoverageGroupID+" does not exist")
	case pgErr.Code == foreignKeyViolation && pgErr.ConstraintName == itemCoverageProfileConstraint:
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeProfileNotFound,
			"buffer profile "+spec.BufferProfileID+" does not exist")
	}
	return err
}

// ListItemCoverages returns the tenant's item coverages, optionally only those
// of one item, ordered by item, site, scope and id.
func (s *Postgres) ListItemCoverages(ctx context.Context, itemID string, limit int) ([]ItemCoverage, error) {
	p, _ := tenant.FromContext(ctx)
	out := []ItemCoverage{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, itemCoverageSelect+` where c.tenant_id = $1 and ($2::uuid is null or c.item_id = $2::uuid)
			order by c.item_id, c.site_id, c.planning_scope, c.id limit $3`, p.TenantID, nullableUUID(itemID), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanItemCoverage(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// GetItemCoverage returns one item coverage by id.
func (s *Postgres) GetItemCoverage(ctx context.Context, id string) (ItemCoverage, error) {
	p, _ := tenant.FromContext(ctx)
	var c ItemCoverage
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		c, err = scanItemCoverage(tx.QueryRow(ctx, itemCoverageSelect+` where c.tenant_id = $1 and c.id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ItemCoverage{}, itemCoverageNotFound()
	}
	return c, err
}

// CreateItemCoverage stores a validated spec at version 1. The same item,
// site and scope again is 409 PLAN-ITEM-COVERAGE-EXISTS and an unknown
// coverage group 422 PLAN-COVERAGE-GROUP-UNKNOWN. With an Idempotency-Key, a
// retry of the same body returns the coverage the first call created
// (replayed true) and another body under the same key is 422.
func (s *Postgres) CreateItemCoverage(ctx context.Context, req ItemCoverageCreate) (ItemCoverage, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out ItemCoverage
	replayed := false
	var key, hash *string
	if req.IdempotencyKey != "" {
		key, hash = &req.IdempotencyKey, &req.InputHash
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			var stored, id string
			err := tx.QueryRow(ctx, `select input_hash, id::text from item_coverage where tenant_id = $1 and idempotency_key = $2`,
				p.TenantID, *key).Scan(&stored, &id)
			if err == nil {
				if stored != *hash {
					return idempotencyReuse()
				}
				c, err := scanItemCoverage(tx.QueryRow(ctx, itemCoverageSelect+` where c.tenant_id = $1 and c.id = $2`, p.TenantID, id))
				out, replayed = c, true
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		c := req.Spec
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into item_coverage (tenant_id, id, item_id, site_id, warehouse_id, coverage_group_id,
			min_qty, max_qty, safety_margin_receipt_days, lead_time_days, vendor_id, planned_order_type, planning_strategy_code,
			planning_scope, decoupling_point, buffer_profile_id, required_shelf_life_days, priority_based, idempotency_key, input_hash)
			values ($1, $2, $3, $4, $5, $6, $7::numeric, $8::numeric, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)`,
			p.TenantID, id, c.ItemID, c.SiteID, optional(c.WarehouseID), c.CoverageGroupID, c.MinQty.String(), c.MaxQty.String(),
			c.SafetyMarginReceiptDays, c.LeadTimeDays, optional(c.VendorID), c.PlannedOrderType, c.PlanningStrategy,
			c.PlanningScope, c.DecouplingPoint, optional(c.BufferProfileID), c.RequiredShelfLifeDays, c.PriorityBased, key, hash); err != nil {
			return err
		}
		var err error
		out, err = scanItemCoverage(tx.QueryRow(ctx, itemCoverageSelect+` where c.tenant_id = $1 and c.id = $2`, p.TenantID, id))
		return err
	})
	if err != nil {
		return ItemCoverage{}, false, itemCoverageWriteError(err, req.Spec)
	}
	return out, replayed, nil
}

// UpdateItemCoverage replaces the item coverage id with spec under If-Match:
// expectedVersion must be its current version (412 otherwise) and the version
// is bumped. The scope key and the coverage group are checked as on create.
func (s *Postgres) UpdateItemCoverage(ctx context.Context, id string, expectedVersion int, spec domain.ItemCoverageSpec) (ItemCoverage, error) {
	p, _ := tenant.FromContext(ctx)
	var out ItemCoverage
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var version int
		err := tx.QueryRow(ctx, `select version from item_coverage where tenant_id = $1 and id = $2 for update`, p.TenantID, id).Scan(&version)
		if errors.Is(err, pgx.ErrNoRows) {
			return itemCoverageNotFound()
		}
		if err != nil {
			return err
		}
		if version != expectedVersion {
			return versionConflict("item coverage", version)
		}
		if _, err := tx.Exec(ctx, `update item_coverage set item_id = $3, site_id = $4, warehouse_id = $5, coverage_group_id = $6,
			min_qty = $7::numeric, max_qty = $8::numeric, safety_margin_receipt_days = $9, lead_time_days = $10, vendor_id = $11,
			planned_order_type = $12, planning_strategy_code = $13, planning_scope = $14, decoupling_point = $15,
			buffer_profile_id = $16, required_shelf_life_days = $17, priority_based = $18, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`,
			p.TenantID, id, spec.ItemID, spec.SiteID, optional(spec.WarehouseID), spec.CoverageGroupID, spec.MinQty.String(),
			spec.MaxQty.String(), spec.SafetyMarginReceiptDays, spec.LeadTimeDays, optional(spec.VendorID), spec.PlannedOrderType,
			spec.PlanningStrategy, spec.PlanningScope, spec.DecouplingPoint, optional(spec.BufferProfileID),
			spec.RequiredShelfLifeDays, spec.PriorityBased); err != nil {
			return err
		}
		out, err = scanItemCoverage(tx.QueryRow(ctx, itemCoverageSelect+` where c.tenant_id = $1 and c.id = $2`, p.TenantID, id))
		return err
	})
	if err != nil {
		return ItemCoverage{}, itemCoverageWriteError(err, spec)
	}
	return out, nil
}
