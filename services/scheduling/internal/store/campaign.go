package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	schedevents "github.com/BenjaminSRussell/passage/services/scheduling/internal/events"
)

// Campaign error codes (README lists them all). SCHED-0011 is the PRD code
// for changing a started campaign.
const (
	CodeCampaignNotFound      = "SCHED-0113"
	CodeOrderInCampaign       = "SCHED-0114"
	CodeCampaignVersion       = "SCHED-0115"
	CodeCampaignInvalid       = "SCHED-0116"
	CodeCampaignNotPlanned    = "SCHED-0117"
	CodeCampaignOrderNotFound = "SCHED-0118"
	CodeCleanoutCounterMoved  = "SCHED-0123"
	CodeCampaignNotStarted    = domain.CodeCampaignNotStarted
)

// CampaignOrder is one production order in a campaign. Position is the order
// it was added in (the manual sequence); Sequence is set once sequenced.
type CampaignOrder struct {
	OrderID           string          `json:"orderId"`
	SetupGroup        string          `json:"setupGroup"`
	Quantity          decimal.Decimal `json:"quantity"`
	RunMinutes        int             `json:"runMinutes"`
	RouteSetupMinutes int             `json:"routeSetupMinutes"`
	DueAt             *time.Time      `json:"dueAt,omitempty"`
	Position          int             `json:"position"`
	Sequence          *int            `json:"sequence"`
}

// Message is a coded warning on a plan, such as SCHED-0012.
type Message struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// CampaignPlan is the stored result of the last sequencing. CarryIn is the
// resource's cleanout counter the plan started from and CarryOut the counter
// it leaves when the campaign starts (BR-23.13). SolverStatus and Optimized
// are set for the cp-sat rule (BR-23.9, SCHED-0006).
type CampaignPlan struct {
	Changeovers  []domain.Changeover      `json:"changeovers"`
	Cleanouts    []domain.PlannedCleanout `json:"cleanouts"`
	Warnings     []Message                `json:"warnings"`
	MatrixID     string                   `json:"matrixId,omitempty"`
	CarryIn      domain.CleanoutCounter   `json:"carryIn"`
	CarryOut     domain.CleanoutCounter   `json:"carryOut"`
	SolverStatus string                   `json:"solverStatus,omitempty"`
	Optimized    bool                     `json:"optimized"`
}

// Campaign groups orders that run contiguously on one resource (FR-23.14).
type Campaign struct {
	ID                string           `json:"id"`
	ResourceID        string           `json:"resourceId"`
	CampaignKey       string           `json:"campaignKey"`
	Status            string           `json:"status"`
	PlannedStart      time.Time        `json:"plannedStart"`
	MaxOrders         *int             `json:"maxOrders"`
	MaxQty            *decimal.Decimal `json:"maxQty"`
	MaxMinutes        *int             `json:"maxMinutes"`
	SequenceRule      string           `json:"sequenceRule"`
	CurrentSetupGroup *string          `json:"currentSetupGroup"`
	OrderCount        int              `json:"orderCount"`
	RunMinutes        int              `json:"runMinutes"`
	ChangeoverMinutes int              `json:"changeoverMinutes"`
	CleanoutMinutes   int              `json:"cleanoutMinutes"`
	Plan              *CampaignPlan    `json:"plan"`
	SequencedAt       *time.Time       `json:"sequencedAt"`
	StartedAt         *time.Time       `json:"startedAt"`
	CompletedAt       *time.Time       `json:"completedAt"`
	CancelledAt       *time.Time       `json:"cancelledAt"`
	CancelReason      *string          `json:"cancelReason"`
	Version           int              `json:"version"`
	CreatedAt         time.Time        `json:"createdAt"`
	Orders            []CampaignOrder  `json:"orders,omitempty"`
	// PlannedEnd and PlacedAt are set while the campaign is placed on its
	// resource (slice 37); Placement lists the jobs and cleanout windows.
	PlannedEnd *time.Time         `json:"plannedEnd"`
	PlacedAt   *time.Time         `json:"placedAt"`
	Placement  *CampaignPlacement `json:"placement"`
	placedRun  *string
}

// Limits returns the campaign's caps for the domain (nil means unlimited).
func (c Campaign) Limits() domain.CampaignLimits {
	var lim domain.CampaignLimits
	if c.MaxOrders != nil {
		lim.MaxOrders = *c.MaxOrders
	}
	if c.MaxQty != nil {
		lim.MaxQty = *c.MaxQty
	}
	if c.MaxMinutes != nil {
		lim.MaxMinutes = *c.MaxMinutes
	}
	return lim
}

// CampaignFilter narrows the campaign list.
type CampaignFilter struct {
	Status     string
	ResourceID string
	Limit      int
}

// CampaignCommand targets one campaign at the version the caller last saw
// (If-Match). Order is the order to add; OrderID the order to remove.
// Proposal is scheduler-opt's answer when sequencing a cp-sat campaign.
type CampaignCommand struct {
	CampaignID string
	Version    int
	Order      CampaignOrder
	OrderID    string
	Reason     string
	Proposal   *domain.SolverProposal
	// Start, OperationIDs (order id to operation id) and HorizonDays steer
	// :place; the zero values mean the planned start, the only operation of
	// each order that can run on the campaign resource, and the configured
	// horizon.
	Start        *time.Time
	OperationIDs map[string]string
	HorizonDays  int
}

// CampaignStore is the campaign part of the Store.
type CampaignStore interface {
	CreateCampaign(ctx context.Context, in Campaign, idem Idem) (Campaign, bool, error)
	ListCampaigns(ctx context.Context, f CampaignFilter) ([]Campaign, error)
	GetCampaign(ctx context.Context, id string) (Campaign, error)
	AddCampaignOrder(ctx context.Context, cmd CampaignCommand, idem Idem) (Campaign, bool, error)
	RemoveCampaignOrder(ctx context.Context, cmd CampaignCommand, idem Idem) (Campaign, bool, error)
	SequenceCampaign(ctx context.Context, cmd CampaignCommand, idem Idem) (Campaign, bool, error)
	StartCampaign(ctx context.Context, cmd CampaignCommand, idem Idem) (Campaign, bool, error)
	CompleteCampaign(ctx context.Context, cmd CampaignCommand, idem Idem) (Campaign, bool, error)
	CancelCampaign(ctx context.Context, cmd CampaignCommand, idem Idem) (Campaign, bool, error)
	PlaceCampaign(ctx context.Context, cmd CampaignCommand, idem Idem) (Campaign, bool, error)
	UnplaceCampaign(ctx context.Context, cmd CampaignCommand, idem Idem) (Campaign, bool, error)
	CampaignSetupMatrix(ctx context.Context, resourceID string, on time.Time) (domain.SetupMatrix, error)
	ProposeCampaigns(ctx context.Context, f ProposalFilter) ([]domain.ProposedCampaign, error)
}

const campaignCols = `c.id::text, c.resource_id::text, c.campaign_key, c.status, c.planned_start, c.max_orders, c.max_qty::text, c.max_minutes,
	c.sequence_rule, c.current_setup_group, (select count(*) from scheduling_campaign_order o where o.campaign_id = c.id)::int,
	c.run_minutes, c.total_changeover_minutes, c.cleanout_minutes, c.plan, c.sequenced_at, c.started_at, c.completed_at, c.cancelled_at, c.cancel_reason, c.version, c.created_at,
	c.planned_end, c.placed_at, c.placed_run_id::text`

func scanCampaign(row pgx.Row) (Campaign, error) {
	var c Campaign
	var maxQty *string
	var plan []byte
	if err := row.Scan(&c.ID, &c.ResourceID, &c.CampaignKey, &c.Status, &c.PlannedStart, &c.MaxOrders, &maxQty, &c.MaxMinutes,
		&c.SequenceRule, &c.CurrentSetupGroup, &c.OrderCount, &c.RunMinutes, &c.ChangeoverMinutes, &c.CleanoutMinutes, &plan,
		&c.SequencedAt, &c.StartedAt, &c.CompletedAt, &c.CancelledAt, &c.CancelReason, &c.Version, &c.CreatedAt,
		&c.PlannedEnd, &c.PlacedAt, &c.placedRun); err != nil {
		return Campaign{}, err
	}
	if maxQty != nil {
		q := dec(*maxQty)
		c.MaxQty = &q
	}
	if plan != nil {
		c.Plan = &CampaignPlan{}
		if err := json.Unmarshal(plan, c.Plan); err != nil {
			return Campaign{}, err
		}
	}
	return c, nil
}

func campaignNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeCampaignNotFound, "campaign not found")
}

// loadCampaign reads a campaign with its orders; lock takes a row lock for a command.
func loadCampaign(ctx context.Context, tx pgx.Tx, id string, lock bool) (Campaign, error) {
	q := `select ` + campaignCols + ` from scheduling_campaign c where c.id = $1`
	if lock {
		q += ` for update of c`
	}
	c, err := scanCampaign(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, campaignNotFound()
	}
	if err != nil {
		return Campaign{}, err
	}
	rows, err := tx.Query(ctx, `select order_id::text, setup_group, quantity::text, run_minutes, route_setup_minutes, due_at, position, sequence
		from scheduling_campaign_order where campaign_id = $1 order by coalesce(sequence, position), position`, id)
	if err != nil {
		return Campaign{}, err
	}
	defer rows.Close()
	c.Orders = []CampaignOrder{}
	for rows.Next() {
		var o CampaignOrder
		var qty string
		if err := rows.Scan(&o.OrderID, &o.SetupGroup, &qty, &o.RunMinutes, &o.RouteSetupMinutes, &o.DueAt, &o.Position, &o.Sequence); err != nil {
			return Campaign{}, err
		}
		o.Quantity = dec(qty)
		c.Orders = append(c.Orders, o)
	}
	if err := rows.Err(); err != nil {
		return Campaign{}, err
	}
	rows.Close()
	if c.PlacedAt != nil {
		if c.Placement, err = loadPlacement(ctx, tx, c); err != nil {
			return Campaign{}, err
		}
	}
	return c, nil
}

// SetupOrders returns the campaign's orders in the order they were added,
// which is the order PlanCampaign receives them in.
func (c Campaign) SetupOrders() []domain.SetupOrder {
	out := make([]domain.SetupOrder, 0, len(c.Orders))
	for _, o := range domainOrders(byPosition(c.Orders)) {
		out = append(out, o.SetupOrder)
	}
	return out
}

func byPosition(os []CampaignOrder) []CampaignOrder {
	out := slices.Clone(os)
	sort.Slice(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out
}

func domainOrders(os []CampaignOrder) []domain.CampaignOrder {
	out := make([]domain.CampaignOrder, 0, len(os))
	for _, o := range os {
		var due time.Time
		if o.DueAt != nil {
			due = *o.DueAt
		}
		out = append(out, domain.CampaignOrder{SetupOrder: domain.SetupOrder{OrderID: o.OrderID, SetupGroup: o.SetupGroup, Due: due,
			Minutes: o.RunMinutes, RouteSetupMinutes: o.RouteSetupMinutes}, Qty: o.Quantity})
	}
	return out
}

func invalidCampaign(err error) error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeCampaignInvalid, err.Error())
}

func insertCampaignOrder(ctx context.Context, tx pgx.Tx, campaignID string, o CampaignOrder) error {
	_, err := tx.Exec(ctx, `insert into scheduling_campaign_order (tenant_id, campaign_id, order_id, setup_group, quantity, run_minutes, route_setup_minutes, due_at, position)
		values ($1, $2, $3, $4, $5::numeric, $6, $7, $8, $9)`, tenantOf(ctx), campaignID, o.OrderID, o.SetupGroup, o.Quantity.String(),
		o.RunMinutes, o.RouteSetupMinutes, o.DueAt, o.Position)
	return err
}

// CreateCampaign stores a proposed campaign with its orders. The orders must
// fit the campaign's order and quantity limits, and an order belongs to at
// most one campaign.
func (s *Postgres) CreateCampaign(ctx context.Context, in Campaign, idem Idem) (Campaign, bool, error) {
	if err := domain.CheckOrders(domainOrders(in.Orders), in.Limits()); err != nil {
		return Campaign{}, false, invalidCampaign(err)
	}
	var out Campaign
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replayRow(ctx, tx, "scheduling_campaign", idem)
		if err != nil {
			return err
		}
		if !found {
			if err := requireResources(ctx, tx, []string{in.ResourceID}); err != nil {
				return err
			}
			var maxQty *string
			if in.MaxQty != nil {
				q := in.MaxQty.String()
				maxQty = &q
			}
			id = ids.New()
			if _, err := tx.Exec(ctx, `insert into scheduling_campaign (tenant_id, id, resource_id, campaign_key, planned_start, max_orders, max_qty, max_minutes,
				sequence_rule, current_setup_group, idempotency_key, idempotency_fingerprint)
				values ($1, $2, $3, $4, $5, $6, $7::numeric, $8, $9, $10, $11, $12)`, tenantOf(ctx), id, in.ResourceID, in.CampaignKey, in.PlannedStart,
				in.MaxOrders, maxQty, in.MaxMinutes, in.SequenceRule, in.CurrentSetupGroup, nullable(idem.Key), nullable(idem.Fingerprint)); err != nil {
				return err
			}
			for i, o := range in.Orders {
				o.Position = i + 1
				if err := insertCampaignOrder(ctx, tx, id, o); err != nil {
					return err
				}
			}
			created = true
		}
		out, err = loadCampaign(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return Campaign{}, false, mapWriteErr(err, CodeOrderInCampaign, "an order already belongs to a campaign")
	}
	return out, created, nil
}

// ListCampaigns returns campaigns newest first, without their orders.
func (s *Postgres) ListCampaigns(ctx context.Context, f CampaignFilter) ([]Campaign, error) {
	out := []Campaign{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+campaignCols+` from scheduling_campaign c
			where ($1 = '' or c.status = $1) and ($2 = '' or c.resource_id::text = $2)
			order by c.created_at desc, c.id desc limit $3`, f.Status, f.ResourceID, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCampaign(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// GetCampaign returns one campaign with its orders in run order.
func (s *Postgres) GetCampaign(ctx context.Context, id string) (Campaign, error) {
	var out Campaign
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadCampaign(ctx, tx, id, false)
		return err
	})
	return out, err
}

// campaignCommand runs a versioned, idempotent command on a locked campaign.
// apply changes the campaign inside the transaction; the reloaded campaign is
// remembered under the Idempotency-Key with the change.
func (s *Postgres) campaignCommand(ctx context.Context, cmd CampaignCommand, idem Idem, apply func(tx pgx.Tx, c Campaign) error) (Campaign, bool, error) {
	return s.campaignCommandTracked(ctx, cmd, idem, func(_ context.Context, tx pgx.Tx, c Campaign) error { return apply(tx, c) })
}

// campaignCommandTracked is campaignCommand for commands that book or free
// capacity: apply gets the context that carries the capacity load tracker, so
// the materialized load commits with the bookings (loadTx).
func (s *Postgres) campaignCommandTracked(ctx context.Context, cmd CampaignCommand, idem Idem, apply func(ctx context.Context, tx pgx.Tx, c Campaign) error) (Campaign, bool, error) {
	var out Campaign
	replayed := false
	err := s.loadTx(&ctx, func(tx pgx.Tx) error {
		if idem.Key != "" {
			found, err := replay(ctx, tx, idem, &out)
			if err != nil || found {
				replayed = found
				return err
			}
		}
		c, err := loadCampaign(ctx, tx, cmd.CampaignID, true)
		if err != nil {
			return err
		}
		if c.Version != cmd.Version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeCampaignVersion,
				fmt.Sprintf("campaign was changed by another user; current version is %d", c.Version))
		}
		if err := apply(ctx, tx, c); err != nil {
			return err
		}
		if out, err = loadCampaign(ctx, tx, cmd.CampaignID, false); err != nil {
			return err
		}
		if idem.Key != "" {
			return remember(ctx, tx, idem, http.StatusOK, out)
		}
		return nil
	})
	if err != nil {
		return Campaign{}, false, mapWriteErr(err, CodeOrderInCampaign, "the order already belongs to a campaign")
	}
	return out, replayed, nil
}

// frozen refuses changes to a started, completed or cancelled campaign (SCHED-0011).
func frozen(status string) error {
	if c := domain.CanJoin(status); c != nil {
		return httpx.NewProblem(http.StatusUnprocessableEntity, c.Code, c.Detail)
	}
	return nil
}

// CodeCampaignPlaced is SCHED-0152: a placed campaign keeps its orders and
// sequence until it is taken off the schedule.
const CodeCampaignPlaced = "SCHED-0152"

// notPlaced refuses a change to the orders or sequence of a campaign that is
// placed on the schedule: its jobs and cleanout windows were laid for exactly
// those orders in that sequence.
func notPlaced(c Campaign) error {
	if c.PlacedAt == nil {
		return nil
	}
	return httpx.NewProblem(http.StatusConflict, CodeCampaignPlaced, "the campaign is placed on the schedule; unplace it before you change its orders or sequence")
}

// unplan clears a campaign's sequence after its order list changed, so a
// planned campaign goes back to proposed until it is sequenced again.
func unplan(ctx context.Context, tx pgx.Tx, id string) error {
	if _, err := tx.Exec(ctx, `update scheduling_campaign_order set sequence = null where campaign_id = $1`, id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `update scheduling_campaign set status = 'proposed', plan = null, sequenced_at = null, total_changeover_minutes = 0,
		cleanout_minutes = 0, run_minutes = 0, version = version + 1 where id = $1`, id)
	return err
}

// AddCampaignOrder adds an order to a campaign that has not started (FR-23.14).
func (s *Postgres) AddCampaignOrder(ctx context.Context, cmd CampaignCommand, idem Idem) (Campaign, bool, error) {
	return s.campaignCommand(ctx, cmd, idem, func(tx pgx.Tx, c Campaign) error {
		if err := frozen(c.Status); err != nil {
			return err
		}
		if err := notPlaced(c); err != nil {
			return err
		}
		o := cmd.Order
		for _, existing := range c.Orders {
			o.Position = max(o.Position, existing.Position)
		}
		o.Position++
		if err := domain.CheckOrders(domainOrders(append(c.Orders, o)), c.Limits()); err != nil {
			return invalidCampaign(err)
		}
		if err := insertCampaignOrder(ctx, tx, c.ID, o); err != nil {
			return err
		}
		return unplan(ctx, tx, c.ID)
	})
}

// RemoveCampaignOrder takes an order out of a campaign that has not started.
func (s *Postgres) RemoveCampaignOrder(ctx context.Context, cmd CampaignCommand, idem Idem) (Campaign, bool, error) {
	return s.campaignCommand(ctx, cmd, idem, func(tx pgx.Tx, c Campaign) error {
		if err := frozen(c.Status); err != nil {
			return err
		}
		if err := notPlaced(c); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `delete from scheduling_campaign_order where campaign_id = $1 and order_id = $2`, c.ID, cmd.OrderID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NewProblem(http.StatusNotFound, CodeCampaignOrderNotFound, "the order is not in this campaign")
		}
		return unplan(ctx, tx, c.ID)
	})
}

// SequenceCampaign plans the campaign with the resource's setup matrix in force
// on the planned start and the cleanout rules of its groups (FR-23.14, FR-23.15,
// BR-23.12, BR-23.13), stores the sequence and plan, marks it planned and
// publishes campaign.sequenced in the same transaction.
func (s *Postgres) SequenceCampaign(ctx context.Context, cmd CampaignCommand, idem Idem) (Campaign, bool, error) {
	return s.campaignCommand(ctx, cmd, idem, func(tx pgx.Tx, c Campaign) error {
		if err := frozen(c.Status); err != nil {
			return err
		}
		if err := notPlaced(c); err != nil {
			return err
		}
		if len(c.Orders) == 0 {
			return invalidCampaign(errors.New("campaign has no orders"))
		}
		matrix, haveMatrix, err := activeMatrix(ctx, tx, c.ResourceID, c.PlannedStart)
		if err != nil {
			return err
		}
		m, err := domain.NewSetupMatrix(matrix.Entries)
		if err != nil {
			return err
		}
		groups := make([]string, 0, len(c.Orders))
		for _, o := range c.Orders {
			groups = append(groups, o.SetupGroup)
		}
		stored, err := cleanoutRules(ctx, tx, distinct(groups))
		if err != nil {
			return err
		}
		rules := make([]domain.CleanoutRule, 0, len(stored))
		for _, r := range stored {
			rules = append(rules, r.Domain())
		}
		current := ""
		if c.CurrentSetupGroup != nil {
			current = *c.CurrentSetupGroup
		}
		carry, err := cleanoutCounter(ctx, tx, c.ResourceID, false)
		if err != nil {
			return err
		}
		var proposal *domain.SolverProposal
		if c.SequenceRule == domain.SequenceCPSAT {
			proposal = cmd.Proposal
		}
		p, err := domain.PlanCampaign(domain.CampaignInput{Status: c.Status, Rule: c.SequenceRule, Matrix: m, Cleanouts: rules,
			CurrentGroup: current, Carry: carry, Limits: c.Limits(), Orders: domainOrders(byPosition(c.Orders)), Proposal: proposal})
		if err != nil {
			return invalidCampaign(err)
		}
		plan := CampaignPlan{Changeovers: p.Sequence.Changeovers, Cleanouts: p.Cleanouts, Warnings: []Message{},
			CarryIn: carry, CarryOut: p.CarryOut, SolverStatus: p.SolverStatus, Optimized: p.Optimized}
		if haveMatrix {
			plan.MatrixID = matrix.ID
		}
		if c.SequenceRule == domain.SequenceCPSAT {
			plan.Warnings = append(plan.Warnings, Message{Code: domain.CodeOptimizationStatus, Detail: domain.SolverMessage(p.SolverStatus, p.Optimized)})
		}
		for _, pair := range p.Sequence.Missing {
			plan.Warnings = append(plan.Warnings, Message{Code: domain.CodeMissingSetupEntry,
				Detail: fmt.Sprintf("Setup matrix has no entry %s; route setup time used.", pair)})
		}
		raw, err := json.Marshal(plan)
		if err != nil {
			return err
		}
		for i, o := range p.Sequence.Orders {
			if _, err := tx.Exec(ctx, `update scheduling_campaign_order set sequence = $3 where campaign_id = $1 and order_id = $2`, c.ID, o.OrderID, i+1); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `update scheduling_campaign set status = 'planned', plan = $2, sequenced_at = now(), run_minutes = $3,
			total_changeover_minutes = $4, cleanout_minutes = $5, version = version + 1 where id = $1 returning version`,
			c.ID, raw, p.RunMinutes, p.ChangeoverMinutes, p.CleanoutMinutes).Scan(&c.Version); err != nil {
			return err
		}
		data := schedevents.CampaignSequencedData{CampaignID: c.ID, ResourceID: c.ResourceID, CampaignKey: c.CampaignKey,
			PlannedStart: rfc(c.PlannedStart), ChangeoverMinutes: p.ChangeoverMinutes, Version: c.Version,
			Orders: []schedevents.CampaignOrderRef{}, Cleanouts: []schedevents.CampaignCleanoutRef{}}
		for i, o := range p.Sequence.Orders {
			data.Orders = append(data.Orders, schedevents.CampaignOrderRef{OrderID: o.OrderID, Sequence: i + 1})
		}
		for _, cl := range p.Cleanouts {
			data.Cleanouts = append(data.Cleanouts, schedevents.CampaignCleanoutRef{AfterOrderID: cl.AfterOrderID, SetupGroup: cl.SetupGroup, Minutes: cl.Minutes, Reason: cl.Reason})
		}
		return enqueue(ctx, tx, "campaign", c.ID, schedevents.CampaignSequenced, data)
	})
}

// StartCampaign freezes a planned campaign (FR-23.14): no order may join or
// leave it afterwards. Until wave 21 reports the first order started, the
// scheduler starts it explicitly. The resource's cleanout counter becomes
// the plan's carry-out (BR-23.13); if another campaign moved the counter
// after this one was sequenced, the start is refused with SCHED-0123 so the
// cleanouts are planned again from the true count.
func (s *Postgres) StartCampaign(ctx context.Context, cmd CampaignCommand, idem Idem) (Campaign, bool, error) {
	return s.campaignCommand(ctx, cmd, idem, func(tx pgx.Tx, c Campaign) error {
		if c.Status != domain.CampaignPlanned {
			if err := frozen(c.Status); err != nil {
				return err
			}
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeCampaignNotPlanned, "sequence the campaign before you start it")
		}
		if c.Plan == nil {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeCampaignNotPlanned, "sequence the campaign before you start it")
		}
		counter, err := cleanoutCounter(ctx, tx, c.ResourceID, true)
		if err != nil {
			return err
		}
		if counter != c.Plan.CarryIn {
			again := "sequence it again"
			if c.PlacedAt != nil {
				// A placed campaign keeps its sequence (SCHED-0152) and its
				// cleanout windows were laid from the stale count.
				again = "take it off the schedule, sequence it again and place it again"
			}
			return httpx.NewProblem(http.StatusConflict, CodeCleanoutCounterMoved, fmt.Sprintf(
				"the resource has run %d batches and %d minutes since its last cleanout, not the %d and %d this campaign was sequenced from; %s",
				counter.Batches, counter.Minutes, c.Plan.CarryIn.Batches, c.Plan.CarryIn.Minutes, again))
		}
		if _, err := tx.Exec(ctx, `update scheduling_cleanout_counter set batches_since = $2, minutes_since = $3, last_campaign_id = $4,
			updated_at = now() where resource_id = $1`, c.ResourceID, c.Plan.CarryOut.Batches, c.Plan.CarryOut.Minutes, c.ID); err != nil {
			return err
		}
		var startedAt time.Time
		var version int
		if err := tx.QueryRow(ctx, `update scheduling_campaign set status = 'started', started_at = now(), version = version + 1
			where id = $1 returning started_at, version`, c.ID).Scan(&startedAt, &version); err != nil {
			return err
		}
		return enqueue(ctx, tx, "campaign", c.ID, schedevents.CampaignStarted,
			schedevents.CampaignStartedData{CampaignID: c.ID, ResourceID: c.ResourceID, StartedAt: rfc(startedAt), Version: version})
	})
}

// CompleteCampaign finishes a started campaign (FR-23.14). Its orders stay on
// it for the record and campaign.completed is published. Its cleanout windows
// that have not begun are released and the resource's materialized load is
// rewritten; windows moved with a job or an unscheduled order are not followed
// (NOT-IMPLEMENTED.md: they clear when the campaign is unplaced, re-placed,
// cancelled or completed).
func (s *Postgres) CompleteCampaign(ctx context.Context, cmd CampaignCommand, idem Idem) (Campaign, bool, error) {
	return s.campaignCommandTracked(ctx, cmd, idem, func(ctx context.Context, tx pgx.Tx, c Campaign) error {
		if conflict := domain.CanComplete(c.Status); conflict != nil {
			return httpx.NewProblem(http.StatusUnprocessableEntity, conflict.Code, conflict.Detail)
		}
		// Cleanout windows that have not begun are not going to be run: they
		// stop closing the resource. Windows already run stay as history, and
		// one under way stays whole: a cleanout is never split (BR-23.13), so
		// the resource is not free until the cleaning in progress ends.
		if _, err := tx.Exec(ctx, `delete from scheduling_campaign_cleanout where campaign_id = $1 and start_at > now()`, c.ID); err != nil {
			return err
		}
		trackResources(ctx, c.ResourceID)
		var at time.Time
		var version int
		if err := tx.QueryRow(ctx, `update scheduling_campaign set status = 'completed', completed_at = now(), version = version + 1
			where id = $1 returning completed_at, version`, c.ID).Scan(&at, &version); err != nil {
			return err
		}
		return enqueue(ctx, tx, "campaign", c.ID, schedevents.CampaignCompleted,
			schedevents.CampaignCompletedData{CampaignID: c.ID, ResourceID: c.ResourceID, CompletedAt: rfc(at), Version: version})
	})
}

// CancelCampaign cancels a campaign that is not finished and releases its
// orders so they can join another campaign (the order belongs to at most one).
// A started campaign gives back the cleanout counter it advanced, but only
// while nothing has moved the counter since; otherwise the counter stays, as
// the later campaign already built on it.
func (s *Postgres) CancelCampaign(ctx context.Context, cmd CampaignCommand, idem Idem) (Campaign, bool, error) {
	return s.campaignCommandTracked(ctx, cmd, idem, func(ctx context.Context, tx pgx.Tx, c Campaign) error {
		if conflict := domain.CanCancel(c.Status); conflict != nil {
			return httpx.NewProblem(http.StatusUnprocessableEntity, conflict.Code, conflict.Detail)
		}
		restored := false
		if c.Status == domain.CampaignStarted && c.Plan != nil {
			counter, err := cleanoutCounter(ctx, tx, c.ResourceID, true)
			if err != nil {
				return err
			}
			var last *string
			if err := tx.QueryRow(ctx, `select last_campaign_id::text from scheduling_cleanout_counter where resource_id = $1`, c.ResourceID).Scan(&last); err != nil {
				return err
			}
			if last != nil && *last == c.ID && counter == c.Plan.CarryOut {
				if _, err := tx.Exec(ctx, `update scheduling_cleanout_counter set batches_since = $2, minutes_since = $3, last_campaign_id = null,
					updated_at = now() where resource_id = $1`, c.ResourceID, c.Plan.CarryIn.Batches, c.Plan.CarryIn.Minutes); err != nil {
					return err
				}
				restored = true
			}
		}
		orderIDs := make([]string, 0, len(c.Orders))
		for _, o := range c.Orders {
			orderIDs = append(orderIDs, o.OrderID)
		}
		if _, err := tx.Exec(ctx, `delete from scheduling_campaign_order where campaign_id = $1`, c.ID); err != nil {
			return err
		}
		// A cancelled campaign holds no closed time; its jobs stay as ordinary
		// jobs of their orders.
		if _, err := tx.Exec(ctx, `delete from scheduling_campaign_cleanout where campaign_id = $1`, c.ID); err != nil {
			return err
		}
		trackResources(ctx, c.ResourceID)
		var at time.Time
		var version int
		if err := tx.QueryRow(ctx, `update scheduling_campaign set status = 'cancelled', cancelled_at = now(), cancel_reason = nullif($2, ''),
			placed_at = null, placed_run_id = null, planned_end = null, version = version + 1 where id = $1 returning cancelled_at, version`, c.ID, cmd.Reason).Scan(&at, &version); err != nil {
			return err
		}
		return enqueue(ctx, tx, "campaign", c.ID, schedevents.CampaignCancelled, schedevents.CampaignCancelledData{CampaignID: c.ID,
			ResourceID: c.ResourceID, CancelledAt: rfc(at), Reason: cmd.Reason, OrderIDs: orderIDs, CounterRestored: restored, Version: version})
	})
}

// cleanoutCounter reads a resource's cleanout counter; a resource without one
// has run nothing since a cleanout. lock creates the row when missing and
// locks it, so two campaigns on one resource start one after the other.
func cleanoutCounter(ctx context.Context, tx pgx.Tx, resourceID string, lock bool) (domain.CleanoutCounter, error) {
	q := `select batches_since, minutes_since from scheduling_cleanout_counter where resource_id = $1`
	if lock {
		if _, err := tx.Exec(ctx, `insert into scheduling_cleanout_counter (tenant_id, resource_id) values ($1, $2)
			on conflict (tenant_id, resource_id) do nothing`, tenantOf(ctx), resourceID); err != nil {
			return domain.CleanoutCounter{}, err
		}
		q += ` for update`
	}
	var c domain.CleanoutCounter
	err := tx.QueryRow(ctx, q, resourceID).Scan(&c.Batches, &c.Minutes)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CleanoutCounter{}, nil
	}
	return c, err
}

// CampaignSetupMatrix returns the resource's setup matrix in force on a day,
// empty when there is none. The API prices the CP-SAT request with it; the
// sequencing transaction reprices the answer with the matrix it reads.
func (s *Postgres) CampaignSetupMatrix(ctx context.Context, resourceID string, on time.Time) (domain.SetupMatrix, error) {
	var out domain.SetupMatrix
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		matrix, _, err := activeMatrix(ctx, tx, resourceID, on)
		if err != nil {
			return err
		}
		out, err = domain.NewSetupMatrix(matrix.Entries)
		return err
	})
	return out, err
}
