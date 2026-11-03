package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// CodeOperationFinished is SF-0157: an online job-start names a job whose operation production
// has finished (w21 operation.statuschanged.v1 to finished), at or after the finish.
const CodeOperationFinished = "SF-0157"

// OperationStatusIn is what the shop floor keeps of production's operation.statuschanged.v1.
// OrderID and OperNumber are the references the schedule projects on a job (order_ref is the
// production order id, stored in lower case, operation_ref the operation number). Finished is true
// when the operation went to finished and false when it was reopened. At is the event's occurredAt
// and OrderVersion the order's version after the change, which orders the events of one order.
type OperationStatusIn struct {
	OrderID      string
	OperNumber   int
	Status       string
	Finished     bool
	At           time.Time
	OrderVersion int
	Actor        string
	EventID      string
}

// ApplyOperationStatus stores production's latest status of one operation inside tx (the
// consumer's inbox transaction). Events are ordered by the order version production writes under
// the order lock (every finish and reopen bumps it), then by occurredAt: occurredAt alone is the
// production transaction's start time, so two commands racing on one order can carry times in the
// other order than they committed. An event older than the stored one changes nothing, so a
// redelivery or reordering never reopens a finished operation or finishes a reopened one. It
// reports whether the stored status changed.
func (s *Postgres) ApplyOperationStatus(ctx context.Context, tx pgx.Tx, in OperationStatusIn) (bool, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return false, errors.New("tenant context is required")
	}
	tag, err := tx.Exec(ctx, `insert into operation_production_status (tenant_id, order_ref, operation_ref, finished, production_status, changed_at, order_version, actor, event_id)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		on conflict (tenant_id, order_ref, operation_ref) do update
		set finished = excluded.finished, production_status = excluded.production_status, changed_at = excluded.changed_at,
		    order_version = excluded.order_version, actor = excluded.actor, event_id = excluded.event_id, updated_at = now()
		where (operation_production_status.order_version, operation_production_status.changed_at) <= (excluded.order_version, excluded.changed_at)`,
		p.TenantID, strings.ToLower(in.OrderID), strconv.Itoa(in.OperNumber), in.Finished, in.Status, in.At, in.OrderVersion, in.Actor, in.EventID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// productionFinishedStart applies production's finish to a new job-start (FR-25.3, BR-25.7). It
// looks at a job-start whose job's operation production finished at or before the start; any
// other registration, a start before the finish (work done while the operation was still open), a
// job the dispatch projection does not hold and an operation production reopened are untouched.
// Such a start sent online is refused (SF-0157, 422): the operator is at the kiosk and is told to
// have the operation reopened. Sent offline (a replay of work the operator already did), it is
// accepted and flagged job-finished-conflict for supervisor review, as BR-25.7 asks of a job
// finished server-side while the kiosk was offline; conflict reports that. A client UUID already
// stored is the replay of an accepted item and is left to the duplicate path.
func productionFinishedStart(ctx context.Context, tx pgx.Tx, tenantID string, in Registration) (conflict bool, err error) {
	if in.JobID == nil || in.Kind != KindJobStart {
		return false, nil
	}
	var finishedAt time.Time
	err = tx.QueryRow(ctx, `select o.changed_at from shop_job j
		join operation_production_status o on o.tenant_id = j.tenant_id and o.order_ref = lower(j.order_ref) and o.operation_ref = j.operation_ref
		where j.tenant_id = $1 and j.id = $2 and o.finished and o.changed_at <= $3
		  and not exists (select 1 from time_registration t where t.tenant_id = $1 and t.id = $4)`,
		tenantID, *in.JobID, in.StartedAt, in.ID).Scan(&finishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if in.Offline {
		return true, nil
	}
	return false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeOperationFinished,
		fmt.Sprintf("job %s: production finished its operation at %s; ask the planner to reopen the operation before starting work on it", *in.JobID, finishedAt.UTC().Format(time.RFC3339)))
}
