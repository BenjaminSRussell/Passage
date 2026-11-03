package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Stable error codes of the planned-order intake (FR-22.10, BR-22.9).
const (
	CodeIntakeNotFound = "PRODUCTION_INTAKE_NOT_FOUND"
	CodeIntakeNotOpen  = "PRODUCTION_INTAKE_NOT_OPEN"
)

// Intake statuses.
const (
	IntakeOpen      = "open"
	IntakeConverted = "converted"
	IntakeDismissed = "dismissed"
)

// PlannedIntake is a firmed production planned order waiting to become a production order. The
// order a conversion creates takes TargetID as its id and LegalEntityID, ProductID, SiteID, WarehouseID,
// Quantity and DeliveryDate from here. LegalEntityID is nil while planning's event does not name it.
type PlannedIntake struct {
	ID                 string          `json:"id"`
	LegalEntityID      *string         `json:"legalEntityId"`
	PlannedOrderID     string          `json:"plannedOrderId"`
	PlanVersionID      string          `json:"planVersionId"`
	BatchID            string          `json:"batchId"`
	TargetID           string          `json:"targetId"`
	ProductID          string          `json:"productId"`
	SiteID             string          `json:"siteId"`
	WarehouseID        *string         `json:"warehouseId"`
	Quantity           decimal.Decimal `json:"quantity"`
	OrderDate          string          `json:"orderDate"`
	DeliveryDate       string          `json:"deliveryDate"`
	QuotaArrangementID *string         `json:"quotaArrangementId"`
	QuotaSourceID      *string         `json:"quotaSourceId"`
	Status             string          `json:"status"`
	OrderID            *string         `json:"orderId"`
	DismissReason      *string         `json:"dismissReason"`
	Version            int             `json:"version"`
	ReceivedAt         time.Time       `json:"receivedAt"`
	UpdatedAt          time.Time       `json:"updatedAt"`
}

// PlannedIntakeIn is one plannedorder.firmed.v1 the consumer stores.
type PlannedIntakeIn struct {
	PlannedOrderID, PlanVersionID, BatchID, TargetID, ProductID, SiteID string
	LegalEntityID, WarehouseID, QuotaArrangementID, QuotaSourceID       *string
	Quantity                                                            decimal.Decimal
	OrderDate, DeliveryDate, EventID                                    string
}

// IntakeFilter narrows the intake list. Cursor is the id of the last row of the previous page.
type IntakeFilter struct {
	Status, SiteID, Cursor string
	Limit                  int
}

// IntakePage is a page of the intake, newest first.
type IntakePage struct {
	Items      []PlannedIntake `json:"items"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

// IntakeDismiss sets an open intake row aside with a reason (If-Match is its version).
type IntakeDismiss struct {
	ID      string
	IfMatch int
	Reason  string
	Actor   string
}

func intakeNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeIntakeNotFound, "planned order intake not found")
}

const intakeColumns = `id::text, legal_entity_id::text, planned_order_id::text, plan_version_id::text, batch_id::text, target_id::text, product_id::text, site_id::text,
	warehouse_id::text, quantity::text, order_date::text, delivery_date::text, quota_arrangement_id::text, quota_source_id::text,
	status, order_id::text, dismiss_reason, version, received_at, updated_at`

func scanIntake(row pgx.Row) (PlannedIntake, error) {
	var i PlannedIntake
	var qty string
	err := row.Scan(&i.ID, &i.LegalEntityID, &i.PlannedOrderID, &i.PlanVersionID, &i.BatchID, &i.TargetID, &i.ProductID, &i.SiteID, &i.WarehouseID, &qty,
		&i.OrderDate, &i.DeliveryDate, &i.QuotaArrangementID, &i.QuotaSourceID, &i.Status, &i.OrderID, &i.DismissReason, &i.Version, &i.ReceivedAt, &i.UpdatedAt)
	i.Quantity = dec(qty)
	return i, err
}

// RecordPlannedOrder stores a firmed production planned order in the consumer's inbox transaction.
// A planned order already stored (a redelivery under another event id) changes nothing: planning
// refuses to firm a planned order twice (BR-22.9, PLAN-0002) and a firmed one is never changed, so the
// first row stands. It reports whether a row was added.
//
// The consumer's service principal carries no legal entity, so its transaction admits no row of the
// legal-entity policy. The insert alone runs with the scope of the row it writes: the planned order's
// legal entity, or every entity for a row without one (visible to all-entity callers only).
func (s *Postgres) RecordPlannedOrder(ctx context.Context, tx pgx.Tx, in PlannedIntakeIn) (bool, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return false, errors.New("tenant context is required")
	}
	scope := "*"
	if in.LegalEntityID != nil {
		scope = *in.LegalEntityID
	}
	var saved string
	if err := tx.QueryRow(ctx, `select coalesce(current_setting('passage.legal_entity_scopes', true), ''), set_config('passage.legal_entity_scopes', $1, true)`,
		scope).Scan(&saved, new(string)); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `insert into production_planned_intake (tenant_id, id, legal_entity_id, planned_order_id, plan_version_id, batch_id, target_id, product_id, site_id,
			warehouse_id, quantity, order_date, delivery_date, quota_arrangement_id, quota_source_id, event_id)
		values ($1, gen_random_uuid(), $15, $2, $3, $4, $5, $6, $7, $8, $9, $10::date, $11::date, $12, $13, $14)
		on conflict (tenant_id, planned_order_id) do nothing`,
		p.TenantID, in.PlannedOrderID, in.PlanVersionID, in.BatchID, in.TargetID, in.ProductID, in.SiteID,
		in.WarehouseID, in.Quantity.String(), in.OrderDate, in.DeliveryDate, in.QuotaArrangementID, in.QuotaSourceID, in.EventID, in.LegalEntityID)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `select set_config('passage.legal_entity_scopes', $1, true)`, saved); err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// GetPlannedIntake reads one intake row.
func (s *Postgres) GetPlannedIntake(ctx context.Context, id string) (PlannedIntake, error) {
	p, _ := tenant.FromContext(ctx)
	var out PlannedIntake
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanIntake(tx.QueryRow(ctx, `select `+intakeColumns+` from production_planned_intake where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PlannedIntake{}, intakeNotFound()
	}
	return out, err
}

// ListPlannedIntake lists the intake, newest id first, filtered by status and site.
func (s *Postgres) ListPlannedIntake(ctx context.Context, f IntakeFilter) (IntakePage, error) {
	p, _ := tenant.FromContext(ctx)
	if f.Limit <= 0 {
		f.Limit = 50
	}
	page := IntakePage{Items: []PlannedIntake{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+intakeColumns+` from production_planned_intake
			where tenant_id = $1 and ($2 = '' or status = $2) and ($3 = '' or site_id = nullif($3, '')::uuid) and ($4 = '' or id < nullif($4, '')::uuid)
			order by id desc limit $5`, p.TenantID, f.Status, f.SiteID, f.Cursor, f.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			i, err := scanIntake(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, i)
		}
		return rows.Err()
	})
	if err != nil {
		return IntakePage{}, err
	}
	if len(page.Items) > f.Limit {
		page.Items = page.Items[:f.Limit]
		page.NextCursor = page.Items[f.Limit-1].ID
	}
	return page, nil
}

// DismissPlannedIntake sets an open row aside. A row already converted or dismissed is a 409; a stale
// version is a 409 conflict_version.
func (s *Postgres) DismissPlannedIntake(ctx context.Context, in IntakeDismiss) (PlannedIntake, error) {
	p, _ := tenant.FromContext(ctx)
	var out PlannedIntake
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockIntake(ctx, tx, p.TenantID, in.ID)
		if err != nil {
			return err
		}
		if cur.Version != in.IfMatch {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the intake row changed; reload it")
		}
		if cur.Status != IntakeOpen {
			return intakeNotOpen(cur.Status)
		}
		out, err = scanIntake(tx.QueryRow(ctx, `update production_planned_intake set status = 'dismissed', dismiss_reason = $3, version = version + 1,
			updated_at = now(), updated_by = $4 where tenant_id = $1 and id = $2 returning `+intakeColumns, p.TenantID, in.ID, in.Reason, in.Actor))
		return err
	})
	return out, err
}

func intakeNotOpen(status string) error {
	return httpx.NewProblem(http.StatusConflict, CodeIntakeNotOpen, "the planned order is already "+status)
}

func lockIntake(ctx context.Context, tx pgx.Tx, tenantID, id string) (PlannedIntake, error) {
	i, err := scanIntake(tx.QueryRow(ctx, `select `+intakeColumns+` from production_planned_intake where tenant_id = $1 and id = $2 for update`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return PlannedIntake{}, intakeNotFound()
	}
	return i, err
}

// lockOpenIntake runs inside CreateOrder's transaction before the order is inserted: it locks the
// row and refuses one that is not open, so a second conversion fails on the row, not on the order id.
func lockOpenIntake(ctx context.Context, tx pgx.Tx, tenantID, intakeID string) error {
	cur, err := lockIntake(ctx, tx, tenantID, intakeID)
	if err != nil {
		return err
	}
	if cur.Status != IntakeOpen {
		return intakeNotOpen(cur.Status)
	}
	return nil
}

// convertIntake marks the locked row converted to the new order, in the order's transaction.
func convertIntake(ctx context.Context, tx pgx.Tx, tenantID, actor, intakeID, orderID string) error {
	_, err := tx.Exec(ctx, `update production_planned_intake set status = 'converted', order_id = $3, version = version + 1, updated_at = now(), updated_by = $4
		where tenant_id = $1 and id = $2`, tenantID, intakeID, orderID, actor)
	return err
}
