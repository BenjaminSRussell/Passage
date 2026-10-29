package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	eamevents "github.com/BenjaminSRussell/passage/services/eam/internal/events"
)

// WorkOrder is a maintenance order for one asset (FR-44.7). Costs are kept as
// decimal strings; the GL posting itself is a later slice (FR-44.11).
type WorkOrder struct {
	ID              string     `json:"id"`
	Number          string     `json:"number"`
	AssetID         string     `json:"assetId"`
	Type            string     `json:"workOrderType"`
	Status          string     `json:"status"`
	Priority        int        `json:"priority"`
	Currency        string     `json:"currency"`
	PlanLineID      *string    `json:"planLineId,omitempty"`
	RequestID       *string    `json:"requestId,omitempty"`
	JobType         string     `json:"jobType,omitempty"`
	DueAt           *time.Time `json:"dueAt,omitempty"`
	ActualStart     *time.Time `json:"actualStart,omitempty"`
	ActualEnd       *time.Time `json:"actualEnd,omitempty"`
	DowntimeMinutes int        `json:"downtimeMinutes"`
	PartsCost       string     `json:"partsCost"`
	LaborCost       string     `json:"laborCost"`
	WarrantyID      *string    `json:"warrantyId,omitempty"`
	WarrantyCovered bool       `json:"warrantyCovered"`
	Version         int        `json:"version"`
	CreatedAt       time.Time  `json:"createdAt"`
}

const workOrderColumns = `id::text, number, asset_id::text, work_order_type, status, priority, currency,
  plan_line_id::text, request_id::text, coalesce(job_type, ''), due_at, actual_start, actual_end,
  downtime_minutes, parts_cost::text, labor_cost::text, warranty_id::text, warranty_covered, version, created_at`

func scanWorkOrder(row pgx.Row) (WorkOrder, error) {
	var w WorkOrder
	err := row.Scan(&w.ID, &w.Number, &w.AssetID, &w.Type, &w.Status, &w.Priority, &w.Currency,
		&w.PlanLineID, &w.RequestID, &w.JobType, &w.DueAt, &w.ActualStart, &w.ActualEnd,
		&w.DowntimeMinutes, &w.PartsCost, &w.LaborCost, &w.WarrantyID, &w.WarrantyCovered, &w.Version, &w.CreatedAt)
	return w, err
}

func getWorkOrder(ctx context.Context, tx pgx.Tx, t, id string, lock bool) (WorkOrder, error) {
	q := `select ` + workOrderColumns + ` from work_order where tenant_id = $1 and id = $2`
	if lock {
		q += ` for update`
	}
	w, err := scanWorkOrder(tx.QueryRow(ctx, q, t, id))
	return w, notFound(err, "work order")
}

// insertWorkOrder creates a work order in CREATED and returns its id. Every
// creation path (by hand, request conversion, plan generation) runs the
// warranty check here (BR-44.14) on the order's date: its due date, or now. The
// number comes from the work-order sequence, allocated last in the transaction.
func insertWorkOrder(ctx context.Context, tx pgx.Tx, t string, in WorkOrder, key string, now time.Time) (string, error) {
	at := now
	if in.DueAt != nil {
		at = *in.DueAt
	}
	w, err := coveringWarranty(ctx, tx, t, in.AssetID, at, in.JobType)
	if err != nil {
		return "", err
	}
	var warrantyID *string
	if w != nil {
		warrantyID = &w.ID
	}
	id := ids.New()
	_, err = tx.Exec(ctx, `insert into work_order (tenant_id, id, number, asset_id, work_order_type, status, priority,
	    plan_line_id, request_id, job_type, currency, due_at, warranty_id, warranty_covered, idempotency_key)
	  values ($1, $2, 'WO-' || nextval('work_order_number_seq'), $3, $4, 'CREATED', $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		t, id, in.AssetID, in.Type, in.Priority, in.PlanLineID, in.RequestID, optStr(in.JobType), in.Currency,
		in.DueAt, warrantyID, warrantyID != nil, optStr(key))
	return id, err
}

// CreateWorkOrder opens a work order. The asset must be ACTIVE (EAM-AST-001).
func (s *Postgres) CreateWorkOrder(ctx context.Context, in WorkOrder, key string) (WorkOrder, error) {
	var out WorkOrder
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if id, err := existingID(ctx, tx, "work_order", t, key); err != nil || id != "" {
			if err != nil {
				return err
			}
			var gerr error
			out, gerr = getWorkOrder(ctx, tx, t, id, false)
			return gerr
		}
		asset, err := getAsset(ctx, tx, t, in.AssetID, false)
		if err != nil {
			return err
		}
		if !domain.ActiveState(asset.LifecycleState) {
			return problem(422, CodeAssetState, "asset is "+asset.LifecycleState+"; work orders need ACTIVE")
		}
		if in.Type == domain.TypeCapital {
			// FR-44.11: a CAPITAL order needs a fixed asset or CIP project, which
			// this service cannot link yet (wave 37), so none can be valid.
			return problem(422, CodeCapital, "CAPITAL work order needs a fixed asset or CIP project")
		}
		if in.Type == domain.TypeEmergency {
			in.Priority = 1
		} else if in.Priority == 0 {
			in.Priority = domain.DefaultTypePriority(in.Type, asset.Criticality)
		}
		id, err := insertWorkOrder(ctx, tx, t, in, key, s.now())
		if err != nil {
			return err
		}
		if err := enqueue(ctx, tx, t, "workorder", id, eamevents.WorkOrderCreated, eamevents.WorkOrderCreatedData{
			WorkOrderID: id, AssetID: in.AssetID, Type: in.Type}); err != nil {
			return err
		}
		out, err = getWorkOrder(ctx, tx, t, id, false)
		return err
	})
	return out, err
}

// Transition is a requested lifecycle change of a work order.
type Transition struct {
	To              domain.Status
	Version         int
	ActualStart     *time.Time
	ActualEnd       *time.Time
	DowntimeMinutes *int
	Actor           string
}

// TransitionWorkOrder applies a lifecycle transition (BR-44.3) guarded by the
// caller's version. Completion needs ordered actual times, records the
// downtime and resets the plan line (BR-44.2), which reschedules at once. A
// repeated key returns the work order as it is now without a second
// transition.
func (s *Postgres) TransitionWorkOrder(ctx context.Context, id string, in Transition, key string) (WorkOrder, error) {
	var out WorkOrder
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		cur, err := getWorkOrder(ctx, tx, t, id, true)
		if err != nil {
			return err
		}
		if key != "" {
			var woID string
			err := tx.QueryRow(ctx, `select work_order_id::text from work_order_transition
			  where tenant_id = $1 and idempotency_key = $2`, t, key).Scan(&woID)
			if err == nil {
				if woID != id {
					return problem(422, CodeKeyReuse, "Idempotency-Key was already used for another work order")
				}
				out = cur
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if cur.Version != in.Version {
			return problem(412, CodeVersion, "work order version changed")
		}
		from, to := domain.Status(cur.Status), in.To
		if !domain.CanTransition(from, to, cur.Priority == 1) {
			return problem(422, CodeWOTransition, "transition "+cur.Status+" to "+string(to)+" not allowed")
		}
		now := s.now()
		if to == domain.StatusInProgress {
			// Permits and LOTO gate the start of work (BR-44.11, FR-44.19).
			if err := checkPermitGate(ctx, tx, t, cur, now); err != nil {
				return err
			}
		}
		newStart, newEnd := cur.ActualStart, cur.ActualEnd
		if in.ActualStart != nil {
			newStart = in.ActualStart
		}
		if in.ActualEnd != nil {
			newEnd = in.ActualEnd
		}
		if to == domain.StatusInProgress && newStart == nil {
			newStart = &now
		}
		downtime := cur.DowntimeMinutes
		if to == domain.StatusCompleted {
			if newEnd == nil {
				newEnd = &now
			}
			if err := domain.ValidCompletion(newStart, newEnd); err != nil {
				return problem(422, CodeWOTimes, err.Error())
			}
			if domain.FaultRequired(cur.Type) {
				f, err := currentFault(ctx, tx, t, id)
				if err != nil {
					return err
				}
				if f == nil {
					return problem(422, CodeFaultMissing, "register a fault before completing "+cur.Type+" work orders")
				}
			}
			if in.DowntimeMinutes != nil {
				windows, err := recordedDowntime(ctx, tx, t, id)
				if err != nil {
					return err
				}
				if *in.DowntimeMinutes < windows {
					return problem(422, CodeDowntime, fmt.Sprintf(
						"downtime of %d minutes is less than the %d minutes of downtime windows already recorded", *in.DowntimeMinutes, windows))
				}
				downtime = *in.DowntimeMinutes
			}
		}
		if _, err := tx.Exec(ctx, `update work_order set status = $3, actual_start = $4, actual_end = $5,
		    downtime_minutes = $6, version = version + 1, updated_at = now()
		  where tenant_id = $1 and id = $2 and version = $7`,
			t, id, string(to), newStart, newEnd, downtime, in.Version); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into work_order_transition (tenant_id, id, work_order_id, from_status,
		    to_status, at, actor, idempotency_key) values ($1, $2, $3, $4, $5, $6, $7, $8)`,
			t, ids.New(), id, string(from), string(to), now, optStr(in.Actor), optStr(key)); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, t, "workorder", id, eamevents.WorkOrderStatus, eamevents.WorkOrderStatusData{
			WorkOrderID: id, From: string(from), To: string(to), At: now.Format(time.RFC3339Nano),
			Actor: in.Actor}); err != nil {
			return err
		}
		if to == domain.StatusCompleted {
			if err := enqueue(ctx, tx, t, "workorder", id, eamevents.WorkOrderCompleted, eamevents.WorkOrderCompletedData{
				WorkOrderID: id, AssetID: cur.AssetID, Currency: cur.Currency, PartsCost: cur.PartsCost,
				LaborCost: cur.LaborCost, DowntimeMins: downtime,
				CompletedAt: newEnd.UTC().Format(time.RFC3339Nano)}); err != nil {
				return err
			}
			if cur.WarrantyCovered && cur.WarrantyID != nil {
				if err := claimWarranty(ctx, tx, t, cur); err != nil {
					return err
				}
			}
			if cur.PlanLineID != nil {
				if err := resetPlanLine(ctx, tx, t, *cur.PlanLineID, *newEnd, now); err != nil {
					return err
				}
			}
		}
		out, err = getWorkOrder(ctx, tx, t, id, false)
		return err
	})
	return out, err
}

// resetPlanLine records the completion on the plan line and reschedules it (BR-44.2).
func resetPlanLine(ctx context.Context, tx pgx.Tx, t, lineID string, completedAt, now time.Time) error {
	if _, err := tx.Exec(ctx, `update plan_line pl set last_completed_at = $3,
	    last_completed_reading = coalesce((select c.last_reading from counter c
	      where c.tenant_id = pl.tenant_id and c.id = pl.counter_id), pl.last_completed_reading)
	  where pl.tenant_id = $1 and pl.id = $2`, t, lineID, completedAt); err != nil {
		return err
	}
	_, err := scheduleLines(ctx, tx, t, "and pl.id = $2", []any{lineID}, now)
	return err
}

// RecordLabor books hours at a trade rate onto an open work order (FR-44.10).
// The same key replays without a second booking.
func (s *Postgres) RecordLabor(ctx context.Context, woID string, hours, rate decimal.Decimal, workerID *string, key string) (WorkOrder, error) {
	var out WorkOrder
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		cur, err := getWorkOrder(ctx, tx, t, woID, true)
		if err != nil {
			return err
		}
		if key != "" {
			var owner string
			err := tx.QueryRow(ctx, `select work_order_id::text from work_order_labor
			  where tenant_id = $1 and idempotency_key = $2`, t, key).Scan(&owner)
			if err == nil {
				if owner != woID {
					return problem(422, CodeKeyReuse, "Idempotency-Key was already used for labor on another work order")
				}
				out = cur
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if !domain.Status(cur.Status).Open() {
			return problem(422, CodeWOTransition, "labor cannot be recorded on a "+cur.Status+" work order")
		}
		cost := domain.LaborCost(hours, rate).String()
		if _, err := tx.Exec(ctx, `insert into work_order_labor (tenant_id, id, work_order_id, hours, rate, cost,
		    worker_id, idempotency_key)
		  values ($1, $2, $3, $4::numeric, $5::numeric, $6::numeric, $7, $8)`,
			t, ids.New(), woID, hours.String(), rate.String(), cost, workerID, optStr(key)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update work_order set labor_cost = labor_cost + $3::numeric, updated_at = now()
		  where tenant_id = $1 and id = $2`, t, woID, cost); err != nil {
			return err
		}
		out, err = getWorkOrder(ctx, tx, t, woID, false)
		return err
	})
	return out, err
}

// AssetKPI computes MTBF, MTTR and availability for an asset over [from, to)
// (BR-44.8). Repairs are CORRECTIVE and EMERGENCY work orders that completed in
// the window; downtime is the recorded downtime of every completed work order.
func (s *Postgres) AssetKPI(ctx context.Context, assetID string, from, to time.Time) (domain.KPI, error) {
	var out domain.KPI
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		rows, err := tx.Query(ctx, `select (extract(epoch from (actual_end - actual_start)) / 3600.0)::float8,
		    downtime_minutes, work_order_type
		  from work_order where tenant_id = $1 and asset_id = $2
		    and status in ('COMPLETED', 'CLOSED') and actual_end >= $3 and actual_end < $4`,
			t, assetID, from, to)
		if err != nil {
			return err
		}
		defer rows.Close()
		var repairs []float64
		var downtimeMinutes int
		for rows.Next() {
			var hours float64
			var mins int
			var typ string
			if err := rows.Scan(&hours, &mins, &typ); err != nil {
				return err
			}
			downtimeMinutes += mins
			if typ == "CORRECTIVE" || typ == "EMERGENCY" {
				repairs = append(repairs, hours)
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out = domain.Reliability(to.Sub(from).Hours(), float64(downtimeMinutes)/60, repairs)
		return nil
	})
	return out, err
}

// ListWorkOrders returns a page of work orders, newest first, optionally for
// one status or asset (8.44.14 Work order list).
func (s *Postgres) ListWorkOrders(ctx context.Context, opt ListOptions) (Page[WorkOrder], error) {
	var out Page[WorkOrder]
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		rows, err := tx.Query(ctx, `select `+workOrderColumns+` from work_order
		  where tenant_id = $1 and ($2::text is null or status = $2) and ($3::uuid is null or asset_id = $3)
		    and ($4::uuid is null or id < $4)
		  order by id desc limit $5`, t, optStr(opt.Status), optStr(opt.AssetID), optStr(opt.Cursor), opt.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		var items []WorkOrder
		for rows.Next() {
			w, err := scanWorkOrder(rows)
			if err != nil {
				return err
			}
			items = append(items, w)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out = pageOf(items, opt.Limit, func(w WorkOrder) string { return w.ID })
		return nil
	})
	return out, err
}

// LaborLine is one labor booking on a work order (FR-44.10).
type LaborLine struct {
	ID        string    `json:"id"`
	Hours     string    `json:"hours"`
	Rate      string    `json:"rate"`
	Cost      string    `json:"cost"`
	WorkerID  *string   `json:"workerId,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// StatusChange is one entry of a work order's status history (BR-44.3).
type StatusChange struct {
	From  string    `json:"from"`
	To    string    `json:"to"`
	At    time.Time `json:"at"`
	Actor *string   `json:"actor,omitempty"`
}

// WorkOrderDetail is a work order with its asset type, labor, status history,
// current fault registration and downtime windows.
type WorkOrderDetail struct {
	WorkOrder
	AssetType string           `json:"assetType"`
	Labor     []LaborLine      `json:"labor"`
	History   []StatusChange   `json:"history"`
	Fault     *WorkOrderFault  `json:"fault,omitempty"`
	Downtime  []DowntimeWindow `json:"downtime"`
}

// GetWorkOrder returns one work order with its detail.
func (s *Postgres) GetWorkOrder(ctx context.Context, id string) (WorkOrderDetail, error) {
	var out WorkOrderDetail
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		w, err := getWorkOrder(ctx, tx, t, id, false)
		if err != nil {
			return err
		}
		out, err = workOrderDetail(ctx, tx, t, w)
		return err
	})
	return out, err
}

// workOrderDetail reads the parts of the detail view that live in other tables.
func workOrderDetail(ctx context.Context, tx pgx.Tx, t string, w WorkOrder) (WorkOrderDetail, error) {
	out := WorkOrderDetail{WorkOrder: w, Labor: []LaborLine{}, History: []StatusChange{}, Downtime: []DowntimeWindow{}}
	if err := tx.QueryRow(ctx, `select asset_type from asset where tenant_id = $1 and id = $2`, t, w.AssetID).
		Scan(&out.AssetType); err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `select id::text, hours::text, rate::text, cost::text, worker_id::text, created_at
	  from work_order_labor where tenant_id = $1 and work_order_id = $2 order by id`, t, w.ID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var l LaborLine
		if err := rows.Scan(&l.ID, &l.Hours, &l.Rate, &l.Cost, &l.WorkerID, &l.CreatedAt); err != nil {
			rows.Close()
			return out, err
		}
		out.Labor = append(out.Labor, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, `select from_status, to_status, at, actor from work_order_transition
	  where tenant_id = $1 and work_order_id = $2 order by at, id`, t, w.ID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var c StatusChange
		if err := rows.Scan(&c.From, &c.To, &c.At, &c.Actor); err != nil {
			rows.Close()
			return out, err
		}
		out.History = append(out.History, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, `select id::text, resource_id::text, starts_at, ends_at, minutes, recorded_by
	  from downtime_window where tenant_id = $1 and work_order_id = $2 order by starts_at, id`, t, w.ID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var d DowntimeWindow
		if err := rows.Scan(&d.ID, &d.ResourceID, &d.From, &d.To, &d.Minutes, &d.RecordedBy); err != nil {
			rows.Close()
			return out, err
		}
		out.Downtime = append(out.Downtime, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.Fault, err = currentFault(ctx, tx, t, w.ID)
	return out, err
}
