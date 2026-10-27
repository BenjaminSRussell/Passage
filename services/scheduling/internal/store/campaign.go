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

// CampaignPlan is the stored result of the last sequencing.
type CampaignPlan struct {
	Changeovers []domain.Changeover      `json:"changeovers"`
	Cleanouts   []domain.PlannedCleanout `json:"cleanouts"`
	Warnings    []Message                `json:"warnings"`
	MatrixID    string                   `json:"matrixId,omitempty"`
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
	Version           int              `json:"version"`
	CreatedAt         time.Time        `json:"createdAt"`
	Orders            []CampaignOrder  `json:"orders,omitempty"`
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
type CampaignCommand struct {
	CampaignID string
	Version    int
	Order      CampaignOrder
	OrderID    string
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
}

const campaignCols = `c.id::text, c.resource_id::text, c.campaign_key, c.status, c.planned_start, c.max_orders, c.max_qty::text, c.max_minutes,
	c.sequence_rule, c.current_setup_group, (select count(*) from scheduling_campaign_order o where o.campaign_id = c.id)::int,
	c.run_minutes, c.total_changeover_minutes, c.cleanout_minutes, c.plan, c.sequenced_at, c.started_at, c.version, c.created_at`

func scanCampaign(row pgx.Row) (Campaign, error) {
	var c Campaign
	var maxQty *string
	var plan []byte
	if err := row.Scan(&c.ID, &c.ResourceID, &c.CampaignKey, &c.Status, &c.PlannedStart, &c.MaxOrders, &maxQty, &c.MaxMinutes,
		&c.SequenceRule, &c.CurrentSetupGroup, &c.OrderCount, &c.RunMinutes, &c.ChangeoverMinutes, &c.CleanoutMinutes, &plan,
		&c.SequencedAt, &c.StartedAt, &c.Version, &c.CreatedAt); err != nil {
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
	return c, rows.Err()
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
	var out Campaign
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
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
		if err := apply(tx, c); err != nil {
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
		byPosition := slices.Clone(c.Orders)
		sort.Slice(byPosition, func(i, j int) bool { return byPosition[i].Position < byPosition[j].Position })
		current := ""
		if c.CurrentSetupGroup != nil {
			current = *c.CurrentSetupGroup
		}
		p, err := domain.PlanCampaign(domain.CampaignInput{Status: c.Status, Rule: c.SequenceRule, Matrix: m, Cleanouts: rules,
			CurrentGroup: current, Limits: c.Limits(), Orders: domainOrders(byPosition)})
		if err != nil {
			return invalidCampaign(err)
		}
		plan := CampaignPlan{Changeovers: p.Sequence.Changeovers, Cleanouts: p.Cleanouts, Warnings: []Message{}}
		if haveMatrix {
			plan.MatrixID = matrix.ID
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
// scheduler starts it explicitly.
func (s *Postgres) StartCampaign(ctx context.Context, cmd CampaignCommand, idem Idem) (Campaign, bool, error) {
	return s.campaignCommand(ctx, cmd, idem, func(tx pgx.Tx, c Campaign) error {
		if c.Status != domain.CampaignPlanned {
			if err := frozen(c.Status); err != nil {
				return err
			}
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeCampaignNotPlanned, "sequence the campaign before you start it")
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
