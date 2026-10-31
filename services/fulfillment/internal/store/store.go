// Package store persists shipments, pick and pack quantities, containers and
// packing slips. Every command runs in a tenant-scoped transaction, writes its
// outbox events in the same transaction, and replays Idempotency-Key requests
// from idempotency_keys.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
)

// Error codes returned by the store (service catalog FUL-*).
const (
	CodeShipmentNotFound   = "FUL-0020"
	CodeStatus             = "FUL-0001"
	CodeOverPick           = "FUL-0004"
	CodeInvalidMove        = "FUL-0021"
	CodeOverPack           = "FUL-0023"
	CodeNegativeQty        = "FUL-0030"
	CodeSlipNotFound       = "FUL-0040"
	CodeLineNotFound       = "FUL-0041"
	CodeCostMissing        = "FUL-0042"
	CodeSlipNotCancellable = "FUL-0043"
	CodeNothingToShip      = "FUL-0044"
	CodeSlipInvoiced       = "FUL-0010"
	CodeWeightExceeded     = "FUL-0013"
	CodeRateMissing        = "FUL-0014"
	CodeRTPNegative        = "FUL-0015"
	CodeValidation         = "FUL-0050"
	CodeSSCCInvalid        = "FUL-0007"
	CodePickBelowPacked    = "FUL-0031"
	CodeWaveLimits         = "FUL-0032"
	CodeGroupBy            = "FUL-0033"
	CodeWaveTooLarge       = "FUL-0034"
	CodeWaveNotFound       = "FUL-0052"
	CodeWaveConflict       = "FUL-0053"
	CodeContainerNotFound  = "FUL-0054"
	CodeVersion            = "conflict_version"
	CodeIdempotencyReuse   = "idempotency_key_reuse"
	eventSource            = "fulfillment"
)

// Shipment is the aggregate returned by the API.
type Shipment struct {
	ID              string                `json:"id"`
	ShipmentNumber  string                `json:"shipmentNumber"`
	LegalEntityID   string                `json:"legalEntityId"`
	WarehouseID     string                `json:"warehouseId"`
	OrderID         string                `json:"orderId"`
	CustomerID      string                `json:"customerId"`
	Status          domain.ShipmentStatus `json:"status"`
	PlannedShipDate time.Time             `json:"plannedShipDate"`
	ActualShipDate  *time.Time            `json:"actualShipDate"`
	CarrierCode     *string               `json:"carrierCode"`
	Version         int                   `json:"version"`
	Lines           []ShipmentLine        `json:"lines"`
	Containers      []Container           `json:"containers"`
}

// ShipmentSummary is one row of the shipments list (delivery monitor, section 5.28.14).
type ShipmentSummary struct {
	ID              string                `json:"id"`
	ShipmentNumber  string                `json:"shipmentNumber"`
	LegalEntityID   string                `json:"legalEntityId"`
	WarehouseID     string                `json:"warehouseId"`
	OrderID         string                `json:"orderId"`
	CustomerID      string                `json:"customerId"`
	Status          domain.ShipmentStatus `json:"status"`
	PlannedShipDate time.Time             `json:"plannedShipDate"`
	ActualShipDate  *time.Time            `json:"actualShipDate"`
	CarrierCode     *string               `json:"carrierCode"`
	Version         int                   `json:"version"`
	LineCount       int                   `json:"lineCount"`
}

// ShipmentFilter narrows the shipments list. Empty fields do not filter.
type ShipmentFilter struct {
	Status      string
	WarehouseID string
	OrderID     string
	Limit       int
}

// PackingSlipFilter narrows the packing slip journal. Empty fields do not filter.
type PackingSlipFilter struct {
	ShipmentID string
	OrderID    string
	Status     string
	Limit      int
}

// ShipmentLine is one schedule line of a shipment with its pick and pack quantities.
type ShipmentLine struct {
	ID            string          `json:"id"`
	OrderLineID   string          `json:"orderLineId"`
	ProductID     string          `json:"productId"`
	Unit          string          `json:"unit"`
	Planned       decimal.Decimal `json:"qtyPlanned"`
	Picked        decimal.Decimal `json:"qtyPicked"`
	Packed        decimal.Decimal `json:"qtyPacked"`
	Shipped       decimal.Decimal `json:"qtyShipped"`
	Closed        decimal.Decimal `json:"qtyClosed"`
	PickConfirmed bool            `json:"pickConfirmed"`
	Zone          *string         `json:"zone"`
	// ShortPickReason is the reason given for the latest short pick, if any.
	ShortPickReason *string `json:"shortPickReason"`
	// TrackingRequired is set at release for a batch or serial tracked product
	// (BR-28.10); Tracking lists the batch, serial and location entries.
	TrackingRequired bool            `json:"trackingRequired"`
	Tracking         []TrackingEntry `json:"tracking"`
}

// Container is a closed package with its SSCC (BR-28.5). Tare, returnable and
// packaging material are copied from the container type when it is packed;
// gross weight is the tare plus the unit weights the packer supplied.
type Container struct {
	ID                string          `json:"id"`
	ContainerType     string          `json:"containerType"`
	SSCC              string          `json:"sscc"`
	Status            string          `json:"status"`
	TareKg            decimal.Decimal `json:"tareKg"`
	GrossKg           decimal.Decimal `json:"grossKg"`
	Returnable        bool            `json:"returnable"`
	PackagingMaterial *string         `json:"packagingMaterial"`
	// PackingSlipID is the slip the container shipped with; null until then.
	PackingSlipID *string         `json:"packingSlipId"`
	Lines         []ContainerLine `json:"lines"`
}

// ContainerLine is a quantity of one shipment line packed into a container.
type ContainerLine struct {
	ShipmentLineID string          `json:"shipmentLineId"`
	Qty            decimal.Decimal `json:"qty"`
}

// PackingSlip is a posted (or reversing) delivery document.
type PackingSlip struct {
	ID           string            `json:"id"`
	Number       string            `json:"packingSlipNumber"`
	ShipmentID   string            `json:"shipmentId"`
	OrderID      string            `json:"orderId"`
	Status       string            `json:"status"`
	PostingDate  time.Time         `json:"postingDate"`
	Currency     string            `json:"currency"`
	TotalCost    decimal.Decimal   `json:"totalCost"`
	ReversalOfID *string           `json:"reversalOfId"`
	Lines        []PackingSlipLine `json:"lines"`
	Journal      []JournalEntry    `json:"journal"`
	// PackagingFees are the statistical packaging fee lines (BR-28.12). They
	// are not journal lines; a reversing slip carries them negated.
	PackagingFees []PackagingFeeLine `json:"packagingFees"`
}

// PackingSlipSummary is one row of the packing slip journal (section 5.28.14).
type PackingSlipSummary struct {
	ID           string          `json:"id"`
	Number       string          `json:"packingSlipNumber"`
	ShipmentID   string          `json:"shipmentId"`
	OrderID      string          `json:"orderId"`
	Status       string          `json:"status"`
	PostingDate  time.Time       `json:"postingDate"`
	Currency     string          `json:"currency"`
	TotalCost    decimal.Decimal `json:"totalCost"`
	ReversalOfID *string         `json:"reversalOfId"`
}

// JournalEntry is one signed journal line as stored on the slip and returned
// by the API: debits positive, credits negative (BR-28.1).
type JournalEntry struct {
	Account     string          `json:"account"`
	Amount      decimal.Decimal `json:"amount"`
	Currency    string          `json:"currency"`
	OrderLineID string          `json:"orderLineId"`
}

// ReleaseResult is the outcome of a release run: the shipments created and the
// lines the store skipped because they are already on an open shipment.
type ReleaseResult struct {
	Shipments []Shipment    `json:"shipments"`
	Skipped   []domain.Skip `json:"skipped"`
}

// PackingSlipLine is one shipped line at its cost snapshot.
type PackingSlipLine struct {
	OrderLineID    string          `json:"orderLineId"`
	ShipmentLineID string          `json:"shipmentLineId"`
	Qty            decimal.Decimal `json:"qty"`
	CostAmount     decimal.Decimal `json:"costAmount"`
	CostSnapshotID string          `json:"costSnapshotId"`
}

// PackResult is the response of a pack command.
type PackResult struct {
	Container Container `json:"container"`
	Shipment  Shipment  `json:"shipment"`
}

// PickInput confirms the picked quantity of one shipment line.
type PickInput struct {
	ShipmentLineID string
	Picked         decimal.Decimal
	// Reason is the short pick reason code; only valid when Picked is below plan.
	Reason string
}

// PackInput moves a quantity of one shipment line into the container.
type PackInput struct {
	ShipmentLineID string
	Qty            decimal.Decimal
}

// Cost is the unit cost snapshot used for a line when the slip posts (BR-28.2).
type Cost struct {
	UnitCost       decimal.Decimal
	CostSnapshotID string
}

// PostInput is the packing-slip posting request.
type PostInput struct {
	PostingDate    time.Time
	Currency       string
	CogsAtShipment bool
	Costs          map[string]Cost // keyed by shipment line id
	// CountryCode is the ship-to country whose packaging fee rates apply
	// (FR-28.14). Empty posts no packaging fee.
	CountryCode string
	// EmitASN publishes the advance ship notice of the containers that ship
	// with this slip (FR-28.9).
	EmitASN bool
}

// Idem is the Idempotency-Key and the hash of the request body.
type Idem struct {
	Key  string
	Hash string
}

// Store is the persistence contract the API depends on.
type Store interface {
	RunRelease(ctx context.Context, warehouseID string, groups []domain.ReleaseGroup, idem Idem) (ReleaseResult, error)
	ListShipments(ctx context.Context, f ShipmentFilter) ([]ShipmentSummary, error)
	GetShipment(ctx context.Context, id string) (Shipment, error)
	ListPackingSlips(ctx context.Context, f PackingSlipFilter) ([]PackingSlipSummary, error)
	GetPackingSlip(ctx context.Context, id string) (PackingSlip, error)
	ConfirmPick(ctx context.Context, id string, version int, picks []PickInput, idem Idem) (Shipment, error)
	PackContainer(ctx context.Context, id string, version int, containerType string, lines []PackInput, idem Idem) (PackResult, error)
	PostPackingSlip(ctx context.Context, id string, version int, in PostInput, idem Idem) (PackingSlip, error)
	CancelPackingSlip(ctx context.Context, slipID string, idem Idem) (PackingSlip, error)
	// DeliveredNotInvoiced is the DNI report (FR-28.12, inquiry.go).
	DeliveredNotInvoiced(ctx context.Context, f DNIFilter) ([]domain.DNIRow, error)
	// ListSSCCs is the SSCC register (BR-28.5, sscc.go).
	ListSSCCs(ctx context.Context, f SSCCFilter) ([]SSCCRow, error)
	// ListBackorders is the short picks and backorders report (FR-28.2, backorders.go).
	ListBackorders(ctx context.Context, f BackorderFilter) ([]BackorderRow, error)
	PackingStore
	WorkbenchStore
	WavePickStore
	TrackingStore
	Ping(ctx context.Context) error
}

// Postgres implements Store. gs1Prefix and extension drive SSCC allocation.
type Postgres struct {
	pool      *pgxpool.Pool
	gs1Prefix string
	extension int
}

// New returns a Postgres store that issues SSCCs under gs1Prefix.
func New(pool *pgxpool.Pool, gs1Prefix string, extension int) *Postgres {
	return &Postgres{pool: pool, gs1Prefix: gs1Prefix, extension: extension}
}

// Ping checks the database connection for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// runIdempotent replays a stored response for a known key, refuses a key that
// was used with a different body, or runs the command and stores its response.
// It runs inside the command transaction, so the record and the work commit together.
func runIdempotent[T any](ctx context.Context, tx pgx.Tx, tenantID string, idem Idem, run func() (T, error)) (T, error) {
	var zero T
	// Serialise requests that share a key, so a concurrent retry waits for the
	// first one and then replays it instead of running the command twice.
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1 || '/idem/' || $2, 0))`, tenantID, idem.Key); err != nil {
		return zero, err
	}
	var storedHash string
	var body []byte
	err := tx.QueryRow(ctx, `select request_hash, response_body from idempotency_keys where tenant_id = $1 and key = $2`, tenantID, idem.Key).Scan(&storedHash, &body)
	if err == nil {
		if storedHash != idem.Hash {
			return zero, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
		}
		var out T
		if err := json.Unmarshal(body, &out); err != nil {
			return zero, err
		}
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}
	out, err := run()
	if err != nil {
		return zero, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return zero, err
	}
	if _, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status, response_body) values ($1, $2, $3, 200, $4)`, tenantID, idem.Key, idem.Hash, raw); err != nil {
		return zero, err
	}
	return out, nil
}

// nextNumber allocates the next value of a gap-free sequence inside tx. A
// rollback also rolls back the increment, so numbers stay gap-free.
func nextNumber(ctx context.Context, tx pgx.Tx, tenantID, sequenceID string) (int64, error) {
	var v int64
	err := tx.QueryRow(ctx, `insert into number_counter (tenant_id, sequence_id, next_value) values ($1, $2, 2)
		on conflict (tenant_id, sequence_id) do update set next_value = number_counter.next_value + 1
		returning next_value - 1`, tenantID, sequenceID).Scan(&v)
	return v, err
}

func enqueue(ctx context.Context, tx pgx.Tx, aggregateID, eventType, tenantID string, data any) error {
	return enqueueFor(ctx, tx, "shipment", aggregateID, eventType, tenantID, data)
}

// enqueueFor writes an event of an aggregate other than the shipment (a wave).
func enqueueFor(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType, tenantID string, data any) error {
	evt, err := events.New(eventType, eventSource, aggregateID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

func dec(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}

// mapDomain converts a domain rule failure into a problem with its catalog code.
func mapDomain(err error) error {
	switch {
	case errors.Is(err, domain.ErrOverPick):
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeOverPick, err.Error())
	case errors.Is(err, domain.ErrOverPack):
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeOverPack, err.Error())
	case errors.Is(err, domain.ErrNegativeQty):
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeNegativeQty, err.Error())
	case errors.Is(err, domain.ErrPickBelowPacked):
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodePickBelowPacked, err.Error())
	case errors.Is(err, domain.ErrWaveLimits):
		return httpx.NewProblem(http.StatusBadRequest, CodeWaveLimits, err.Error())
	case errors.Is(err, domain.ErrGroupBy):
		return httpx.NewProblem(http.StatusBadRequest, CodeGroupBy, err.Error())
	case errors.Is(err, domain.ErrWaveTooLarge):
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeWaveTooLarge, err.Error())
	case errors.Is(err, domain.ErrInvalidMove):
		return httpx.NewProblem(http.StatusConflict, CodeInvalidMove, err.Error())
	case errors.Is(err, domain.ErrContainerWeight):
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeWeightExceeded, err.Error())
	case errors.Is(err, domain.ErrRuleInvalid), errors.Is(err, domain.ErrSerialFraction):
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeValidation, err.Error())
	}
	return err
}

func statusProblem(action string, status domain.ShipmentStatus) error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeStatus, fmt.Sprintf("cannot %s a shipment in status %s", action, status))
}

func checkVersion(sh Shipment, version int) error {
	if sh.Version != version {
		return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersion, fmt.Sprintf("shipment is at version %d, request sent %d", sh.Version, version))
	}
	return nil
}

func lineIndex(sh Shipment, lineID string) int {
	for i, l := range sh.Lines {
		if l.ID == lineID {
			return i
		}
	}
	return -1
}

func lineQty(l ShipmentLine) domain.LineQty {
	return domain.LineQty{Planned: l.Planned, Picked: l.Picked, Packed: l.Packed, Shipped: l.Shipped, Confirmed: l.PickConfirmed}
}

func lineQtys(sh Shipment) []domain.LineQty {
	out := make([]domain.LineQty, 0, len(sh.Lines))
	for _, l := range sh.Lines {
		out = append(out, lineQty(l))
	}
	return out
}

// bumpShipment moves the shipment to next if its version still matches, which
// makes concurrent commands fail with 412 rather than overwrite each other. The
// actual ship date is the first shipment of the shipment; it stays while any
// quantity is shipped (a partial shipment re-picking its remainder) and clears
// when the last slip is cancelled.
func bumpShipment(ctx context.Context, tx pgx.Tx, tenantID string, sh *Shipment, next domain.ShipmentStatus) error {
	tag, err := tx.Exec(ctx, `update shipment set status = $4::text, version = version + 1, updated_at = now(),
		actual_ship_date = case
			when $4::text in ('SHIPPED', 'PARTIALLY_SHIPPED', 'DELIVERED') then coalesce(actual_ship_date, now())
			when exists (select 1 from shipment_line sl where sl.tenant_id = $1 and sl.shipment_id = $2 and sl.qty_shipped > 0) then actual_ship_date
			else null end
		where tenant_id = $1 and id = $2 and version = $3`, tenantID, sh.ID, sh.Version, string(next))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersion, "shipment changed since it was read; reload and retry")
	}
	sh.Status = next
	sh.Version++
	return nil
}

// RunRelease creates one shipment per release group. Runs for one warehouse are
// serialised, and a line already on a shipment that is not cancelled is
// skipped as already_released, so a repeated run never releases a line twice.
func (s *Postgres) RunRelease(ctx context.Context, warehouseID string, groups []domain.ReleaseGroup, idem Idem) (ReleaseResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out ReleaseResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (ReleaseResult, error) {
			if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1 || '/release/' || $2, 0))`, p.TenantID, warehouseID); err != nil {
				return ReleaseResult{}, err
			}
			released, err := releasedLines(ctx, tx, groups)
			if err != nil {
				return ReleaseResult{}, err
			}
			result := ReleaseResult{Shipments: []Shipment{}, Skipped: []domain.Skip{}}
			for _, g := range groups {
				open := g
				open.Lines = nil
				for _, l := range g.Lines {
					if released[l.OrderLineID] {
						result.Skipped = append(result.Skipped, domain.Skip{OrderLineID: l.OrderLineID, Reason: domain.SkipAlreadyReleased})
						continue
					}
					open.Lines = append(open.Lines, l)
				}
				if len(open.Lines) == 0 {
					continue
				}
				sh, err := insertReleased(ctx, tx, p.TenantID, open)
				if err != nil {
					return ReleaseResult{}, err
				}
				result.Shipments = append(result.Shipments, sh)
			}
			return result, nil
		})
		out = res
		return err
	})
	return out, err
}

// releasedLines returns the order lines of groups that already sit on a
// shipment that is not cancelled.
func releasedLines(ctx context.Context, tx pgx.Tx, groups []domain.ReleaseGroup) (map[string]bool, error) {
	var lineIDs []string
	for _, g := range groups {
		for _, l := range g.Lines {
			lineIDs = append(lineIDs, l.OrderLineID)
		}
	}
	out := map[string]bool{}
	if len(lineIDs) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `select distinct sl.order_line_id::text from shipment_line sl
		join shipment s on s.tenant_id = sl.tenant_id and s.id = sl.shipment_id
		where sl.order_line_id = any($1::uuid[]) and s.status <> 'CANCELLED'`, lineIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ListShipments returns shipments newest first, filtered by status, warehouse and order.
func (s *Postgres) ListShipments(ctx context.Context, f ShipmentFilter) ([]ShipmentSummary, error) {
	out := []ShipmentSummary{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select s.id, s.shipment_number, s.legal_entity_id, s.warehouse_id, s.order_id, s.customer_id, s.status,
				s.planned_ship_date, s.actual_ship_date, s.carrier_code, s.version,
				(select count(*) from shipment_line sl where sl.tenant_id = s.tenant_id and sl.shipment_id = s.id)
			from shipment s
			where ($1 = '' or s.status = $1)
			  and ($2 = '' or s.warehouse_id::text = $2)
			  and ($3 = '' or s.order_id::text = $3)
			order by s.created_at desc, s.shipment_number desc
			limit $4`, f.Status, f.WarehouseID, f.OrderID, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r ShipmentSummary
			var status string
			if err := rows.Scan(&r.ID, &r.ShipmentNumber, &r.LegalEntityID, &r.WarehouseID, &r.OrderID, &r.CustomerID, &status,
				&r.PlannedShipDate, &r.ActualShipDate, &r.CarrierCode, &r.Version, &r.LineCount); err != nil {
				return err
			}
			r.Status = domain.ShipmentStatus(status)
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// ListPackingSlips returns packing slips newest first (packing slip journal).
func (s *Postgres) ListPackingSlips(ctx context.Context, f PackingSlipFilter) ([]PackingSlipSummary, error) {
	out := []PackingSlipSummary{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id, packing_slip_number, shipment_id, order_id, status, posting_date, currency, total_cost::text, reversal_of_id
			from packing_slip
			where ($1 = '' or shipment_id::text = $1)
			  and ($2 = '' or order_id::text = $2)
			  and ($3 = '' or status = $3)
			order by created_at desc, packing_slip_number desc
			limit $4`, f.ShipmentID, f.OrderID, f.Status, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r PackingSlipSummary
			var total string
			if err := rows.Scan(&r.ID, &r.Number, &r.ShipmentID, &r.OrderID, &r.Status, &r.PostingDate, &r.Currency, &total, &r.ReversalOfID); err != nil {
				return err
			}
			r.TotalCost = dec(total)
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetPackingSlip returns one packing slip with its lines and journal.
func (s *Postgres) GetPackingSlip(ctx context.Context, id string) (PackingSlip, error) {
	var slip PackingSlip
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		slip, err = loadSlip(ctx, tx, id)
		return err
	})
	return slip, err
}

func insertReleased(ctx context.Context, tx pgx.Tx, tenantID string, g domain.ReleaseGroup) (Shipment, error) {
	first := g.Lines[0]
	seq, err := nextNumber(ctx, tx, tenantID, "SH")
	if err != nil {
		return Shipment{}, err
	}
	sh := Shipment{
		ID: ids.New(), ShipmentNumber: fmt.Sprintf("SH-%07d", seq), LegalEntityID: first.LegalEntityID,
		WarehouseID: g.WarehouseID, OrderID: g.OrderID, CustomerID: first.CustomerID,
		Status: domain.StatusReleased, PlannedShipDate: g.ShipDate, Version: 1,
		Lines: []ShipmentLine{}, Containers: []Container{},
	}
	// The API refuses lines of one order with different carriers, so any line names it.
	for _, l := range g.Lines {
		if l.CarrierCode != "" {
			sh.CarrierCode = optional(l.CarrierCode)
			break
		}
	}
	if _, err := tx.Exec(ctx, `insert into shipment (tenant_id, id, shipment_number, legal_entity_id, warehouse_id, order_id, customer_id, status, planned_ship_date, carrier_code)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, tenantID, sh.ID, sh.ShipmentNumber, sh.LegalEntityID, sh.WarehouseID, sh.OrderID, sh.CustomerID,
		string(sh.Status), sh.PlannedShipDate, sh.CarrierCode); err != nil {
		return Shipment{}, err
	}
	refs := []fulevents.LineRef{}
	for _, l := range g.Lines {
		line := ShipmentLine{ID: ids.New(), OrderLineID: l.OrderLineID, ProductID: l.ProductID, Unit: l.Unit, Planned: l.Qty,
			Picked: decimal.Zero, Packed: decimal.Zero, Shipped: decimal.Zero, Zone: optional(l.Zone),
			TrackingRequired: l.TrackingRequired, Tracking: []TrackingEntry{}}
		if _, err := tx.Exec(ctx, `insert into shipment_line (tenant_id, id, shipment_id, order_line_id, product_id, unit, qty_planned, zone_code, tracking_required)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, tenantID, line.ID, sh.ID, line.OrderLineID, line.ProductID, line.Unit, line.Planned, line.Zone, l.TrackingRequired); err != nil {
			return Shipment{}, err
		}
		sh.Lines = append(sh.Lines, line)
		refs = append(refs, fulevents.LineRef{OrderLineID: l.OrderLineID, Qty: l.Qty.String()})
	}
	if err := enqueue(ctx, tx, sh.ID, fulevents.ShipmentReleased, tenantID, fulevents.ShipmentReleasedData{
		ShipmentID: sh.ID, ShipmentNumber: sh.ShipmentNumber, OrderID: sh.OrderID, WarehouseID: sh.WarehouseID, Lines: refs,
	}); err != nil {
		return Shipment{}, err
	}
	return sh, nil
}

func (s *Postgres) GetShipment(ctx context.Context, id string) (Shipment, error) {
	var sh Shipment
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		sh, err = loadShipment(ctx, tx, id)
		return err
	})
	return sh, err
}

func loadShipment(ctx context.Context, tx pgx.Tx, id string) (Shipment, error) {
	var sh Shipment
	var status string
	err := tx.QueryRow(ctx, `select id, shipment_number, legal_entity_id, warehouse_id, order_id, customer_id, status, planned_ship_date, actual_ship_date, carrier_code, version
		from shipment where id = $1`, id).Scan(&sh.ID, &sh.ShipmentNumber, &sh.LegalEntityID, &sh.WarehouseID, &sh.OrderID, &sh.CustomerID, &status, &sh.PlannedShipDate,
		&sh.ActualShipDate, &sh.CarrierCode, &sh.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return sh, httpx.NewProblem(http.StatusNotFound, CodeShipmentNotFound, "shipment not found")
	}
	if err != nil {
		return sh, err
	}
	sh.Status = domain.ShipmentStatus(status)
	sh.Lines = []ShipmentLine{}
	rows, err := tx.Query(ctx, `select id, order_line_id, product_id, unit, qty_planned::text, qty_picked::text, qty_packed::text, qty_shipped::text, pick_confirmed, zone_code, qty_closed::text, short_pick_reason, tracking_required
		from shipment_line where shipment_id = $1 order by id`, id)
	if err != nil {
		return sh, err
	}
	for rows.Next() {
		var l ShipmentLine
		var planned, picked, packed, shipped, closed string
		if err := rows.Scan(&l.ID, &l.OrderLineID, &l.ProductID, &l.Unit, &planned, &picked, &packed, &shipped, &l.PickConfirmed, &l.Zone, &closed, &l.ShortPickReason, &l.TrackingRequired); err != nil {
			rows.Close()
			return sh, err
		}
		l.Planned, l.Picked, l.Packed, l.Shipped, l.Closed = dec(planned), dec(picked), dec(packed), dec(shipped), dec(closed)
		l.Tracking = []TrackingEntry{}
		sh.Lines = append(sh.Lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return sh, err
	}
	if err := loadTracking(ctx, tx, &sh); err != nil {
		return sh, err
	}
	sh.Containers, err = loadContainers(ctx, tx, id)
	return sh, err
}

func loadContainers(ctx context.Context, tx pgx.Tx, shipmentID string) ([]Container, error) {
	rows, err := tx.Query(ctx, `select id, container_type, sscc, status, tare_weight_kg::text, gross_weight_kg::text, returnable, packaging_material_code, packing_slip_id
		from container where shipment_id = $1 and status <> 'UNPACKED' order by sscc`, shipmentID)
	if err != nil {
		return nil, err
	}
	containers := []Container{}
	for rows.Next() {
		var c Container
		var tare, gross string
		if err := rows.Scan(&c.ID, &c.ContainerType, &c.SSCC, &c.Status, &tare, &gross, &c.Returnable, &c.PackagingMaterial, &c.PackingSlipID); err != nil {
			rows.Close()
			return nil, err
		}
		c.TareKg, c.GrossKg = dec(tare), dec(gross)
		c.Lines = []ContainerLine{}
		containers = append(containers, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range containers {
		lrows, err := tx.Query(ctx, `select shipment_line_id, qty::text from container_line where container_id = $1 order by shipment_line_id`, containers[i].ID)
		if err != nil {
			return nil, err
		}
		for lrows.Next() {
			var cl ContainerLine
			var qty string
			if err := lrows.Scan(&cl.ShipmentLineID, &qty); err != nil {
				lrows.Close()
				return nil, err
			}
			cl.Qty = dec(qty)
			containers[i].Lines = append(containers[i].Lines, cl)
		}
		lrows.Close()
	}
	return containers, nil
}

func (s *Postgres) ConfirmPick(ctx context.Context, id string, version int, picks []PickInput, idem Idem) (Shipment, error) {
	p, _ := tenant.FromContext(ctx)
	var out Shipment
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (Shipment, error) {
			sh, err := loadShipment(ctx, tx, id)
			if err != nil {
				return sh, err
			}
			if err := checkVersion(sh, version); err != nil {
				return sh, err
			}
			return sh, confirmPickTx(ctx, tx, p.TenantID, "", &sh, picks)
		})
		out = res
		return err
	})
	return out, err
}

// ShortPickReasons are the reason codes a short pick can carry (section
// 5.28.14, short picks report "reason" column).
var ShortPickReasons = []string{"OUT_OF_STOCK", "DAMAGED", "NOT_FOUND", "QUALITY_HOLD", "OTHER"}

// ValidShortPickReason reports whether code is a known reason ("" is allowed).
func ValidShortPickReason(code string) bool {
	if code == "" {
		return true
	}
	for _, r := range ShortPickReasons {
		if r == code {
			return true
		}
	}
	return false
}

// confirmPickTx applies the picks to a loaded shipment inside the caller's
// transaction: it records each line, advances the shipment to PICKING (PICKED
// once every line is confirmed) and writes pick.confirmed (FR-28.2, BR-28.7).
// The caller has checked the version. waveID is set when the picks come from a
// wave confirmation and is carried in the event.
func confirmPickTx(ctx context.Context, tx pgx.Tx, tenantID, waveID string, sh *Shipment, picks []PickInput) error {
	// A partially shipped shipment re-picks its remainder (FR-28.8).
	if !pickable(sh.Status) {
		return statusProblem("pick", sh.Status)
	}
	refs := []fulevents.PickLine{}
	for _, pk := range picks {
		idx := lineIndex(*sh, pk.ShipmentLineID)
		if idx < 0 {
			return httpx.NewProblem(http.StatusNotFound, CodeLineNotFound, "shipment line not found")
		}
		if !ValidShortPickReason(pk.Reason) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeValidation, "unknown short pick reason "+pk.Reason)
		}
		next, backorder, err := domain.ConfirmPick(lineQty(sh.Lines[idx]), pk.Picked)
		if err != nil {
			return mapDomain(err)
		}
		// A re-pick cannot drop below the batch and serial entries already
		// captured for the line (FUL-0035); the entries are corrected first.
		if err := domain.CheckTrackedWithinPicked(entryQty(sh.Lines[idx].Tracking), next.Picked); err != nil {
			return mapTrackingErr(err)
		}
		if pk.Reason != "" && !backorder.IsPositive() {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeValidation, "a reason applies to a short pick only")
		}
		l := sh.Lines[idx]
		// A pick below the planned quantity opens a backorder; the first
		// short pick dates it for the short-pick report (FR-28.2, backorders.go)
		// and a reason, when given, is kept (the last one wins).
		if _, err := tx.Exec(ctx, `update shipment_line set qty_picked = $3, pick_confirmed = true,
				short_picked_at = case when $4::boolean then coalesce(short_picked_at, now()) else short_picked_at end,
				short_pick_reason = case when $5::text <> '' then $5::text else short_pick_reason end
			where tenant_id = $1 and id = $2`,
			tenantID, l.ID, next.Picked, backorder.IsPositive(), pk.Reason); err != nil {
			return err
		}
		l.Picked, l.PickConfirmed = next.Picked, true
		if pk.Reason != "" {
			r := pk.Reason
			l.ShortPickReason = &r
		}
		sh.Lines[idx] = l
		ref := fulevents.PickLine{OrderLineID: l.OrderLineID, Qty: next.Picked.String(), Backordered: backorder.String()}
		if pk.Reason != "" {
			ref.ShortReason = pk.Reason
		}
		refs = append(refs, ref)
	}
	target, err := domain.Advance(sh.Status, domain.StatusPicking)
	if err != nil {
		return mapDomain(err)
	}
	if domain.AllConfirmed(lineQtys(*sh)) {
		if target, err = domain.Advance(target, domain.StatusPicked); err != nil {
			return mapDomain(err)
		}
	}
	if err := bumpShipment(ctx, tx, tenantID, sh, target); err != nil {
		return err
	}
	return enqueue(ctx, tx, sh.ID, fulevents.PickConfirmed, tenantID, fulevents.PickConfirmedData{ShipmentID: sh.ID, WaveID: waveID, Lines: refs})
}

func (s *Postgres) PackContainer(ctx context.Context, id string, version int, containerType string, lines []PackInput, idem Idem) (PackResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out PackResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (PackResult, error) {
			sh, err := loadShipment(ctx, tx, id)
			if err != nil {
				return PackResult{}, err
			}
			if err := checkVersion(sh, version); err != nil {
				return PackResult{}, err
			}
			if sh.Status != domain.StatusPicked && sh.Status != domain.StatusPacked {
				return PackResult{}, statusProblem("pack", sh.Status)
			}
			cont := Container{ID: ids.New(), ContainerType: containerType, Status: "CLOSED", Lines: []ContainerLine{}}
			for _, in := range lines {
				idx := lineIndex(sh, in.ShipmentLineID)
				if idx < 0 {
					return PackResult{}, httpx.NewProblem(http.StatusNotFound, CodeLineNotFound, "shipment line not found")
				}
				if err := domain.CanPack(lineQty(sh.Lines[idx]), in.Qty); err != nil {
					return PackResult{}, mapDomain(err)
				}
				cont.Lines = append(cont.Lines, ContainerLine{ShipmentLineID: in.ShipmentLineID, Qty: in.Qty})
			}
			// A known container type gives the tare, the returnable flag and the
			// packaging material; a free-text type packs with none of them.
			ct, known, err := loadContainerType(ctx, tx, containerType)
			if err != nil {
				return PackResult{}, err
			}
			if known {
				cont.TareKg, cont.GrossKg, cont.Returnable, cont.PackagingMaterial = ct.TareKg, ct.TareKg, ct.Returnable, ct.PackagingMaterial
			}
			if err := s.insertContainer(ctx, tx, p.TenantID, &sh, &cont); err != nil {
				return PackResult{}, err
			}
			target := sh.Status
			if domain.AllPacked(lineQtys(sh)) {
				if target, err = domain.Advance(sh.Status, domain.StatusPacked); err != nil {
					return PackResult{}, mapDomain(err)
				}
			}
			if err := bumpShipment(ctx, tx, p.TenantID, &sh, target); err != nil {
				return PackResult{}, err
			}
			return PackResult{Container: cont, Shipment: sh}, nil
		})
		out = res
		return err
	})
	return out, err
}

// PostPackingSlip posts the packed, not yet shipped quantities of a PACKED
// shipment: it numbers the slip (PS- per legal entity), journals each stocked
// line per BR-28.1, marks the closed containers SHIPPED with this slip, moves
// the shipment to SHIPPED (or PARTIALLY_SHIPPED while a planned remainder is
// left, FR-28.8) and writes packingslip.posted (and asn.issued when asked), all
// in one transaction (FR-28.4).
func (s *Postgres) PostPackingSlip(ctx context.Context, id string, version int, in PostInput, idem Idem) (PackingSlip, error) {
	p, _ := tenant.FromContext(ctx)
	var out PackingSlip
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (PackingSlip, error) {
			sh, err := loadShipment(ctx, tx, id)
			if err != nil {
				return PackingSlip{}, err
			}
			if err := checkVersion(sh, version); err != nil {
				return PackingSlip{}, err
			}
			if sh.Status != domain.StatusPacked {
				return PackingSlip{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeStatus, domain.ErrStatusNotPacked.Error())
			}
			var postLines []domain.PostingLine
			slip := PackingSlip{ID: ids.New(), ShipmentID: sh.ID, OrderID: sh.OrderID,
				Status: "POSTED", PostingDate: in.PostingDate, Currency: in.Currency, Lines: []PackingSlipLine{}, PackagingFees: []PackagingFeeLine{}}
			for _, l := range sh.Lines {
				q := l.Packed.Sub(l.Shipped)
				if !q.IsPositive() {
					continue
				}
				cost, ok := in.Costs[l.ID]
				if !ok {
					return PackingSlip{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeCostMissing, "unit cost missing for shipment line "+l.ID)
				}
				postLines = append(postLines, domain.PostingLine{OrderLineID: l.OrderLineID, Kind: domain.KindStocked, Qty: q,
					UnitCost: cost.UnitCost, CostSnapshotID: cost.CostSnapshotID})
				slip.Lines = append(slip.Lines, PackingSlipLine{OrderLineID: l.OrderLineID, ShipmentLineID: l.ID, Qty: q,
					CostSnapshotID: cost.CostSnapshotID})
			}
			if len(slip.Lines) == 0 {
				return PackingSlip{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeNothingToShip, "no packed quantity is waiting to ship on this shipment")
			}
			journal, err := domain.PackingSlipPosting(postLines, in.CogsAtShipment, in.Currency)
			if err != nil {
				return PackingSlip{}, err
			}
			seq, err := nextNumber(ctx, tx, p.TenantID, "PS:"+sh.LegalEntityID)
			if err != nil {
				return PackingSlip{}, err
			}
			slip.Number = fmt.Sprintf("PS-%07d", seq)
			slip.Journal = toEntries(journal)
			for i := range slip.Lines {
				slip.Lines[i].CostAmount = lineAmount(journal, slip.Lines[i].OrderLineID)
				slip.TotalCost = slip.TotalCost.Add(slip.Lines[i].CostAmount)
			}
			if err := insertSlip(ctx, tx, p.TenantID, sh.LegalEntityID, slip); err != nil {
				return PackingSlip{}, err
			}
			tracked, err := stampTracking(ctx, tx, p.TenantID, slip.ID, sh, slip.Lines)
			if err != nil {
				return PackingSlip{}, err
			}
			if slip.PackagingFees, err = recordPackagingFees(ctx, tx, p.TenantID, sh, slip, in.CountryCode); err != nil {
				return PackingSlip{}, err
			}
			// Returnable containers leaving with this slip raise the customer's
			// balance (BR-28.13); counted before the containers become SHIPPED.
			returnables, err := countReturnables(ctx, tx, sh.ID, "CLOSED")
			if err != nil {
				return PackingSlip{}, err
			}
			if err := moveBalances(ctx, tx, p.TenantID, sh.CustomerID, returnables, 1); err != nil {
				return PackingSlip{}, err
			}
			for _, pl := range slip.Lines {
				if _, err := tx.Exec(ctx, `update shipment_line set qty_shipped = qty_shipped + $3 where tenant_id = $1 and id = $2`,
					p.TenantID, pl.ShipmentLineID, pl.Qty); err != nil {
					return PackingSlip{}, err
				}
				idx := lineIndex(sh, pl.ShipmentLineID)
				sh.Lines[idx].Shipped = sh.Lines[idx].Shipped.Add(pl.Qty)
			}
			// The ASN lists the containers leaving now: the CLOSED ones, read
			// before they become SHIPPED.
			asn := asnData(sh, slip)
			if _, err := tx.Exec(ctx, `update container set status = 'SHIPPED', packing_slip_id = $3 where tenant_id = $1 and shipment_id = $2 and status = 'CLOSED'`,
				p.TenantID, sh.ID, slip.ID); err != nil {
				return PackingSlip{}, err
			}
			target, err := domain.Advance(sh.Status, domain.PostTarget(lineQtys(sh)))
			if err != nil {
				return PackingSlip{}, mapDomain(err)
			}
			if err := bumpShipment(ctx, tx, p.TenantID, &sh, target); err != nil {
				return PackingSlip{}, err
			}
			posted := slipPostedData(slip, sh, returnables)
			for i, pl := range slip.Lines {
				posted.Lines[i].ProductID = sh.Lines[lineIndex(sh, pl.ShipmentLineID)].ProductID
				posted.Lines[i].Tracking = trackingEventEntries(tracked[pl.ShipmentLineID])
			}
			if err := enqueue(ctx, tx, sh.ID, fulevents.PackingSlipPosted, p.TenantID, posted); err != nil {
				return PackingSlip{}, err
			}
			if in.EmitASN {
				if err := enqueue(ctx, tx, sh.ID, fulevents.ASNIssued, p.TenantID, asn); err != nil {
					return PackingSlip{}, err
				}
			}
			return slip, nil
		})
		out = res
		return err
	})
	return out, err
}

// CancelPackingSlip reverses a posted, uninvoiced slip: the original becomes
// CANCELLED, a reversing slip carries the mirror journal, shipped quantities
// come back, the containers that shipped with this slip return to CLOSED and
// the shipment returns to PACKED (FR-28.5, BR-28.6). Other slips of a partially
// shipped shipment are untouched. An invoiced slip is refused with FUL-0010.
func (s *Postgres) CancelPackingSlip(ctx context.Context, slipID string, idem Idem) (PackingSlip, error) {
	p, _ := tenant.FromContext(ctx)
	var out PackingSlip
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (PackingSlip, error) {
			orig, err := loadSlip(ctx, tx, slipID)
			if err != nil {
				return PackingSlip{}, err
			}
			if orig.Status == "INVOICED" {
				return PackingSlip{}, httpx.NewProblem(http.StatusConflict, CodeSlipInvoiced,
					fmt.Sprintf("cannot cancel: packing slip %s is invoiced; post a credit note first", orig.Number))
			}
			if orig.Status != "POSTED" || orig.ReversalOfID != nil {
				return PackingSlip{}, httpx.NewProblem(http.StatusConflict, CodeSlipNotCancellable, "packing slip is not posted or is itself a reversal")
			}
			sh, err := loadShipment(ctx, tx, orig.ShipmentID)
			if err != nil {
				return PackingSlip{}, err
			}
			// The shipment returns to PACKED with the slip's quantity packed and
			// unshipped again. A partially shipped shipment whose remainder is
			// already picked stays PICKED: packing the remainder makes it PACKED.
			target := domain.StatusPacked
			if sh.Status == domain.StatusPicked {
				target = domain.StatusPicked
			}
			if _, err := domain.Advance(sh.Status, target); err != nil {
				return PackingSlip{}, mapDomain(err)
			}
			rev := PackingSlip{ID: ids.New(), ShipmentID: orig.ShipmentID, OrderID: orig.OrderID, Status: "POSTED",
				PostingDate: dateOnly(time.Now()), Currency: orig.Currency, TotalCost: orig.TotalCost.Neg(), ReversalOfID: &orig.ID,
				Journal: toEntries(domain.Reverse(toJournal(orig.Journal))), Lines: []PackingSlipLine{}, PackagingFees: []PackagingFeeLine{}}
			seq, err := nextNumber(ctx, tx, p.TenantID, "PS:"+sh.LegalEntityID)
			if err != nil {
				return PackingSlip{}, err
			}
			rev.Number = fmt.Sprintf("PS-%07d", seq)
			for _, l := range orig.Lines {
				rev.Lines = append(rev.Lines, PackingSlipLine{OrderLineID: l.OrderLineID, ShipmentLineID: l.ShipmentLineID,
					Qty: l.Qty.Neg(), CostAmount: l.CostAmount.Neg(), CostSnapshotID: l.CostSnapshotID})
				if _, err := tx.Exec(ctx, `update shipment_line set qty_shipped = qty_shipped - $3 where tenant_id = $1 and id = $2`,
					p.TenantID, l.ShipmentLineID, l.Qty); err != nil {
					return PackingSlip{}, err
				}
			}
			if err := insertSlip(ctx, tx, p.TenantID, sh.LegalEntityID, rev); err != nil {
				return PackingSlip{}, err
			}
			if rev.PackagingFees, err = mirrorFeeLines(ctx, tx, p.TenantID, sh.LegalEntityID, orig, rev); err != nil {
				return PackingSlip{}, err
			}
			// The returnable containers come back from the customer's balance; a
			// customer who already returned them blocks the cancel (FUL-0015).
			returnables, err := countSlipReturnables(ctx, tx, orig.ID)
			if err != nil {
				return PackingSlip{}, err
			}
			if err := moveBalances(ctx, tx, p.TenantID, sh.CustomerID, returnables, -1); err != nil {
				return PackingSlip{}, err
			}
			if _, err := tx.Exec(ctx, `update packing_slip set status = 'CANCELLED' where tenant_id = $1 and id = $2`, p.TenantID, orig.ID); err != nil {
				return PackingSlip{}, err
			}
			if _, err := tx.Exec(ctx, `update container set status = 'CLOSED', packing_slip_id = null where tenant_id = $1 and packing_slip_id = $2`,
				p.TenantID, orig.ID); err != nil {
				return PackingSlip{}, err
			}
			// The entries that shipped on this slip go back to the pick.
			if _, err := tx.Exec(ctx, `update pick_tracking set packing_slip_id = null where tenant_id = $1 and packing_slip_id = $2`,
				p.TenantID, orig.ID); err != nil {
				return PackingSlip{}, err
			}
			if err := bumpShipment(ctx, tx, p.TenantID, &sh, target); err != nil {
				return PackingSlip{}, err
			}
			if err := enqueue(ctx, tx, sh.ID, fulevents.PackingSlipCancelled, p.TenantID, fulevents.PackingSlipCancelledData{
				PackingSlipID: orig.ID, ReversalID: rev.ID, ShipmentID: sh.ID, OrderID: sh.OrderID,
				PostingDate: rev.PostingDate.Format("2006-01-02"), Currency: rev.Currency, Lines: slipLineData(rev.Lines),
				Journal: journalData(rev.Journal), PackagingFees: feeData(rev.PackagingFees), Returnables: returnableData(returnables, -1),
			}); err != nil {
				return PackingSlip{}, err
			}
			return rev, nil
		})
		out = res
		return err
	})
	return out, err
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func toEntries(lines []domain.JournalLine) []JournalEntry {
	out := make([]JournalEntry, 0, len(lines))
	for _, l := range lines {
		out = append(out, JournalEntry{Account: l.Account, Amount: l.Amount.Value, Currency: l.Amount.Currency, OrderLineID: l.OrderLineID})
	}
	return out
}

func toJournal(entries []JournalEntry) []domain.JournalLine {
	out := make([]domain.JournalLine, 0, len(entries))
	for _, e := range entries {
		out = append(out, domain.JournalLine{Account: e.Account, Amount: money.Amount{Value: e.Amount, Currency: e.Currency}, OrderLineID: e.OrderLineID})
	}
	return out
}

// lineAmount returns the cost posted for one order line: the credit to 1400, as a positive amount.
func lineAmount(journal []domain.JournalLine, orderLineID string) decimal.Decimal {
	total := decimal.Zero
	for _, j := range journal {
		if j.OrderLineID == orderLineID && j.Account == domain.AccountInventory {
			total = total.Add(j.Amount.Value.Neg())
		}
	}
	return total
}

func insertSlip(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, slip PackingSlip) error {
	journal, err := json.Marshal(slip.Journal)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into packing_slip (tenant_id, id, legal_entity_id, shipment_id, order_id, packing_slip_number, status, posting_date, currency, total_cost, journal, reversal_of_id)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`, tenantID, slip.ID, legalEntityID, slip.ShipmentID, slip.OrderID, slip.Number,
		slip.Status, slip.PostingDate, slip.Currency, slip.TotalCost, journal, slip.ReversalOfID); err != nil {
		return err
	}
	for _, l := range slip.Lines {
		if _, err := tx.Exec(ctx, `insert into packing_slip_line (tenant_id, packing_slip_id, order_line_id, shipment_line_id, qty, cost_amount, cost_snapshot_id)
			values ($1, $2, $3, $4, $5, $6, $7)`, tenantID, slip.ID, l.OrderLineID, l.ShipmentLineID, l.Qty, l.CostAmount, l.CostSnapshotID); err != nil {
			return err
		}
	}
	return nil
}

func loadSlip(ctx context.Context, tx pgx.Tx, id string) (PackingSlip, error) {
	var slip PackingSlip
	var journal []byte
	var total string
	err := tx.QueryRow(ctx, `select id, packing_slip_number, shipment_id, order_id, status, posting_date, currency, total_cost::text, journal, reversal_of_id
		from packing_slip where id = $1`, id).Scan(&slip.ID, &slip.Number, &slip.ShipmentID, &slip.OrderID, &slip.Status, &slip.PostingDate, &slip.Currency, &total, &journal, &slip.ReversalOfID)
	if errors.Is(err, pgx.ErrNoRows) {
		return slip, httpx.NewProblem(http.StatusNotFound, CodeSlipNotFound, "packing slip not found")
	}
	if err != nil {
		return slip, err
	}
	slip.TotalCost = dec(total)
	if err := json.Unmarshal(journal, &slip.Journal); err != nil {
		return slip, err
	}
	rows, err := tx.Query(ctx, `select order_line_id, shipment_line_id, qty::text, cost_amount::text, cost_snapshot_id from packing_slip_line where packing_slip_id = $1 order by shipment_line_id`, id)
	if err != nil {
		return slip, err
	}
	defer rows.Close()
	slip.Lines = []PackingSlipLine{}
	for rows.Next() {
		var l PackingSlipLine
		var qty, cost string
		if err := rows.Scan(&l.OrderLineID, &l.ShipmentLineID, &qty, &cost, &l.CostSnapshotID); err != nil {
			return slip, err
		}
		l.Qty, l.CostAmount = dec(qty), dec(cost)
		slip.Lines = append(slip.Lines, l)
	}
	if err := rows.Err(); err != nil {
		return slip, err
	}
	rows.Close()
	slip.PackagingFees, err = loadFeeLines(ctx, tx, id)
	return slip, err
}

func journalData(lines []JournalEntry) []fulevents.JournalEntry {
	out := []fulevents.JournalEntry{}
	for _, l := range lines {
		out = append(out, fulevents.JournalEntry{Account: l.Account, Amount: l.Amount.String(), Currency: l.Currency, OrderLineID: l.OrderLineID})
	}
	return out
}

func slipLineData(lines []PackingSlipLine) []fulevents.SlipLine {
	out := []fulevents.SlipLine{}
	for _, l := range lines {
		out = append(out, fulevents.SlipLine{OrderLineID: l.OrderLineID, Qty: l.Qty.String(), CostAmount: l.CostAmount.String(), CostSnapshotID: l.CostSnapshotID})
	}
	return out
}

func slipPostedData(slip PackingSlip, sh Shipment, returnables []returnableCount) fulevents.PackingSlipPostedData {
	return fulevents.PackingSlipPostedData{
		PackingSlipID: slip.ID, PackingSlipNumber: slip.Number, ShipmentID: sh.ID, OrderID: sh.OrderID, CustomerID: sh.CustomerID,
		PostingDate: slip.PostingDate.Format("2006-01-02"), Currency: slip.Currency, TotalCost: slip.TotalCost.String(),
		Lines: slipLineData(slip.Lines), Journal: journalData(slip.Journal),
		PackagingFees: feeData(slip.PackagingFees), Returnables: returnableData(returnables, 1),
	}
}
