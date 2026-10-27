package store

import (
	"context"
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
	CodeIdempotencyReuse = "idempotency_key_reuse"

	uniqueViolation = "23505"
	// idempotencyConstraint is Postgres' default name for UNIQUE (tenant_id, idempotency_key).
	idempotencyConstraint = "plan_version_tenant_id_idempotency_key_key"
	lockNotAvailable      = "55P03"
	dateLayout            = "2006-01-02"
	eventSource           = "planning"
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
	Ping(ctx context.Context) error
}

// Postgres implements Store.
type Postgres struct {
	pool *pgxpool.Pool
}

// New returns a Postgres store over pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

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

// PlannedOrder is one planned order row of the current version.
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

func enqueue(ctx context.Context, tx pgx.Tx, planID, tenantID string, versionID string, data planevents.PlanCompletedData) error {
	evt, err := events.New(planevents.PlanCompleted, eventSource, versionID, tenantID, data)
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
		if _, err := tx.Exec(ctx, `insert into plan_version (tenant_id, id, plan_id, number, site_id, as_of, status, items_planned, planned_orders_count, messages_count, duration_ms, plan_hash, input_hash, idempotency_key)
			values ($1, $2, $3, $4, $5, $6::date, 'current', $7, $8, $9, $10, $11, $12, $13)`,
			p.TenantID, versionID, planID, number, req.SiteID, asOf, req.ItemsPlanned, len(r.Orders), len(r.Messages), req.DurationMs, r.Hash, req.InputHash, key); err != nil {
			return err
		}
		// One round trip per kind of output row instead of one per row; COPY is
		// not used because it is not supported on tables with forced RLS.
		batch := &pgx.Batch{}
		counts := planevents.PlannedCounts{}
		for _, o := range r.Orders {
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
			batch.Queue(`insert into planned_order (tenant_id, id, plan_version_id, number, item_id, order_type, qty, order_date, delivery_date, futures_date, delay_days, lot_rule)
				values ($1, $2, $3, $4, $5, $6, $7::numeric, $8::date, $9::date, $10::date, $11, $12)`,
				p.TenantID, ids.New(), versionID, o.Number, o.ItemID, o.OrderType, o.Qty.String(), dayDate(req.AsOf, o.OrderDate), dayDate(req.AsOf, o.DeliveryDate), futures, o.DelayDays, o.LotRule)
		}
		for _, pg := range r.Pegging {
			batch.Queue(`insert into pegging (tenant_id, id, plan_version_id, item_id, supply_ref, demand_ref, demand_ref_type, demand_date, qty)
				values ($1, $2, $3, $4, $5, $6, $7, $8::date, $9::numeric)`,
				p.TenantID, ids.New(), versionID, pg.ItemID, pg.SupplyRef, pg.DemandRef, pg.DemandRefType, dayDate(req.AsOf, pg.DemandDate), pg.Qty.String())
		}
		for _, m := range r.Messages {
			batch.Queue(`insert into action_message (tenant_id, id, plan_version_id, item_id, action, supply_ref, demand_ref, suggested_date, delay_days, qty)
				values ($1, $2, $3, $4, $5, $6, $7, $8::date, $9, $10::numeric)`,
				p.TenantID, ids.New(), versionID, m.ItemID, m.Action, m.SupplyRef, m.DemandRef, dayDate(req.AsOf, m.SuggestedDate), m.DelayDays, m.Qty.String())
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return err
		}
		v = Version{ID: versionID, PlanCode: req.PlanCode, Number: number, Status: "current", AsOf: asOf, ItemsPlanned: req.ItemsPlanned,
			PlannedOrders: len(r.Orders), Messages: len(r.Messages), DurationMs: req.DurationMs, PlanHash: r.Hash, inputHash: req.InputHash}
		return enqueue(ctx, tx, planID, p.TenantID, versionID, planevents.PlanCompletedData{
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
	o.delay_days, o.lot_rule, o.status
	from planned_order o join plan_version v on v.tenant_id = o.tenant_id and v.id = o.plan_version_id
	join plan p on p.tenant_id = v.tenant_id and p.id = v.plan_id`

func scanOrder(row pgx.Row) (PlannedOrder, error) {
	var o PlannedOrder
	err := row.Scan(&o.ID, &o.VersionID, &o.Number, &o.ItemID, &o.OrderType, &o.Qty, &o.OrderDate, &o.DeliveryDate, &o.FuturesDate, &o.DelayDays, &o.LotRule, &o.Status)
	return o, err
}

// ListPlannedOrders reads the current version's planned orders of a plan.
func (s *Postgres) ListPlannedOrders(ctx context.Context, planCode string, f OrderFilter, limit int) ([]PlannedOrder, error) {
	p, _ := tenant.FromContext(ctx)
	out := []PlannedOrder{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, orderSelect+` where o.tenant_id = $1 and p.code = $2 and v.status = 'current'
			and ($3::text = '' or o.status = $3) and ($4::text = '' or o.item_id::text = $4)
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
