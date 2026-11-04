package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
)

// Movement kinds of a returnable packaging balance (FR-28.16, BR-28.13).
const (
	MovementShipped           = "SHIPPED"
	MovementShipmentCancelled = "SHIPMENT_CANCELLED"
	MovementReturned          = "RETURNED"
	MovementAdjusted          = "ADJUSTED"
)

// CodeNotReturnable refuses an adjustment of a container type that does not exist
// or is not returnable: only returnable containers have balances.
const CodeNotReturnable = "FUL-0069"

// ReturnableMovement is one change of a customer's balance of one container type.
// Qty is signed; BalanceAfter is the balance the change left.
type ReturnableMovement struct {
	ID            string          `json:"id"`
	CustomerID    string          `json:"customerId"`
	ContainerType string          `json:"containerType"`
	Kind          string          `json:"kind"`
	Qty           decimal.Decimal `json:"qty"`
	BalanceAfter  decimal.Decimal `json:"balanceAfter"`
	// Reference is the packing slip id of SHIPPED and SHIPMENT_CANCELLED, or the
	// caller's own reference (a delivery note, a count sheet) of an adjustment.
	Reference *string   `json:"reference"`
	Reason    *string   `json:"reason"`
	Actor     *string   `json:"actor"`
	CreatedAt time.Time `json:"createdAt"`
}

// AdjustInput is a manual correction of a balance. Delta is a signed whole number
// of containers, not zero; Reason is required.
type AdjustInput struct {
	CustomerID    string
	ContainerType string
	Delta         decimal.Decimal
	Reason        string
	Reference     string
}

// MovementFilter selects movements, newest first.
type MovementFilter struct {
	CustomerID    string
	ContainerType string
	Kind          string
	Limit         int
}

func actorOf(ctx context.Context) string {
	p, _ := tenant.FromContext(ctx)
	return p.UserID
}

// recordMovement appends one movement in the transaction that changed the balance,
// after that transaction locked the balance row. created_at is the clock at the insert
// (the column default), not the start of the transaction: a transaction that waited for
// the lock is stamped after the one it waited for, so the history in created_at order
// is the order the balance changed in and each balance_after is the running sum.
func recordMovement(ctx context.Context, tx pgx.Tx, tenantID, customerID, containerType, kind string, qty, after decimal.Decimal, reference, reason, actor string) (ReturnableMovement, error) {
	m := ReturnableMovement{ID: ids.New(), CustomerID: customerID, ContainerType: containerType, Kind: kind, Qty: qty, BalanceAfter: after,
		Reference: optional(reference), Reason: optional(reason), Actor: optional(actor)}
	err := tx.QueryRow(ctx, `insert into returnable_packaging_movement (tenant_id, id, customer_id, container_type, kind, qty, balance_after, reference, reason, actor)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) returning created_at`,
		tenantID, m.ID, customerID, containerType, kind, qty, after, m.Reference, m.Reason, m.Actor).Scan(&m.CreatedAt)
	return m, err
}

func movementData(customerID string, m ReturnableMovement) fulevents.ReturnableMovementData {
	return fulevents.ReturnableMovementData{MovementID: m.ID, CustomerID: customerID, ContainerType: m.ContainerType, Kind: m.Kind,
		Qty: m.Qty.String(), BalanceAfter: m.BalanceAfter.String(), Reference: deref(m.Reference), Reason: deref(m.Reason)}
}

// AdjustReturnable corrects a customer's balance of a returnable container type by
// a signed number of containers (an opening balance, a count at the customer, a
// pallet lost in transit). A correction that would take the balance below zero is
// refused with FUL-0015 (BR-28.13); a container type that is unknown or not
// returnable with FUL-0069. The movement and returnablepackaging.adjusted are
// written in the same transaction.
func (s *Postgres) AdjustReturnable(ctx context.Context, in AdjustInput, idem Idem) (ReturnableMovement, error) {
	p, _ := tenant.FromContext(ctx)
	var out ReturnableMovement
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (ReturnableMovement, error) {
			ct, ok, err := loadContainerType(ctx, tx, in.ContainerType)
			if err != nil {
				return ReturnableMovement{}, err
			}
			if !ok || !ct.Returnable {
				return ReturnableMovement{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeNotReturnable,
					fmt.Sprintf("container type %s is unknown or not returnable", in.ContainerType))
			}
			// The row lock of an existing balance serialises this correction with a post,
			// cancel, return or another correction of the same balance, so the check below
			// reads the balance the correction applies to.
			var bal string
			cur := decimal.Zero
			err = tx.QueryRow(ctx, `select balance::text from returnable_packaging_balance where customer_id = $1 and container_type = $2 for update`,
				in.CustomerID, in.ContainerType).Scan(&bal)
			found := err == nil
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return ReturnableMovement{}, err
			}
			if found {
				cur = dec(bal)
			}
			if _, aerr := domain.ApplyBalance(cur, in.Delta); aerr != nil {
				return ReturnableMovement{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeRTPNegative,
					fmt.Sprintf("returnable packaging balance would become negative for customer %s: %s %s held, adjustment %s",
						in.CustomerID, cur.String(), in.ContainerType, in.Delta.String()))
			}
			var after string
			if found {
				err = tx.QueryRow(ctx, `update returnable_packaging_balance set balance = balance + $3
					where customer_id = $1 and container_type = $2 returning balance::text`,
					in.CustomerID, in.ContainerType, in.Delta).Scan(&after)
			} else {
				// Only a positive correction passes the check without a row. Add rather
				// than set: a concurrent first post or correction may insert the row
				// before this insert, which then waits for it and adds to its balance
				// instead of overwriting it.
				err = tx.QueryRow(ctx, `insert into returnable_packaging_balance (tenant_id, customer_id, container_type, balance)
					values ($1, $2, $3, $4)
					on conflict (tenant_id, customer_id, container_type) do update set balance = returnable_packaging_balance.balance + excluded.balance
					returning balance::text`,
					p.TenantID, in.CustomerID, in.ContainerType, in.Delta).Scan(&after)
			}
			if err != nil {
				return ReturnableMovement{}, err
			}
			next := dec(after)
			m, err := recordMovement(ctx, tx, p.TenantID, in.CustomerID, in.ContainerType, MovementAdjusted, in.Delta, next, in.Reference, in.Reason, p.UserID)
			if err != nil {
				return m, err
			}
			return m, enqueueFor(ctx, tx, "returnable_packaging", in.CustomerID, fulevents.ReturnableAdjusted, p.TenantID, movementData(in.CustomerID, m))
		})
		out = res
		return err
	})
	return out, err
}

// ListReturnableMovements returns the movements of the balances, newest first,
// optionally of one customer, container type or kind.
func (s *Postgres) ListReturnableMovements(ctx context.Context, f MovementFilter) ([]ReturnableMovement, error) {
	out := []ReturnableMovement{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id, customer_id, container_type, kind, qty::text, balance_after::text, reference, reason, actor, created_at
			from returnable_packaging_movement
			where ($1::text = '' or customer_id = nullif($1::text, '')::uuid)
			  and ($2::text = '' or container_type = $2)
			  and ($3::text = '' or kind = $3)
			order by created_at desc, id desc limit $4`, f.CustomerID, f.ContainerType, f.Kind, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m ReturnableMovement
			var qty, after string
			if err := rows.Scan(&m.ID, &m.CustomerID, &m.ContainerType, &m.Kind, &qty, &after, &m.Reference, &m.Reason, &m.Actor, &m.CreatedAt); err != nil {
				return err
			}
			m.Qty, m.BalanceAfter = dec(qty), dec(after)
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}
