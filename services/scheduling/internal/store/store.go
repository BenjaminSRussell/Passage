// Package store persists scheduling state in Postgres. Every query runs inside
// a tenant-scoped transaction (dbx.WithTenantTx, so row-level security applies)
// and every write commits its outbox events in that same transaction.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	schedevents "github.com/BenjaminSRussell/passage/services/scheduling/internal/events"
)

const (
	// MaxBars is the Gantt window limit (PRD 4.23.5).
	MaxBars = 5000
	// MaxCapacityDays bounds the open-interval inquiry window.
	MaxCapacityDays = 62

	CodeResourceDuplicate  = "SCHED-0101"
	CodeResourceNotFound   = "SCHED-0102"
	CodeOperationDuplicate = "SCHED-0103"
	CodeRunNotFound        = "SCHED-0104"
	CodeJobNotFound        = "SCHED-0105"
	CodeWindowTooLarge     = "SCHED-0106"
	CodeUnknownResource    = "SCHED-0107"
	// CodeEngineeringMapped is returned when another resource already stands
	// for the engineering resource.
	CodeEngineeringMapped = "SCHED-0132"
	// CodeOrderHasNoOperations is returned by the per-order schedule command
	// for an order id with no operations in scheduling.
	CodeOrderHasNoOperations = "SCHED-0135"
	CodeConcurrentBooking    = "SCHED-0001"
	CodeIdempotencyReuse     = "idempotency_key_reuse"

	uniqueViolation = "23505"
	// engineeringIndex keeps one scheduling resource per engineering resource
	// in a tenant (migration 0014).
	engineeringIndex   = "scheduling_resource_engineering_id"
	exclusionViolation = "23P01"
	eventSource        = "scheduling"
	scopeMove          = "move"
)

// Idem carries an Idempotency-Key and a fingerprint of the request body. A
// replay with a different fingerprint is refused (422).
type Idem struct{ Key, Fingerprint string }

type Resource struct {
	ID       string         `json:"id"`
	Code     string         `json:"code"`
	Name     string         `json:"name"`
	Finite   bool           `json:"finite"`
	TimeZone string         `json:"timeZone"`
	Shifts   []domain.Shift `json:"shifts"`
	// EfficiencyPct is the resource's efficiency in whole percent (BR-23.1):
	// a job of N process minutes occupies ceil(N x 100 / EfficiencyPct)
	// minutes on it, and the route setup time is stretched the same way. 100
	// means the planned minutes are the booked minutes.
	EfficiencyPct int       `json:"efficiencyPct"`
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"createdAt"`
	// EngineeringResourceID is the id of the engineering (wave 09) resource
	// this resource stands for; production orders name that id on their jobs
	// (FR-23.10). Nil when the resource is not mapped.
	EngineeringResourceID *string `json:"engineeringResourceId"`
}

// Operation is an operation of a production order waiting for, or holding, a
// place in the schedule. SetupGroup and SetupMinutes drive changeovers
// (FR-23.15), Attributes are matched against capacity blocks (FR-23.17) and
// NetworkID names the resource network governing the step from the order's
// previous operation into this one (FR-23.13).
type Operation struct {
	ID          string     `json:"id"`
	OrderID     string     `json:"orderId"`
	OperNumber  int        `json:"operNumber"`
	Minutes     int        `json:"minutes"`
	DueAt       *time.Time `json:"dueAt,omitempty"`
	ReadyAt     *time.Time `json:"readyAt,omitempty"`
	Priority    int        `json:"priority"`
	ResourceIDs []string   `json:"resourceIds"`
	// SecondaryResourceIDs are booked with the primary for the whole job
	// (BR-23.4).
	SecondaryResourceIDs []string `json:"secondaryResourceIds"`
	SetupGroup           *string  `json:"setupGroup"`
	SetupMinutes         int      `json:"setupMinutes"`
	// QueueBeforeMinutes and QueueAfterMinutes are the elapsed waits in front
	// of and behind the operation (FR-23.3); no resource is booked for them.
	QueueBeforeMinutes int `json:"queueBeforeMinutes"`
	QueueAfterMinutes  int `json:"queueAfterMinutes"`
	// Quantity is the order quantity the operation works on and
	// OverlapQuantity the part of it that must be through before the next
	// operation may start (BR-23.5); decimal strings, both optional.
	Quantity        *string           `json:"quantity"`
	OverlapQuantity *string           `json:"overlapQuantity"`
	Attributes      map[string]string `json:"attributes"`
	NetworkID       *string           `json:"networkId"`
	Status          string            `json:"status"`
	CreatedAt       time.Time         `json:"createdAt"`
	// Version starts at 1 and rises with every change; PATCH and DELETE need it
	// in If-Match.
	Version   int        `json:"version"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// RunWarning is a finding about a placed operation that did not stop the run
// (SCHED-0003, SCHED-0010, SCHED-0012, SCHED-0014).
type RunWarning struct {
	Code        string `json:"code"`
	OperationID string `json:"operationId"`
	Detail      string `json:"detail"`
}

type Run struct {
	ID                   string       `json:"id"`
	Scope                string       `json:"scope"`
	Mode                 string       `json:"mode"`
	Direction            string       `json:"direction"`
	Finite               bool         `json:"finite"`
	Algorithm            string       `json:"algorithm"`
	Status               string       `json:"status"`
	OperationsCount      int          `json:"operationsCount"`
	MakespanMinutes      int          `json:"makespanMinutes"`
	TotalLatenessMinutes int          `json:"totalLatenessMinutes"`
	SolverStatus         *string      `json:"solverStatus"`
	Messages             []string     `json:"messages"`
	Warnings             []RunWarning `json:"warnings"`
	CreatedAt            time.Time    `json:"createdAt"`
	// Snapshot is true when the jobs the run placed were kept (slice 29), so
	// the run can be compared and rolled back to. Move runs and runs from
	// before slice 29 have none.
	Snapshot bool `json:"snapshot"`
	// RollbackOf is the run a rollback restored; nil for other runs.
	RollbackOf *string `json:"rollbackOf"`
}

// Bar is one segment of a scheduled job, as drawn on the Gantt. The first
// segment carries the job's changeover minutes, which open the bar.
type Bar struct {
	JobID      string `json:"jobId"`
	OrderID    string `json:"orderId"`
	OperNumber int    `json:"operNumber"`
	// OrderRef and OperationRef are the keys shop floor execution uses: the
	// production order id and the operation number as text.
	OrderRef          string    `json:"orderRef"`
	OperationRef      string    `json:"operationRef"`
	ResourceID        string    `json:"resourceId"`
	Segment           int       `json:"segment"`
	Start             time.Time `json:"start"`
	End               time.Time `json:"end"`
	LateMinutes       int       `json:"lateMinutes"`
	Firmed            bool      `json:"firmed"`
	SetupGroup        *string   `json:"setupGroup"`
	ChangeoverMinutes int       `json:"changeoverMinutes"`
	// ChangeoverCost is the matrix entry's cost of the changeover in
	// ChangeoverCurrency, and CleaningSetID the cleaning standard operation set
	// it requires (FR-23.15). Null when no entry priced the job, and on later
	// segments.
	ChangeoverCost     *decimal.Decimal `json:"changeoverCost"`
	ChangeoverCurrency *string          `json:"changeoverCurrency"`
	CleaningSetID      *string          `json:"cleaningSetId"`
	// OutsideCapacity is true when part of the segment lies outside the
	// resource's open capacity as it is now, for example inside a downtime
	// added after the job was booked (SCHED-0002, AC-23.7).
	OutsideCapacity bool `json:"outsideCapacity"`
	// BlockBreach is true when the segment lies in open capacity but in time
	// the resource's active capacity blocks do not leave a job with these
	// attributes (BR-23.15), for example after a block was added or its
	// condition changed. The job stays where it is until the next run or move.
	BlockBreach bool `json:"blockBreach"`
	// attributes (the operation's, as JSON) and finite (the row reserves
	// capacity, so it is not from an infinite run) feed BlockBreach.
	attributes string
	finite     bool
	// SecondaryResourceIDs are the resources booked with the job's primary for
	// the same segments (BR-23.4).
	SecondaryResourceIDs []string `json:"secondaryResourceIds"`
	// Secondary is true for the bar of a secondary booking (BR-23.4): the job
	// holds ResourceID as a secondary resource for these segments and
	// PrimaryResourceID is the resource it runs on. Such a bar is read-only
	// on the Gantt (the job moves through its primary bar); it carries no
	// changeover, no secondary list and no block breach.
	Secondary         bool    `json:"secondary"`
	PrimaryResourceID *string `json:"primaryResourceId"`
}

type RunRequest struct {
	Scope        string
	Mode         string
	Finite       bool
	OperationIDs []string
	// OrderID is the order of a scope order run: every operation of it.
	OrderID     string
	Start       time.Time
	HorizonDays int
	// Backward schedules each order from its due date (FR-23.2, BR-23.2).
	Backward bool
}

type MoveRequest struct {
	JobID       string
	ResourceID  string
	Start       time.Time
	HorizonDays int
	DryRun      bool
	// Propagate also re-plans the orders pegged to the moved order (FR-23.8),
	// up to the BR-23.10 limits.
	Propagate bool
	// Override commits the move over capacity violations of the moved job
	// (SCHED-0002, SCHED-0005; see domain.MoveInput.Override). The caller has
	// checked the override duty.
	Override bool
}

// Violation is a constraint a move was committed over.
type Violation struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	JobID  string `json:"jobId,omitempty"`
}

// Change is one operation whose placement moves.
type Change struct {
	OperationID string    `json:"operationId"`
	OrderID     string    `json:"orderId"`
	ResourceID  string    `json:"resourceId"`
	OldStart    time.Time `json:"oldStart"`
	OldEnd      time.Time `json:"oldEnd"`
	NewStart    time.Time `json:"newStart"`
	NewEnd      time.Time `json:"newEnd"`
	LateMinutes int       `json:"lateMinutes"`
}

// DependentChange is a pegged order whose dates move with a propagated move.
type DependentChange struct {
	OrderID  string    `json:"orderId"`
	OldStart time.Time `json:"oldStart"`
	OldEnd   time.Time `json:"oldEnd"`
	NewStart time.Time `json:"newStart"`
	NewEnd   time.Time `json:"newEnd"`
}

// MoveOutcome is the preview or the committed result of a move. Conflict is
// set when the move is infeasible; nothing was written in that case.
// Dependents lists the pegged orders that move with it, and Truncated says
// the dependent walk stopped at a BR-23.10 limit (SCHED-0007).
type MoveOutcome struct {
	Changes    []Change          `json:"changes"`
	OrderID    string            `json:"orderId"`
	OrderStart time.Time         `json:"orderStart"`
	OrderEnd   time.Time         `json:"orderEnd"`
	Dependents []DependentChange `json:"dependents"`
	Truncated  bool              `json:"truncated"`
	Run        *Run              `json:"run,omitempty"`
	// Overridden lists the violations the move was planned or committed over
	// (MoveRequest.Override); empty otherwise.
	Overridden []Violation      `json:"overridden"`
	Conflict   *domain.Conflict `json:"-"`
}

// Firm is the result of firming or unfirming an operation's placement.
type Firm struct {
	JobID      string    `json:"jobId"`
	OrderID    string    `json:"orderId"`
	ResourceID string    `json:"resourceId"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Firmed     bool      `json:"firmed"`
}

// LoadFilter selects resources and the week window for the load view.
type LoadFilter struct {
	ResourceIDs  []string
	From         time.Time
	Weeks        int
	ThresholdPct int
	Consecutive  int
}

// OperationFilter narrows the operation list.
type OperationFilter struct {
	OrderID string
	Status  string
	Limit   int
}

// Store is what the API layer needs.
type Store interface {
	CreateResource(ctx context.Context, in Resource, idem Idem) (Resource, bool, error)
	ListResources(ctx context.Context) ([]Resource, error)
	GetResource(ctx context.Context, id string) (Resource, error)
	ResourceCapacity(ctx context.Context, id string, from, to time.Time) ([]domain.Interval, error)
	CreateOperation(ctx context.Context, in Operation, idem Idem) (Operation, bool, error)
	ListOperations(ctx context.Context, f OperationFilter) ([]Operation, error)
	OperationStore
	RunSchedule(ctx context.Context, req RunRequest, idem Idem) (Run, bool, error)
	GetRun(ctx context.Context, id string) (Run, error)
	ListRuns(ctx context.Context, limit int) ([]Run, error)
	Gantt(ctx context.Context, from, to time.Time, resourceIDs []string) ([]Bar, error)
	MoveJob(ctx context.Context, req MoveRequest, idem Idem) (MoveOutcome, bool, error)
	FirmJob(ctx context.Context, jobID string, firmed bool, idem Idem) (Firm, bool, error)
	CapacityLoad(ctx context.Context, f LoadFilter) ([]domain.WeekLoad, error)
	LoadStore
	BottleneckStore
	DispatchStore
	RunKPIStore
	NetworkFeasibilityStore
	RunSnapshotStore
	SetupStore
	PegStore
	OverrideStore
	ResourceUpdateStore
	ParametersStore
	SetupUpdateStore
	CampaignStore
	OrderImportStore
	Ping(ctx context.Context) error
}

// Postgres implements Store on a pgx pool.
type Postgres struct {
	pool           *pgxpool.Pool
	bnPct, bnWeeks int
	clock          func() time.Time
}

// New returns a Postgres store.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks database reachability for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func tenantOf(ctx context.Context) string {
	p, _ := tenant.FromContext(ctx)
	return p.TenantID
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func reuseProblem() error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
}

// mapWriteErr turns constraint violations into stable problem codes. Two
// resources mapped to one engineering resource at the same moment both pass
// the check in checkEngineeringFree; the unique index refuses the second, and
// it gets the same answer as the check gives.
func mapWriteErr(err error, dupCode, dupDetail string) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case uniqueViolation:
			if pg.ConstraintName == engineeringIndex {
				return httpx.NewProblem(http.StatusConflict, CodeEngineeringMapped, "another resource already stands for that engineering resource")
			}
			return httpx.NewProblem(http.StatusConflict, dupCode, dupDetail)
		case exclusionViolation:
			return httpx.NewProblem(http.StatusConflict, CodeConcurrentBooking, "a concurrent change booked the same resource; reload and try again")
		}
	}
	return err
}

func newEvent(ctx context.Context, eventType, subject string, data any) (events.Event, error) {
	return events.New(eventType, eventSource, subject, tenantOf(ctx), data)
}

func enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType string, data any) error {
	evt, err := newEvent(ctx, eventType, aggregateID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// RunSchedule runs the heuristic over the selection (or every operation for
// scope site), replacing the non-firmed jobs of those operations. Firmed jobs
// and the jobs of unselected operations in the same orders stay put and hold
// back their successors. The run, its jobs, statuses and events commit together.
func (s *Postgres) RunSchedule(ctx context.Context, req RunRequest, idem Idem) (Run, bool, error) {
	var out Run
	created := false
	err := s.loadTx(&ctx, func(tx pgx.Tx) error {
		if idem.Key != "" {
			var stored string
			existing, err := scanRun(tx.QueryRow(ctx, `select idempotency_fingerprint, `+runCols+` from schedule_run where idempotency_key = $1`, idem.Key), &stored)
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
		resources, finite, err := loadResources(ctx, tx)
		if err != nil {
			return err
		}
		selection := req.OperationIDs
		if selection == nil {
			selection = []string{}
		}
		if req.Scope == "order" {
			rows, err := tx.Query(ctx, `select id::text from scheduling_operation where order_id = $1 order by oper_number`, req.OrderID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				selection = append(selection, id)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			if len(selection) == 0 {
				return httpx.NewProblem(http.StatusNotFound, CodeOrderHasNoOperations, "no operations are known for that order")
			}
		}
		ops, err := loadOperations(ctx, tx, "$1::boolean or o.id = any($2::uuid[])", true, req.Scope == "site", selection)
		if err != nil {
			return err
		}
		opIDs, orderIDs := idsOf(ops)
		prior, err := orderDates(ctx, tx, orderIDs)
		if err != nil {
			return err
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
		kept, err := placementsOfOrders(ctx, tx, orderIDs)
		if err != nil {
			return err
		}
		fixed := make([]domain.Placement, 0, len(kept))
		todo := make([]domain.Operation, 0, len(ops))
		for _, pl := range kept {
			fixed = append(fixed, pl)
		}
		for _, op := range ops {
			if _, firmed := kept[op.ID]; !firmed {
				todo = append(todo, op)
			}
		}
		booked, err := loadBooked(ctx, tx, req.Start)
		if err != nil {
			return err
		}
		networks, err := loadConstraints(ctx, tx, resources, req.Start, req.HorizonDays, req.Start)
		if err != nil {
			return err
		}
		pegs, parentEnds, unplacedParents, err := pegsForRun(ctx, tx, orderIDs)
		if err != nil {
			return err
		}
		res := domain.Schedule(domain.Problem{Resources: resources, Operations: todo, Booked: booked, Fixed: fixed,
			Networks: networks, Start: req.Start, HorizonDays: req.HorizonDays, Infinite: !req.Finite, Backward: req.Backward, DailyBuckets: req.Mode == "operations",
			Pegs: pegs, ParentEnds: parentEnds, UnplacedParents: unplacedParents})
		direction := "forward"
		if req.Backward {
			direction = "backward"
		}
		run := Run{ID: ids.New(), Scope: req.Scope, Mode: req.Mode, Direction: direction, Finite: req.Finite, Algorithm: "heuristic",
			Status: "completed", OperationsCount: len(todo), MakespanMinutes: domain.Makespan(res.Placements),
			TotalLatenessMinutes: domain.TotalLateness(res.Placements), Messages: unplacedMessages(res.Unplaced),
			Warnings: runWarnings(res.Warnings), Snapshot: true}
		if err := insertRun(ctx, tx, &run, idem); err != nil {
			return err
		}
		if err := writeSnapshot(ctx, tx, run.ID, res.Placements); err != nil {
			return err
		}
		rowFinite := finite
		if !req.Finite {
			rowFinite = map[string]bool{}
		}
		if err := writePlacements(ctx, tx, run.ID, res.Placements, rowFinite); err != nil {
			return err
		}
		placed := make([]string, 0, len(res.Placements))
		for _, pl := range res.Placements {
			placed = append(placed, pl.OperationID)
		}
		unplaced := make([]string, 0, len(res.Unplaced))
		for _, u := range res.Unplaced {
			unplaced = append(unplaced, u.OperationID)
		}
		if err := setStatus(ctx, tx, placed, "scheduled"); err != nil {
			return err
		}
		if err := setStatus(ctx, tx, unplaced, "unplaced"); err != nil {
			return err
		}
		if err := enqueuePlacements(ctx, tx, run.ID, res.Placements, false); err != nil {
			return err
		}
		if err := enqueueOrderDates(ctx, tx, orderIDs, prior); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "run", run.ID, schedevents.RunCompleted, schedevents.RunCompletedData{RunID: run.ID, Scope: run.Scope,
			Operations: run.OperationsCount, MakespanMinutes: run.MakespanMinutes, TotalLatenessMinutes: run.TotalLatenessMinutes}); err != nil {
			return err
		}
		out, created = run, true
		return nil
	})
	if err != nil {
		return Run{}, false, mapWriteErr(err, CodeConcurrentBooking, "a concurrent run or move changed the same jobs; try again")
	}
	return out, created, nil
}

// MoveJob plans or commits a move of one scheduled job (FR-23.8). The order's
// operations are locked so concurrent moves of one order serialize; the
// exclusion constraint stops a concurrent booking on another order. A commit
// writes a move-scoped run so every change is traceable, and replays of the
// same Idempotency-Key return the stored outcome (replayed = true).
func (s *Postgres) MoveJob(ctx context.Context, req MoveRequest, idem Idem) (MoveOutcome, bool, error) {
	var out MoveOutcome
	replayed := false
	err := s.loadTx(&ctx, func(tx pgx.Tx) error {
		if !req.DryRun && idem.Key != "" {
			found, err := replay(ctx, tx, idem, &out)
			if err != nil || found {
				replayed = found
				return err
			}
		}
		// The move honours the mode of the run that placed the job (FR-23.2)
		// for the whole move, and its own run records it, so a chain of moves
		// keeps the mode. Every segment of a job carries the same run.
		var orderID, mode string
		err := tx.QueryRow(ctx, `select j.order_id::text, r.mode from scheduled_job j
			join schedule_run r on r.tenant_id = j.tenant_id and r.id = j.run_id where j.job_id = $1 limit 1`, req.JobID).Scan(&orderID, &mode)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeJobNotFound, "scheduled job not found")
		}
		if err != nil {
			return err
		}
		ops, err := loadOperations(ctx, tx, "o.order_id = $1::uuid", true, orderID)
		if err != nil {
			return err
		}
		var walk []string
		var limit domain.PropagationLimit
		var pegs []domain.Peg
		var dependents []domain.Operation
		if req.Propagate {
			all, err := loadPegs(ctx, tx)
			if err != nil {
				return err
			}
			params, err := loadParameters(ctx, tx, false)
			if err != nil {
				return err
			}
			limit = params.Domain().Limit()
			walk, out.Truncated = domain.DependentsOf(orderID, all, limit)
			pegs = pegsAmong(all, append([]string{orderID}, walk...))
			if len(walk) > 0 {
				if dependents, err = loadOperations(ctx, tx, "o.order_id = any($1::uuid[])", true, walk); err != nil {
					return err
				}
			}
		}
		orders := append([]string{orderID}, walk...)
		resources, finite, err := loadResources(ctx, tx)
		if err != nil {
			return err
		}
		placements, err := placementsOfOrders(ctx, tx, orders)
		if err != nil {
			return err
		}
		firmed, err := firmedJobs(ctx, tx, orders)
		if err != nil {
			return err
		}
		horizonStart := req.Start
		for _, pl := range placements {
			if pl.Start.Before(horizonStart) {
				horizonStart = pl.Start
			}
		}
		horizonStart = horizonStart.Add(-24 * time.Hour)
		booked, err := loadBooked(ctx, tx, horizonStart)
		if err != nil {
			return err
		}
		networks, err := loadConstraints(ctx, tx, resources, horizonStart, req.HorizonDays, req.Start)
		if err != nil {
			return err
		}
		rid := req.ResourceID
		if rid == "" {
			rid = placements[req.JobID].ResourceID
		}
		plan := domain.PlanMove(domain.MoveInput{Resources: resources, Start: horizonStart, HorizonDays: req.HorizonDays,
			Booked: booked, Order: ops, Current: placements, Moved: req.JobID, ResourceID: rid, At: req.Start, Firmed: firmed, Networks: networks,
			Dependents: dependents, Pegs: pegs, Override: req.Override, DailyBuckets: mode == "operations"})
		if plan.Conflict != nil {
			out.Conflict = plan.Conflict
			return nil
		}
		out.OrderID = orderID
		out.Overridden = make([]Violation, 0, len(plan.Overridden))
		for _, c := range plan.Overridden {
			out.Overridden = append(out.Overridden, Violation{Code: c.Code, Detail: c.Detail, JobID: c.JobID})
		}
		out.Changes = make([]Change, 0, len(plan.Changes))
		merged := maps.Clone(placements)
		for _, c := range plan.Changes {
			merged[c.OperationID] = c
			old := placements[c.OperationID]
			out.Changes = append(out.Changes, Change{OperationID: c.OperationID, OrderID: c.OrderID, ResourceID: c.ResourceID,
				OldStart: old.Start, OldEnd: old.End, NewStart: c.Start, NewEnd: c.End, LateMinutes: c.LateMinutes})
		}
		before, after := spans(placements), spans(merged)
		out.OrderStart, out.OrderEnd = after[orderID].Start, after[orderID].End
		out.Dependents = make([]DependentChange, 0, len(plan.Dependents))
		for _, oid := range plan.Dependents {
			out.Dependents = append(out.Dependents, DependentChange{OrderID: oid, OldStart: before[oid].Start, OldEnd: before[oid].End,
				NewStart: after[oid].Start, NewEnd: after[oid].End})
		}
		if req.DryRun {
			return nil
		}
		affected := append([]string{orderID}, plan.Dependents...)
		prior, err := orderDates(ctx, tx, affected)
		if err != nil {
			return err
		}
		run := Run{ID: ids.New(), Scope: scopeMove, Mode: mode, Direction: "forward", Finite: true, Algorithm: "heuristic",
			Status: "completed", OperationsCount: len(plan.Changes), MakespanMinutes: domain.Makespan(plan.Changes),
			TotalLatenessMinutes: domain.TotalLateness(plan.Changes), Messages: []string{}}
		for _, v := range out.Overridden {
			run.Messages = append(run.Messages, fmt.Sprintf("%s overridden for job %s: %s", v.Code, req.JobID, v.Detail))
		}
		if out.Truncated {
			run.Messages = append(run.Messages, fmt.Sprintf("%s: propagation truncated after %d orders / %d levels; run the site schedule",
				domain.CodePropagationTruncated, len(walk), limit.Levels))
		}
		if err := insertRun(ctx, tx, &run, Idem{}); err != nil {
			return err
		}
		changed := make([]string, 0, len(plan.Changes))
		for _, c := range plan.Changes {
			changed = append(changed, c.OperationID)
		}
		if err := trackOps(ctx, tx, changed); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from scheduled_job where job_id = any($1::uuid[])`, changed); err != nil {
			return err
		}
		if err := dropOrphanSecondaries(ctx, tx, changed); err != nil {
			return err
		}
		if err := writePlacements(ctx, tx, run.ID, plan.Changes, finite); err != nil {
			return err
		}
		if err := enqueuePlacements(ctx, tx, run.ID, plan.Changes, false); err != nil {
			return err
		}
		if err := enqueueOrderDates(ctx, tx, affected, prior); err != nil {
			return err
		}
		out.Run = &run
		if idem.Key != "" {
			return remember(ctx, tx, idem, http.StatusOK, out)
		}
		return nil
	})
	if err != nil {
		return MoveOutcome{}, false, mapWriteErr(err, CodeConcurrentBooking, "a concurrent change moved the same jobs; reload and try again")
	}
	return out, replayed, nil
}

// FirmJob sets the firmed flag on every segment of one operation. A firmed
// placement is frozen against later runs and moves (FR-23.4). The change is
// announced as job.scheduled in the same transaction.
func (s *Postgres) FirmJob(ctx context.Context, jobID string, firmed bool, idem Idem) (Firm, bool, error) {
	var out Firm
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if idem.Key != "" {
			found, err := replay(ctx, tx, idem, &out)
			if err != nil || found {
				replayed = found
				return err
			}
		}
		var runID string
		err := tx.QueryRow(ctx, `select order_id::text, run_id::text from scheduled_job where job_id = $1::uuid limit 1 for update`, jobID).
			Scan(&out.OrderID, &runID)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeJobNotFound, "scheduled job not found")
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update scheduled_job set firmed = $2 where job_id = $1::uuid`, jobID, firmed); err != nil {
			return err
		}
		pls, err := placementsOfOrders(ctx, tx, []string{out.OrderID})
		if err != nil {
			return err
		}
		pl := pls[jobID]
		out.JobID, out.ResourceID, out.Start, out.End, out.Firmed = jobID, pl.ResourceID, pl.Start, pl.End, firmed
		if err := enqueuePlacements(ctx, tx, runID, []domain.Placement{pl}, firmed); err != nil {
			return err
		}
		if idem.Key != "" {
			return remember(ctx, tx, idem, http.StatusOK, out)
		}
		return nil
	})
	if err != nil {
		return Firm{}, false, mapWriteErr(err, CodeConcurrentBooking, "a concurrent request used the same Idempotency-Key; try again")
	}
	return out, replayed, nil
}

// CapacityLoad returns weekly load for the selected resources (FR-23.9).
// Booked minutes count every job, including infinite-run jobs, so overload
// shows as load above 100%.
func (s *Postgres) CapacityLoad(ctx context.Context, f LoadFilter) ([]domain.WeekLoad, error) {
	out := []domain.WeekLoad{}
	from := domain.WeekStart(f.From)
	to := from.AddDate(0, 0, 7*f.Weeks)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		resources, byResource, err := loadResourcesBooked(ctx, tx, f.ResourceIDs, from, to)
		if err != nil {
			return err
		}
		for _, r := range resources {
			out = append(out, domain.WeeklyLoad(r, domain.LoadIntervals(r, from, f.Weeks), byResource[r.ID], from, f.Weeks, f.ThresholdPct, f.Consecutive)...)
		}
		return nil
	})
	return out, err
}

// loadResourcesBooked reads the selected resources (all when none are named) and the
// primary and secondary bookings that overlap [from, to), grouped by resource.
func loadResourcesBooked(ctx context.Context, tx pgx.Tx, rids []string, from, to time.Time) ([]domain.Resource, map[string][]domain.Busy, error) {
	if rids == nil {
		rids = []string{}
	}
	resources, _, err := loadResources(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	rows, err := tx.Query(ctx, `select resource_id::text, job_id::text, start_at, end_at from scheduled_job
		where end_at > $1 and start_at < $2 and ($3::boolean or resource_id = any($4::uuid[]))
		union all
		select resource_id::text, job_id::text, start_at, end_at from scheduled_job_secondary
		where end_at > $1 and start_at < $2 and ($3::boolean or resource_id = any($4::uuid[]))`, from, to, len(rids) == 0, rids)
	if err != nil {
		return nil, nil, err
	}
	booked, err := scanBusy(rows)
	if err != nil {
		return nil, nil, err
	}
	byResource := map[string][]domain.Busy{}
	for _, b := range booked {
		byResource[b.ResourceID] = append(byResource[b.ResourceID], b)
	}
	wanted := map[string]bool{}
	for _, id := range rids {
		wanted[id] = true
	}
	kept := resources[:0:0]
	for _, r := range resources {
		if len(wanted) == 0 || wanted[r.ID] {
			kept = append(kept, r)
		}
	}
	return kept, byResource, nil
}

// Gantt returns the segments overlapping [from, to) on the given resources
// (all resources when none are given). A window over MaxBars is refused.
func (s *Postgres) Gantt(ctx context.Context, from, to time.Time, resourceIDs []string) ([]Bar, error) {
	if resourceIDs == nil {
		resourceIDs = []string{}
	}
	bars := []Bar{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select j.job_id::text, j.order_id::text, j.oper_number, j.resource_id::text, j.segment_no, j.start_at, j.end_at,
				j.late_minutes, j.firmed, o.setup_group, j.changeover_minutes, j.changeover_cost::text, j.changeover_currency, j.cleaning_operation_set_id,
				o.secondary_resource_ids::text[], o.attributes::text, j.finite
			from scheduled_job j join scheduling_operation o on o.tenant_id = j.tenant_id and o.id = j.job_id
			where j.end_at > $1 and j.start_at < $2 and ($3::boolean or j.resource_id = any($4::uuid[]))
			order by j.start_at, j.resource_id, j.segment_no limit $5`,
			from, to, len(resourceIDs) == 0, resourceIDs, MaxBars+1)
		if err != nil {
			return err
		}
		for rows.Next() {
			var b Bar
			var cost *string
			if err := rows.Scan(&b.JobID, &b.OrderID, &b.OperNumber, &b.ResourceID, &b.Segment, &b.Start, &b.End, &b.LateMinutes, &b.Firmed,
				&b.SetupGroup, &b.ChangeoverMinutes, &cost, &b.ChangeoverCurrency, &b.CleaningSetID, &b.SecondaryResourceIDs, &b.attributes, &b.finite); err != nil {
				rows.Close()
				return err
			}
			if cost != nil {
				v := dec(*cost)
				b.ChangeoverCost = &v
			}
			b.OrderRef, b.OperationRef = b.OrderID, domain.OperationRef(b.OperNumber)
			bars = append(bars, b)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(bars) > MaxBars {
			return nil
		}
		secondary, err := secondaryBars(ctx, tx, from, to, resourceIDs, MaxBars+1-len(bars))
		if err != nil {
			return err
		}
		if len(secondary) > 0 {
			bars = append(bars, secondary...)
			sort.SliceStable(bars, func(i, j int) bool {
				a, b := bars[i], bars[j]
				if !a.Start.Equal(b.Start) {
					return a.Start.Before(b.Start)
				}
				if a.ResourceID != b.ResourceID {
					return a.ResourceID < b.ResourceID
				}
				return a.Segment < b.Segment
			})
		}
		if len(bars) == 0 || len(bars) > MaxBars {
			return nil
		}
		return flagOutsideCapacity(ctx, tx, bars)
	})
	if err != nil {
		return nil, err
	}
	if len(bars) > MaxBars {
		return nil, httpx.NewProblem(http.StatusUnprocessableEntity, CodeWindowTooLarge, "the window holds more than 5000 bars; narrow the window or the resources")
	}
	return bars, nil
}

// secondaryBars reads the secondary bookings overlapping [from, to) on the
// given resources (all when none) as read-only bars on the secondary
// resource's row (BR-23.4). They take the job's late minutes and firmed flag
// from its primary segment with the same number.
// At most limit bars are read: the caller only needs to know the window is
// over MaxBars in total.
func secondaryBars(ctx context.Context, tx pgx.Tx, from, to time.Time, resourceIDs []string, limit int) ([]Bar, error) {
	rows, err := tx.Query(ctx, `select s.job_id::text, p.order_id::text, p.oper_number, s.resource_id::text, p.resource_id::text, s.segment_no,
			s.start_at, s.end_at, p.late_minutes, p.firmed, o.setup_group, s.finite
		from scheduled_job_secondary s
		join scheduled_job p on p.tenant_id = s.tenant_id and p.job_id = s.job_id and p.segment_no = s.segment_no
		join scheduling_operation o on o.tenant_id = s.tenant_id and o.id = s.job_id
		where s.end_at > $1 and s.start_at < $2 and ($3::boolean or s.resource_id = any($4::uuid[]))
		order by s.start_at, s.resource_id, s.segment_no limit $5`,
		from, to, len(resourceIDs) == 0, resourceIDs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bar
	for rows.Next() {
		b := Bar{Secondary: true, SecondaryResourceIDs: []string{}}
		var primary string
		if err := rows.Scan(&b.JobID, &b.OrderID, &b.OperNumber, &b.ResourceID, &primary, &b.Segment, &b.Start, &b.End,
			&b.LateMinutes, &b.Firmed, &b.SetupGroup, &b.finite); err != nil {
			return nil, err
		}
		b.PrimaryResourceID = &primary
		b.OrderRef, b.OperationRef = b.OrderID, domain.OperationRef(b.OperNumber)
		out = append(out, b)
	}
	return out, rows.Err()
}

// flagOutsideCapacity marks the bars that are no longer inside their
// resource's open capacity, or that of a secondary resource booked with them,
// calendar and overrides applied (AC-23.7).
func flagOutsideCapacity(ctx context.Context, tx pgx.Tx, bars []Bar) error {
	resources, _, err := loadResources(ctx, tx)
	if err != nil {
		return err
	}
	lo, hi := bars[0].Start, bars[0].End
	for _, b := range bars {
		if b.Start.Before(lo) {
			lo = b.Start
		}
		if b.End.After(hi) {
			hi = b.End
		}
	}
	// A day either side so the local days straddling the window are whole.
	from := lo.Add(-24 * time.Hour)
	days := int(hi.Sub(from)/(24*time.Hour)) + 2
	open := make(map[string][]domain.Interval, len(resources))
	for _, r := range resources {
		open[r.ID] = r.Open(from, days)
	}
	for i := range bars {
		b := &bars[i]
		b.OutsideCapacity = domain.OutsideOpen(open[b.ResourceID], b.Start, b.End)
		// A secondary resource is booked for the same segment, so a downtime
		// on it (or a calendar that no longer covers it) flags the bar too.
		for _, sid := range b.SecondaryResourceIDs {
			if iv, ok := open[sid]; ok && domain.OutsideOpen(iv, b.Start, b.End) {
				b.OutsideCapacity = true
			}
		}
	}
	return flagBlockBreach(ctx, tx, bars, resources, open, from, days)
}

// flagBlockBreach marks the bars of finite bookings (not infinite runs) that lie in their resource's
// open capacity but outside the capacity its active blocks leave a job with
// the operation's attributes (BR-23.15, domain.Placeable), the same test as
// blockedOutJobs. A bar that is outside open capacity is not also a breach.
func flagBlockBreach(ctx context.Context, tx pgx.Tx, bars []Bar, resources []domain.Resource,
	open map[string][]domain.Interval, from time.Time, days int) error {
	blocks, err := activeBlocks(ctx, tx)
	if err != nil || len(blocks) == 0 {
		return err
	}
	loc := make(map[string]*time.Location, len(resources))
	for _, r := range resources {
		loc[r.ID] = r.Location
	}
	expanded := map[string][]domain.CapacityBlock{}
	placeable := map[string][]domain.Interval{}
	for i := range bars {
		b := &bars[i]
		if b.Secondary || b.OutsideCapacity || !b.finite || len(blocks[b.ResourceID]) == 0 {
			continue
		}
		if _, ok := expanded[b.ResourceID]; !ok {
			for _, blk := range blocks[b.ResourceID] {
				db, err := blk.expand(loc[b.ResourceID], from, days)
				if err != nil {
					return err
				}
				expanded[b.ResourceID] = append(expanded[b.ResourceID], db)
			}
		}
		key := b.ResourceID + "|" + b.attributes
		iv, ok := placeable[key]
		if !ok {
			var attrs map[string]string
			if err := json.Unmarshal([]byte(b.attributes), &attrs); err != nil {
				return err
			}
			iv = domain.Placeable(open[b.ResourceID], expanded[b.ResourceID], attrs)
			placeable[key] = iv
		}
		b.BlockBreach = domain.OutsideOpen(iv, b.Start, b.End)
	}
	return nil
}

// writePlacements inserts one scheduled_job row per segment, pipelined in a
// single batch. finite maps resource id to whether the row reserves capacity.
// The job's changeover minutes, cost and cleaning set go on its first segment.
func writePlacements(ctx context.Context, tx pgx.Tx, runID string, ps []domain.Placement, finite map[string]bool) error {
	tenantID := tenantOf(ctx)
	trackPlacements(ctx, ps)
	batch := &pgx.Batch{}
	for _, pl := range ps {
		for i, seg := range pl.Segments {
			minutes := int(seg.End.Sub(seg.Start) / time.Minute)
			changeover := 0
			var cost, currency, cleaning *string
			if i == 0 {
				changeover = pl.ChangeoverMinutes
				if pl.ChangeoverCost != nil {
					v := pl.ChangeoverCost.Value.String()
					cost, currency = &v, &pl.ChangeoverCost.Currency
				}
				cleaning = nullable(pl.CleaningSetID)
			}
			batch.Queue(`insert into scheduled_job (tenant_id, id, job_id, order_id, oper_number, resource_id, segment_no,
				start_at, end_at, minutes, run_id, finite, late_minutes, changeover_minutes, changeover_cost, changeover_currency,
				cleaning_operation_set_id) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::numeric, $16, $17)`,
				tenantID, ids.New(), pl.OperationID, pl.OrderID, pl.OperNumber, pl.ResourceID, i+1, seg.Start, seg.End, minutes,
				runID, finite[pl.ResourceID], pl.LateMinutes, changeover, cost, currency, cleaning)
		}
	}
	for _, pl := range ps {
		for _, sid := range pl.Secondaries {
			for i, seg := range pl.Segments {
				batch.Queue(`insert into scheduled_job_secondary (tenant_id, id, job_id, resource_id, segment_no, start_at, end_at, run_id, finite)
					values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
					tenantID, ids.New(), pl.OperationID, sid, i+1, seg.Start, seg.End, runID, finite[sid])
			}
		}
	}
	return tx.SendBatch(ctx, batch).Close()
}

// dropOrphanSecondaries deletes the secondary bookings of operations that no
// longer hold a primary placement: called after their scheduled_job rows are
// deleted, so the secondaries of firmed jobs, which stay, are kept.
func dropOrphanSecondaries(ctx context.Context, tx pgx.Tx, opIDs []string) error {
	_, err := tx.Exec(ctx, `delete from scheduled_job_secondary s where s.job_id = any($1::uuid[])
		and not exists (select 1 from scheduled_job j where j.tenant_id = s.tenant_id and j.job_id = s.job_id)`, opIDs)
	return err
}

func setStatus(ctx context.Context, tx pgx.Tx, opIDs []string, status string) error {
	if len(opIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `update scheduling_operation set status = $2 where id = any($1::uuid[])`, opIDs, status)
	return err
}

// costOf and currencyOf spell a placement's changeover cost for events: the
// exact decimal and its currency, empty when no matrix entry priced it.
func costOf(pl domain.Placement) string {
	if pl.ChangeoverCost == nil {
		return ""
	}
	return pl.ChangeoverCost.Value.String()
}

func currencyOf(pl domain.Placement) string {
	if pl.ChangeoverCost == nil {
		return ""
	}
	return pl.ChangeoverCost.Currency
}

// enqueuePlacements writes one job.scheduled event per placed operation.
func enqueuePlacements(ctx context.Context, tx pgx.Tx, runID string, ps []domain.Placement, firmed bool) error {
	batch := &pgx.Batch{}
	for _, pl := range ps {
		evt, err := newEvent(ctx, schedevents.JobScheduled, pl.OperationID, schedevents.JobScheduledData{JobID: pl.OperationID,
			OrderID: pl.OrderID, OperNumber: pl.OperNumber, OrderRef: pl.OrderID, OperationRef: domain.OperationRef(pl.OperNumber),
			ResourceID: pl.ResourceID, Start: rfc(pl.Start), End: rfc(pl.End),
			ChangeoverMinutes: pl.ChangeoverMinutes, ChangeoverCost: costOf(pl), ChangeoverCurrency: currencyOf(pl),
			CleaningSetID: pl.CleaningSetID, SecondaryResourceIDs: pl.Secondaries, RunID: runID, Firmed: firmed})
		if err != nil {
			return err
		}
		payload, err := json.Marshal(evt)
		if err != nil {
			return err
		}
		batch.Queue(`insert into outbox(id, tenant_id, aggregate_type, aggregate_id, type, payload) values($1,$2,$3,$4,$5,$6)`,
			evt.ID, evt.TenantID, "job", pl.OperationID, evt.Type, payload)
	}
	return tx.SendBatch(ctx, batch).Close()
}

// enqueueOrderDates emits order dates-changed for every order whose span
// differs from prior (or that had no prior schedule).
func enqueueOrderDates(ctx context.Context, tx pgx.Tx, orderIDs []string, prior map[string]OrderDates) error {
	after, err := orderDates(ctx, tx, orderIDs)
	if err != nil {
		return err
	}
	for _, id := range orderIDs {
		a, ok := after[id]
		if !ok {
			continue
		}
		if b, had := prior[id]; had && b.Start.Equal(a.Start) && b.End.Equal(a.End) && b.Late == a.Late {
			continue
		}
		if err := enqueue(ctx, tx, "order", id, schedevents.OrderDatesChanged, schedevents.OrderDatesChangedData{OrderID: id,
			Start: rfc(a.Start), End: rfc(a.End), LateMinutes: a.Late}); err != nil {
			return err
		}
	}
	return nil
}

// replay looks up a stored outcome for idem. It returns found=true and fills
// out when the same request was already answered.
func replay(ctx context.Context, tx pgx.Tx, idem Idem, out any) (bool, error) {
	var hash string
	var body []byte
	err := tx.QueryRow(ctx, `select request_hash, response_body from idempotency_keys where key = $1`, idem.Key).Scan(&hash, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hash != idem.Fingerprint {
		return false, reuseProblem()
	}
	return true, json.Unmarshal(body, out)
}

// remember stores the outcome of a command under its Idempotency-Key in the
// command's transaction, so the record exists only if the change committed.
func remember(ctx context.Context, tx pgx.Tx, idem Idem, status int, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status, response_body) values ($1, $2, $3, $4, $5)`,
		tenantOf(ctx), idem.Key, idem.Fingerprint, status, raw)
	return err
}

func idsOf(ops []domain.Operation) (opIDs, orderIDs []string) {
	seen := map[string]bool{}
	opIDs = make([]string, 0, len(ops))
	orderIDs = []string{}
	for _, op := range ops {
		opIDs = append(opIDs, op.ID)
		if !seen[op.OrderID] {
			seen[op.OrderID] = true
			orderIDs = append(orderIDs, op.OrderID)
		}
	}
	return opIDs, orderIDs
}

func runWarnings(ws []domain.Warning) []RunWarning {
	out := make([]RunWarning, 0, len(ws))
	for _, w := range ws {
		out = append(out, RunWarning{Code: w.Code, OperationID: w.OperationID, Detail: w.Detail})
	}
	return out
}

func unplacedMessages(us []domain.Unplaced) []string {
	out := make([]string, 0, len(us))
	for _, u := range us {
		out = append(out, u.OperationID+": "+u.Reason)
	}
	return out
}
