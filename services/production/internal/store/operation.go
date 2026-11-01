package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

// CodeOperationNotFound refuses an operation command on an operation number the order's route does not have.
const CodeOperationNotFound = "PRODUCTION_OPERATION_NOT_FOUND"

// OperationCommand is one `:finish` or `:reopen` on an operation of an order
// (FR-21.6). IfMatch is the order version; RequestHash identifies the command
// for Idempotency-Key replays.
type OperationCommand struct {
	OrderID        string
	OperNumber     int
	Action         domain.OperAction
	IfMatch        int
	Actor          string
	TraceID        string
	IdempotencyKey string
	RequestHash    string
}

// OperationOutcome says what an operation command did: Replayed answers a
// stored Idempotency-Key, Changed is false when the action was already in effect.
type OperationOutcome struct {
	Replayed bool
	Changed  bool
}

// ApplyOperationCommand finishes or reopens one operation (FR-21.6). It locks the
// order (BR-21.4) and checks If-Match against its version, applies
// domain.OperationTransition, and when the status changes writes it, bumps the
// order version, logs the command with its Idempotency-Key and enqueues
// operation.statuschanged, all in one transaction. A command already in effect
// returns the order unchanged; a replay of the key returns the current order.
func (s *Postgres) ApplyOperationCommand(ctx context.Context, c OperationCommand) (Order, OperationOutcome, error) {
	p, _ := tenant.FromContext(ctx)
	var out Order
	var res OperationOutcome
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var loggedOrder, loggedHash string
		err := tx.QueryRow(ctx, `select order_id::text, request_hash from production_operation_log where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, c.IdempotencyKey).Scan(&loggedOrder, &loggedHash)
		if err == nil {
			if loggedOrder != c.OrderID || loggedHash != c.RequestHash {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used for another operation or command")
			}
			res.Replayed = true
			out, err = loadOrder(ctx, tx, p.TenantID, c.OrderID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		h, err := lockOrder(ctx, tx, p.TenantID, c.OrderID, c.IfMatch)
		if err != nil {
			return err
		}
		ops, err := loadOperations(ctx, tx, p.TenantID, c.OrderID)
		if err != nil {
			return err
		}
		op, ok := findOperation(ops, c.OperNumber)
		if !ok {
			return httpx.NewProblem(http.StatusNotFound, CodeOperationNotFound, fmt.Sprintf("operation %d is not on the order's route", c.OperNumber))
		}
		next, changed, err := domain.OperationTransition(h.status, op, c.Action)
		if err != nil {
			return domainProblem(err)
		}
		from := op.Status
		if from == "" {
			from = domain.OperOpen
		}
		if changed {
			if _, err := tx.Exec(ctx, `update production_operation set status = $4 where tenant_id = $1 and order_id = $2 and oper_number = $3`,
				p.TenantID, c.OrderID, c.OperNumber, string(next)); err != nil {
				return err
			}
			// The operation's setup and process jobs follow it (FR-21.6): :finish closes them to job
			// cards, :reopen opens them again, started when a job card reported on them.
			if _, err := tx.Exec(ctx, `update production_job set status = case when $4 = 'finished' then 'finished'
					when hours_reported = 0 and qty_good = 0 and qty_error = 0 then 'planned' else 'started' end
				where tenant_id = $1 and order_id = $2 and oper_number = $3 and job_type in ('setup', 'process')
				  and (($4 = 'finished') <> (status = 'finished'))`,
				p.TenantID, c.OrderID, c.OperNumber, string(next)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update production_order set version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`,
				p.TenantID, c.OrderID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into production_operation_log (tenant_id, id, legal_entity_id, order_id, oper_number, action,
					from_status, to_status, actor, trace_id, idempotency_key, request_hash)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`, p.TenantID, ids.New(), h.legalEntityID, c.OrderID, c.OperNumber,
				string(c.Action), string(from), string(next), c.Actor, c.TraceID, c.IdempotencyKey, c.RequestHash); err != nil {
				return err
			}
			res.Changed = true
		}
		out, err = loadOrder(ctx, tx, p.TenantID, c.OrderID)
		if err != nil || !changed {
			return err
		}
		return enqueue(ctx, tx, c.OrderID, prodevents.OperationStatusChanged, p.TenantID, c.Actor, prodevents.OperationStatusChangedData{
			OrderID: c.OrderID, OperNumber: c.OperNumber, Action: string(c.Action), From: string(from), To: string(next),
			OrderVersion: out.Version, Actor: c.Actor, OccurredAt: out.UpdatedAt.UTC().Format(time.RFC3339Nano),
		})
	})
	return out, res, err
}
