package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
)

const (
	resourceCols = "id::text, code, name, finite, time_zone, shifts, version, created_at"
	opCols       = "id::text, order_id::text, oper_number, minutes, due_at, ready_at, priority, candidate_resource_ids::text[], setup_group, setup_minutes, attributes, network_id::text, status, created_at"
	runCols      = "id::text, scope, mode, direction, finite, algorithm, status, operations_count, makespan_minutes, total_lateness_minutes, solver_status, messages, warnings, created_at"
)

func scanResource(row pgx.Row, fp *string) (Resource, error) {
	var r Resource
	var shifts []byte
	dest := []any{&r.ID, &r.Code, &r.Name, &r.Finite, &r.TimeZone, &shifts, &r.Version, &r.CreatedAt}
	if fp != nil {
		dest = append([]any{fp}, dest...)
	}
	if err := row.Scan(dest...); err != nil {
		return Resource{}, err
	}
	return r, json.Unmarshal(shifts, &r.Shifts)
}

// CreateResource stores a resource calendar. A replay returns the stored row
// with created=false.
func (s *Postgres) CreateResource(ctx context.Context, in Resource, idem Idem) (Resource, bool, error) {
	shifts, err := json.Marshal(in.Shifts)
	if err != nil {
		return Resource{}, false, err
	}
	var out Resource
	created := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if idem.Key != "" {
			var stored string
			existing, err := scanResource(tx.QueryRow(ctx, `select idempotency_fingerprint, `+resourceCols+` from scheduling_resource where idempotency_key = $1`, idem.Key), &stored)
			if err == nil {
				if stored != idem.Fingerprint {
					return reuseProblem()
				}
				out = existing
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		row, err := scanResource(tx.QueryRow(ctx, `insert into scheduling_resource (tenant_id, id, code, name, finite, time_zone, shifts, idempotency_key, idempotency_fingerprint)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9) returning `+resourceCols,
			tenantOf(ctx), ids.New(), in.Code, in.Name, in.Finite, in.TimeZone, shifts, nullable(idem.Key), nullable(idem.Fingerprint)), nil)
		if err != nil {
			return err
		}
		out, created = row, true
		return nil
	})
	if err != nil {
		return Resource{}, false, mapWriteErr(err, CodeResourceDuplicate, "a resource with this code already exists")
	}
	return out, created, nil
}

// ListResources returns every resource of the tenant ordered by code.
func (s *Postgres) ListResources(ctx context.Context) ([]Resource, error) {
	out := []Resource{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+resourceCols+` from scheduling_resource order by code`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanResource(rows, nil)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetResource returns one resource.
func (s *Postgres) GetResource(ctx context.Context, id string) (Resource, error) {
	var out Resource
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := scanResource(tx.QueryRow(ctx, `select `+resourceCols+` from scheduling_resource where id = $1`, id), nil)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeResourceNotFound, "resource not found")
		}
		out = r
		return err
	})
	return out, err
}

// ResourceCapacity returns the open UTC intervals of a resource that overlap
// [from, to), clipped to the window (FR-23.1, materialized on read).
func (s *Postgres) ResourceCapacity(ctx context.Context, id string, from, to time.Time) ([]domain.Interval, error) {
	r, err := s.GetResource(ctx, id)
	if err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(r.TimeZone)
	if err != nil {
		return nil, err
	}
	days := int(to.Sub(from)/(24*time.Hour)) + 2
	out := []domain.Interval{}
	for _, iv := range domain.OpenIntervals(loc, r.Shifts, from.Add(-24*time.Hour), days+1) {
		if !iv.End.After(from) || !iv.Start.Before(to) {
			continue
		}
		if iv.Start.Before(from) {
			iv.Start = from
		}
		if iv.End.After(to) {
			iv.End = to
		}
		out = append(out, iv)
	}
	return out, nil
}

func scanOperation(row pgx.Row, fp *string) (Operation, error) {
	var o Operation
	var due, ready *time.Time
	var attrs []byte
	dest := []any{&o.ID, &o.OrderID, &o.OperNumber, &o.Minutes, &due, &ready, &o.Priority, &o.ResourceIDs,
		&o.SetupGroup, &o.SetupMinutes, &attrs, &o.NetworkID, &o.Status, &o.CreatedAt}
	if fp != nil {
		dest = append([]any{fp}, dest...)
	}
	if err := row.Scan(dest...); err != nil {
		return Operation{}, err
	}
	o.DueAt, o.ReadyAt = due, ready
	o.Attributes = map[string]string{}
	return o, json.Unmarshal(attrs, &o.Attributes)
}

// CreateOperation registers an operation's demand for scheduling. Every
// candidate resource, and the network when one is named, must exist in the
// tenant.
func (s *Postgres) CreateOperation(ctx context.Context, in Operation, idem Idem) (Operation, bool, error) {
	if in.Attributes == nil {
		in.Attributes = map[string]string{}
	}
	attrs, err := json.Marshal(in.Attributes)
	if err != nil {
		return Operation{}, false, err
	}
	var out Operation
	created := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if idem.Key != "" {
			var stored string
			existing, err := scanOperation(tx.QueryRow(ctx, `select idempotency_fingerprint, `+opCols+` from scheduling_operation where idempotency_key = $1`, idem.Key), &stored)
			if err == nil {
				if stored != idem.Fingerprint {
					return reuseProblem()
				}
				out = existing
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var known int
		if err := tx.QueryRow(ctx, `select count(*) from scheduling_resource where id = any($1::uuid[])`, in.ResourceIDs).Scan(&known); err != nil {
			return err
		}
		if known != len(uniqueStrings(in.ResourceIDs)) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeUnknownResource, "every candidate resource must exist")
		}
		if in.NetworkID != nil {
			var exists bool
			if err := tx.QueryRow(ctx, `select exists(select 1 from scheduling_resource_network where id = $1)`, *in.NetworkID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeNetworkNotFound, "the resource network does not exist")
			}
		}
		row, err := scanOperation(tx.QueryRow(ctx, `insert into scheduling_operation (tenant_id, id, order_id, oper_number, minutes, due_at, ready_at, priority,
				candidate_resource_ids, setup_group, setup_minutes, attributes, network_id, idempotency_key, idempotency_fingerprint)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9::uuid[], $10, $11, $12, $13, $14, $15) returning `+opCols,
			tenantOf(ctx), ids.New(), in.OrderID, in.OperNumber, in.Minutes, in.DueAt, in.ReadyAt, in.Priority, in.ResourceIDs,
			in.SetupGroup, in.SetupMinutes, attrs, in.NetworkID, nullable(idem.Key), nullable(idem.Fingerprint)), nil)
		if err != nil {
			return err
		}
		out, created = row, true
		return nil
	})
	if err != nil {
		return Operation{}, false, mapWriteErr(err, CodeOperationDuplicate, "this order already has an operation with that number")
	}
	return out, created, nil
}

// ListOperations returns operations ordered by order and operation number.
func (s *Postgres) ListOperations(ctx context.Context, f OperationFilter) ([]Operation, error) {
	out := []Operation{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+opCols+` from scheduling_operation
			where ($1 = '' or order_id::text = $1) and ($2 = '' or status = $2)
			order by order_id, oper_number limit $3`, f.OrderID, f.Status, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			o, err := scanOperation(rows, nil)
			if err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	return out, err
}

func scanRun(row pgx.Row, fp *string) (Run, error) {
	var r Run
	var msgs, warnings []byte
	dest := []any{&r.ID, &r.Scope, &r.Mode, &r.Direction, &r.Finite, &r.Algorithm, &r.Status, &r.OperationsCount,
		&r.MakespanMinutes, &r.TotalLatenessMinutes, &r.SolverStatus, &msgs, &warnings, &r.CreatedAt}
	if fp != nil {
		dest = append([]any{fp}, dest...)
	}
	if err := row.Scan(dest...); err != nil {
		return Run{}, err
	}
	if err := json.Unmarshal(msgs, &r.Messages); err != nil {
		return Run{}, err
	}
	return r, json.Unmarshal(warnings, &r.Warnings)
}

func insertRun(ctx context.Context, tx pgx.Tx, r *Run, idem Idem) error {
	if r.Messages == nil {
		r.Messages = []string{}
	}
	if r.Warnings == nil {
		r.Warnings = []RunWarning{}
	}
	msgs, err := json.Marshal(r.Messages)
	if err != nil {
		return err
	}
	warnings, err := json.Marshal(r.Warnings)
	if err != nil {
		return err
	}
	return tx.QueryRow(ctx, `insert into schedule_run (tenant_id, id, scope, mode, direction, finite, algorithm, status, operations_count,
		makespan_minutes, total_lateness_minutes, messages, warnings, idempotency_key, idempotency_fingerprint, finished_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, now()) returning created_at`,
		tenantOf(ctx), r.ID, r.Scope, r.Mode, r.Direction, r.Finite, r.Algorithm, r.Status, r.OperationsCount,
		r.MakespanMinutes, r.TotalLatenessMinutes, msgs, warnings, nullable(idem.Key), nullable(idem.Fingerprint)).Scan(&r.CreatedAt)
}

// GetRun returns one schedule run.
func (s *Postgres) GetRun(ctx context.Context, id string) (Run, error) {
	var out Run
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		row, err := scanRun(tx.QueryRow(ctx, `select `+runCols+` from schedule_run where id = $1`, id), nil)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeRunNotFound, "schedule run not found")
		}
		out = row
		return err
	})
	return out, err
}

// ListRuns returns the newest runs first.
func (s *Postgres) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	out := []Run{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+runCols+` from schedule_run order by created_at desc, id desc limit $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRun(rows, nil)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

func loadResources(ctx context.Context, tx pgx.Tx) ([]domain.Resource, map[string]bool, error) {
	rows, err := tx.Query(ctx, `select id::text, code, finite, time_zone, shifts from scheduling_resource order by code`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []domain.Resource
	finite := map[string]bool{}
	for rows.Next() {
		var r domain.Resource
		var tz string
		var shifts []byte
		if err := rows.Scan(&r.ID, &r.Code, &r.Finite, &tz, &shifts); err != nil {
			return nil, nil, err
		}
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal(shifts, &r.Shifts); err != nil {
			return nil, nil, err
		}
		r.Location = loc
		finite[r.ID] = r.Finite
		out = append(out, r)
	}
	return out, finite, rows.Err()
}

// loadOperations reads operations matching where (bound positionally, columns
// qualified with o.), ordered by order and operation number, with the code of
// the network each names. lock takes row locks on the operations so
// concurrent runs and moves over the same operations serialize.
func loadOperations(ctx context.Context, tx pgx.Tx, where string, lock bool, args ...any) ([]domain.Operation, error) {
	q := `select o.id::text, o.order_id::text, o.oper_number, o.minutes, o.due_at, o.ready_at, o.priority, o.candidate_resource_ids::text[],
			coalesce(o.setup_group, ''), o.setup_minutes, o.attributes, coalesce(n.code, '')
		from scheduling_operation o
		left join scheduling_resource_network n on n.tenant_id = o.tenant_id and n.id = o.network_id
		where ` + where + ` order by o.order_id, o.oper_number`
	if lock {
		q += ` for update of o`
	}
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Operation
	for rows.Next() {
		var op domain.Operation
		var due, ready *time.Time
		var attrs []byte
		if err := rows.Scan(&op.ID, &op.OrderID, &op.OperNumber, &op.Minutes, &due, &ready, &op.Priority, &op.Candidates,
			&op.SetupGroup, &op.SetupMinutes, &attrs, &op.Network); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(attrs, &op.Attributes); err != nil {
			return nil, err
		}
		if due != nil {
			op.DueAt = *due
		}
		if ready != nil {
			op.ReadyAt = *ready
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// loadBooked reads the capacity-reserving (finite) segments that end after
// from, with the setup group, route setup and changeover the scheduler checks
// a job inserted in front against. Infinite-run rows never block other work.
func loadBooked(ctx context.Context, tx pgx.Tx, from time.Time) ([]domain.Busy, error) {
	rows, err := tx.Query(ctx, `select j.resource_id::text, j.job_id::text, j.start_at, j.end_at, coalesce(o.setup_group, ''), o.setup_minutes, j.changeover_minutes
		from scheduled_job j join scheduling_operation o on o.tenant_id = j.tenant_id and o.id = j.job_id
		where j.finite and j.end_at > $1`, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Busy
	for rows.Next() {
		var b domain.Busy
		if err := rows.Scan(&b.ResourceID, &b.JobID, &b.Start, &b.End, &b.Group, &b.Setup, &b.Changeover); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func scanBusy(rows pgx.Rows) ([]domain.Busy, error) {
	defer rows.Close()
	var out []domain.Busy
	for rows.Next() {
		var b domain.Busy
		if err := rows.Scan(&b.ResourceID, &b.JobID, &b.Start, &b.End); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// placementsOfOrders rebuilds each scheduled operation's placement of the
// given orders from its segments, keyed by operation id.
func placementsOfOrders(ctx context.Context, tx pgx.Tx, orderIDs []string) (map[string]domain.Placement, error) {
	rows, err := tx.Query(ctx, `select job_id::text, order_id::text, oper_number, resource_id::text, start_at, end_at, late_minutes, changeover_minutes
		from scheduled_job where order_id = any($1::uuid[]) order by job_id, segment_no`, orderIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]domain.Placement{}
	for rows.Next() {
		var jobID, oid, rid string
		var oper, late, changeover int
		var start, end time.Time
		if err := rows.Scan(&jobID, &oid, &oper, &rid, &start, &end, &late, &changeover); err != nil {
			return nil, err
		}
		pl, ok := out[jobID]
		if !ok {
			pl = domain.Placement{OperationID: jobID, OrderID: oid, OperNumber: oper, ResourceID: rid, Start: start, LateMinutes: late,
				ChangeoverMinutes: changeover}
		}
		pl.Segments = append(pl.Segments, domain.Segment{Start: start, End: end})
		pl.End = end
		out[jobID] = pl
	}
	return out, rows.Err()
}

func firmedJobs(ctx context.Context, tx pgx.Tx, orderID string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `select distinct job_id::text from scheduled_job where order_id = $1::uuid and firmed`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// OrderDates is the scheduled span of an order.
type OrderDates struct {
	Start time.Time
	End   time.Time
	Late  int
}

func orderDates(ctx context.Context, tx pgx.Tx, orderIDs []string) (map[string]OrderDates, error) {
	out := map[string]OrderDates{}
	if len(orderIDs) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `select order_id::text, min(start_at), max(end_at), max(late_minutes) from scheduled_job
		where order_id = any($1::uuid[]) group by order_id`, orderIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var d OrderDates
		if err := rows.Scan(&id, &d.Start, &d.End, &d.Late); err != nil {
			return nil, err
		}
		out[id] = d
	}
	return out, rows.Err()
}

func uniqueStrings(in []string) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, s := range in {
		out[s] = true
	}
	return out
}
