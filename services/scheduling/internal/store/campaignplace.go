package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	schedevents "github.com/BenjaminSRussell/passage/services/scheduling/internal/events"
)

// Placement error codes. SCHED-0149 (no contiguous window) is the domain's.
const (
	// CodeCampaignOperation is SCHED-0150: an order of the campaign has no
	// single operation to place on the campaign resource.
	CodeCampaignOperation = "SCHED-0150"
	// CodeCampaignNotPlacedYet is SCHED-0151: the campaign holds no placement.
	CodeCampaignNotPlacedYet = "SCHED-0151"
)

// CampaignJob is the job of one campaign order as it sits on the schedule now
// (a move after the placement shows here). Start and End span every segment of
// the job, changeover included.
type CampaignJob struct {
	OrderID           string    `json:"orderId"`
	OperationID       string    `json:"operationId"`
	OperNumber        int       `json:"operNumber"`
	Start             time.Time `json:"start"`
	End               time.Time `json:"end"`
	ChangeoverMinutes int       `json:"changeoverMinutes"`
	LateMinutes       int       `json:"lateMinutes"`
}

// CampaignCleanout is the closed window a planned cleanout holds on the
// resource (BR-23.13).
type CampaignCleanout struct {
	AfterOrderID  string    `json:"afterOrderId"`
	SetupGroup    string    `json:"setupGroup"`
	Minutes       int       `json:"minutes"`
	Reason        string    `json:"reason"`
	CleaningSetID string    `json:"cleaningSetId,omitempty"`
	Start         time.Time `json:"start"`
	End           time.Time `json:"end"`
}

// CampaignPlacement is a campaign laid on its resource: the run that wrote it,
// its jobs in sequence, its cleanout windows and the warnings of the run.
type CampaignPlacement struct {
	RunID     string             `json:"runId"`
	PlacedAt  time.Time          `json:"placedAt"`
	Jobs      []CampaignJob      `json:"jobs"`
	Cleanouts []CampaignCleanout `json:"cleanouts"`
	Warnings  []RunWarning       `json:"warnings"`
}

// loadPlacement reads the placement of a placed campaign from the schedule.
func loadPlacement(ctx context.Context, tx pgx.Tx, c Campaign) (*CampaignPlacement, error) {
	p := &CampaignPlacement{PlacedAt: *c.PlacedAt, Jobs: []CampaignJob{}, Cleanouts: []CampaignCleanout{}, Warnings: []RunWarning{}}
	if c.placedRun != nil {
		p.RunID = *c.placedRun
		var raw []byte
		if err := tx.QueryRow(ctx, `select warnings from schedule_run where id = $1::uuid`, p.RunID).Scan(&raw); err == nil {
			_ = json.Unmarshal(raw, &p.Warnings)
		}
	}
	rows, err := tx.Query(ctx, `select o.order_id::text, o.operation_id::text, j.oper_number, min(j.start_at), max(j.end_at),
			coalesce(max(j.changeover_minutes), 0), coalesce(max(j.late_minutes), 0)
		from scheduling_campaign_order o join scheduled_job j on j.job_id = o.operation_id
		where o.campaign_id = $1 group by o.order_id, o.operation_id, j.oper_number, o.sequence order by o.sequence`, c.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var j CampaignJob
		if err := rows.Scan(&j.OrderID, &j.OperationID, &j.OperNumber, &j.Start, &j.End, &j.ChangeoverMinutes, &j.LateMinutes); err != nil {
			rows.Close()
			return nil, err
		}
		p.Jobs = append(p.Jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	cr, err := tx.Query(ctx, `select after_order_id::text, setup_group, minutes, reason, coalesce(cleaning_operation_set_id, ''), start_at, end_at
		from scheduling_campaign_cleanout where campaign_id = $1 order by start_at`, c.ID)
	if err != nil {
		return nil, err
	}
	defer cr.Close()
	for cr.Next() {
		var w CampaignCleanout
		if err := cr.Scan(&w.AfterOrderID, &w.SetupGroup, &w.Minutes, &w.Reason, &w.CleaningSetID, &w.Start, &w.End); err != nil {
			return nil, err
		}
		p.Cleanouts = append(p.Cleanouts, w)
	}
	return p, cr.Err()
}

// campaignOperation picks the operation of an order that carries the campaign:
// the one the request names, or else the only one that can run on the campaign
// resource.
func campaignOperation(orderID, resourceID, named string, ops []domain.Operation) (domain.Operation, error) {
	var can []domain.Operation
	for _, op := range ops {
		if slices.Contains(op.Candidates, resourceID) {
			can = append(can, op)
		}
	}
	bad := func(msg string) error {
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeCampaignOperation, fmt.Sprintf("order %s: %s", orderID, msg))
	}
	if named != "" {
		for _, op := range can {
			if op.ID == named {
				return op, nil
			}
		}
		return domain.Operation{}, bad("the named operation does not belong to the order or cannot run on the campaign resource")
	}
	switch len(can) {
	case 0:
		return domain.Operation{}, bad("no operation of the order can run on the campaign resource; create the operation first")
	case 1:
		return can[0], nil
	}
	return domain.Operation{}, bad(fmt.Sprintf("%d operations can run on the campaign resource; name the one that carries the campaign in operationIds", len(can)))
}

// campaignOperations returns, in campaign sequence, the operation each order
// places, every order id, and every operation of each order (its route) keyed
// by order id. The orders' operations are locked like a run locks them.
func campaignOperations(ctx context.Context, tx pgx.Tx, c Campaign, named map[string]string) ([]domain.Operation, []string, map[string][]domain.Operation, error) {
	orderIDs := make([]string, 0, len(c.Orders))
	for _, o := range c.Orders {
		orderIDs = append(orderIDs, o.OrderID)
	}
	ops, err := loadOperations(ctx, tx, "o.order_id = any($1::uuid[])", true, orderIDs)
	if err != nil {
		return nil, nil, nil, err
	}
	byOrder := map[string][]domain.Operation{}
	for _, op := range ops {
		byOrder[op.OrderID] = append(byOrder[op.OrderID], op)
	}
	chosen := make([]domain.Operation, 0, len(c.Orders))
	for _, o := range c.Orders {
		op, err := campaignOperation(o.OrderID, c.ResourceID, named[o.OrderID], byOrder[o.OrderID])
		if err != nil {
			return nil, nil, nil, err
		}
		chosen = append(chosen, op)
	}
	return chosen, orderIDs, byOrder, nil
}

// placedOperations are the operations an earlier placement of the campaign booked.
func placedOperations(ctx context.Context, tx pgx.Tx, campaignID string) ([]string, error) {
	rows, err := tx.Query(ctx, `select operation_id::text from scheduling_campaign_order where campaign_id = $1 and operation_id is not null`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// clearPlacement takes a campaign's non-firmed jobs and cleanout windows off
// the schedule and opens their operations again. A firmed job refuses it.
func clearPlacement(ctx context.Context, tx pgx.Tx, campaignID string, opIDs []string) error {
	if len(opIDs) == 0 {
		_, err := tx.Exec(ctx, `delete from scheduling_campaign_cleanout where campaign_id = $1`, campaignID)
		return err
	}
	firmed, err := firmedOperations(ctx, tx, opIDs)
	if err != nil {
		return err
	}
	if len(firmed) > 0 {
		return firmedProblem("an operation of the campaign")
	}
	if err := trackOps(ctx, tx, opIDs); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `delete from scheduled_job where job_id = any($1::uuid[]) and not firmed`, opIDs); err != nil {
		return err
	}
	if err := dropOrphanSecondaries(ctx, tx, opIDs); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `delete from scheduling_campaign_cleanout where campaign_id = $1`, campaignID); err != nil {
		return err
	}
	return setStatus(ctx, tx, opIDs, "open")
}

// PlaceCampaign lays a sequenced campaign on its resource (FR-23.14): one run
// of scope campaign books the jobs of its orders back to back in plan sequence,
// with the setup matrix changeovers in front of them (the first is the leading
// setup) and each planned cleanout as a closed window after its order, never
// split (BR-23.13). The campaign is never interleaved with other work: a job
// of another order in the way moves the start behind it. A repeat replaces the
// earlier placement. Jobs, windows, run and events commit together.
func (s *Postgres) PlaceCampaign(ctx context.Context, cmd CampaignCommand, idem Idem) (Campaign, bool, error) {
	return s.campaignCommandTracked(ctx, cmd, idem, func(ctx context.Context, tx pgx.Tx, c Campaign) error {
		if c.Status != domain.CampaignPlanned || c.Plan == nil {
			if err := frozen(c.Status); err != nil {
				return err
			}
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeCampaignNotPlanned, "sequence the campaign before you place it")
		}
		chosen, orderIDs, chains, err := campaignOperations(ctx, tx, c, cmd.OperationIDs)
		if err != nil {
			return err
		}
		prev, err := placedOperations(ctx, tx, c.ID)
		if err != nil {
			return err
		}
		all := append([]string(nil), prev...)
		for _, op := range chosen {
			if !slices.Contains(all, op.ID) {
				all = append(all, op.ID)
			}
		}
		prior, err := orderDates(ctx, tx, orderIDs)
		if err != nil {
			return err
		}
		if err := clearPlacement(ctx, tx, c.ID, all); err != nil {
			return err
		}
		start := c.PlannedStart
		if cmd.Start != nil {
			start = *cmd.Start
		}
		resources, finite, err := loadResources(ctx, tx)
		if err != nil {
			return err
		}
		networks, err := loadConstraints(ctx, tx, resources, start, cmd.HorizonDays, start)
		if err != nil {
			return err
		}
		if c.CurrentSetupGroup != nil {
			for i := range resources {
				if resources[i].ID == c.ResourceID {
					resources[i].CurrentGroup = *c.CurrentSetupGroup
				}
			}
		}
		booked, err := loadBooked(ctx, tx, start)
		if err != nil {
			return err
		}
		// The orders' other operations as they sit on the schedule now: the
		// campaign job keeps their precedence and queue times.
		current, err := placementsOfOrders(ctx, tx, orderIDs)
		if err != nil {
			return err
		}
		firmed, err := firmedJobs(ctx, tx, orderIDs)
		if err != nil {
			return err
		}
		ops := make([]domain.Operation, 0, len(chosen))
		for i, op := range chosen {
			o := c.Orders[i]
			if o.RunMinutes > 0 {
				op.Minutes = o.RunMinutes
			}
			op.SetupGroup, op.SetupMinutes = o.SetupGroup, o.RouteSetupMinutes
			if o.DueAt != nil {
				op.DueAt = *o.DueAt
			}
			// The network step into the operation is checked against its
			// scheduled predecessor once the campaign is laid (pushLater).
			op.Network = ""
			ops = append(ops, op)
		}
		res, conf := domain.PlaceCampaign(domain.CampaignPlaceInput{ResourceID: c.ResourceID, Resources: resources, Booked: booked, Start: start,
			HorizonDays: cmd.HorizonDays, Operations: ops, Cleanouts: c.Plan.Cleanouts, Chains: chains, Current: current, Firmed: firmed, Networks: networks})
		if conf != nil {
			status := http.StatusUnprocessableEntity
			switch conf.Code {
			case domain.CodeOverlap, domain.CodeCampaignWindow, domain.CodeFirmed:
				status = http.StatusConflict
			}
			return httpx.NewProblem(status, conf.Code, conf.Detail)
		}
		// The orders' later operations that moved behind their campaign job
		// are rewritten in the same run.
		pushed := make([]string, 0, len(res.Pushed))
		for _, pl := range res.Pushed {
			pushed = append(pushed, pl.OperationID)
		}
		if err := trackOps(ctx, tx, pushed); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from scheduled_job where job_id = any($1::uuid[]) and not firmed`, pushed); err != nil {
			return err
		}
		if err := dropOrphanSecondaries(ctx, tx, pushed); err != nil {
			return err
		}
		written := append(slices.Clone(res.Placements), res.Pushed...)
		run := Run{ID: ids.New(), Scope: "campaign", Mode: "jobs", Direction: "forward", Finite: true, Algorithm: "heuristic", Status: "completed",
			OperationsCount: len(written), MakespanMinutes: domain.Makespan(written), TotalLatenessMinutes: domain.TotalLateness(written),
			Messages: []string{}, Warnings: runWarnings(res.Warnings)}
		if len(pushed) > 0 {
			run.Messages = append(run.Messages, fmt.Sprintf("%d later operations of the campaign orders moved forward behind their campaign job", len(pushed)))
		}
		if err := insertRun(ctx, tx, &run, Idem{}); err != nil {
			return err
		}
		if err := writePlacements(ctx, tx, run.ID, written, finite); err != nil {
			return err
		}
		placed := make([]string, 0, len(res.Placements))
		for _, pl := range res.Placements {
			placed = append(placed, pl.OperationID)
		}
		if err := setStatus(ctx, tx, placed, "scheduled"); err != nil {
			return err
		}
		for i, op := range chosen {
			if _, err := tx.Exec(ctx, `update scheduling_campaign_order set operation_id = $3::uuid where campaign_id = $1 and order_id = $2`, c.ID, c.Orders[i].OrderID, op.ID); err != nil {
				return err
			}
		}
		for _, w := range res.Cleanouts {
			if w.Minutes <= 0 {
				continue
			}
			if _, err := tx.Exec(ctx, `insert into scheduling_campaign_cleanout (tenant_id, campaign_id, after_order_id, resource_id, start_at, end_at, minutes,
				setup_group, reason, cleaning_operation_set_id) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
				tenantOf(ctx), c.ID, w.AfterOrderID, c.ResourceID, w.Start, w.End, w.Minutes, w.SetupGroup, w.Reason, nullable(w.CleaningSetID)); err != nil {
				return err
			}
		}
		trackResources(ctx, c.ResourceID)
		var version int
		if err := tx.QueryRow(ctx, `update scheduling_campaign set placed_at = now(), placed_run_id = $2, planned_end = $3, version = version + 1
			where id = $1 returning version`, c.ID, run.ID, res.End).Scan(&version); err != nil {
			return err
		}
		if err := enqueuePlacements(ctx, tx, run.ID, written, false); err != nil {
			return err
		}
		if err := enqueueOrderDates(ctx, tx, orderIDs, prior); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "run", run.ID, schedevents.RunCompleted, schedevents.RunCompletedData{RunID: run.ID, Scope: run.Scope,
			Operations: run.OperationsCount, MakespanMinutes: run.MakespanMinutes, TotalLatenessMinutes: run.TotalLatenessMinutes}); err != nil {
			return err
		}
		data := schedevents.CampaignPlacedData{CampaignID: c.ID, ResourceID: c.ResourceID, RunID: run.ID, Start: rfc(res.Start), End: rfc(res.End),
			Jobs: []schedevents.CampaignJobRef{}, Cleanouts: []schedevents.CampaignCleanoutWindow{}, Version: version}
		for _, pl := range res.Placements {
			data.Jobs = append(data.Jobs, schedevents.CampaignJobRef{OrderID: pl.OrderID, JobID: pl.OperationID, Start: rfc(pl.Start), End: rfc(pl.End),
				ChangeoverMinutes: pl.ChangeoverMinutes})
		}
		for _, w := range res.Cleanouts {
			data.Cleanouts = append(data.Cleanouts, schedevents.CampaignCleanoutWindow{AfterOrderID: w.AfterOrderID, SetupGroup: w.SetupGroup, Minutes: w.Minutes,
				Reason: w.Reason, CleaningSetID: w.CleaningSetID, Start: rfc(w.Start), End: rfc(w.End)})
		}
		return enqueue(ctx, tx, "campaign", c.ID, schedevents.CampaignPlaced, data)
	})
}

// UnplaceCampaign takes a placed campaign off the schedule: the jobs of its
// orders are removed and open again, the cleanout windows are open capacity
// again, and each order is announced as unscheduled so production clears its
// dates. A firmed job of the campaign refuses it (SCHED-0138).
func (s *Postgres) UnplaceCampaign(ctx context.Context, cmd CampaignCommand, idem Idem) (Campaign, bool, error) {
	return s.campaignCommandTracked(ctx, cmd, idem, func(ctx context.Context, tx pgx.Tx, c Campaign) error {
		if err := frozen(c.Status); err != nil {
			return err
		}
		if c.PlacedAt == nil {
			return httpx.NewProblem(http.StatusConflict, CodeCampaignNotPlacedYet, "the campaign is not placed on the schedule")
		}
		opIDs, err := placedOperations(ctx, tx, c.ID)
		if err != nil {
			return err
		}
		orderIDs := make([]string, 0, len(c.Orders))
		for _, o := range c.Orders {
			orderIDs = append(orderIDs, o.OrderID)
		}
		prior, err := orderDates(ctx, tx, orderIDs)
		if err != nil {
			return err
		}
		if err := clearPlacement(ctx, tx, c.ID, opIDs); err != nil {
			return err
		}
		trackResources(ctx, c.ResourceID)
		if _, err := tx.Exec(ctx, `update scheduling_campaign_order set operation_id = null where campaign_id = $1`, c.ID); err != nil {
			return err
		}
		var version int
		if err := tx.QueryRow(ctx, `update scheduling_campaign set placed_at = null, placed_run_id = null, planned_end = null, version = version + 1
			where id = $1 returning version`, c.ID).Scan(&version); err != nil {
			return err
		}
		// An order that keeps other operations on the schedule has new dates;
		// one with nothing left is announced by order.unscheduled alone.
		if err := enqueueOrderDates(ctx, tx, orderIDs, prior); err != nil {
			return err
		}
		for _, o := range c.Orders {
			nums := []int{}
			if n := operNumberOf(c, o.OrderID); n > 0 {
				nums = append(nums, n)
			}
			if err := enqueue(ctx, tx, "order", o.OrderID, schedevents.OrderUnscheduled, schedevents.OrderUnscheduledData{
				OrderID: o.OrderID, Operations: nums, KeptFirmed: []int{}}); err != nil {
				return err
			}
		}
		return enqueue(ctx, tx, "campaign", c.ID, schedevents.CampaignUnplaced, schedevents.CampaignUnplacedData{CampaignID: c.ID,
			ResourceID: c.ResourceID, OrderIDs: orderIDs, Version: version})
	})
}

// operNumberOf is the number of the operation a placed campaign booked for an
// order, from its placement.
func operNumberOf(c Campaign, orderID string) int {
	if c.Placement != nil {
		for _, j := range c.Placement.Jobs {
			if j.OrderID == orderID {
				return j.OperNumber
			}
		}
	}
	return 0
}
