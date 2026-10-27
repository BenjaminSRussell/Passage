package store

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// HoldRequest is a hold applied by hand through the holds API (FR-27.8).
// CREDIT holds are placed only by the credit check.
type HoldRequest struct {
	HoldType   string
	ReasonCode string
}

// ApplyHold places a MANUAL, FRAUD or PRICE_REVIEW hold on an order. The order
// must be at the expected version (If-Match), in a status that can still change
// (DRAFT, OPEN or CONFIRMED) and must not already carry an ACTIVE hold of the
// same type. The hold, the version bump and orderhold.applied commit in one
// transaction. A FRAUD hold then blocks confirmation (BR-27.4).
func (s *Postgres) ApplyHold(ctx context.Context, orderID string, version int, in HoldRequest, actor string) (Order, error) {
	p, _ := tenant.FromContext(ctx)
	if !domain.CanApplyHold(in.HoldType) {
		return Order{}, problem(http.StatusUnprocessableEntity, CodeHoldNotApplicable, fmt.Sprintf("a %s hold cannot be applied by hand", in.HoldType))
	}
	var out Order
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		o, err := loadOrder(ctx, tx, p.TenantID, orderID, true)
		if err != nil {
			return err
		}
		if o.Version != version {
			return versionConflict()
		}
		if !domain.CanChange(o.Status) {
			return problem(http.StatusUnprocessableEntity, CodeHoldNotApplicable, fmt.Sprintf("order is %s and cannot take a hold", o.Status))
		}
		if hasActiveHold(o, in.HoldType) {
			return problem(http.StatusUnprocessableEntity, CodeHoldNotApplicable, "the order already has an active "+in.HoldType+" hold")
		}
		holdID := ids.New()
		if _, err := tx.Exec(ctx, `insert into order_hold (tenant_id, id, legal_entity_id, order_id, hold_type, reason_code, order_amount, applied_by)
			values ($1, $2, $3, $4, $5, $6, $7::numeric, $8)`,
			p.TenantID, holdID, o.LegalEntityID, o.ID, in.HoldType, in.ReasonCode, o.NetTotal.String(), actor); err != nil {
			return err
		}
		if err := setOrderState(ctx, tx, p.TenantID, o.ID, o.Status); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "order", o.ID, salesevents.OrderHoldApplied, p.TenantID, salesevents.OrderHoldData{
			OrderID: o.ID, HoldID: holdID, HoldType: in.HoldType, ReasonCode: in.ReasonCode, Actor: actor, Version: o.Version + 1}); err != nil {
			return err
		}
		out, err = loadOrder(ctx, tx, p.TenantID, o.ID, false)
		return err
	})
	return out, err
}
