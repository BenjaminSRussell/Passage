package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	eamevents "github.com/BenjaminSRussell/passage/services/eam/internal/events"
)

// Round is a maintenance round: ordered stops on assets of one site, inspected
// together (FR-44.14). TravelMinutes is the total travel between stops.
type Round struct {
	ID            string             `json:"id"`
	Code          string             `json:"code"`
	Name          string             `json:"name"`
	SiteID        string             `json:"siteId"`
	Active        bool               `json:"active"`
	Stops         []domain.RoundStop `json:"stops"`
	TravelMinutes int                `json:"travelMinutes"`
	CreatedAt     time.Time          `json:"createdAt"`
}

// WorkOrderStop is one stop of a ROUND work order with its outcome once recorded.
type WorkOrderStop struct {
	Sequence      int        `json:"sequence"`
	AssetID       string     `json:"assetId"`
	JobType       string     `json:"jobType"`
	Mandatory     bool       `json:"mandatory"`
	TravelMinutes int        `json:"travelMinutes"`
	Outcome       *string    `json:"outcome,omitempty"`
	Reason        *string    `json:"reason,omitempty"`
	RequestID     *string    `json:"requestId,omitempty"`
	RecordedBy    *string    `json:"recordedBy,omitempty"`
	RecordedAt    *time.Time `json:"recordedAt,omitempty"`
}

// RoundWorkOrderInput is what an on-demand round occurrence's work order needs.
type RoundWorkOrderInput struct {
	Currency string
	DueAt    *time.Time
}

const roundColumns = `id::text, code, name, site_id::text, active, created_at`

// CreateRound defines a round (FR-44.14). The stops follow the round rules
// (EAM-RND-003), every stop asset exists on the round's site, no stop job type
// needs a permit to work (a round work order cannot carry one permit per stop),
// and the code is unique (EAM-DUP-001). A repeated key returns the stored round.
func (s *Postgres) CreateRound(ctx context.Context, in Round, key string) (Round, error) {
	var out Round
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if id, err := existingID(ctx, tx, "maintenance_round", t, key); err != nil || id != "" {
			if err != nil {
				return err
			}
			var gerr error
			out, gerr = getRound(ctx, tx, t, id)
			return gerr
		}
		if err := domain.ValidateRoundStops(in.Stops); err != nil {
			return gateProblem(err)
		}
		for _, st := range in.Stops {
			a, err := getAsset(ctx, tx, t, st.AssetID, false)
			if err != nil {
				return err
			}
			if a.SiteID != in.SiteID {
				return problem(422, domain.CodeRoundDefinition, fmt.Sprintf("stop %d asset %s is on another site", st.Sequence, a.AssetNumber))
			}
			types, err := requiredPermitTypes(ctx, tx, t, st.JobType)
			if err != nil {
				return err
			}
			if len(types) > 0 {
				return problem(422, domain.CodeRoundDefinition, fmt.Sprintf(
					"stop %d job type %s needs a %s permit to work; plan that job as its own work order", st.Sequence, st.JobType, types[0].Code))
			}
		}
		if dup, err := taken(ctx, tx, "maintenance_round", "tenant_id = $1 and code = $2", t, in.Code); err != nil || dup {
			if err != nil {
				return err
			}
			return problem(409, CodeDuplicate, "maintenance round "+in.Code+" already exists")
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into maintenance_round (tenant_id, id, code, name, site_id, idempotency_key, created_at)
		  values ($1, $2, $3, $4, $5, $6, $7)`, t, id, in.Code, in.Name, in.SiteID, optStr(key), s.now()); err != nil {
			return err
		}
		for _, st := range in.Stops {
			if _, err := tx.Exec(ctx, `insert into round_stop (tenant_id, round_id, sequence, asset_id, job_type, mandatory,
			    travel_minutes) values ($1, $2, $3, $4, $5, $6, $7)`,
				t, id, st.Sequence, st.AssetID, st.JobType, st.Mandatory, st.TravelMinutes); err != nil {
				return err
			}
		}
		var err error
		out, err = getRound(ctx, tx, t, id)
		return err
	})
	return out, err
}

// GetRound returns one round with its stops.
func (s *Postgres) GetRound(ctx context.Context, id string) (Round, error) {
	var out Round
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		var err error
		out, err = getRound(ctx, tx, t, id)
		return err
	})
	return out, err
}

// ListRounds pages the rounds, newest first, each with its stops.
func (s *Postgres) ListRounds(ctx context.Context, opt ListOptions) (Page[Round], error) {
	var out Page[Round]
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		rows, err := tx.Query(ctx, `select `+roundColumns+` from maintenance_round
		  where tenant_id = $1 and ($2::uuid is null or id < $2) order by id desc limit $3`,
			t, optStr(opt.Cursor), opt.Limit+1)
		if err != nil {
			return err
		}
		var items []Round
		for rows.Next() {
			r, err := scanRound(rows)
			if err != nil {
				rows.Close()
				return err
			}
			items = append(items, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		out = pageOf(items, opt.Limit, func(r Round) string { return r.ID })
		for i := range out.Items {
			if out.Items[i].Stops, err = roundStops(ctx, tx, t, out.Items[i].ID); err != nil {
				return err
			}
			out.Items[i].TravelMinutes = domain.RoundTravelMinutes(out.Items[i].Stops)
		}
		return nil
	})
	return out, err
}

// CreateRoundWorkOrder opens one ROUND work order for a round outside its plan
// (an extra occurrence). Plan lines of type ROUND generate the scheduled ones.
// Every stop asset must be ACTIVE (EAM-AST-001). A repeated key returns the
// order created with it.
func (s *Postgres) CreateRoundWorkOrder(ctx context.Context, roundID string, in RoundWorkOrderInput, key string) (WorkOrder, error) {
	var out WorkOrder
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if woID, err := existingID(ctx, tx, "work_order", t, key); err != nil || woID != "" {
			if err != nil {
				return err
			}
			var gerr error
			out, gerr = getWorkOrder(ctx, tx, t, woID, false)
			if gerr == nil && out.Type != domain.TypeRound {
				return problem(422, CodeKeyReuse, "Idempotency-Key was already used for another work order")
			}
			return gerr
		}
		r, err := getRound(ctx, tx, t, roundID)
		if err != nil {
			return err
		}
		if !r.Active {
			return problem(422, domain.CodeRoundDefinition, "round "+r.Code+" is inactive")
		}
		if bad, err := inactiveStop(ctx, tx, t, r); err != nil || bad != "" {
			if err != nil {
				return err
			}
			return problem(422, CodeAssetState, bad)
		}
		woID, err := insertRoundWorkOrder(ctx, tx, t, r, nil, in.Currency, in.DueAt, key, s.now())
		if err != nil {
			return err
		}
		out, err = getWorkOrder(ctx, tx, t, woID, false)
		return err
	})
	return out, err
}

// inactiveStop names the first stop whose asset is not ACTIVE, or returns "".
func inactiveStop(ctx context.Context, tx pgx.Tx, t string, r Round) (string, error) {
	for _, st := range r.Stops {
		a, err := getAsset(ctx, tx, t, st.AssetID, false)
		if err != nil {
			return "", err
		}
		if !domain.ActiveState(a.LifecycleState) {
			return fmt.Sprintf("stop %d asset %s is %s; work orders need ACTIVE", st.Sequence, a.AssetNumber, a.LifecycleState), nil
		}
	}
	return "", nil
}

// insertRoundWorkOrder creates the ROUND work order of one occurrence: booked on
// the first stop's asset, with every stop copied in stop order (one job per
// stop, BR-44.9), and publishes workorder.created in the same transaction.
func insertRoundWorkOrder(ctx context.Context, tx pgx.Tx, t string, r Round, lineID *string, currency string,
	dueAt *time.Time, key string, now time.Time) (string, error) {
	anchor := r.Stops[0].AssetID
	woID, err := insertWorkOrder(ctx, tx, t, WorkOrder{AssetID: anchor, Type: domain.TypeRound,
		Priority: domain.DefaultTypePriority(domain.TypeRound, 0), Currency: currency, PlanLineID: lineID, DueAt: dueAt}, key, now)
	if err != nil {
		return "", err
	}
	for _, st := range r.Stops {
		if _, err := tx.Exec(ctx, `insert into work_order_stop (tenant_id, work_order_id, sequence, asset_id, job_type,
		    mandatory, travel_minutes) values ($1, $2, $3, $4, $5, $6, $7)`,
			t, woID, st.Sequence, st.AssetID, st.JobType, st.Mandatory, st.TravelMinutes); err != nil {
			return "", err
		}
	}
	err = enqueue(ctx, tx, t, "workorder", woID, eamevents.WorkOrderCreated, eamevents.WorkOrderCreatedData{
		WorkOrderID: woID, AssetID: anchor, Type: domain.TypeRound, PlanLineID: deref(lineID, "")})
	return woID, err
}

// StopOutcome is a DONE or SKIPPED result for one stop (BR-44.9).
type StopOutcome struct {
	Outcome string
	Reason  string
	Actor   string
}

// RecordStopOutcome sets one stop of an IN_PROGRESS ROUND work order to DONE or
// SKIPPED, once. SKIPPED needs a reason, and a skipped mandatory stop raises a
// maintenance request with source ROUND for the stop's asset in the same
// transaction (BR-44.9). The work order row is locked first, so an outcome and
// the completion check never interleave. A repeated key returns the stop as
// recorded.
func (s *Postgres) RecordStopOutcome(ctx context.Context, woID string, seq int, in StopOutcome, key string) (WorkOrderStop, error) {
	var out WorkOrderStop
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		cur, err := getWorkOrder(ctx, tx, t, woID, true)
		if err != nil {
			return err
		}
		if key != "" {
			var owner string
			var ownerSeq int
			err := tx.QueryRow(ctx, `select work_order_id::text, sequence from work_order_stop
			  where tenant_id = $1 and idempotency_key = $2`, t, key).Scan(&owner, &ownerSeq)
			if err == nil {
				if owner != woID || ownerSeq != seq {
					return problem(422, CodeKeyReuse, "Idempotency-Key was already used for another stop")
				}
				out, err = getStop(ctx, tx, t, woID, seq, false)
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if cur.Type != domain.TypeRound {
			return problem(422, domain.CodeStopOutcome, "stop outcomes apply to ROUND work orders only")
		}
		if domain.Status(cur.Status) != domain.StatusInProgress {
			return problem(422, domain.CodeStopOutcome, "stops are recorded while the work order is IN_PROGRESS; it is "+cur.Status)
		}
		reason, err := domain.NormalizeStopReason(in.Outcome, in.Reason)
		if err != nil {
			return gateProblem(err)
		}
		stop, err := getStop(ctx, tx, t, woID, seq, true)
		if err != nil {
			return err
		}
		if stop.Outcome != nil {
			return problem(422, domain.CodeStopOutcome, fmt.Sprintf("stop %d is already %s", seq, *stop.Outcome))
		}
		var requestID *string
		if domain.FollowUpRequired(stop.Mandatory, in.Outcome) {
			req, err := insertRequest(ctx, tx, t, Request{AssetID: stop.AssetID, Source: "ROUND", Severity: 3,
				Description: fmt.Sprintf("Mandatory round stop %d (%s) on %s was skipped: %s", seq, stop.JobType, cur.Number, reason)}, "")
			if err != nil {
				return err
			}
			requestID = &req.ID
		}
		if _, err := tx.Exec(ctx, `update work_order_stop set outcome = $4, reason = $5, request_id = $6,
		    recorded_by = $7, recorded_at = $8, idempotency_key = $9
		  where tenant_id = $1 and work_order_id = $2 and sequence = $3`,
			t, woID, seq, in.Outcome, optStr(reason), requestID, optStr(in.Actor), s.now(), optStr(key)); err != nil {
			return err
		}
		out, err = getStop(ctx, tx, t, woID, seq, false)
		return err
	})
	return out, err
}

// openStops counts the stops of a work order that have no outcome yet.
func openStops(ctx context.Context, tx pgx.Tx, t, woID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `select count(*) from work_order_stop
	  where tenant_id = $1 and work_order_id = $2 and outcome is null`, t, woID).Scan(&n)
	return n, err
}

const stopColumns = `sequence, asset_id::text, job_type, mandatory, travel_minutes, outcome, reason,
  request_id::text, recorded_by, recorded_at`

func scanStop(row pgx.Row) (WorkOrderStop, error) {
	var st WorkOrderStop
	err := row.Scan(&st.Sequence, &st.AssetID, &st.JobType, &st.Mandatory, &st.TravelMinutes, &st.Outcome,
		&st.Reason, &st.RequestID, &st.RecordedBy, &st.RecordedAt)
	return st, err
}

func getStop(ctx context.Context, tx pgx.Tx, t, woID string, seq int, lock bool) (WorkOrderStop, error) {
	q := `select ` + stopColumns + ` from work_order_stop where tenant_id = $1 and work_order_id = $2 and sequence = $3`
	if lock {
		q += ` for update`
	}
	st, err := scanStop(tx.QueryRow(ctx, q, t, woID, seq))
	return st, notFound(err, "round stop")
}

// workOrderStops lists the stops of a work order in stop order (none unless ROUND).
func workOrderStops(ctx context.Context, tx pgx.Tx, t, woID string) ([]WorkOrderStop, error) {
	rows, err := tx.Query(ctx, `select `+stopColumns+` from work_order_stop
	  where tenant_id = $1 and work_order_id = $2 order by sequence`, t, woID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkOrderStop{}
	for rows.Next() {
		st, err := scanStop(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func scanRound(row pgx.Row) (Round, error) {
	var r Round
	err := row.Scan(&r.ID, &r.Code, &r.Name, &r.SiteID, &r.Active, &r.CreatedAt)
	return r, err
}

func getRound(ctx context.Context, tx pgx.Tx, t, id string) (Round, error) {
	r, err := scanRound(tx.QueryRow(ctx, `select `+roundColumns+` from maintenance_round
	  where tenant_id = $1 and id = $2`, t, id))
	if err != nil {
		return r, notFound(err, "maintenance round")
	}
	if r.Stops, err = roundStops(ctx, tx, t, id); err != nil {
		return r, err
	}
	r.TravelMinutes = domain.RoundTravelMinutes(r.Stops)
	return r, nil
}

func roundStops(ctx context.Context, tx pgx.Tx, t, roundID string) ([]domain.RoundStop, error) {
	rows, err := tx.Query(ctx, `select sequence, asset_id::text, job_type, mandatory, travel_minutes
	  from round_stop where tenant_id = $1 and round_id = $2 order by sequence`, t, roundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.RoundStop{}
	for rows.Next() {
		var st domain.RoundStop
		if err := rows.Scan(&st.Sequence, &st.AssetID, &st.JobType, &st.Mandatory, &st.TravelMinutes); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}
