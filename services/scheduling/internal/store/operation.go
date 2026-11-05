package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	schedevents "github.com/BenjaminSRussell/passage/services/scheduling/internal/events"
)

// Problem codes of operation maintenance.
const (
	CodeOperationNotFound  = "SCHED-0136"
	CodeOperationVersion   = "SCHED-0137"
	CodeOperationFirmed    = "SCHED-0138"
	CodeCandidateSecondary = "SCHED-0139"
	// CodeOverlapQuantity: the overlap quantity needs a quantity and cannot exceed it (BR-23.5).
	CodeOverlapQuantity = "SCHED-0142"
)

// OperationPatch changes an operation; nil and empty fields stay as they are.
// The order, the operation number, the secondary resources, the setup group,
// the network and the attributes are fixed when the operation is created.
type OperationPatch struct {
	Minutes      *int
	DueAt        *time.Time
	ReadyAt      *time.Time
	Priority     *int
	SetupMinutes *int
	// QueueBeforeMinutes and QueueAfterMinutes change the waits around the
	// operation (FR-23.3).
	QueueBeforeMinutes *int
	QueueAfterMinutes  *int
	// Quantity and OverlapQuantity change the quantities of the overlap rule
	// (BR-23.5). An OverlapQuantity of zero clears the overlap.
	Quantity        *decimal.Decimal
	OverlapQuantity *decimal.Decimal
	// ResourceIDs replaces the candidate resources, in priority order.
	ResourceIDs []string
}

// reschedules reports whether the change makes the operation's placement stale:
// everything but the priority, which only orders the next run.
func (p OperationPatch) reschedules() bool {
	return p.Minutes != nil || p.DueAt != nil || p.ReadyAt != nil || p.SetupMinutes != nil || p.QueueBeforeMinutes != nil || p.QueueAfterMinutes != nil || p.OverlapQuantity != nil || p.ResourceIDs != nil
}

// OperationUpdate is the changed operation. JobsRemoved is true when its
// non-firmed jobs left the schedule and the operation is open again.
type OperationUpdate struct {
	Operation
	JobsRemoved bool `json:"jobsRemoved"`
}

// OperationRemoval is the answer to deleting an operation.
type OperationRemoval struct {
	OperationID string `json:"operationId"`
	OrderID     string `json:"orderId"`
	OperNumber  int    `json:"operNumber"`
	JobsRemoved bool   `json:"jobsRemoved"`
}

// OrderUnschedule is the answer to taking an order off the schedule.
type OrderUnschedule struct {
	OrderID string `json:"orderId"`
	// Operations are the operation numbers that are open again.
	Operations []int `json:"operations"`
	// JobsRemoved counts the operations that lost jobs.
	JobsRemoved int `json:"jobsRemoved"`
	// KeptFirmed are the operation numbers a firmed job holds; they stay put.
	KeptFirmed []int `json:"keptFirmed"`
}

// OperationStore is operation maintenance, part of the Store.
type OperationStore interface {
	GetOperation(ctx context.Context, id string) (Operation, error)
	UpdateOperation(ctx context.Context, id string, version int, patch OperationPatch, idem Idem) (OperationUpdate, bool, error)
	DeleteOperation(ctx context.Context, id string, version int, idem Idem) (OperationRemoval, bool, error)
	UnscheduleOrder(ctx context.Context, orderID string, idem Idem) (OrderUnschedule, bool, error)
}

func operationNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeOperationNotFound, "operation not found")
}

// GetOperation reads one operation.
func (s *Postgres) GetOperation(ctx context.Context, id string) (Operation, error) {
	var out Operation
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		o, err := scanOperation(tx.QueryRow(ctx, `select `+opCols+` from scheduling_operation where id = $1::uuid`, id), nil)
		if errors.Is(err, pgx.ErrNoRows) {
			return operationNotFound()
		}
		out = o
		return err
	})
	return out, err
}

// lockOperation reads and locks an operation, so a retry that raced the first
// request waits for it. found is false when the operation does not exist.
func lockOperation(ctx context.Context, tx pgx.Tx, id string) (Operation, bool, error) {
	o, err := scanOperation(tx.QueryRow(ctx, `select `+opCols+` from scheduling_operation where id = $1::uuid for update`, id), nil)
	if errors.Is(err, pgx.ErrNoRows) {
		return Operation{}, false, nil
	}
	return o, err == nil, err
}

// firmedOperations returns the operation ids among opIDs that a firmed job holds.
func firmedOperations(ctx context.Context, tx pgx.Tx, opIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	rows, err := tx.Query(ctx, `select distinct job_id::text from scheduled_job where job_id = any($1::uuid[]) and firmed`, opIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func firmedProblem(what string) error {
	return httpx.NewProblem(http.StatusConflict, CodeOperationFirmed, what+" is held by a firmed job; unfirm it first")
}

// UpdateOperation changes an operation under optimistic versioning (FR-23.4).
// A firmed operation is refused. Changing the duration, dates, setup minutes or
// candidates takes the operation's non-firmed jobs off the schedule and opens
// it again (JobsRemoved); the next run places it. A priority change only
// orders the next run and leaves the jobs. A replay of the same key returns
// the stored answer, even after the version has moved on.
func (s *Postgres) UpdateOperation(ctx context.Context, id string, version int, patch OperationPatch, idem Idem) (OperationUpdate, bool, error) {
	var out OperationUpdate
	replayed := false
	err := s.loadTx(&ctx, func(tx pgx.Tx) error {
		cur, found, err := lockOperation(ctx, tx, id)
		if err != nil {
			return err
		}
		// A retry answers as the first request did, also once the operation
		// has been deleted since.
		if found, err := replay(ctx, tx, idem, &out); err != nil || found {
			replayed = found
			return err
		}
		if !found {
			return operationNotFound()
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeOperationVersion,
				fmt.Sprintf("operation was changed by another user; current version is %d", cur.Version))
		}
		if patch.reschedules() {
			firmed, err := firmedOperations(ctx, tx, []string{id})
			if err != nil {
				return err
			}
			if firmed[id] {
				return firmedProblem("the operation")
			}
		}
		if patch.Minutes != nil {
			cur.Minutes = *patch.Minutes
		}
		if patch.DueAt != nil {
			cur.DueAt = patch.DueAt
		}
		if patch.ReadyAt != nil {
			cur.ReadyAt = patch.ReadyAt
		}
		if patch.Priority != nil {
			cur.Priority = *patch.Priority
		}
		if patch.SetupMinutes != nil {
			cur.SetupMinutes = *patch.SetupMinutes
		}
		if patch.QueueBeforeMinutes != nil {
			cur.QueueBeforeMinutes = *patch.QueueBeforeMinutes
		}
		if patch.QueueAfterMinutes != nil {
			cur.QueueAfterMinutes = *patch.QueueAfterMinutes
		}
		if patch.Quantity != nil {
			q := patch.Quantity.StringFixed(4)
			cur.Quantity = &q
		}
		if patch.OverlapQuantity != nil {
			if patch.OverlapQuantity.IsZero() {
				cur.OverlapQuantity = nil
			} else {
				o := patch.OverlapQuantity.StringFixed(4)
				cur.OverlapQuantity = &o
			}
		}
		if cur.OverlapQuantity != nil && (cur.Quantity == nil || dec(*cur.OverlapQuantity).GreaterThan(dec(*cur.Quantity))) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeOverlapQuantity, "the overlap quantity needs a quantity and cannot exceed it")
		}
		if patch.ResourceIDs != nil {
			var known int
			if err := tx.QueryRow(ctx, `select count(*) from scheduling_resource where id = any($1::uuid[])`, patch.ResourceIDs).Scan(&known); err != nil {
				return err
			}
			if known != len(uniqueStrings(patch.ResourceIDs)) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeUnknownResource, "every candidate resource must exist")
			}
			for _, r := range patch.ResourceIDs {
				if slices.Contains(cur.SecondaryResourceIDs, r) {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeCandidateSecondary, "a candidate resource cannot also be a secondary resource of the operation")
				}
			}
			cur.ResourceIDs = patch.ResourceIDs
		}
		removed := false
		if patch.reschedules() || (patch.Quantity != nil && cur.OverlapQuantity != nil) {
			if removed, err = dropJobs(ctx, tx, id); err != nil {
				return err
			}
		}
		upd, err := scanOperation(tx.QueryRow(ctx, `update scheduling_operation set minutes = $2, due_at = $3, ready_at = $4, priority = $5, setup_minutes = $6,
				queue_before_minutes = $8, queue_after_minutes = $9, quantity = $10::text::numeric, overlap_quantity = $11::text::numeric, candidate_resource_ids = $7::uuid[], version = version + 1, updated_at = now() where id = $1::uuid returning `+opCols,
			id, cur.Minutes, cur.DueAt, cur.ReadyAt, cur.Priority, cur.SetupMinutes, cur.ResourceIDs, cur.QueueBeforeMinutes, cur.QueueAfterMinutes, cur.Quantity, cur.OverlapQuantity), nil)
		if err != nil {
			return err
		}
		out = OperationUpdate{Operation: upd, JobsRemoved: removed}
		data := schedevents.OperationUpdatedData{OperationID: upd.ID, OrderID: upd.OrderID, OperNumber: upd.OperNumber, Minutes: upd.Minutes,
			Priority: upd.Priority, ResourceIDs: upd.ResourceIDs, Version: upd.Version, JobsRemoved: removed,
			QueueBeforeMinutes: upd.QueueBeforeMinutes, QueueAfterMinutes: upd.QueueAfterMinutes,
			Quantity: upd.Quantity, OverlapQuantity: upd.OverlapQuantity}
		if upd.DueAt != nil {
			d := rfc(*upd.DueAt)
			data.DueAt = &d
		}
		if upd.ReadyAt != nil {
			d := rfc(*upd.ReadyAt)
			data.ReadyAt = &d
		}
		if err := enqueue(ctx, tx, "operation", upd.ID, schedevents.OperationUpdated, data); err != nil {
			return err
		}
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	if err != nil {
		return OperationUpdate{}, false, mapWriteErr(err, CodeIdempotencyReuse, "a concurrent request used the same Idempotency-Key; retry")
	}
	return out, replayed, nil
}

// DeleteOperation removes an operation and its non-firmed jobs. An operation a
// firmed job holds is refused. A replay of the same key answers as the first
// request did, although the operation is gone.
func (s *Postgres) DeleteOperation(ctx context.Context, id string, version int, idem Idem) (OperationRemoval, bool, error) {
	var out OperationRemoval
	replayed := false
	err := s.loadTx(&ctx, func(tx pgx.Tx) error {
		cur, found, err := lockOperation(ctx, tx, id)
		if err != nil {
			return err
		}
		// A retry that raced the first request finds the operation gone once the
		// lock is released; it replays instead of failing.
		if found, err := replay(ctx, tx, idem, &out); err != nil || found {
			replayed = found
			return err
		}
		if !found {
			return operationNotFound()
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeOperationVersion,
				fmt.Sprintf("operation was changed by another user; current version is %d", cur.Version))
		}
		firmed, err := firmedOperations(ctx, tx, []string{id})
		if err != nil {
			return err
		}
		if firmed[id] {
			return firmedProblem("the operation")
		}
		removed, err := dropJobs(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from scheduling_operation where id = $1::uuid`, id); err != nil {
			return err
		}
		out = OperationRemoval{OperationID: id, OrderID: cur.OrderID, OperNumber: cur.OperNumber, JobsRemoved: removed}
		if err := enqueue(ctx, tx, "operation", id, schedevents.OperationDeleted, schedevents.OperationDeletedData{
			OperationID: id, OrderID: cur.OrderID, OperNumber: cur.OperNumber, JobsRemoved: removed}); err != nil {
			return err
		}
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	if err != nil {
		return OperationRemoval{}, false, mapWriteErr(err, CodeIdempotencyReuse, "a concurrent request used the same Idempotency-Key; retry")
	}
	return out, replayed, nil
}

// UnscheduleOrder takes an order off the schedule (FR-23.11): every operation
// loses its non-firmed jobs and is open again, so the next run can place the
// order afresh. Operations a firmed job holds stay as they are and are
// listed. An order with no operations is not found.
func (s *Postgres) UnscheduleOrder(ctx context.Context, orderID string, idem Idem) (OrderUnschedule, bool, error) {
	var out OrderUnschedule
	replayed := false
	err := s.loadTx(&ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, oper_number from scheduling_operation where order_id = $1::uuid order by oper_number for update`, orderID)
		if err != nil {
			return err
		}
		type held struct {
			id     string
			number int
		}
		var ops []held
		for rows.Next() {
			var h held
			if err := rows.Scan(&h.id, &h.number); err != nil {
				rows.Close()
				return err
			}
			ops = append(ops, h)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if found, err := replay(ctx, tx, idem, &out); err != nil || found {
			replayed = found
			return err
		}
		if len(ops) == 0 {
			return httpx.NewProblem(http.StatusNotFound, CodeOrderHasNoOperations, "no operations are known for that order")
		}
		opIDs := make([]string, 0, len(ops))
		for _, o := range ops {
			opIDs = append(opIDs, o.id)
		}
		firmed, err := firmedOperations(ctx, tx, opIDs)
		if err != nil {
			return err
		}
		out = OrderUnschedule{OrderID: orderID, Operations: []int{}, KeptFirmed: []int{}}
		for _, o := range ops {
			if firmed[o.id] {
				out.KeptFirmed = append(out.KeptFirmed, o.number)
				continue
			}
			removed, err := dropJobs(ctx, tx, o.id)
			if err != nil {
				return err
			}
			if removed {
				out.JobsRemoved++
			}
			out.Operations = append(out.Operations, o.number)
		}
		if err := enqueue(ctx, tx, "order", orderID, schedevents.OrderUnscheduled, schedevents.OrderUnscheduledData{
			OrderID: orderID, Operations: out.Operations, KeptFirmed: out.KeptFirmed}); err != nil {
			return err
		}
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	if err != nil {
		return OrderUnschedule{}, false, mapWriteErr(err, CodeIdempotencyReuse, "a concurrent request used the same Idempotency-Key; retry")
	}
	return out, replayed, nil
}
