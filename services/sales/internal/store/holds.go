package store

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

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
	err := s.orderTx(ctx, p.TenantID, &out, func(tx pgx.Tx) error {
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

// RejectHold rejects the ACTIVE CREDIT hold of an unconfirmed order with a
// comment and cancels the order, all in one transaction under the order's row
// lock (slice 26, PRD §5.27.6): the hold becomes REJECTED with who, when and
// why, the order's other ACTIVE holds are CLEARED, lines, allocation and
// agreement draw are returned as for :cancel, the order gets a version whose
// reason is the comment, and orderhold.rejected.v1 and order.cancelled.v1 are
// queued. As for a release, the user who applied the hold may not decide it.
// A stale version is 412 SLS-0006, a hold that is not ACTIVE 422 SLS-0022, a
// hold of another type or an order that is no longer awaiting confirmation 422
// SLS-0062.
func (s *Postgres) RejectHold(ctx context.Context, orderID, holdID string, version int, comment, actor string, now time.Time) (Order, error) {
	p, _ := tenant.FromContext(ctx)
	var out Order
	err := s.orderTx(ctx, p.TenantID, &out, func(tx pgx.Tx) error {
		o, err := loadOrder(ctx, tx, p.TenantID, orderID, true)
		if err != nil {
			return err
		}
		if o.Version != version {
			return versionConflict()
		}
		var hold *Hold
		for i := range o.Holds {
			if o.Holds[i].ID == holdID {
				hold = &o.Holds[i]
			}
		}
		if hold == nil {
			return problem(http.StatusNotFound, CodeHoldNotFound, "hold not found")
		}
		if hold.Status != domain.HoldActive {
			return problem(http.StatusUnprocessableEntity, CodeHoldNotActive, "hold is not active")
		}
		if !domain.CanRejectHold(hold.HoldType, hold.Status, o.Status) {
			return problem(http.StatusUnprocessableEntity, CodeHoldNotRejectable,
				fmt.Sprintf("only a CREDIT hold of an order awaiting confirmation can be rejected; this is a %s hold on a %s order", hold.HoldType, o.Status))
		}
		if hold.AppliedBy == actor {
			return problem(http.StatusForbidden, "AUTH-0003", "the user who applied a hold may not reject it")
		}
		if _, err := tx.Exec(ctx, `update order_hold set status = 'REJECTED', rejected_by = $3, rejected_at = $4, rejection_comment = $5
			where tenant_id = $1 and id = $2`, p.TenantID, holdID, actor, now, comment); err != nil {
			return err
		}
		// The cancellation is :cancel's own (cancelOrderTx): it clears the
		// other ACTIVE holds, returns quota, schedule and agreement draw, and
		// writes the version and order.cancelled with the comment as reason.
		cancelled, err := cancelOrderTx(ctx, tx, p.TenantID, o, rejectionReason(comment), actor, now)
		if err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "order", o.ID, salesevents.OrderHoldRejected, p.TenantID, salesevents.OrderHoldRejectedData{
			OrderID: o.ID, HoldID: holdID, HoldType: hold.HoldType, ReasonCode: hold.ReasonCode, Comment: comment, Actor: actor, Version: cancelled.Version}); err != nil {
			return err
		}
		out = cancelled
		return nil
	})
	return out, err
}

// rejectionReason is the cancellation reason of a rejected order: the comment
// behind a fixed prefix, cut to the 500 characters a cancel reason may have
// (order.cancelled.v1 reason maxLength). The whole comment stays on the hold
// and in orderhold.rejected.v1.
func rejectionReason(comment string) string {
	r := []rune("Credit hold rejected: " + comment)
	if len(r) > changeReasonMaxSize {
		r = r[:changeReasonMaxSize]
	}
	return string(r)
}
