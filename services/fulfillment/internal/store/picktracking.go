package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"

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

// Error codes of the tracking rules (BR-28.10).
const (
	CodeTrackingRequired = "FUL-0006"
	CodeTrackingQty      = "FUL-0035"
	CodeSerialTaken      = "FUL-0036"
)

// TrackingStore captures batch, serial and location at pick (BR-28.10).
type TrackingStore interface {
	CaptureTracking(ctx context.Context, shipmentID, lineID string, version int, entries []domain.TrackingEntry, idem Idem) (Shipment, error)
}

// TrackingEntry is a stored batch or serial entry of a shipment line. A set
// PackingSlipID means the quantity has shipped on that slip.
type TrackingEntry struct {
	ID            string          `json:"id"`
	BatchNo       *string         `json:"batchNo"`
	SerialNo      *string         `json:"serialNo"`
	LocationCode  *string         `json:"locationCode"`
	Qty           decimal.Decimal `json:"qty"`
	PackingSlipID *string         `json:"packingSlipId"`
}

// loadTracking fills the tracking entries of every line of the shipment.
func loadTracking(ctx context.Context, tx pgx.Tx, sh *Shipment) error {
	rows, err := tx.Query(ctx, `select id, shipment_line_id, batch_no, serial_no, location_code, qty::text, packing_slip_id
		from pick_tracking where shipment_id = $1 order by created_at, id`, sh.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var e TrackingEntry
		var lineID, qty string
		if err := rows.Scan(&e.ID, &lineID, &e.BatchNo, &e.SerialNo, &e.LocationCode, &qty, &e.PackingSlipID); err != nil {
			return err
		}
		e.Qty = dec(qty)
		if i := lineIndex(*sh, lineID); i >= 0 {
			sh.Lines[i].Tracking = append(sh.Lines[i].Tracking, e)
		}
	}
	return rows.Err()
}

func strVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func trackingEventEntries(entries []TrackingEntry) []fulevents.TrackingEntry {
	out := []fulevents.TrackingEntry{}
	for _, e := range entries {
		out = append(out, fulevents.TrackingEntry{BatchNo: strVal(e.BatchNo), SerialNo: strVal(e.SerialNo), LocationCode: strVal(e.LocationCode), Qty: e.Qty.String()})
	}
	return out
}

// unshipped returns the entries not yet on a packing slip.
func unshipped(entries []TrackingEntry) []TrackingEntry {
	out := []TrackingEntry{}
	for _, e := range entries {
		if e.PackingSlipID == nil {
			out = append(out, e)
		}
	}
	return out
}

// shippedEntries returns the entries already on a packing slip.
func shippedEntries(entries []TrackingEntry) []TrackingEntry {
	out := []TrackingEntry{}
	for _, e := range entries {
		if e.PackingSlipID != nil {
			out = append(out, e)
		}
	}
	return out
}

func entryQty(entries []TrackingEntry) decimal.Decimal {
	total := decimal.Zero
	for _, e := range entries {
		total = total.Add(e.Qty)
	}
	return total
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// CaptureTracking replaces the unshipped batch, serial and location entries of a
// shipment line with entries (BR-28.10). The line must have a picked quantity
// that covers every entry, already shipped and new together, and a serial number
// can belong to one live line of its product only (FUL-0036). The shipment must
// still pick, pack or ship its remainder; its version is bumped so a concurrent
// command fails the version check. pick.tracked carries the new entries.
func (s *Postgres) CaptureTracking(ctx context.Context, shipmentID, lineID string, version int, entries []domain.TrackingEntry, idem Idem) (Shipment, error) {
	p, _ := tenant.FromContext(ctx)
	var out Shipment
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (Shipment, error) {
			sh, err := loadShipment(ctx, tx, shipmentID)
			if err != nil {
				return sh, err
			}
			if err := checkVersion(sh, version); err != nil {
				return sh, err
			}
			switch sh.Status {
			case domain.StatusPicking, domain.StatusPicked, domain.StatusPacked, domain.StatusPartiallyShipped:
			default:
				return sh, statusProblem("capture tracking on", sh.Status)
			}
			idx := lineIndex(sh, lineID)
			if idx < 0 {
				return sh, httpx.NewProblem(http.StatusNotFound, CodeLineNotFound, "shipment line not found")
			}
			if err := domain.ValidateTracking(entries); err != nil {
				return sh, mapTrackingErr(err)
			}
			line := sh.Lines[idx]
			shippedQty := entryQty(line.Tracking).Sub(entryQty(unshipped(line.Tracking)))
			if err := domain.CheckTrackedWithinPicked(shippedQty.Add(domain.TrackedQty(entries)), line.Picked); err != nil {
				return sh, mapTrackingErr(err)
			}
			// Bump first: a concurrent capture on the same version waits on the
			// shipment row and fails the version check instead of interleaving.
			if err := bumpShipment(ctx, tx, p.TenantID, &sh, sh.Status); err != nil {
				return sh, err
			}
			if _, err := tx.Exec(ctx, `delete from pick_tracking where tenant_id = $1 and shipment_line_id = $2 and packing_slip_id is null`,
				p.TenantID, line.ID); err != nil {
				return sh, err
			}
			var serials []string
			for _, e := range entries {
				if e.SerialNo != "" {
					serials = append(serials, e.SerialNo)
				}
			}
			if len(serials) > 0 {
				var taken string
				err := tx.QueryRow(ctx, `select serial_no from pick_tracking where tenant_id = $1 and product_id = $2 and serial_no = any($3) limit 1`,
					p.TenantID, line.ProductID, serials).Scan(&taken)
				if err == nil {
					return sh, serialTaken(taken)
				}
				if !errors.Is(err, pgx.ErrNoRows) {
					return sh, err
				}
			}
			for _, e := range entries {
				if _, err := tx.Exec(ctx, `insert into pick_tracking (tenant_id, id, legal_entity_id, shipment_id, shipment_line_id, product_id, batch_no, serial_no, location_code, qty)
					values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, p.TenantID, ids.New(), sh.LegalEntityID, sh.ID, line.ID, line.ProductID,
					nullable(e.BatchNo), nullable(e.SerialNo), nullable(e.LocationCode), e.Qty); err != nil {
					var pgErr *pgconn.PgError
					if errors.As(err, &pgErr) && pgErr.Code == "23505" {
						return sh, serialTaken("")
					}
					return sh, err
				}
			}
			sh.Lines[idx].Tracking = []TrackingEntry{}
			if err := loadTracking(ctx, tx, &sh); err != nil {
				return sh, err
			}
			return sh, enqueue(ctx, tx, sh.ID, fulevents.PickTracked, p.TenantID, fulevents.PickTrackedData{
				ShipmentID: sh.ID, ShipmentNumber: sh.ShipmentNumber, WarehouseID: sh.WarehouseID,
				Line: fulevents.TrackedLine{OrderLineID: line.OrderLineID, ProductID: line.ProductID,
					Entries: trackingEventEntries(unshipped(sh.Lines[idx].Tracking))},
			})
		})
		out = res
		return err
	})
	return out, err
}

func serialTaken(serial string) error {
	msg := "a serial number is already assigned to another shipment line of this product"
	if serial != "" {
		msg = fmt.Sprintf("serial %s is already assigned to another shipment line of this product", serial)
	}
	return httpx.NewProblem(http.StatusConflict, CodeSerialTaken, msg)
}

func mapTrackingErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrTrackingRequired):
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeTrackingRequired, err.Error())
	case errors.Is(err, domain.ErrTrackingQty):
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeTrackingQty, err.Error())
	case errors.Is(err, domain.ErrTrackingEntry):
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeValidation, err.Error())
	}
	return err
}

// stampTracking marks the unshipped entries of the slip's lines as shipped on
// the slip and returns them per shipment line for the posted event.
func stampTracking(ctx context.Context, tx pgx.Tx, tenantID, slipID string, sh Shipment, lines []PackingSlipLine) (map[string][]TrackingEntry, error) {
	out := map[string][]TrackingEntry{}
	for _, pl := range lines {
		l := sh.Lines[lineIndex(sh, pl.ShipmentLineID)]
		pending := unshipped(l.Tracking)
		if err := domain.CheckTrackedForPost(l.TrackingRequired, pl.Qty, entryQty(pending)); err != nil {
			return nil, httpx.NewProblem(http.StatusUnprocessableEntity, CodeTrackingRequired,
				fmt.Sprintf("%s for product %s on shipment line %s", err.Error(), l.ProductID, l.ID))
		}
		if len(pending) == 0 || !entryQty(pending).Equal(pl.Qty) {
			continue
		}
		if _, err := tx.Exec(ctx, `update pick_tracking set packing_slip_id = $3 where tenant_id = $1 and shipment_line_id = $2 and packing_slip_id is null`,
			tenantID, l.ID, slipID); err != nil {
			return nil, err
		}
		out[l.ID] = pending
	}
	return out, nil
}
