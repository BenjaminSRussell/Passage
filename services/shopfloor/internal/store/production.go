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
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
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
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if in.Finished {
		if err := stopOpenStartsOfFinishedOperation(ctx, tx, p.TenantID, in); err != nil {
			return true, err
		}
	}
	return true, nil
}

// stopOpenStartsOfFinishedOperation closes, in the consumer's transaction, the open job-starts of
// every worker on the jobs of the operation production just finished (FR-25.3): a closed job-stop
// from the start to the finish (see domain.PlanProductionStop) flagged production-finished, with
// registration.posted, so the job's labour hours end at the finish and the worker's kiosk does not
// keep a finished job open. Starts of a bundle stay open; the clocked-in inquiry and the kiosk
// session show them with the job's productionFinishedAt. A stop the kiosk sends later for the same
// start supersedes the server's (supersedeAutoStop), so the hours are not counted twice.
//
// The operation's jobs are locked exclusively first, then the workers in id order: a registration
// batch takes the same job locks shared before its workers (lockRegistrationJobs, lockJobWorkers),
// so a batch on these jobs either committed before this read of the open starts or waits for the
// finish to commit, and the two lock orders cannot cross.
func stopOpenStartsOfFinishedOperation(ctx context.Context, tx pgx.Tx, tenantID string, in OperationStatusIn) error {
	jobs, err := collect(ctx, tx, `select id::text from shop_job where tenant_id = $1 and lower(order_ref) = $2 and operation_ref = $3`,
		tenantID, strings.ToLower(in.OrderID), strconv.Itoa(in.OperNumber))
	if err != nil || len(jobs) == 0 {
		return err
	}
	if err := lockJobs(ctx, tx, tenantID, jobs, "pg_advisory_xact_lock"); err != nil {
		return err
	}
	onOperation := make(map[string]bool, len(jobs))
	for _, j := range jobs {
		onOperation[j] = true
	}
	workers, err := collect(ctx, tx, `select distinct worker_id::text from time_registration
		where tenant_id = $1 and job_id = any($2::uuid[]) and kind = 'job-start' and stopped_at is null
		order by 1`, tenantID, jobs)
	if err != nil {
		return err
	}
	for _, w := range workers {
		if _, err := tx.Exec(ctx, `select 1 from worker where tenant_id = $1 and id = $2 for update`, tenantID, w); err != nil {
			return err
		}
		open, err := openStarts(ctx, tx, tenantID, w)
		if err != nil {
			return err
		}
		var onJob []domain.OpenStart
		for _, o := range open {
			if onOperation[o.JobID] {
				onJob = append(onJob, o)
			}
		}
		stops, _ := domain.PlanProductionStop(onJob, in.At)
		for _, unit := range stops {
			hours, err := unit.Hours()
			if err != nil {
				return err
			}
			if _, err := insertServerStop(ctx, tx, tenantID, w, unit.Members[0], unit, hours[0], domain.FlagProductionFinished, productionStopNote); err != nil {
				return err
			}
		}
	}
	return nil
}

// collect runs a query of one text column and returns its values in row order.
func collect(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]string, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
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
