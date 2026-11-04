// Package store persists production orders in Postgres. Every write runs in a
// tenant-scoped transaction (RLS) and enqueues its event in the same transaction.
package store

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

// Stable error codes returned by the store.
const (
	CodeOrderNotFound    = "PRODUCTION_ORDER_NOT_FOUND"
	CodeVersionConflict  = "conflict_version"
	CodeIdempotencyReuse = "idempotency_key_reuse"
	CodeOriginConflict   = "PRODUCTION_ORIGIN_CONFLICT"
	eventSource          = "production"
	aggregateType        = "production_order"
)

// Order is the order view served by the API: header, exploded BOM, route,
// planned cost. Decimals serialize as strings.
type Order struct {
	ID            string          `json:"id"`
	LegalEntityID string          `json:"legalEntityId"`
	OrderNumber   string          `json:"orderNumber"`
	ProductID     string          `json:"productId"`
	SiteID        string          `json:"siteId"`
	WarehouseID   string          `json:"warehouseId"`
	Currency      string          `json:"currency"`
	ItemGroup     string          `json:"itemGroup,omitempty"`
	Quantity      decimal.Decimal `json:"quantity"`
	QtyRAF        decimal.Decimal `json:"qtyRaf"`
	QtyError      decimal.Decimal `json:"qtyError"`
	Status        domain.Status   `json:"status"`
	DeliveryDate  *string         `json:"deliveryDate,omitempty"`
	// StartDate and EndDate are the dates `:schedule` gave the order (UTC dates of the first
	// start and the last finish); absent while it is unscheduled. SchedulingMethod is the
	// method of the last schedule, operations until one runs.
	StartDate        *string                 `json:"startDate,omitempty"`
	EndDate          *string                 `json:"endDate,omitempty"`
	SchedulingMethod domain.SchedulingMethod `json:"schedulingMethod"`
	// ScheduleBasis says how the dates were found: infinite (:schedule, a re-date) or finite
	// (scheduling's windows applied, FR-23.10); FiniteRunID and FiniteScheduledAt name that plan.
	// FiniteCompressedOperations are the operations whose finite window is shorter than their
	// jobs' planned hours (a warning, not a refusal).
	ScheduleBasis              string     `json:"scheduleBasis"`
	FiniteRunID                *string    `json:"finiteRunId,omitempty"`
	FiniteScheduledAt          *time.Time `json:"finiteScheduledAt,omitempty"`
	FiniteCompressedOperations []int      `json:"finiteCompressedOperations,omitempty"`
	OriginType                 string     `json:"originType"`
	OriginID                   *string    `json:"originId,omitempty"`
	// ConfigurationID is the saved configuration of a SalesOrderLine order.
	ConfigurationID *string `json:"configurationId,omitempty"`
	// QuotaArrangementID and QuotaSourceID are the quota arrangement and source planning allocated a
	// converted planned order to (FR-22.17); null on other orders.
	QuotaArrangementID *string `json:"quotaArrangementId,omitempty"`
	QuotaSourceID      *string `json:"quotaSourceId,omitempty"`
	// ReworkReference is the original order, batch or serial of a rework order (FR-21.20).
	ReworkReference *domain.ReworkRef `json:"reworkReference,omitempty"`
	// SplitFromOrderID is the order a split took this order's quantity from;
	// MergedIntoOrderID the order a merge moved it into (FR-21.15).
	SplitFromOrderID  *string `json:"splitFromOrderId,omitempty"`
	MergedIntoOrderID *string `json:"mergedIntoOrderId,omitempty"`
	// OrderBOMModified is set once the order's BOM or route was edited after
	// explosion (FR-21.18); OCM leaves such an order to manual review.
	OrderBOMModified bool               `json:"orderBomModified"`
	Version          int                `json:"version"`
	Lines            []domain.OrderLine `json:"lines"`
	Operations       []domain.Operation `json:"operations"`
	// CoProducts are the co-products and by-products declared on the order (FR-21.14).
	CoProducts []domain.CoProduct `json:"coProducts"`
	Costs      []domain.CostRow   `json:"costs"`
	CreatedAt  time.Time          `json:"createdAt"`
	UpdatedAt  time.Time          `json:"updatedAt"`
}

// OrderSummary is one row of the order list (PRD 4.21.14 "Production orders list").
type OrderSummary struct {
	ID            string          `json:"id"`
	LegalEntityID string          `json:"legalEntityId"`
	OrderNumber   string          `json:"orderNumber"`
	ProductID     string          `json:"productId"`
	SiteID        string          `json:"siteId"`
	WarehouseID   string          `json:"warehouseId"`
	Currency      string          `json:"currency"`
	Quantity      decimal.Decimal `json:"quantity"`
	QtyRAF        decimal.Decimal `json:"qtyRaf"`
	QtyError      decimal.Decimal `json:"qtyError"`
	Remaining     decimal.Decimal `json:"remaining"`
	Status        domain.Status   `json:"status"`
	DeliveryDate  *string         `json:"deliveryDate,omitempty"`
	OriginType    string          `json:"originType"`
	// ReworkReference is set on a rework order that names its original (FR-21.20).
	ReworkReference *domain.ReworkRef `json:"reworkReference,omitempty"`
	PlannedCost     decimal.Decimal   `json:"plannedCost"`
	ActualCost      decimal.Decimal   `json:"actualCost"`
	Version         int               `json:"version"`
	CreatedAt       time.Time         `json:"createdAt"`
}

// OrderPage is a keyset page of the order list, newest first.
type OrderPage struct {
	Items      []OrderSummary `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

// ListFilter narrows the order list. Empty fields do not filter. ReworkOf is
// an order id: only the rework orders that reference it (FR-21.20). Cursor is
// the id of the last order of the previous page.
type ListFilter struct {
	Status        domain.Status
	LegalEntityID string
	SiteID        string
	ProductID     string
	OriginType    string
	ReworkOf      string
	Cursor        string
	Limit         int
}

// StatusLogEntry is one applied command (FR-21.2): actor, time and trace id.
type StatusLogEntry struct {
	Command    domain.Command `json:"command"`
	From       domain.Status  `json:"from"`
	To         domain.Status  `json:"to"`
	Actor      string         `json:"actor"`
	TraceID    string         `json:"traceId"`
	OccurredAt time.Time      `json:"occurredAt"`
}

// NewOrder is a validated order to create, with its exploded snapshot.
// RequestHash identifies the request body for Idempotency-Key replays.
type NewOrder struct {
	LegalEntityID   string
	ProductID       string
	SiteID          string
	WarehouseID     string
	ItemGroup       string
	Currency        string
	Quantity        decimal.Decimal
	DeliveryDate    *string
	OriginType      string
	OriginID        *string
	ConfigurationID *string
	// QuotaArrangementID and QuotaSourceID come from the planned order of an intake conversion.
	QuotaArrangementID, QuotaSourceID *string
	// ID is the id the order takes; empty draws a new one. A planned-order conversion sets it to the
	// planned order's target id (FR-22.10).
	ID string
	// PlannedIntakeID is the intake row this order converts; the row is marked converted in the same transaction.
	PlannedIntakeID string
	// ReworkReference names the original of a rework order (OriginType Rework), or is nil.
	ReworkReference *domain.ReworkRef
	Lines           []domain.OrderLine
	Operations      []domain.Operation
	// CoProducts are the co-products and by-products the order yields (FR-21.14), planned.
	CoProducts []domain.CoProduct
	// EstimateOnCreate runs `:estimate` in the create transaction (PRD 4.21.13
	// "Estimate on create"), so the order is created Estimated with its planned cost.
	EstimateOnCreate bool
	TraceID          string
	IdempotencyKey   string
	RequestHash      string
}

// OrderUpdate is a PATCH of the header. A nil Quantity leaves it; SetDeliveryDate
// with a nil DeliveryDate clears the date.
type OrderUpdate struct {
	OrderID         string
	IfMatch         int
	Quantity        *decimal.Decimal
	SetDeliveryDate bool
	DeliveryDate    *string
	Actor           string
}

// Transition is one `:command` request. ResetTo is the target of
// `:reset-status` (empty for the previous status). Good and Error are the
// quantities of `:report-finished`, which posts a report-as-finished journal;
// RAFTolerancePct bounds them (FR-21.7).
type Transition struct {
	OrderID         string
	IfMatch         int
	Command         domain.Command
	ResetTo         domain.Status
	Good            decimal.Decimal
	Error           decimal.Decimal
	RAFTolerancePct decimal.Decimal
	ZeroOutputDuty  bool
	// Schedule carries the `:schedule` parameters; nil schedules forward from Now with the
	// operations method. Now is the clock the default start reads (the API sets it).
	Schedule       *ScheduleParams
	Now            time.Time
	Actor          string
	TraceID        string
	IdempotencyKey string
	RequestHash    string
}

// Store is what the API needs from persistence.
type Store interface {
	Ping(ctx context.Context) error
	CreateOrder(ctx context.Context, in NewOrder) (Order, bool, error)
	GetPlannedIntake(ctx context.Context, id string) (PlannedIntake, error)
	ListPlannedIntake(ctx context.Context, f IntakeFilter) (IntakePage, error)
	DismissPlannedIntake(ctx context.Context, in IntakeDismiss) (PlannedIntake, error)
	GetOrder(ctx context.Context, id string) (Order, error)
	ListOrders(ctx context.Context, f ListFilter) (OrderPage, error)
	StatusLog(ctx context.Context, orderID string) ([]StatusLogEntry, error)
	UpdateOrder(ctx context.Context, u OrderUpdate) (Order, error)
	Transition(ctx context.Context, t Transition) (Order, bool, error)
	Costs(ctx context.Context, orderID string) (CostSummary, error)
	CreateJournal(ctx context.Context, in NewJournal) (Journal, bool, error)
	PostJournal(ctx context.Context, r PostRequest) (Journal, bool, error)
	GetJournal(ctx context.Context, id string) (Journal, error)
	ListJournals(ctx context.Context, orderID string) ([]Journal, error)
	DeleteJournal(ctx context.Context, id string) error
	ListPostingProfiles(ctx context.Context, legalEntityID string) ([]PostingProfile, error)
	SavePostingProfile(ctx context.Context, in ProfileSave) (PostingProfile, bool, error)
	AssignAsBuilt(ctx context.Context, in NewAsBuilt) (AsBuiltRecord, bool, error)
	ListAsBuilt(ctx context.Context, f AsBuiltFilter) ([]AsBuiltRecord, error)
	DetachAsBuilt(ctx context.Context, id, actor string) (AsBuiltRecord, error)
	SplitOrder(ctx context.Context, in Split) (SplitResult, error)
	MergeOrders(ctx context.Context, in Merge) (MergeResult, error)
	BulkTransition(ctx context.Context, in Bulk) []BulkItem
	EditOrder(ctx context.Context, in OrderEdit) (Order, error)
	ApplyFiniteDates(ctx context.Context, in FiniteDates) (Order, bool, error)
	OrderEdits(ctx context.Context, orderID string) ([]OrderEditEntry, error)
	WIPReport(ctx context.Context, f WIPFilter) (WIPReport, error)
	VarianceReport(ctx context.Context, f VarianceFilter) (VarianceReport, error)
	CreateTriggerPoint(ctx context.Context, in NewTriggerPoint) (TriggerPoint, bool, error)
	ListTriggerPoints(ctx context.Context, orderID string) ([]TriggerPoint, error)
	DeleteTriggerPoint(ctx context.Context, id string) error
	SubcontractOperations(ctx context.Context, f SubcontractFilter) (SubcontractReport, error)
	OperationsOverview(ctx context.Context, f OperationFilter) (OperationsOverview, error)
	JournalInquiry(ctx context.Context, f JournalFilter) (JournalInquiry, error)
	MaterialRequirements(ctx context.Context, f MaterialFilter) (MaterialRequirements, error)
	MaterialItems(ctx context.Context, f MaterialFilter) (MaterialItems, error)
	KPIs(ctx context.Context, f KPIFilter) (KPIReport, error)
	ApplyOperationCommand(ctx context.Context, c OperationCommand) (Order, OperationOutcome, error)
	StaleCreatedOrders(ctx context.Context, in HousekeepingScan, defaultDays int) ([]HousekeepingOrder, int, error)
	FullyReportedOrders(ctx context.Context, in HousekeepingScan) ([]HousekeepingOrder, int, error)
	HousekeepingReplay(ctx context.Context, legalEntityID, key, requestHash string) ([]HousekeepingOrder, error)
	ListParameters(ctx context.Context, legalEntityID string) ([]StoredParameters, error)
	SaveParameters(ctx context.Context, in ParametersSave) (StoredParameters, bool, error)
	ListOCMProfiles(ctx context.Context, legalEntityID string) ([]OCMProfile, error)
	SaveOCMProfile(ctx context.Context, in OCMProfileSave) (OCMProfile, bool, error)
	DetectOrderChanges(ctx context.Context, in ChangeDetect) (ChangeDetectResult, error)
	ListOrderChanges(ctx context.Context, f ChangeFilter) (ChangePage, error)
	GetOrderChange(ctx context.Context, id string) (OrderChange, error)
	PreviewOrderChange(ctx context.Context, id string) (ChangePreview, error)
	ApplyOrderChange(ctx context.Context, in ChangeApply) (ChangeApplyResult, error)
	DismissOrderChange(ctx context.Context, in ChangeDismiss) (OrderChange, error)
	ListJobs(ctx context.Context, f JobFilter) (JobPage, error)
	RunCostSnapshot(ctx context.Context, in CostSnapshotRun) (CostSnapshotResult, error)
	CostSnapshots(ctx context.Context, f CostSnapshotFilter) (CostSnapshotPage, error)
}

// Postgres is the production store.
type Postgres struct {
	pool *pgxpool.Pool
}

var _ Store = (*Postgres)(nil)

// New returns a store over pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// querier is satisfied by both pgx.Tx and the pool, so loaders run in or out of a transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// dec parses a numeric value selected as text. Database numerics are always well formed.
func dec(s string) decimal.Decimal {
	v, _ := decimal.NewFromString(s)
	return v
}

func notFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeOrderNotFound, "production order not found")
}

func enqueue(ctx context.Context, tx pgx.Tx, orderID, eventType, tenantID, actor string, data any) error {
	evt, err := events.New(eventType, eventSource, orderID, tenantID, data)
	if err != nil {
		return err
	}
	evt.Actor = actor
	return outbox.Enqueue(ctx, tx, aggregateType, orderID, evt)
}

// plannedOriginIndex keeps one order per planned order (migration 0026).
const plannedOriginIndex = "production_order_planned_origin"

// CreateOrder inserts an order with its exploded lines and operations and
// allocates its number from the legal entity's sequence. A replay with the same
// Idempotency-Key and body returns the stored order and created=false; the same
// key with another body is refused.
func (s *Postgres) CreateOrder(ctx context.Context, in NewOrder) (Order, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out Order
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if existing, found, err := existingSalesLineOrder(ctx, tx, p.TenantID, in); err != nil || found {
			out = existing
			return err
		}
		var replayID, replayHash string
		err := tx.QueryRow(ctx, `select id::text, request_hash from production_order where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, in.IdempotencyKey).Scan(&replayID, &replayHash)
		if err == nil {
			if replayHash != in.RequestHash {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			out, err = loadOrder(ctx, tx, p.TenantID, replayID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if ref := in.ReworkReference; ref != nil {
			if err := checkReworkSource(ctx, tx, p.TenantID, in, *ref); err != nil {
				return err
			}
		}
		prm, err := effectiveParameters(ctx, tx, p.TenantID, in.LegalEntityID, in.SiteID, domain.Parameters{EstimateOnCreate: in.EstimateOnCreate})
		if err != nil {
			return err
		}
		if in.PlannedIntakeID != "" {
			if err := lockOpenIntake(ctx, tx, p.TenantID, in.PlannedIntakeID); err != nil {
				return err
			}
		}
		id, err := insertOrder(ctx, tx, p.TenantID, p.UserID, in)
		if err != nil {
			return err
		}
		if in.PlannedIntakeID != "" {
			if err := convertIntake(ctx, tx, p.TenantID, p.UserID, in.PlannedIntakeID, id); err != nil {
				return err
			}
		}
		if prm.EstimateOnCreate {
			if err := estimateOnCreate(ctx, tx, p.TenantID, p.UserID, id, in); err != nil {
				return err
			}
		}
		out, err = loadOrder(ctx, tx, p.TenantID, id)
		created = err == nil
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == plannedOriginIndex {
		// The intake row was open, but an order already names the planned order (one a plain create
		// made before slice 26, possibly in another legal entity): the planned order is taken.
		return Order{}, false, httpx.NewProblem(http.StatusConflict, CodeIntakeNotOpen, "the planned order already has a production order")
	}
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "production_order_sales_line_origin" {
		var raced Order
		var found bool
		rerr := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (e error) {
			raced, found, e = existingSalesLineOrder(ctx, tx, p.TenantID, in)
			return e
		})
		if rerr != nil {
			return Order{}, false, rerr
		}
		if found {
			return raced, false, nil
		}
		return Order{}, false, httpx.NewProblem(http.StatusConflict, CodeOriginConflict, "the sales order line already has an order that this legal entity cannot read")
	}
	return out, created, err
}

// existingSalesLineOrder makes the create idempotent per sales order line: the
// order already built for the line is returned (created=false) when it was made for
// the same product and configuration, and any other request for the line is refused.
func existingSalesLineOrder(ctx context.Context, tx pgx.Tx, tenantID string, in NewOrder) (Order, bool, error) {
	if in.OriginType != domain.OriginSalesLine || in.OriginID == nil {
		return Order{}, false, nil
	}
	var id, product string
	var configuration *string
	err := tx.QueryRow(ctx, `select id::text, product_id::text, configuration_id::text from production_order
		where tenant_id = $1 and origin_type = 'SalesOrderLine' and origin_id = $2`, tenantID, *in.OriginID).Scan(&id, &product, &configuration)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, false, nil
	}
	if err != nil {
		return Order{}, false, err
	}
	if product != in.ProductID || configuration == nil || in.ConfigurationID == nil || *configuration != *in.ConfigurationID {
		return Order{}, false, httpx.NewProblem(http.StatusConflict, CodeOriginConflict, "the order of this sales order line was made for another product or configuration")
	}
	order, err := loadOrder(ctx, tx, tenantID, id)
	return order, true, err
}

// estimateOnCreate applies `:estimate` to a just-created order inside its create
// transaction (PRD 4.21.13): the planned cost rows, Created → Estimated with a
// version bump, the status log entry and order.statuschanged after order.created,
// as the command itself does. Its log key derives from the create's key.
func estimateOnCreate(ctx context.Context, tx pgx.Tx, tenantID, actor, orderID string, in NewOrder) error {
	to, err := domain.Transition(domain.Created, domain.CmdEstimate)
	if err != nil {
		return domainProblem(err)
	}
	if err := replaceEstimate(ctx, tx, tenantID, orderID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update production_order set status = $3, version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`,
		tenantID, orderID, string(to)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into production_status_log (tenant_id, id, legal_entity_id, order_id, command, from_status, to_status, actor, trace_id, idempotency_key, request_hash)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, tenantID, ids.New(), in.LegalEntityID, orderID, string(domain.CmdEstimate),
		string(domain.Created), string(to), actor, in.TraceID, "sys:estimate-on-create:"+in.IdempotencyKey, in.RequestHash); err != nil {
		return err
	}
	var updatedAt time.Time
	if err := tx.QueryRow(ctx, `select updated_at from production_order where tenant_id = $1 and id = $2`, tenantID, orderID).Scan(&updatedAt); err != nil {
		return err
	}
	zero := "0"
	return enqueue(ctx, tx, orderID, prodevents.OrderStatusChanged, tenantID, actor, prodevents.OrderStatusChangedData{
		OrderID: orderID, From: string(domain.Created), To: string(to), Quantity: in.Quantity.String(), QtyRAF: zero,
		QtyError: zero, Actor: actor, OccurredAt: updatedAt.UTC().Format(time.RFC3339Nano),
	})
}

// insertOrder allocates the order number from the legal entity's sequence, inserts the
// order with its lines and operations and enqueues order.created. It returns the new id.
// A rework reference is stored as given; the caller checks it.
func insertOrder(ctx context.Context, tx pgx.Tx, tenantID, actor string, in NewOrder) (string, error) {
	var seq int64
	if err := tx.QueryRow(ctx, `insert into production_order_sequence (tenant_id, legal_entity_id, next_value) values ($1, $2, 2)
		on conflict (tenant_id, legal_entity_id) do update set next_value = production_order_sequence.next_value + 1
		returning next_value - 1`, tenantID, in.LegalEntityID).Scan(&seq); err != nil {
		return "", err
	}
	var refType, refID *string
	if ref := in.ReworkReference; ref != nil {
		t := string(ref.Type)
		refType, refID = &t, &ref.ID
	}
	id := in.ID
	if id == "" {
		id = ids.New()
	}
	number := fmt.Sprintf("PO-%06d", seq)
	if _, err := tx.Exec(ctx, `insert into production_order (tenant_id, id, legal_entity_id, order_number, product_id, site_id, warehouse_id, currency,
		quantity, delivery_date, origin_type, origin_id, idempotency_key, request_hash, item_group, rework_reference_type, rework_reference_id, configuration_id,
		quota_arrangement_id, quota_source_id)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::date, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)`,
		tenantID, id, in.LegalEntityID, number, in.ProductID, in.SiteID, in.WarehouseID, in.Currency,
		in.Quantity.String(), in.DeliveryDate, in.OriginType, in.OriginID, in.IdempotencyKey, in.RequestHash, in.ItemGroup, refType, refID, in.ConfigurationID, in.QuotaArrangementID, in.QuotaSourceID); err != nil {
		return "", err
	}
	if err := insertLines(ctx, tx, tenantID, id, in.Lines); err != nil {
		return "", err
	}
	if err := insertOperations(ctx, tx, tenantID, id, in.Operations); err != nil {
		return "", err
	}
	if err := insertCoProducts(ctx, tx, tenantID, id, in.LegalEntityID, in.CoProducts); err != nil {
		return "", err
	}
	data := prodevents.OrderCreatedData{
		OrderID: id, OrderNumber: number, ProductID: in.ProductID, Quantity: in.Quantity.String(), SiteID: in.SiteID,
		DeliveryDate: in.DeliveryDate, OriginType: in.OriginType, OriginID: in.OriginID, ConfigurationID: in.ConfigurationID,
	}
	if ref := in.ReworkReference; ref != nil {
		data.ReworkReference = &prodevents.ReworkReference{Type: string(ref.Type), ID: ref.ID}
	}
	return id, enqueue(ctx, tx, id, prodevents.OrderCreated, tenantID, actor, data)
}

// checkReworkSource checks a rework order's reference to an original order
// (FR-21.20): the order exists for the caller, made the same product and
// reported at least the rework quantity as finished. Batch and serial
// references are kept as entered until tracking (wave 16) can resolve them.
func checkReworkSource(ctx context.Context, tx pgx.Tx, tenantID string, in NewOrder, ref domain.ReworkRef) error {
	if ref.Type != domain.ReworkRefOrder {
		return nil
	}
	var product, raf string
	err := tx.QueryRow(ctx, `select product_id::text, qty_raf::text from production_order where tenant_id = $1 and id = $2`,
		tenantID, ref.ID).Scan(&product, &raf)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeReworkReference, "the referenced production order does not exist")
	}
	if err != nil {
		return err
	}
	return domainProblem(domain.CheckReworkSource(in.ProductID, in.Quantity, product, dec(raf)))
}

func insertLines(ctx context.Context, tx pgx.Tx, tenantID, orderID string, lines []domain.OrderLine) error {
	for i, l := range lines {
		flushing := l.Flushing
		if flushing == "" {
			flushing = domain.FlushManual
		}
		if _, err := tx.Exec(ctx, `insert into production_bom_line (tenant_id, order_id, line_number, line_seq, item_id, qty_per, series_size, scrap_pct, round_up,
				qty_required, unit_cost_est, flushing_principle)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			tenantID, orderID, l.LineNo, i+1, l.ItemID, l.QtyPer.String(), l.SeriesSize.String(), l.ScrapPct.String(), l.RoundUp,
			l.QtyRequired.String(), l.UnitCost.String(), string(flushing)); err != nil {
			return err
		}
	}
	return nil
}

func insertOperations(ctx context.Context, tx pgx.Tx, tenantID, orderID string, ops []domain.Operation) error {
	for _, op := range ops {
		if _, err := tx.Exec(ctx, `insert into production_operation (tenant_id, order_id, oper_number, resource_id, setup_time_h, run_time_h_per, process_qty, scrap_pct,
			labor_rate, overhead_rate, subcontract, subcontract_price, queue_before_h, queue_after_h, transit_h)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
			tenantID, orderID, op.OperNumber, op.ResourceID, op.SetupH.String(), op.RunHPer.String(), op.ProcessQty.String(), op.ScrapPct.String(),
			op.LaborRate.String(), op.OverheadRate.String(), op.Subcontract, op.SubcontractPrice.String(),
			op.QueueBeforeH.String(), op.QueueAfterH.String(), op.TransitH.String()); err != nil {
			return err
		}
	}
	return nil
}

// GetOrder returns one order with its lines, operations and planned cost.
func (s *Postgres) GetOrder(ctx context.Context, id string) (Order, error) {
	p, _ := tenant.FromContext(ctx)
	var out Order
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = loadOrder(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

// ListOrders returns a page of orders, newest first (ids are UUIDv7, so id
// order is creation order). RLS limits the rows to the caller's legal entities.
func (s *Postgres) ListOrders(ctx context.Context, f ListFilter) (OrderPage, error) {
	p, _ := tenant.FromContext(ctx)
	if f.Limit <= 0 {
		f.Limit = 50
	}
	page := OrderPage{Items: []OrderSummary{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select o.id::text, o.legal_entity_id::text, o.order_number, o.product_id::text, o.site_id::text,
				o.warehouse_id::text, o.currency::text, o.quantity::text, o.qty_raf::text, o.qty_error::text, o.status,
				o.delivery_date::text, o.version, o.created_at, o.origin_type, o.rework_reference_type, o.rework_reference_id,
				coalesce((select sum(c.planned_amount) from production_cost_estimate c
				          where c.tenant_id = o.tenant_id and c.order_id = o.id), 0)::text,
				coalesce((select sum(w.amount_dr - w.amount_cr) from production_wip_posting w
				          where w.tenant_id = o.tenant_id and w.order_id = o.id
				            and w.posting_type in ('wip-material', 'wip-labor', 'wip-overhead')), 0)::text
			from production_order o
			where o.tenant_id = $1
			  and ($2 = '' or o.status = $2)
			  and ($3 = '' or o.legal_entity_id = nullif($3, '')::uuid)
			  and ($4 = '' or o.site_id = nullif($4, '')::uuid)
			  and ($5 = '' or o.product_id = nullif($5, '')::uuid)
			  and ($6 = '' or o.id < nullif($6, '')::uuid)
			  and ($8 = '' or o.origin_type = $8)
			  and ($9 = '' or (o.rework_reference_type = 'order' and o.rework_reference_id = $9))
			order by o.id desc
			limit $7`,
			p.TenantID, string(f.Status), f.LegalEntityID, f.SiteID, f.ProductID, f.Cursor, f.Limit+1, f.OriginType, f.ReworkOf)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o OrderSummary
			var status, qty, raf, qerr, planned, actual string
			var refType, refID *string
			if err := rows.Scan(&o.ID, &o.LegalEntityID, &o.OrderNumber, &o.ProductID, &o.SiteID, &o.WarehouseID, &o.Currency,
				&qty, &raf, &qerr, &status, &o.DeliveryDate, &o.Version, &o.CreatedAt, &o.OriginType, &refType, &refID, &planned, &actual); err != nil {
				return err
			}
			o.ReworkReference = reworkRef(refType, refID)
			o.Status = domain.Status(status)
			o.Quantity, o.QtyRAF, o.QtyError = dec(qty), dec(raf), dec(qerr)
			o.Remaining = decimal.Max(o.Quantity.Sub(o.QtyRAF).Sub(o.QtyError), decimal.Zero)
			o.PlannedCost = domain.RoundMoney(dec(planned), o.Currency)
			o.ActualCost = domain.RoundMoney(dec(actual), o.Currency)
			page.Items = append(page.Items, o)
		}
		return rows.Err()
	})
	if err != nil {
		return OrderPage{}, err
	}
	if len(page.Items) > f.Limit {
		page.Items = page.Items[:f.Limit]
		page.NextCursor = page.Items[f.Limit-1].ID
	}
	return page, nil
}

// StatusLog returns the applied commands of an order, oldest first.
func (s *Postgres) StatusLog(ctx context.Context, orderID string) ([]StatusLogEntry, error) {
	p, _ := tenant.FromContext(ctx)
	out := []StatusLogEntry{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from production_order where tenant_id = $1 and id = $2)`, p.TenantID, orderID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return notFound()
		}
		rows, err := tx.Query(ctx, `select command, from_status, to_status, actor, trace_id, occurred_at from production_status_log
			where tenant_id = $1 and order_id = $2 order by occurred_at, id`, p.TenantID, orderID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e StatusLogEntry
			var cmd, from, to string
			if err := rows.Scan(&cmd, &from, &to, &e.Actor, &e.TraceID, &e.OccurredAt); err != nil {
				return err
			}
			e.Command, e.From, e.To = domain.Command(cmd), domain.Status(from), domain.Status(to)
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// orderHead is the locked header row a command, edit or journal post works on.
type orderHead struct {
	id            string
	status        domain.Status
	version       int
	quantity      decimal.Decimal
	qtyRAF        decimal.Decimal
	qtyError      decimal.Decimal
	legalEntityID string
	productID     string
	siteID        string
	itemGroup     string
	currency      string
	createdOn     string
	originType    string
}

// rework reports whether the order is a rework order (FR-21.20).
func (h orderHead) rework() bool { return h.originType == domain.OriginRework }

// lockOrder takes the row lock that serializes commands and journal posts on
// one order (BR-21.4) and checks If-Match against the version; ifMatch 0
// skips the check (journal posts carry no If-Match).
func lockOrder(ctx context.Context, tx pgx.Tx, tenantID, orderID string, ifMatch int) (orderHead, error) {
	h := orderHead{id: orderID}
	var status, qty, raf, qerr string
	err := tx.QueryRow(ctx, `select status, version, quantity::text, qty_raf::text, qty_error::text, legal_entity_id::text,
			product_id::text, site_id::text, item_group, currency::text, (created_at at time zone 'UTC')::date::text, origin_type
		from production_order where tenant_id = $1 and id = $2 for update`, tenantID, orderID).Scan(&status, &h.version, &qty, &raf, &qerr,
		&h.legalEntityID, &h.productID, &h.siteID, &h.itemGroup, &h.currency, &h.createdOn, &h.originType)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, notFound()
	}
	if err != nil {
		return h, err
	}
	if ifMatch != 0 && h.version != ifMatch {
		return h, httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, fmt.Sprintf("order was changed by another user; current version is %d", h.version))
	}
	h.status = domain.Status(status)
	h.quantity, h.qtyRAF, h.qtyError = dec(qty), dec(raf), dec(qerr)
	return h, nil
}

// UpdateOrder applies a PATCH of the header under If-Match. A quantity change
// re-explodes the lines and re-plans the operations from the stored snapshot,
// and re-estimates an Estimated order, all in one transaction.
func (s *Postgres) UpdateOrder(ctx context.Context, u OrderUpdate) (Order, error) {
	p, _ := tenant.FromContext(ctx)
	var out Order
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		h, err := lockOrder(ctx, tx, p.TenantID, u.OrderID, u.IfMatch)
		if err != nil {
			return err
		}
		if err := domain.CheckEdit(h.status, u.Quantity != nil, u.SetDeliveryDate); err != nil {
			return domainProblem(err)
		}
		qty := h.quantity
		// FR-21.10: what purchasing was asked for before the quantity or the delivery
		// date (the PO due date) moves; settled once the header is written.
		requested, err := requestedSubcontracts(ctx, tx, p.TenantID, u.OrderID)
		if err != nil {
			return err
		}
		if u.Quantity != nil {
			qty = *u.Quantity
			if err := requantify(ctx, tx, p.TenantID, u.OrderID, qty); err != nil {
				return err
			}
			if h.status == domain.Estimated {
				if err := replaceEstimate(ctx, tx, p.TenantID, u.OrderID); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, `update production_order set quantity = $3,
				delivery_date = case when $4 then $5::date else delivery_date end,
				version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, u.OrderID, qty.String(), u.SetDeliveryDate, u.DeliveryDate); err != nil {
			return err
		}
		if err := settleSubcontracts(ctx, tx, p.TenantID, u.OrderID, u.Actor, requested, false); err != nil {
			return err
		}
		out, err = loadOrder(ctx, tx, p.TenantID, u.OrderID)
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, u.OrderID, prodevents.OrderUpdated, p.TenantID, u.Actor, prodevents.OrderUpdatedData{
			OrderID: u.OrderID, Quantity: out.Quantity.String(), DeliveryDate: out.DeliveryDate, Status: string(out.Status),
			Version: out.Version, Actor: u.Actor,
		})
	})
	return out, err
}

// requantify rewrites the required line quantities and operation process
// quantities for a new order quantity.
func requantify(ctx context.Context, tx pgx.Tx, tenantID, orderID string, qty decimal.Decimal) error {
	lines, err := loadLines(ctx, tx, tenantID, orderID)
	if err != nil {
		return err
	}
	if lines, err = domain.Requantify(lines, qty); err != nil {
		return domainProblem(err)
	}
	ops, err := loadOperations(ctx, tx, tenantID, orderID)
	if err != nil {
		return err
	}
	if ops, err = domain.PlanOperations(ops, qty); err != nil {
		return domainProblem(err)
	}
	if err := replanCoProducts(ctx, tx, tenantID, orderID, qty); err != nil {
		return err
	}
	return writeQuantities(ctx, tx, tenantID, orderID, lines, ops)
}

// writeQuantities stores re-exploded required line quantities and operation
// process quantities of an order.
func writeQuantities(ctx context.Context, tx pgx.Tx, tenantID, orderID string, lines []domain.OrderLine, ops []domain.Operation) error {
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `update production_bom_line set qty_required = $4 where tenant_id = $1 and order_id = $2 and line_number = $3`,
			tenantID, orderID, l.LineNo, l.QtyRequired.String()); err != nil {
			return err
		}
	}
	for _, op := range ops {
		if _, err := tx.Exec(ctx, `update production_operation set process_qty = $4 where tenant_id = $1 and order_id = $2 and oper_number = $3`,
			tenantID, orderID, op.OperNumber, op.ProcessQty.String()); err != nil {
			return err
		}
	}
	return nil
}

// Transition applies a `:command` to an order. It locks the order row, checks
// If-Match against the version, applies the state machine and its guards and
// the posting effects of BR-21.1 in the same transaction: :estimate replaces
// the cost rows, :start backflushes the start-flushed lines, :report-finished
// posts a report-as-finished journal, :end settles WIP into variances, and a
// reset reverses the journals of the statuses it leaves (FR-21.9) and drops
// the estimate on the way back to Created. It writes the status log and the
// event, and bumps the version.
// A replay of the same Idempotency-Key and request returns the current order
// with replayed=true and changes nothing.
func (s *Postgres) Transition(ctx context.Context, t Transition) (Order, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out Order
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var sched *domain.Schedule
		var loggedOrder, loggedHash string
		err := tx.QueryRow(ctx, `select order_id::text, request_hash from production_status_log where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, t.IdempotencyKey).Scan(&loggedOrder, &loggedHash)
		if err == nil {
			if loggedOrder != t.OrderID || loggedHash != t.RequestHash {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used for another order or command")
			}
			replayed = true
			out, err = loadOrder(ctx, tx, p.TenantID, t.OrderID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		h, err := lockOrder(ctx, tx, p.TenantID, t.OrderID, t.IfMatch)
		if err != nil {
			return err
		}
		from := h.status
		var to domain.Status
		if t.Command == domain.CmdResetStatus && t.ResetTo != "" {
			to, err = domain.ResetTo(from, t.ResetTo)
		} else {
			to, err = domain.Transition(from, t.Command)
		}
		if err != nil {
			return domainProblem(err)
		}
		// FR-21.16: an automatic OCM profile applies the order's pending changes at
		// its next status transition, before the command takes effect.
		if domain.CheckChangeEligible(from) == nil {
			if err := autoApplyChanges(ctx, tx, p.TenantID, t.OrderID, t.Command, t.Actor, t.TraceID); err != nil {
				return err
			}
		}
		prm, err := orderParameters(ctx, tx, p.TenantID, h, domain.Parameters{RAFTolerancePct: t.RAFTolerancePct})
		if err != nil {
			return err
		}
		switch t.Command {
		case domain.CmdEnd:
			if err := domain.CheckEnd(from, h.qtyRAF, t.ZeroOutputDuty); err != nil {
				return domainProblem(err)
			}
			if prm.AsBuiltRequired {
				n, err := countAttachedAsBuilt(ctx, tx, p.TenantID, h.id)
				if err != nil {
					return err
				}
				if err := domain.CheckAsBuiltRequired(true, h.qtyRAF, n); err != nil {
					return domainProblem(err)
				}
			}
			if err := settleOrder(ctx, tx, p.TenantID, h, t.Actor, prm.ReworkCapitalize); err != nil {
				return err
			}
		case domain.CmdStart:
			if err := startOrder(ctx, tx, p.TenantID, &h, t.Actor, "sys:start:"+t.IdempotencyKey); err != nil {
				return err
			}
		case domain.CmdReportFinished:
			if err := domain.CheckReportFinished(h.quantity, h.qtyRAF, h.qtyError, t.Good, t.Error, prm.RAFTolerancePct); err != nil {
				return domainProblem(err)
			}
			raf := NewJournal{Type: domain.JournalReportFinished, Proposal: domain.ProposeProportional, QtyGood: t.Good, QtyError: t.Error,
				CoProductsProportional: true}
			if err := autoPost(ctx, tx, p.TenantID, &h, raf, t.Actor, "sys:raf:"+t.IdempotencyKey, prm.RAFTolerancePct); err != nil {
				return err
			}
		case domain.CmdEstimate:
			if err := replaceEstimate(ctx, tx, p.TenantID, t.OrderID); err != nil {
				return err
			}
		case domain.CmdSchedule:
			sched, err = scheduleOrder(ctx, tx, p.TenantID, h, t)
			if err != nil {
				return err
			}
		case domain.CmdResetStatus:
			// FR-21.9: a reset reverses what the statuses it leaves posted.
			if types := reversedBy(from, to); len(types) > 0 {
				if err := checkNoSplitTransfer(ctx, tx, p.TenantID, t.OrderID, types); err != nil {
					return err
				}
				if err := reverseJournals(ctx, tx, p.TenantID, &h, types, t.Actor, "sys:reset:"+t.IdempotencyKey); err != nil {
					return err
				}
				// BR-21.14: the reversal takes back the reports as finished (and, before
				// Started, the consumption), so the as-built records detach.
				if err := detachOrderAsBuilt(ctx, tx, p.TenantID, t.OrderID, t.Actor); err != nil {
					return err
				}
			}
			if to == domain.Created {
				if _, err := tx.Exec(ctx, `delete from production_cost_estimate where tenant_id = $1 and order_id = $2`, p.TenantID, t.OrderID); err != nil {
					return err
				}
			}
		}
		// A reset to Estimated or Created, or a cancel, takes the schedule back: the jobs and the
		// dates go, so the capacity they held is free again (wave 23 reads the jobs).
		if from != to && (to == domain.Created || to == domain.Estimated || to == domain.Cancelled) {
			if err := clearSchedule(ctx, tx, p.TenantID, t.OrderID); err != nil {
				return err
			}
		}
		if to == domain.Cancelled {
			// FR-21.10: a cancelled order withdraws the vendor POs it asked for.
			requested, err := requestedSubcontracts(ctx, tx, p.TenantID, t.OrderID)
			if err != nil {
				return err
			}
			if err := settleSubcontracts(ctx, tx, p.TenantID, t.OrderID, t.Actor, requested, true); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update production_order set status = $3, qty_raf = $4, qty_error = $5, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, t.OrderID, string(to), h.qtyRAF.String(), h.qtyError.String()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into production_status_log (tenant_id, id, legal_entity_id, order_id, command, from_status, to_status, actor, trace_id, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, p.TenantID, ids.New(), h.legalEntityID, t.OrderID, string(t.Command),
			string(from), string(to), t.Actor, t.TraceID, t.IdempotencyKey, t.RequestHash); err != nil {
			return err
		}
		// FR-21.10: releasing asks purchasing for the vendor PO of each subcontract
		// operation not requested yet (a reset back to Released requests nothing new).
		var requests []prodevents.SubcontractPORequestedData
		if to == domain.Released && from != to {
			if requests, err = claimSubcontractRequests(ctx, tx, p.TenantID, t.OrderID, t.Actor); err != nil {
				return err
			}
		}
		out, err = loadOrder(ctx, tx, p.TenantID, t.OrderID)
		if err != nil {
			return err
		}
		if from == to && t.Command != domain.CmdReportFinished {
			return nil // a re-estimate changes cost rows, not the status
		}
		if err := enqueue(ctx, tx, t.OrderID, prodevents.OrderStatusChanged, p.TenantID, t.Actor, prodevents.OrderStatusChangedData{
			OrderID: t.OrderID, From: string(from), To: string(to), Quantity: out.Quantity.String(), QtyRAF: out.QtyRAF.String(),
			QtyError: out.QtyError.String(), StartDate: out.StartDate, EndDate: out.EndDate, Actor: t.Actor,
			OccurredAt: out.UpdatedAt.UTC().Format(time.RFC3339Nano),
		}); err != nil {
			return err
		}
		if sched != nil {
			if err := enqueueScheduled(ctx, tx, p.TenantID, out, t.Actor, "schedule", scheduleParams(t).Direction, *sched); err != nil {
				return err
			}
		}
		return enqueueSubcontractRequests(ctx, tx, p.TenantID, t.OrderID, t.Actor, requests)
	})
	return out, replayed, err
}

// reversedBy lists the journal types a reset from `from` to `to` reverses:
// leaving ReportedAsFinished reverses the reports as finished, and going back
// before Started reverses every journal.
func reversedBy(from, to domain.Status) []domain.JournalType {
	started := from == domain.Started || from == domain.ReportedAsFinished
	switch {
	case started && to != domain.Started && to != domain.ReportedAsFinished:
		return []domain.JournalType{domain.JournalPickingList, domain.JournalRouteCard, domain.JournalJobCard, domain.JournalReportFinished}
	case from == domain.ReportedAsFinished:
		return []domain.JournalType{domain.JournalReportFinished}
	}
	return nil
}

// replaceEstimate recomputes planned cost from the stored lines and operations
// and replaces the cost rows of the order.
func replaceEstimate(ctx context.Context, tx pgx.Tx, tenantID, orderID string) error {
	lines, err := loadLines(ctx, tx, tenantID, orderID)
	if err != nil {
		return err
	}
	ops, err := loadOperations(ctx, tx, tenantID, orderID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `delete from production_cost_estimate where tenant_id = $1 and order_id = $2`, tenantID, orderID); err != nil {
		return err
	}
	for _, c := range domain.Estimate(lines, ops) {
		if _, err := tx.Exec(ctx, `insert into production_cost_estimate (tenant_id, order_id, cost_group, resource_key, planned_qty, planned_amount, fixed_amount)
			values ($1, $2, $3, $4, $5, $6, $7)`, tenantID, orderID, string(c.Group), c.Key, c.Qty.String(), c.Amount.String(), c.Fixed.String()); err != nil {
			return err
		}
	}
	return nil
}

// domainProblem maps a domain rule violation to a problem: an illegal
// transition, a locked field, an as-built change or an order edit in the wrong
// status, an operation command on an order or operation that takes no
// feedback, or an edit that collides with the order's lines, operations or
// journals is a 409 conflict with the order's state, the other rules are 422.
func domainProblem(err error) error {
	var de *domain.Error
	if !errors.As(err, &de) {
		return err
	}
	status := http.StatusUnprocessableEntity
	switch de.Code {
	case domain.CodeIllegalTransition, domain.CodeNotEditable, domain.CodeAsBuiltState,
		domain.CodeEditNotAllowed, domain.CodeEditTargetExists, domain.CodeEditTargetInUse, domain.CodeOperationState, domain.CodeChangeNotEligible,
		domain.CodeChangeSuperseded, domain.CodeSplitRefused, domain.CodeFiniteState, domain.CodeFiniteLocked:
		status = http.StatusConflict
	}
	return httpx.NewProblem(status, de.Code, de.Detail)
}

func loadOrder(ctx context.Context, q querier, tenantID, id string) (Order, error) {
	var o Order
	var status, qty, raf, qerr string
	var refType, refID *string
	err := q.QueryRow(ctx, `select id::text, legal_entity_id::text, order_number, product_id::text, site_id::text, warehouse_id::text, currency::text,
		quantity::text, qty_raf::text, qty_error::text, status, delivery_date::text, origin_type, origin_id::text, version, created_at, updated_at,
		item_group, rework_reference_type, rework_reference_id, split_from_order_id::text, merged_into_order_id::text, order_bom_modified,
		configuration_id::text, start_date::text, end_date::text, scheduling_method, schedule_basis, finite_run_id, finite_scheduled_at, finite_compressed_opers,
		quota_arrangement_id::text, quota_source_id::text
		from production_order where tenant_id = $1 and id = $2`, tenantID, id).Scan(&o.ID, &o.LegalEntityID, &o.OrderNumber, &o.ProductID,
		&o.SiteID, &o.WarehouseID, &o.Currency, &qty, &raf, &qerr, &status, &o.DeliveryDate, &o.OriginType, &o.OriginID, &o.Version,
		&o.CreatedAt, &o.UpdatedAt, &o.ItemGroup, &refType, &refID, &o.SplitFromOrderID, &o.MergedIntoOrderID, &o.OrderBOMModified, &o.ConfigurationID,
		&o.StartDate, &o.EndDate, &o.SchedulingMethod, &o.ScheduleBasis, &o.FiniteRunID, &o.FiniteScheduledAt, &o.FiniteCompressedOperations,
		&o.QuotaArrangementID, &o.QuotaSourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, notFound()
	}
	if err != nil {
		return Order{}, err
	}
	o.Status = domain.Status(status)
	o.Quantity, o.QtyRAF, o.QtyError = dec(qty), dec(raf), dec(qerr)
	o.ReworkReference = reworkRef(refType, refID)
	if o.Lines, err = loadLines(ctx, q, tenantID, id); err != nil {
		return Order{}, err
	}
	if o.Operations, err = loadOperations(ctx, q, tenantID, id); err != nil {
		return Order{}, err
	}
	if o.CoProducts, err = loadCoProducts(ctx, q, tenantID, id); err != nil {
		return Order{}, err
	}
	o.Costs, err = loadCosts(ctx, q, tenantID, id)
	return o, err
}

// loadLines returns the BOM lines in explosion order (the BOM's line order,
// phantom children in place of their parent).
func loadLines(ctx context.Context, q querier, tenantID, orderID string) ([]domain.OrderLine, error) {
	rows, err := q.Query(ctx, `select line_number, item_id::text, qty_per::text, series_size::text, scrap_pct::text, round_up, qty_required::text, unit_cost_est::text,
		flushing_principle, qty_consumed::text
		from production_bom_line where tenant_id = $1 and order_id = $2 order by line_seq`, tenantID, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.OrderLine{}
	for rows.Next() {
		var l domain.OrderLine
		var qtyPer, series, scrap, req, cost, flushing, consumed string
		if err := rows.Scan(&l.LineNo, &l.ItemID, &qtyPer, &series, &scrap, &l.RoundUp, &req, &cost, &flushing, &consumed); err != nil {
			return nil, err
		}
		l.QtyPer, l.SeriesSize, l.ScrapPct, l.QtyRequired, l.UnitCost = dec(qtyPer), dec(series), dec(scrap), dec(req), dec(cost)
		l.Flushing, l.QtyConsumed = domain.Flushing(flushing), dec(consumed)
		out = append(out, l)
	}
	return out, rows.Err()
}

func loadOperations(ctx context.Context, q querier, tenantID, orderID string) ([]domain.Operation, error) {
	rows, err := q.Query(ctx, `select oper_number, resource_id::text, setup_time_h::text, run_time_h_per::text, process_qty::text, scrap_pct::text,
		labor_rate::text, overhead_rate::text, subcontract, subcontract_price::text, hours_reported::text, qty_good::text, qty_error::text, status,
		po_requested_at, po_cancelled_at, queue_before_h::text, queue_after_h::text, transit_h::text, planned_start, planned_end
		from production_operation where tenant_id = $1 and order_id = $2 order by oper_number`, tenantID, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Operation{}
	for rows.Next() {
		var op domain.Operation
		var setup, run, proc, scrap, labor, oh, sub, hours, good, bad, status, qb, qa, transit string
		if err := rows.Scan(&op.OperNumber, &op.ResourceID, &setup, &run, &proc, &scrap, &labor, &oh, &op.Subcontract, &sub,
			&hours, &good, &bad, &status, &op.PORequestedAt, &op.POCancelledAt, &qb, &qa, &transit, &op.PlannedStart, &op.PlannedEnd); err != nil {
			return nil, err
		}
		op.QueueBeforeH, op.QueueAfterH, op.TransitH = dec(qb), dec(qa), dec(transit)
		op.HoursReported, op.QtyGood, op.QtyError, op.Status = dec(hours), dec(good), dec(bad), domain.OperStatus(status)
		op.SetupH, op.RunHPer, op.ProcessQty, op.ScrapPct = dec(setup), dec(run), dec(proc), dec(scrap)
		op.LaborRate, op.OverheadRate, op.SubcontractPrice = dec(labor), dec(oh), dec(sub)
		out = append(out, op)
	}
	return out, rows.Err()
}

func loadCosts(ctx context.Context, q querier, tenantID, orderID string) ([]domain.CostRow, error) {
	rows, err := q.Query(ctx, `select cost_group, resource_key, planned_qty::text, planned_amount::text, fixed_amount::text
		from production_cost_estimate where tenant_id = $1 and order_id = $2 order by cost_group, resource_key`, tenantID, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.CostRow{}
	for rows.Next() {
		var c domain.CostRow
		var g, qty, amt, fixed string
		if err := rows.Scan(&g, &c.Key, &qty, &amt, &fixed); err != nil {
			return nil, err
		}
		c.Group, c.Qty, c.Amount, c.Fixed = domain.CostGroup(g), dec(qty), dec(amt), dec(fixed)
		out = append(out, c)
	}
	return out, rows.Err()
}

func reworkRef(refType, refID *string) *domain.ReworkRef {
	if refType == nil || refID == nil {
		return nil
	}
	return &domain.ReworkRef{Type: domain.ReworkRefType(*refType), ID: *refID}
}
