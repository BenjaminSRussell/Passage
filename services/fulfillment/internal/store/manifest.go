package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
)

// Error codes of the carrier manifest.
const (
	// CodeManifestEmpty: no labelled parcel of the carrier is ready to be manifested.
	CodeManifestEmpty = "FUL-0076"
	// CodeManifestNotFound: no such manifest in the caller's tenant and scope.
	CodeManifestNotFound = "FUL-0077"
	// CodeManifestCollected: the carrier has already collected a parcel on the
	// manifest (an IN_TRANSIT or DELIVERED scan, or its shipment DELIVERED), so
	// the manifest is the record of that handover and cannot be cancelled.
	CodeManifestCollected = "FUL-0078"
	// CodeManifestConflict: a parcel went on another manifest at the same time.
	CodeManifestConflict = "FUL-0079"
)

// ManifestLookbackDays bounds the unmanifested parcels to labels bought in the
// last 30 days: older labels (bought before manifests existed, or never
// collected) are not handed to today's pickup, and the candidate query reads a
// bounded slice of the tracking history through a partial index.
const ManifestLookbackDays = 30

// Manifest statuses.
const (
	ManifestClosed    = "CLOSED"
	ManifestCancelled = "CANCELLED"
)

// ManifestStore is the carrier manifest, the end-of-day close-out of the
// parcels one carrier collects from one warehouse (FR-28.6).
type ManifestStore interface {
	// ManifestCandidates lists the labelled parcels that are on no manifest.
	ManifestCandidates(ctx context.Context, f ManifestCandidateFilter) ([]ManifestCandidate, error)
	CloseManifest(ctx context.Context, in ManifestInput, idem Idem) (Manifest, error)
	ListManifests(ctx context.Context, f ManifestFilter) ([]Manifest, error)
	GetManifest(ctx context.Context, id string) (Manifest, error)
	CancelManifest(ctx context.Context, id string, version int, idem Idem) (Manifest, error)
}

// ManifestCandidateFilter narrows the unmanifested parcels; empty fields match everything.
type ManifestCandidateFilter struct {
	WarehouseID, LegalEntityID, Carrier string
	Limit                               int
}

// ManifestCandidate is a labelled parcel that is on no active manifest. Ready
// says its shipment has shipped (a packing slip posted), so the carrier can
// take it; a parcel that is not ready waits for the goods issue and is left
// out of a manifest.
type ManifestCandidate struct {
	ShipmentID     string    `json:"shipmentId"`
	ShipmentNumber string    `json:"shipmentNumber"`
	LegalEntityID  string    `json:"legalEntityId"`
	WarehouseID    string    `json:"warehouseId"`
	OrderID        string    `json:"orderId"`
	ShipmentStatus string    `json:"shipmentStatus"`
	Carrier        string    `json:"carrier"`
	TrackingNumber string    `json:"trackingNumber"`
	LabelledAt     time.Time `json:"labelledAt"`
	Ready          bool      `json:"ready"`
}

// ManifestInput closes a manifest for the ready parcels of one carrier in one
// warehouse and legal entity. ProviderManifestID is the carrier's own manifest
// reference when the caller has one.
type ManifestInput struct {
	WarehouseID, LegalEntityID, Carrier string
	PickupDate                          time.Time
	ProviderManifestID                  string
}

// ManifestFilter narrows the manifest list; empty fields match everything.
type ManifestFilter struct {
	WarehouseID, Carrier, Status string
	PickupFrom, PickupTo         *time.Time
	Limit                        int
}

// ManifestParcel is one parcel on a manifest. Active is false once the
// manifest is cancelled.
type ManifestParcel struct {
	ShipmentID     string `json:"shipmentId"`
	ShipmentNumber string `json:"shipmentNumber"`
	OrderID        string `json:"orderId"`
	TrackingNumber string `json:"trackingNumber"`
	Active         bool   `json:"active"`
}

// Manifest is a carrier manifest. Parcels is filled by GetManifest only.
type Manifest struct {
	ID                 string           `json:"id"`
	Number             string           `json:"manifestNumber"`
	Status             string           `json:"status"`
	LegalEntityID      string           `json:"legalEntityId"`
	WarehouseID        string           `json:"warehouseId"`
	Carrier            string           `json:"carrier"`
	PickupDate         string           `json:"pickupDate"`
	ProviderManifestID *string          `json:"providerManifestId"`
	ParcelCount        int              `json:"parcelCount"`
	ShipmentCount      int              `json:"shipmentCount"`
	Parcels            []ManifestParcel `json:"parcels"`
	Version            int              `json:"version"`
	CreatedAt          time.Time        `json:"createdAt"`
	CancelledAt        *time.Time       `json:"cancelledAt"`
}

const manifestColumns = `id, manifest_number, status, legal_entity_id, warehouse_id, carrier, to_char(pickup_date, 'YYYY-MM-DD'), provider_manifest_id,
	parcel_count, shipment_count, version, created_at, cancelled_at`

func scanManifest(row pgx.Row) (Manifest, error) {
	var m Manifest
	err := row.Scan(&m.ID, &m.Number, &m.Status, &m.LegalEntityID, &m.WarehouseID, &m.Carrier, &m.PickupDate, &m.ProviderManifestID,
		&m.ParcelCount, &m.ShipmentCount, &m.Version, &m.CreatedAt, &m.CancelledAt)
	m.Parcels = []ManifestParcel{}
	return m, err
}

func manifestNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeManifestNotFound, "manifest not found")
}

func manifestData(m Manifest) fulevents.ManifestData {
	out := fulevents.ManifestData{ManifestID: m.ID, ManifestNumber: m.Number, Status: m.Status, LegalEntityID: m.LegalEntityID, WarehouseID: m.WarehouseID,
		Carrier: m.Carrier, PickupDate: m.PickupDate, ParcelCount: m.ParcelCount, ShipmentCount: m.ShipmentCount, Parcels: []fulevents.ManifestParcel{}}
	if m.ProviderManifestID != nil {
		out.ProviderManifestID = *m.ProviderManifestID
	}
	for _, p := range m.Parcels {
		out.Parcels = append(out.Parcels, fulevents.ManifestParcel{ShipmentID: p.ShipmentID, ShipmentNumber: p.ShipmentNumber, OrderID: p.OrderID, TrackingNumber: p.TrackingNumber})
	}
	return out
}

// shippedStatuses are the shipment statuses from which a labelled parcel has
// left the warehouse's books (a packing slip posted) and waits for the carrier.
// A DELIVERED shipment is not a candidate at all: the carrier already has it.
const shippedStatuses = `('SHIPPED', 'PARTIALLY_SHIPPED')`

// collectedScan says the carrier has scanned the parcel of alias p (a row with
// tenant_id, shipment_id, provider and tracking_number): it is in transit or
// delivered, so it has left the warehouse whatever the manifest says.
func collectedScan(p string) string {
	return `exists (select 1 from shipment_tracking_event x where x.tenant_id = ` + p + `.tenant_id and x.shipment_id = ` + p + `.shipment_id
	and x.provider = ` + p + `.provider and x.tracking_number = ` + p + `.tracking_number and x.status in ('IN_TRANSIT', 'DELIVERED'))`
}

var candidateQuery = `select distinct on (e.shipment_id, e.provider, e.tracking_number)
		s.id, s.shipment_number, s.legal_entity_id, s.warehouse_id, s.order_id, s.status, e.provider, e.tracking_number, e.event_time,
		s.status in ` + shippedStatuses + `
	from shipment_tracking_event e
	join shipment s on s.tenant_id = e.tenant_id and s.id = e.shipment_id
	where e.status = 'LABELLED' and e.event_time >= now() - make_interval(days => ` + strconv.Itoa(ManifestLookbackDays) + `)
	  and s.status not in ('CANCELLED', 'DELIVERED')
	  and ($1 = '' or s.warehouse_id::text = $1) and ($2 = '' or s.legal_entity_id::text = $2) and ($3 = '' or e.provider = $3)
	  and not exists (select 1 from carrier_manifest_line l where l.tenant_id = e.tenant_id and l.shipment_id = e.shipment_id
	      and l.provider = e.provider and l.tracking_number = e.tracking_number and l.active)
	  and not ` + collectedScan("e") + `
	order by e.shipment_id, e.provider, e.tracking_number, e.event_time`

func scanCandidates(rows pgx.Rows) ([]ManifestCandidate, error) {
	defer rows.Close()
	out := []ManifestCandidate{}
	for rows.Next() {
		var c ManifestCandidate
		if err := rows.Scan(&c.ShipmentID, &c.ShipmentNumber, &c.LegalEntityID, &c.WarehouseID, &c.OrderID, &c.ShipmentStatus, &c.Carrier, &c.TrackingNumber, &c.LabelledAt, &c.Ready); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ManifestCandidates lists the labelled parcels that are on no active manifest,
// oldest label first: the report of what a carrier pickup is still missing. It
// covers labels of the last ManifestLookbackDays whose shipment is neither
// cancelled nor delivered and that the carrier has not scanned yet.
func (s *Postgres) ManifestCandidates(ctx context.Context, f ManifestCandidateFilter) ([]ManifestCandidate, error) {
	var out []ManifestCandidate
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select * from (`+candidateQuery+`) c order by c.event_time, c.shipment_number, c.tracking_number limit $4`,
			f.WarehouseID, f.LegalEntityID, f.Carrier, f.Limit)
		if err != nil {
			return err
		}
		out, err = scanCandidates(rows)
		return err
	})
	return out, err
}

// CloseManifest hands the ready parcels of a carrier in a warehouse to a new
// manifest. Parcels whose shipment has not shipped yet stay unmanifested. It is
// FUL-0076 when no parcel is ready. A parcel is on one active manifest only: a
// concurrent close for the same warehouse and carrier waits for this one. The
// manifest.closed event is written in the same transaction.
func (s *Postgres) CloseManifest(ctx context.Context, in ManifestInput, idem Idem) (Manifest, error) {
	p, _ := tenant.FromContext(ctx)
	var out Manifest
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (Manifest, error) {
			if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1 || '/manifest/' || $2 || '/' || $3, 0))`, p.TenantID, in.WarehouseID, in.Carrier); err != nil {
				return Manifest{}, err
			}
			rows, err := tx.Query(ctx, `select * from (`+candidateQuery+`) c order by c.shipment_number, c.tracking_number`, in.WarehouseID, in.LegalEntityID, in.Carrier)
			if err != nil {
				return Manifest{}, err
			}
			all, err := scanCandidates(rows)
			if err != nil {
				return Manifest{}, err
			}
			var ready []ManifestCandidate
			for _, c := range all {
				if c.Ready {
					ready = append(ready, c)
				}
			}
			if len(ready) == 0 {
				return Manifest{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeManifestEmpty,
					fmt.Sprintf("no shipped parcel of %s is waiting in this warehouse (%d labelled parcels wait for their packing slip)", in.Carrier, len(all)))
			}
			seq, err := nextNumber(ctx, tx, p.TenantID, "MF:"+in.LegalEntityID)
			if err != nil {
				return Manifest{}, err
			}
			shipments := map[string]bool{}
			for _, c := range ready {
				shipments[c.ShipmentID] = true
			}
			var provider *string
			if in.ProviderManifestID != "" {
				provider = &in.ProviderManifestID
			}
			m, err := scanManifest(tx.QueryRow(ctx, `insert into carrier_manifest (tenant_id, id, manifest_number, legal_entity_id, warehouse_id, carrier, pickup_date,
					provider_manifest_id, parcel_count, shipment_count, created_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) returning `+manifestColumns,
				p.TenantID, ids.New(), fmt.Sprintf("MF-%07d", seq), in.LegalEntityID, in.WarehouseID, in.Carrier, in.PickupDate, provider, len(ready), len(shipments), p.UserID))
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "42501" {
				return Manifest{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeLegalEntityScope, "the legal entity is outside your legal-entity scope")
			}
			if err != nil {
				return Manifest{}, err
			}
			for _, c := range ready {
				_, err := tx.Exec(ctx, `insert into carrier_manifest_line (tenant_id, manifest_id, legal_entity_id, shipment_id, provider, tracking_number, shipment_number, order_id)
					values ($1, $2, $3, $4, $5, $6, $7, $8)`, p.TenantID, m.ID, in.LegalEntityID, c.ShipmentID, c.Carrier, c.TrackingNumber, c.ShipmentNumber, c.OrderID)
				if errors.As(err, &pgErr) && pgErr.Code == "23505" {
					// The advisory lock serialises closes per warehouse and carrier; the
					// partial unique index is the backstop if a parcel is reached another way.
					return Manifest{}, httpx.NewProblem(http.StatusConflict, CodeManifestConflict, "parcel "+c.TrackingNumber+" went on another manifest at the same time; reload and close again")
				}
				if err != nil {
					return Manifest{}, err
				}
				m.Parcels = append(m.Parcels, ManifestParcel{ShipmentID: c.ShipmentID, ShipmentNumber: c.ShipmentNumber, OrderID: c.OrderID, TrackingNumber: c.TrackingNumber, Active: true})
			}
			return m, enqueueFor(ctx, tx, "carrier_manifest", m.ID, fulevents.ManifestClosed, p.TenantID, manifestData(m))
		})
		out = res
		return err
	})
	return out, err
}

// ListManifests lists manifests, newest pickup first.
func (s *Postgres) ListManifests(ctx context.Context, f ManifestFilter) ([]Manifest, error) {
	out := []Manifest{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+manifestColumns+` from carrier_manifest
			where ($1 = '' or warehouse_id::text = $1) and ($2 = '' or carrier = $2) and ($3 = '' or status = $3)
			  and ($4::date is null or pickup_date >= $4) and ($5::date is null or pickup_date <= $5)
			order by pickup_date desc, manifest_number desc limit $6`, f.WarehouseID, f.Carrier, f.Status, f.PickupFrom, f.PickupTo, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			m, err := scanManifest(rows)
			if err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

func manifestParcels(ctx context.Context, tx pgx.Tx, id string) ([]ManifestParcel, error) {
	rows, err := tx.Query(ctx, `select shipment_id, shipment_number, order_id, tracking_number, active from carrier_manifest_line
		where manifest_id = $1 order by shipment_number, tracking_number`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ManifestParcel{}
	for rows.Next() {
		var p ManifestParcel
		if err := rows.Scan(&p.ShipmentID, &p.ShipmentNumber, &p.OrderID, &p.TrackingNumber, &p.Active); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetManifest returns one manifest with its parcels, FUL-0077 when it is not visible.
func (s *Postgres) GetManifest(ctx context.Context, id string) (Manifest, error) {
	var m Manifest
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		m, err = scanManifest(tx.QueryRow(ctx, `select `+manifestColumns+` from carrier_manifest where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return manifestNotFound()
		}
		if err != nil {
			return err
		}
		m.Parcels, err = manifestParcels(ctx, tx, id)
		return err
	})
	return m, err
}

// CancelManifest withdraws a CLOSED manifest, for example one closed for the
// wrong day or carrier: its parcels become unmanifested again, so the next
// manifest takes them. A stale version is 412; a cancelled manifest is FUL-0001;
// a manifest the carrier has already collected from is FUL-0078.
// manifest.cancelled is written in the same transaction.
func (s *Postgres) CancelManifest(ctx context.Context, id string, version int, idem Idem) (Manifest, error) {
	p, _ := tenant.FromContext(ctx)
	var out Manifest
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (Manifest, error) {
			m, err := scanManifest(tx.QueryRow(ctx, `select `+manifestColumns+` from carrier_manifest where id = $1 for update`, id))
			if errors.Is(err, pgx.ErrNoRows) {
				return m, manifestNotFound()
			}
			if err != nil {
				return m, err
			}
			if m.Version != version {
				return m, httpx.NewProblem(http.StatusPreconditionFailed, CodeVersion, "manifest changed since it was read; reload and retry")
			}
			if m.Status != ManifestClosed {
				return m, httpx.NewProblem(http.StatusUnprocessableEntity, CodeStatus, "manifest is "+m.Status+"; only a CLOSED manifest can be cancelled")
			}
			var collected int
			if err := tx.QueryRow(ctx, `select count(*) from carrier_manifest_line l join shipment s on s.tenant_id = l.tenant_id and s.id = l.shipment_id
				where l.manifest_id = $1 and l.active and (s.status = 'DELIVERED' or `+collectedScan("l")+`)`, id).Scan(&collected); err != nil {
				return m, err
			}
			if collected > 0 {
				return m, httpx.NewProblem(http.StatusUnprocessableEntity, CodeManifestCollected,
					fmt.Sprintf("the carrier has already collected %d parcel(s) on this manifest (in transit or delivered); it is the record of that handover and cannot be cancelled", collected))
			}
			if _, err := tx.Exec(ctx, `update carrier_manifest_line set active = false where manifest_id = $1`, id); err != nil {
				return m, err
			}
			m, err = scanManifest(tx.QueryRow(ctx, `update carrier_manifest set status = 'CANCELLED', cancelled_at = now(), version = version + 1, updated_at = now()
				where id = $1 returning `+manifestColumns, id))
			if err != nil {
				return m, err
			}
			if m.Parcels, err = manifestParcels(ctx, tx, id); err != nil {
				return m, err
			}
			return m, enqueueFor(ctx, tx, "carrier_manifest", m.ID, fulevents.ManifestCancelled, p.TenantID, manifestData(m))
		})
		out = res
		return err
	})
	return out, err
}
