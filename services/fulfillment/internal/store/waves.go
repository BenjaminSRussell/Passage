package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
)

// WorkbenchStore is the shipment cancellation, picking workbench (FR-28.13)
// and carrier tracking (FR-28.10) part of the store.
type WorkbenchStore interface {
	RemainderStore
	LegalEntitySettingStore
	DropShipStore
	CancelShipment(ctx context.Context, id string, version int, idem Idem) (Shipment, error)
	CloseRemainder(ctx context.Context, id string, version int, idem Idem) (Shipment, error)
	UnpackContainer(ctx context.Context, id string, version int, containerID string, idem Idem) (Shipment, error)
	NestContainers(ctx context.Context, id string, version int, containerType string, childIDs []string, idem Idem) (PackResult, error)
	UnnestContainer(ctx context.Context, id string, version int, containerID string, idem Idem) (Shipment, error)
	PreviewWaves(ctx context.Context, req WaveRequest) ([]WavePlan, error)
	CreateWaves(ctx context.Context, req WaveRequest, idem Idem) ([]Wave, error)
	ListWaves(ctx context.Context, f WaveFilter) ([]WaveSummary, error)
	AssignWave(ctx context.Context, id string, version int, picker string, idem Idem) (Wave, error)
	CloseWave(ctx context.Context, id string, version int, idem Idem) (Wave, error)
	GetWave(ctx context.Context, id string) (Wave, error)
	RecordTracking(ctx context.Context, shipmentID string, ev TrackingEvent) (TrackingEvent, bool, error)
	ListTracking(ctx context.Context, shipmentID string) ([]TrackingEvent, error)
}

// WaveRequest filters the RELEASED shipments of one warehouse for the picking
// workbench (FR-28.13) and says how to group them. Empty filters match
// everything; ShipFrom and ShipTo bound the planned ship date.
type WaveRequest struct {
	WarehouseID string
	CustomerID  string
	Carrier     string
	Zone        string
	ShipFrom    *time.Time
	ShipTo      *time.Time
	GroupBy     string
	Limits      domain.WaveLimits
}

// WaveShipment is one shipment of a planned or created wave.
type WaveShipment struct {
	ID              string                `json:"id"`
	ShipmentNumber  string                `json:"shipmentNumber"`
	OrderID         string                `json:"orderId"`
	CustomerID      string                `json:"customerId"`
	Status          domain.ShipmentStatus `json:"status"`
	CarrierCode     *string               `json:"carrierCode"`
	PlannedShipDate time.Time             `json:"plannedShipDate"`
	LineCount       int                   `json:"lineCount"`
}

// WavePlan is one wave of a workbench preview (BR-28.14).
type WavePlan struct {
	GroupKey      string         `json:"groupKey"`
	LegalEntityID string         `json:"legalEntityId"`
	LineCount     int            `json:"lineCount"`
	OrderCount    int            `json:"orderCount"`
	Shipments     []WaveShipment `json:"shipments"`
}

// WaveSummary is one row of the waves list.
type WaveSummary struct {
	ID            string    `json:"id"`
	WaveNumber    string    `json:"waveNumber"`
	LegalEntityID string    `json:"legalEntityId"`
	WarehouseID   string    `json:"warehouseId"`
	GroupBy       string    `json:"groupBy"`
	GroupKey      string    `json:"groupKey"`
	Status        string    `json:"status"`
	LineCount     int       `json:"lineCount"`
	OrderCount    int       `json:"orderCount"`
	CreatedAt     time.Time `json:"createdAt"`
	// Version is the optimistic version of the wave (If-Match on assign and close).
	Version    int        `json:"version"`
	Picker     *string    `json:"picker"`
	AssignedAt *time.Time `json:"assignedAt"`
	ClosedAt   *time.Time `json:"closedAt"`
}

// Wave is a created pick wave with its shipments.
type Wave struct {
	WaveSummary
	Shipments []WaveShipment `json:"shipments"`
	// Lines are the wave's shipment lines with their pick state; GetWave fills
	// them so the wave can be picked from one screen (confirm-pick).
	Lines []WaveLine `json:"lines,omitempty"`
}

// WaveLine is one shipment line of a wave and what is picked of it.
type WaveLine struct {
	ShipmentLineID  string          `json:"shipmentLineId"`
	ShipmentID      string          `json:"shipmentId"`
	ShipmentNumber  string          `json:"shipmentNumber"`
	ProductID       string          `json:"productId"`
	Unit            string          `json:"unit"`
	Zone            *string         `json:"zone"`
	Planned         decimal.Decimal `json:"qtyPlanned"`
	Picked          decimal.Decimal `json:"qtyPicked"`
	PickConfirmed   bool            `json:"pickConfirmed"`
	ShortPickReason *string         `json:"shortPickReason"`
}

// WaveFilter narrows the waves list. Empty fields do not filter.
type WaveFilter struct {
	WarehouseID string
	Status      string // OPEN or CLOSED
	Picker      string
	Unassigned  bool
	Limit       int
}

// candidates loads the RELEASED shipments the workbench filter selects with
// their lines, ordered so that planning is deterministic. A shipment's zone is
// the zone of its first zoned line and its product the product of its first
// line, because a shipment is never split across waves (BR-28.14).
func candidates(ctx context.Context, tx pgx.Tx, req WaveRequest) ([]domain.WaveCandidate, map[string]WaveShipment, error) {
	rows, err := tx.Query(ctx, `select s.id, s.shipment_number, s.order_id, s.legal_entity_id, s.customer_id, s.carrier_code, s.planned_ship_date
		from shipment s
		where s.warehouse_id = $1 and s.status = 'RELEASED'
		  and ($2 = '' or s.customer_id::text = $2)
		  and ($3 = '' or s.carrier_code = $3)
		  and ($4 = '' or exists (select 1 from shipment_line z where z.tenant_id = s.tenant_id and z.shipment_id = s.id and z.zone_code = $4))
		  and ($5::date is null or s.planned_ship_date >= $5::date)
		  and ($6::date is null or s.planned_ship_date <= $6::date)
		order by s.id`,
		req.WarehouseID, req.CustomerID, req.Carrier, req.Zone, req.ShipFrom, req.ShipTo)
	if err != nil {
		return nil, nil, err
	}
	var cands []domain.WaveCandidate
	info := map[string]WaveShipment{}
	index := map[string]int{}
	var shipmentIDs []string
	for rows.Next() {
		var c domain.WaveCandidate
		var ws WaveShipment
		if err := rows.Scan(&c.ShipmentID, &ws.ShipmentNumber, &c.OrderID, &c.LegalEntityID, &c.Customer, &ws.CarrierCode, &ws.PlannedShipDate); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if ws.CarrierCode != nil {
			c.Carrier = *ws.CarrierCode
		}
		ws.ID, ws.OrderID, ws.CustomerID, ws.Status = c.ShipmentID, c.OrderID, c.Customer, domain.StatusReleased
		index[c.ShipmentID] = len(cands)
		cands = append(cands, c)
		info[c.ShipmentID] = ws
		shipmentIDs = append(shipmentIDs, c.ShipmentID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(cands) == 0 {
		return cands, info, nil
	}
	lrows, err := tx.Query(ctx, `select shipment_id::text, id::text, product_id::text, coalesce(zone_code, '')
		from shipment_line where shipment_id = any($1::uuid[]) order by shipment_id, id`, shipmentIDs)
	if err != nil {
		return nil, nil, err
	}
	defer lrows.Close()
	for lrows.Next() {
		var shipmentID, lineID, productID, zone string
		if err := lrows.Scan(&shipmentID, &lineID, &productID, &zone); err != nil {
			return nil, nil, err
		}
		c := &cands[index[shipmentID]]
		if len(c.LineIDs) == 0 {
			c.ProductID = productID
		}
		if c.Zone == "" {
			c.Zone = zone
		}
		c.LineIDs = append(c.LineIDs, lineID)
		ws := info[shipmentID]
		ws.LineCount++
		info[shipmentID] = ws
	}
	return cands, info, lrows.Err()
}

func planView(plan domain.WavePlan, info map[string]WaveShipment) WavePlan {
	out := WavePlan{GroupKey: plan.GroupKey, LegalEntityID: plan.LegalEntityID, LineCount: len(plan.LineIDs),
		OrderCount: len(plan.OrderIDs), Shipments: []WaveShipment{}}
	for _, id := range plan.ShipmentIDs {
		out.Shipments = append(out.Shipments, info[id])
	}
	return out
}

// PreviewWaves returns the waves CreateWaves would create now, without writing.
func (s *Postgres) PreviewWaves(ctx context.Context, req WaveRequest) ([]WavePlan, error) {
	out := []WavePlan{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cands, info, err := candidates(ctx, tx, req)
		if err != nil {
			return err
		}
		plans, err := domain.PlanWaves(cands, req.GroupBy, req.Limits)
		if err != nil {
			return mapDomain(err)
		}
		for _, plan := range plans {
			out = append(out, planView(plan, info))
		}
		return nil
	})
	return out, err
}

// CreateWaves plans the waves and, in one transaction, records each wave with
// its shipments and lines, moves the shipments to PICKING and writes one
// wave.created per wave. A shipment that left RELEASED, or a line that is
// already in a wave, fails the whole request with FUL-0053 (BR-28.14).
func (s *Postgres) CreateWaves(ctx context.Context, req WaveRequest, idem Idem) ([]Wave, error) {
	p, _ := tenant.FromContext(ctx)
	var out []Wave
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() ([]Wave, error) {
			cands, info, err := candidates(ctx, tx, req)
			if err != nil {
				return nil, err
			}
			plans, err := domain.PlanWaves(cands, req.GroupBy, req.Limits)
			if err != nil {
				return nil, mapDomain(err)
			}
			waves := []Wave{}
			for _, plan := range plans {
				w, err := insertWave(ctx, tx, p.TenantID, req, plan, info)
				if err != nil {
					return nil, err
				}
				waves = append(waves, w)
			}
			return waves, nil
		})
		out = res
		return err
	})
	return out, err
}

func insertWave(ctx context.Context, tx pgx.Tx, tenantID string, req WaveRequest, plan domain.WavePlan, info map[string]WaveShipment) (Wave, error) {
	seq, err := nextNumber(ctx, tx, tenantID, "WV")
	if err != nil {
		return Wave{}, err
	}
	w := Wave{WaveSummary: WaveSummary{ID: ids.New(), WaveNumber: fmt.Sprintf("WV-%07d", seq), LegalEntityID: plan.LegalEntityID,
		WarehouseID: req.WarehouseID, GroupBy: req.GroupBy, GroupKey: plan.GroupKey, Status: "OPEN", Version: 1,
		LineCount: len(plan.LineIDs), OrderCount: len(plan.OrderIDs)}, Shipments: []WaveShipment{}}
	if err := tx.QueryRow(ctx, `insert into wave (tenant_id, id, wave_number, legal_entity_id, warehouse_id, group_by, group_key, status, line_count, order_count)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) returning created_at`, tenantID, w.ID, w.WaveNumber, w.LegalEntityID, w.WarehouseID,
		w.GroupBy, w.GroupKey, w.Status, w.LineCount, w.OrderCount).Scan(&w.CreatedAt); err != nil {
		return Wave{}, err
	}
	for _, sid := range plan.ShipmentIDs {
		tag, err := tx.Exec(ctx, `update shipment set status = 'PICKING', version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 and status = 'RELEASED'`, tenantID, sid)
		if err != nil {
			return Wave{}, err
		}
		if tag.RowsAffected() == 0 {
			return Wave{}, httpx.NewProblem(http.StatusConflict, CodeWaveConflict, "a shipment left RELEASED while the waves were planned; preview again")
		}
		if _, err := tx.Exec(ctx, `insert into wave_shipment (tenant_id, wave_id, shipment_id) values ($1, $2, $3)`, tenantID, w.ID, sid); err != nil {
			return Wave{}, err
		}
		ws := info[sid]
		ws.Status = domain.StatusPicking
		w.Shipments = append(w.Shipments, ws)
	}
	for _, lid := range plan.LineIDs {
		if _, err := tx.Exec(ctx, `insert into wave_line (tenant_id, shipment_line_id, wave_id) values ($1, $2, $3)`, tenantID, lid, w.ID); err != nil {
			if isUniqueViolation(err) {
				return Wave{}, httpx.NewProblem(http.StatusConflict, CodeWaveConflict, "a shipment line already belongs to a wave")
			}
			return Wave{}, err
		}
	}
	if err := enqueueFor(ctx, tx, "wave", w.ID, fulevents.WaveCreated, tenantID, fulevents.WaveCreatedData{
		WaveID: w.ID, WaveNumber: w.WaveNumber, LegalEntityID: w.LegalEntityID, WarehouseID: w.WarehouseID, GroupBy: w.GroupBy,
		GroupKey: w.GroupKey, ShipmentIDs: plan.ShipmentIDs, LineCount: w.LineCount, OrderCount: w.OrderCount,
	}); err != nil {
		return Wave{}, err
	}
	return w, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

const waveColumns = `id, wave_number, legal_entity_id, warehouse_id, group_by, group_key, status, line_count, order_count, created_at, version, picker, assigned_at, closed_at`

func scanWave(row pgx.Row) (WaveSummary, error) {
	var r WaveSummary
	err := row.Scan(&r.ID, &r.WaveNumber, &r.LegalEntityID, &r.WarehouseID, &r.GroupBy, &r.GroupKey, &r.Status, &r.LineCount, &r.OrderCount, &r.CreatedAt, &r.Version, &r.Picker, &r.AssignedAt, &r.ClosedAt)
	return r, err
}

// ListWaves returns waves newest first, optionally of one warehouse.
func (s *Postgres) ListWaves(ctx context.Context, f WaveFilter) ([]WaveSummary, error) {
	out := []WaveSummary{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+waveColumns+` from wave
			where ($1 = '' or warehouse_id::text = $1)
			  and ($2 = '' or status = $2)
			  and ($3 = '' or picker = $3)
			  and (not $4 or picker is null)
			order by created_at desc, wave_number desc limit $5`, f.WarehouseID, f.Status, f.Picker, f.Unassigned, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanWave(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetWave returns one wave with its shipments and their current status.
func (s *Postgres) GetWave(ctx context.Context, id string) (Wave, error) {
	var w Wave
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		w, err = getWaveTx(ctx, tx, id)
		return err
	})
	return w, err
}

func getWaveTx(ctx context.Context, tx pgx.Tx, id string) (Wave, error) {
	var w Wave
	sum, err := scanWave(tx.QueryRow(ctx, `select `+waveColumns+` from wave where id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return w, httpx.NewProblem(http.StatusNotFound, CodeWaveNotFound, "wave not found")
	}
	if err != nil {
		return w, err
	}
	w = Wave{WaveSummary: sum, Shipments: []WaveShipment{}}
	rows, err := tx.Query(ctx, `select s.id, s.shipment_number, s.order_id, s.customer_id, s.status, s.carrier_code, s.planned_ship_date,
			(select count(*) from wave_line wl join shipment_line sl on sl.tenant_id = wl.tenant_id and sl.id = wl.shipment_line_id
			 where wl.wave_id = ws.wave_id and sl.shipment_id = s.id)
		from wave_shipment ws join shipment s on s.tenant_id = ws.tenant_id and s.id = ws.shipment_id
		where ws.wave_id = $1 order by s.shipment_number`, id)
	if err != nil {
		return w, err
	}
	defer rows.Close()
	for rows.Next() {
		var ws WaveShipment
		var status string
		if err := rows.Scan(&ws.ID, &ws.ShipmentNumber, &ws.OrderID, &ws.CustomerID, &status, &ws.CarrierCode, &ws.PlannedShipDate, &ws.LineCount); err != nil {
			return w, err
		}
		ws.Status = domain.ShipmentStatus(status)
		w.Shipments = append(w.Shipments, ws)
	}
	if err := rows.Err(); err != nil {
		return w, err
	}
	rows.Close()
	lrows, err := tx.Query(ctx, `select sl.id::text, s.id::text, s.shipment_number, sl.product_id::text, sl.unit, sl.zone_code,
			sl.qty_planned::text, sl.qty_picked::text, sl.pick_confirmed, sl.short_pick_reason
		from wave_line wl
		join shipment_line sl on sl.tenant_id = wl.tenant_id and sl.id = wl.shipment_line_id
		join shipment s on s.tenant_id = sl.tenant_id and s.id = sl.shipment_id
		where wl.wave_id = $1 order by s.shipment_number, sl.id`, id)
	if err != nil {
		return w, err
	}
	defer lrows.Close()
	w.Lines = []WaveLine{}
	for lrows.Next() {
		var l WaveLine
		var planned, picked string
		if err := lrows.Scan(&l.ShipmentLineID, &l.ShipmentID, &l.ShipmentNumber, &l.ProductID, &l.Unit, &l.Zone, &planned, &picked, &l.PickConfirmed, &l.ShortPickReason); err != nil {
			return w, err
		}
		l.Planned, l.Picked = dec(planned), dec(picked)
		w.Lines = append(w.Lines, l)
	}
	return w, lrows.Err()
}
