package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	planevents "github.com/BenjaminSRussell/passage/services/planning/internal/events"
)

const (
	MaxPageSize = 500

	CodeVersionNotFound  = "PLAN-VERSION-NOT-FOUND"
	CodeOrderNotFound    = "PLAN-ORDER-NOT-FOUND"
	CodeAlreadyRunning   = "PLAN-0001"
	CodeOrderNotEditable = "PLAN-ORDER-NOT-EDITABLE"
	CodeOrderInvalid     = "PLAN-ORDER-INVALID"
	CodeForecastNotFound = "PLAN-FORECAST-NOT-FOUND"
	CodeVersionConflict  = "conflict_version"
	CodeIdempotencyReuse = "idempotency_key_reuse"

	uniqueViolation = "23505"
	// idempotencyConstraint is Postgres' default name for UNIQUE (tenant_id, idempotency_key).
	idempotencyConstraint = "plan_version_tenant_id_idempotency_key_key"
	lockNotAvailable      = "55P03"
	dateLayout            = "2006-01-02"
	eventSource           = "planning"
	// planKindStatic is the only plan kind runs have until the dynamic
	// net-change plan exists (FR-22.1).
	planKindStatic = "static"
)

// Store is the persistence contract the API depends on.
type Store interface {
	RunPlan(ctx context.Context, req RunRequest) (Version, bool, error)
	GetVersion(ctx context.Context, planCode string, number int) (Version, error)
	ListVersions(ctx context.Context, planCode string, limit int) ([]Version, error)
	ListPlannedOrders(ctx context.Context, planCode string, f OrderFilter, limit int) ([]PlannedOrder, error)
	GetPlannedOrder(ctx context.Context, id string) (PlannedOrder, error)
	PegsOf(ctx context.Context, versionID, supplyRef string) ([]domain.PegLine, error)
	ListMessages(ctx context.Context, planCode, action string, limit int) ([]Message, error)
	UpdatePlannedOrder(ctx context.Context, id string, expectedVersion int, patch OrderPatch) (PlannedOrder, error)
	DeletePlannedOrder(ctx context.Context, id string, expectedVersion int) error
	FirmPlannedOrders(ctx context.Context, req FirmRequest) (FirmBatch, bool, error)
	UpsertForecastLines(ctx context.Context, modelCode string, expectedVersion int, spec *ModelSpec, cells []ForecastCellInput) (ForecastModel, error)
	ListForecastLines(ctx context.Context, modelCode, itemID, siteID string, limit int) (ForecastModel, []ForecastLine, error)
	ForecastDemand(ctx context.Context, modelCode, siteID string, from time.Time) ([]ForecastDemandLine, error)
	SubmitForecastConsensus(ctx context.Context, modelCode string, expectedVersion int) (ForecastModel, error)
	DecideForecastConsensus(ctx context.Context, modelCode string, expectedVersion int, approve bool) (ForecastModel, *ForecastPublication, error)
	ListNetRequirements(ctx context.Context, planCode, itemID string, limit int) ([]NetRequirement, error)
	ListCoverageGroups(ctx context.Context, limit int) ([]CoverageGroup, error)
	GetCoverageGroup(ctx context.Context, code string) (CoverageGroup, error)
	CreateCoverageGroup(ctx context.Context, req CoverageCreate) (CoverageGroup, bool, error)
	UpdateCoverageGroup(ctx context.Context, expectedVersion int, spec domain.CoverageGroupSpec) (CoverageGroup, error)
	Ping(ctx context.Context) error
}

// DefaultMaxVersions is the default of PLANNING_MAX_VERSIONS (4.22.13 "Max
// retained versions"); MinMaxVersions and MaxMaxVersions are its range.
const (
	DefaultMaxVersions = 20
	MinMaxVersions     = 2
	MaxMaxVersions     = 100
)

// Postgres implements Store.
type Postgres struct {
	pool        *pgxpool.Pool
	maxVersions int
}

// New returns a Postgres store over pool that keeps DefaultMaxVersions
// versions per plan.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool, maxVersions: DefaultMaxVersions} }

// WithMaxVersions sets how many versions of a plan a run keeps; values outside
// MinMaxVersions to MaxMaxVersions are clamped into that range.
func (s *Postgres) WithMaxVersions(n int) *Postgres {
	s.maxVersions = min(max(n, MinMaxVersions), MaxMaxVersions)
	return s
}

// Ping checks the database is reachable.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// RunRequest is one persisted plan run.
type RunRequest struct {
	PlanCode       string
	SiteID         string
	AsOf           time.Time
	ItemsPlanned   int
	DurationMs     int64
	InputHash      string
	IdempotencyKey string
	Result         domain.Result
}

// Version is a plan version as the API shows it.
type Version struct {
	ID            string `json:"id"`
	PlanCode      string `json:"planCode"`
	Number        int    `json:"number"`
	Status        string `json:"status"`
	AsOf          string `json:"asOf"`
	ItemsPlanned  int    `json:"itemsPlanned"`
	PlannedOrders int    `json:"plannedOrders"`
	Messages      int    `json:"messages"`
	DurationMs    int64  `json:"durationMs"`
	PlanHash      string `json:"planHash"`
	inputHash     string
}

// PlannedOrder is one planned order row of a plan version. Status is
// unprocessed, approved, firmed or deleted.
type PlannedOrder struct {
	ID           string  `json:"id"`
	VersionID    string  `json:"versionId"`
	Number       string  `json:"number"`
	ItemID       string  `json:"itemId"`
	OrderType    string  `json:"orderType"`
	Qty          string  `json:"qty"`
	OrderDate    string  `json:"orderDate"`
	DeliveryDate string  `json:"deliveryDate"`
	FuturesDate  *string `json:"futuresDate,omitempty"`
	DelayDays    int     `json:"delayDays"`
	LotRule      string  `json:"lotRule"`
	Status       string  `json:"status"`
	// Version is the order's ETag: every maintenance write bumps it.
	Version  int     `json:"version"`
	VendorID *string `json:"vendorId,omitempty"`
	// FirmedTargetType and FirmedTargetID name the purchase, production or
	// transfer order a firmed planned order became (FR-22.10).
	FirmedTargetType *string `json:"firmedTargetType,omitempty"`
	FirmedTargetID   *string `json:"firmedTargetId,omitempty"`
}

// Message is one action or delay message of the current version.
type Message struct {
	Action        string `json:"action"`
	ItemID        string `json:"itemId"`
	SupplyRef     string `json:"supplyRef"`
	DemandRef     string `json:"demandRef"`
	SuggestedDate string `json:"suggestedDate"`
	DelayDays     int    `json:"delayDays"`
	Qty           string `json:"qty"`
}

// OrderFilter narrows planned-order reads.
type OrderFilter struct {
	Status, ItemID string
}

func idempotencyReuse() error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
}

func dayDate(asOf time.Time, day int) string {
	return asOf.AddDate(0, 0, day).Format(dateLayout)
}

// enqueue writes one event of the plan aggregate to the outbox in tx, keyed by
// the plan so a consumer sees a run's events in order.
func enqueue(ctx context.Context, tx pgx.Tx, typ, planID, tenantID, subject string, data any) error {
	evt, err := events.New(typ, eventSource, subject, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "plan", planID, evt)
}

// RunPlan persists a finished run as the new current version of its plan, in
// one transaction: the previous current version is demoted, so the pointer
// swaps only on success. A repeated Idempotency-Key returns the stored version.
func (s *Postgres) RunPlan(ctx context.Context, req RunRequest) (Version, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var v Version
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if req.IdempotencyKey != "" {
			existing, err := scanVersion(tx.QueryRow(ctx, versionSelect+` where v.tenant_id = $1 and v.idempotency_key = $2`, p.TenantID, req.IdempotencyKey))
			if err == nil {
				if existing.inputHash != req.InputHash {
					return idempotencyReuse()
				}
				v, replayed = existing, true
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		planID, err := lockPlan(ctx, tx, p.TenantID, req.PlanCode)
		if err != nil {
			return err
		}
		var number int
		if err := tx.QueryRow(ctx, `select coalesce(max(number), 0) + 1 from plan_version where tenant_id = $1 and plan_id = $2`, p.TenantID, planID).Scan(&number); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update plan_version set status = 'complete' where tenant_id = $1 and plan_id = $2 and status = 'current'`, p.TenantID, planID); err != nil {
			return err
		}
		versionID := ids.New()
		var key *string
		if req.IdempotencyKey != "" {
			key = &req.IdempotencyKey
		}
		asOf := req.AsOf.Format(dateLayout)
		r := req.Result
		// The run executes inside this request, so the version is written and
		// started in one transaction; plan.started.v1 precedes the run's other
		// events in the outbox.
		if err := enqueue(ctx, tx, planevents.PlanStarted, planID, p.TenantID, versionID, planevents.PlanStartedData{
			PlanID: planID, PlanCode: req.PlanCode, VersionID: versionID, VersionNumber: number, Kind: planKindStatic,
			SiteID: req.SiteID, AsOf: asOf, ItemsPlanned: req.ItemsPlanned,
		}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into plan_version (tenant_id, id, plan_id, number, site_id, as_of, status, items_planned, planned_orders_count, messages_count, duration_ms, plan_hash, input_hash, idempotency_key)
			values ($1, $2, $3, $4, $5, $6::date, 'current', $7, $8, $9, $10, $11, $12, $13)`,
			p.TenantID, versionID, planID, number, req.SiteID, asOf, req.ItemsPlanned, len(r.Orders), len(r.Messages), req.DurationMs, r.Hash, req.InputHash, key); err != nil {
			return err
		}
		// One round trip per kind of output row instead of one per row; COPY is
		// not used because it is not supported on tables with forced RLS.
		batch := &pgx.Batch{}
		counts := planevents.PlannedCounts{}
		orderIDs := make([]string, len(r.Orders))
		for i := range r.Orders {
			orderIDs[i] = ids.New()
		}
		// BR-22.4: orders inside the firming fence are firmed by the run. The
		// firming batch row goes first, since the firmed orders reference it.
		autoFirm, err := autoFirmBatch(req, versionID, orderIDs)
		if err != nil {
			return err
		}
		if autoFirm != nil {
			body, err := json.Marshal(autoFirm.batch)
			if err != nil {
				return err
			}
			batch.Queue(`insert into firming_batch (tenant_id, id, idempotency_key, input_hash, grouping, firmed_count, response)
				values ($1, $2, $3, $4, $5, $6, $7)`, p.TenantID, autoFirm.batch.ID, autoFirmKeyPrefix+versionID, r.Hash,
				autoFirm.batch.Grouping, autoFirm.batch.Firmed, body)
		}
		for i, o := range r.Orders {
			var futures *string
			if o.FuturesDate != nil {
				f := dayDate(req.AsOf, *o.FuturesDate)
				futures = &f
			}
			switch o.OrderType {
			case domain.OrderPurchase:
				counts.Purchase++
			case domain.OrderProduction:
				counts.Production++
			case domain.OrderTransfer:
				counts.Transfer++
			}
			status, batchID, targetType, targetID := "unprocessed", (*string)(nil), (*string)(nil), (*string)(nil)
			if g, ok := autoFirm.groupOf(orderIDs[i]); ok {
				status, batchID, targetType, targetID = "firmed", &autoFirm.batch.ID, &g.TargetType, &g.TargetID
			}
			batch.Queue(`insert into planned_order (tenant_id, id, plan_version_id, number, item_id, order_type, qty, order_date, delivery_date, futures_date, delay_days, lot_rule,
				status, firmed_batch_id, firmed_target_type, firmed_target_id)
				values ($1, $2, $3, $4, $5, $6, $7::numeric, $8::date, $9::date, $10::date, $11, $12, $13, $14, $15, $16)`,
				p.TenantID, orderIDs[i], versionID, o.Number, o.ItemID, o.OrderType, o.Qty.String(), dayDate(req.AsOf, o.OrderDate), dayDate(req.AsOf, o.DeliveryDate), futures, o.DelayDays, o.LotRule,
				status, batchID, targetType, targetID)
		}
		for _, pg := range r.Pegging {
			batch.Queue(`insert into pegging (tenant_id, id, plan_version_id, item_id, supply_ref, demand_ref, demand_ref_type, demand_date, qty)
				values ($1, $2, $3, $4, $5, $6, $7, $8::date, $9::numeric)`,
				p.TenantID, ids.New(), versionID, pg.ItemID, pg.SupplyRef, pg.DemandRef, pg.DemandRefType, dayDate(req.AsOf, pg.DemandDate), pg.Qty.String())
		}
		messageIDs := make([]string, len(r.Messages))
		for i, m := range r.Messages {
			messageIDs[i] = ids.New()
			batch.Queue(`insert into action_message (tenant_id, id, plan_version_id, item_id, action, supply_ref, demand_ref, suggested_date, delay_days, qty)
				values ($1, $2, $3, $4, $5, $6, $7, $8::date, $9, $10::numeric)`,
				p.TenantID, messageIDs[i], versionID, m.ItemID, m.Action, m.SupplyRef, m.DemandRef, dayDate(req.AsOf, m.SuggestedDate), m.DelayDays, m.Qty.String())
		}
		for _, n := range r.Net {
			batch.Queue(`insert into net_requirement (tenant_id, id, plan_version_id, item_id, day, gross_requirements, scheduled_receipts, planned_receipts, pab, net_requirement)
				values ($1, $2, $3, $4, $5::date, $6::numeric, $7::numeric, $8::numeric, $9::numeric, $10::numeric)`,
				p.TenantID, ids.New(), versionID, n.ItemID, dayDate(req.AsOf, n.Day), n.Gross.String(), n.ScheduledReceipts.String(),
				n.PlannedReceipts.String(), n.PAB.String(), n.Net.String())
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return err
		}
		// FR-22.7: one message.raised.v1 per action or delay message, so
		// purchasing, sales and notifications learn of it with the version.
		for i, m := range r.Messages {
			if err := enqueue(ctx, tx, planevents.MessageRaised, planID, p.TenantID, messageIDs[i], planevents.MessageRaisedData{
				MessageID: messageIDs[i], PlanID: planID, PlanCode: req.PlanCode, VersionID: versionID, SiteID: req.SiteID,
				Action: m.Action, ItemID: m.ItemID, SupplyRef: m.SupplyRef, DemandRef: m.DemandRef,
				SuggestedDate: dayDate(req.AsOf, m.SuggestedDate), DelayDays: m.DelayDays, Qty: m.Qty.String(),
			}); err != nil {
				return err
			}
		}
		// One plannedorder.firmed.v1 per auto-firmed order, as a planner's
		// firming writes, so the owning contexts create the target orders.
		for i, o := range r.Orders {
			g, ok := autoFirm.groupOf(orderIDs[i])
			if !ok {
				continue
			}
			evt, err := events.New(planevents.PlannedOrderFirmed, eventSource, orderIDs[i], p.TenantID, planevents.PlannedOrderFirmedData{
				PlannedOrderID: orderIDs[i], PlanVersionID: versionID, BatchID: autoFirm.batch.ID, OrderType: g.OrderType,
				TargetType: g.TargetType, TargetID: g.TargetID, ItemID: o.ItemID, SiteID: req.SiteID, Qty: o.Qty.String(),
				OrderDate: dayDate(req.AsOf, o.OrderDate), DeliveryDate: dayDate(req.AsOf, o.DeliveryDate),
			})
			if err != nil {
				return err
			}
			if err := outbox.Enqueue(ctx, tx, "planned-order", orderIDs[i], evt); err != nil {
				return err
			}
		}
		if err := purgeVersions(ctx, tx, p.TenantID, planID, number-s.maxVersions); err != nil {
			return err
		}
		v = Version{ID: versionID, PlanCode: req.PlanCode, Number: number, Status: "current", AsOf: asOf, ItemsPlanned: req.ItemsPlanned,
			PlannedOrders: len(r.Orders), Messages: len(r.Messages), DurationMs: req.DurationMs, PlanHash: r.Hash, inputHash: req.InputHash}
		return enqueue(ctx, tx, planevents.PlanCompleted, planID, p.TenantID, versionID, planevents.PlanCompletedData{
			PlanID: planID, PlanCode: req.PlanCode, VersionID: versionID, VersionNumber: number, ItemsPlanned: req.ItemsPlanned,
			PlannedOrders: counts, Messages: len(r.Messages), DurationMs: req.DurationMs, PlanHash: r.Hash,
		})
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == idempotencyConstraint {
		return Version{}, false, idempotencyReuse()
	}
	return v, replayed, err
}

// purgeVersions deletes the versions of a plan numbered at or below cut with
// their planned orders, pegging, messages and net requirements (FR-22.1: 20 retained versions by
// default). It runs inside the run transaction that holds the plan lock. A
// version that holds a firmed planned order is kept: the order records which
// target order it became, and its batch's replay still names it.
func purgeVersions(ctx context.Context, tx pgx.Tx, tenantID, planID string, cut int) error {
	if cut < 1 {
		return nil
	}
	const doomed = `select v.id from plan_version v where v.tenant_id = $1 and v.plan_id = $2 and v.number <= $3 and v.status <> 'current'
		and not exists (select 1 from planned_order o where o.tenant_id = v.tenant_id and o.plan_version_id = v.id and o.status = 'firmed')`
	for _, table := range []string{"net_requirement", "pegging", "action_message", "planned_order"} {
		if _, err := tx.Exec(ctx, `delete from `+table+` where tenant_id = $1 and plan_version_id in (`+doomed+`)`, tenantID, planID, cut); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `delete from plan_version where tenant_id = $1 and id in (`+doomed+`)`, tenantID, planID, cut)
	return err
}

// lockPlan creates the plan on first use and locks its row for the run; a second
// run that cannot take the lock immediately is refused (FR-22.1, AC-22.9).
func lockPlan(ctx context.Context, tx pgx.Tx, tenantID, code string) (string, error) {
	if _, err := tx.Exec(ctx, `insert into plan (tenant_id, id, code) values ($1, $2, $3) on conflict (tenant_id, code) do nothing`, tenantID, ids.New(), code); err != nil {
		return "", err
	}
	var id string
	err := tx.QueryRow(ctx, `select id::text from plan where tenant_id = $1 and code = $2 for update nowait`, tenantID, code).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == lockNotAvailable {
		return "", httpx.NewProblem(http.StatusConflict, CodeAlreadyRunning, "a run of plan "+code+" is already in progress")
	}
	return id, err
}

const versionSelect = `select v.id::text, p.code, v.number, v.status, to_char(v.as_of, 'YYYY-MM-DD'), v.items_planned,
	v.planned_orders_count, v.messages_count, v.duration_ms, v.plan_hash, v.input_hash
	from plan_version v join plan p on p.tenant_id = v.tenant_id and p.id = v.plan_id`

func scanVersion(row pgx.Row) (Version, error) {
	var v Version
	err := row.Scan(&v.ID, &v.PlanCode, &v.Number, &v.Status, &v.AsOf, &v.ItemsPlanned, &v.PlannedOrders, &v.Messages, &v.DurationMs, &v.PlanHash, &v.inputHash)
	return v, err
}

// GetVersion returns version number of the plan, current or historical.
func (s *Postgres) GetVersion(ctx context.Context, planCode string, number int) (Version, error) {
	p, _ := tenant.FromContext(ctx)
	var v Version
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		v, err = scanVersion(tx.QueryRow(ctx, versionSelect+` where v.tenant_id = $1 and p.code = $2 and v.number = $3`, p.TenantID, planCode, number))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, httpx.NewProblem(http.StatusNotFound, CodeVersionNotFound, "plan version not found")
	}
	return v, err
}

// ListVersions returns the plan's run history, newest first (4.22.14 "Plan run history").
func (s *Postgres) ListVersions(ctx context.Context, planCode string, limit int) ([]Version, error) {
	p, _ := tenant.FromContext(ctx)
	out := []Version{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, versionSelect+` where v.tenant_id = $1 and p.code = $2 order by v.number desc limit $3`, p.TenantID, planCode, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanVersion(rows)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

const orderSelect = `select o.id::text, v.id::text, o.number, o.item_id::text, o.order_type, o.qty::text,
	to_char(o.order_date, 'YYYY-MM-DD'), to_char(o.delivery_date, 'YYYY-MM-DD'), to_char(o.futures_date, 'YYYY-MM-DD'),
	o.delay_days, o.lot_rule, o.status, o.version, o.vendor_id::text, o.firmed_target_type, o.firmed_target_id::text
	from planned_order o join plan_version v on v.tenant_id = o.tenant_id and v.id = o.plan_version_id
	join plan p on p.tenant_id = v.tenant_id and p.id = v.plan_id`

func scanOrder(row pgx.Row) (PlannedOrder, error) {
	var o PlannedOrder
	err := row.Scan(&o.ID, &o.VersionID, &o.Number, &o.ItemID, &o.OrderType, &o.Qty, &o.OrderDate, &o.DeliveryDate, &o.FuturesDate, &o.DelayDays, &o.LotRule, &o.Status, &o.Version, &o.VendorID,
		&o.FirmedTargetType, &o.FirmedTargetID)
	return o, err
}

// ListPlannedOrders reads the current version's planned orders of a plan.
// Deleted orders are left out.
func (s *Postgres) ListPlannedOrders(ctx context.Context, planCode string, f OrderFilter, limit int) ([]PlannedOrder, error) {
	p, _ := tenant.FromContext(ctx)
	out := []PlannedOrder{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, orderSelect+` where o.tenant_id = $1 and p.code = $2 and v.status = 'current'
			and (case when $3::text = '' then o.status <> 'deleted' else o.status = $3 end) and ($4::text = '' or o.item_id::text = $4)
			order by o.number limit $5`, p.TenantID, planCode, f.Status, f.ItemID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			o, err := scanOrder(rows)
			if err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	return out, err
}

// GetPlannedOrder returns one planned order by id.
func (s *Postgres) GetPlannedOrder(ctx context.Context, id string) (PlannedOrder, error) {
	p, _ := tenant.FromContext(ctx)
	var o PlannedOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		o, err = scanOrder(tx.QueryRow(ctx, orderSelect+` where o.tenant_id = $1 and o.id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PlannedOrder{}, httpx.NewProblem(http.StatusNotFound, CodeOrderNotFound, "planned order not found")
	}
	return o, err
}

// PegsOf returns the demand that one supply reference covers in a version.
func (s *Postgres) PegsOf(ctx context.Context, versionID, supplyRef string) ([]domain.PegLine, error) {
	p, _ := tenant.FromContext(ctx)
	var out []domain.PegLine
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select demand_ref, demand_ref_type, qty::text from pegging
			where tenant_id = $1 and plan_version_id = $2 and supply_ref = $3 order by demand_ref, demand_date`, p.TenantID, versionID, supplyRef)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line domain.PegLine
			var qty string
			if err := rows.Scan(&line.DemandRef, &line.DemandRefType, &qty); err != nil {
				return err
			}
			if line.Qty, err = decimal.NewFromString(qty); err != nil {
				return err
			}
			out = append(out, line)
		}
		return rows.Err()
	})
	return out, err
}

// ListMessages reads the current version's messages, optionally of one action.
func (s *Postgres) ListMessages(ctx context.Context, planCode, action string, limit int) ([]Message, error) {
	p, _ := tenant.FromContext(ctx)
	out := []Message{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select m.action, m.item_id::text, m.supply_ref, m.demand_ref, to_char(m.suggested_date, 'YYYY-MM-DD'), m.delay_days, m.qty::text
			from action_message m join plan_version v on v.tenant_id = m.tenant_id and v.id = m.plan_version_id
			join plan p on p.tenant_id = v.tenant_id and p.id = v.plan_id
			where m.tenant_id = $1 and p.code = $2 and v.status = 'current' and ($3::text = '' or m.action = $3)
			order by m.action, m.supply_ref, m.demand_ref limit $4`, p.TenantID, planCode, action, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m Message
			if err := rows.Scan(&m.Action, &m.ItemID, &m.SupplyRef, &m.DemandRef, &m.SuggestedDate, &m.DelayDays, &m.Qty); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}
