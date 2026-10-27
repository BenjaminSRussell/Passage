package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
)

// TrackingEvent is one carrier scan or status change for a shipment (FR-28.10).
type TrackingEvent struct {
	Provider       string    `json:"provider"`
	EventKey       string    `json:"eventKey"`
	TrackingNumber string    `json:"trackingNumber"`
	Status         string    `json:"status"`
	Description    string    `json:"description"`
	EventTime      time.Time `json:"eventTime"`
}

// RecordTracking stores a carrier event and publishes tracking.updated. The
// provider event key makes it idempotent: a redelivered webhook or poll returns
// the stored event with created = false and publishes nothing. A DELIVERED
// event moves a SHIPPED shipment to DELIVERED (BR-28.6); a partially shipped
// shipment stays as it is until its remainder ships.
func (s *Postgres) RecordTracking(ctx context.Context, shipmentID string, ev TrackingEvent) (TrackingEvent, bool, error) {
	p, _ := tenant.FromContext(ctx)
	out, created := ev, false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var orderID, status string
		err := tx.QueryRow(ctx, `select order_id, status from shipment where id = $1 for update`, shipmentID).Scan(&orderID, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeShipmentNotFound, "shipment not found")
		}
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `insert into shipment_tracking_event (tenant_id, id, shipment_id, provider, event_key, tracking_number, status, description, event_time)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			on conflict (tenant_id, shipment_id, provider, event_key) do nothing`,
			p.TenantID, ids.New(), shipmentID, ev.Provider, ev.EventKey, ev.TrackingNumber, ev.Status, ev.Description, ev.EventTime)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return tx.QueryRow(ctx, `select provider, event_key, tracking_number, status, description, event_time from shipment_tracking_event
				where shipment_id = $1 and provider = $2 and event_key = $3`, shipmentID, ev.Provider, ev.EventKey).
				Scan(&out.Provider, &out.EventKey, &out.TrackingNumber, &out.Status, &out.Description, &out.EventTime)
		}
		created = true
		if ev.Status == "DELIVERED" && status == "SHIPPED" {
			if _, err := tx.Exec(ctx, `update shipment set status = 'DELIVERED', version = version + 1, updated_at = now()
				where tenant_id = $1 and id = $2 and status = 'SHIPPED'`, p.TenantID, shipmentID); err != nil {
				return err
			}
		}
		return enqueue(ctx, tx, shipmentID, fulevents.TrackingUpdated, p.TenantID, fulevents.TrackingUpdatedData{
			ShipmentID: shipmentID, OrderID: orderID, Provider: ev.Provider, TrackingNumber: ev.TrackingNumber, EventKey: ev.EventKey,
			Status: ev.Status, Description: ev.Description, EventTime: ev.EventTime.UTC().Format(time.RFC3339),
		})
	})
	return out, created, err
}

// ListTracking returns the shipment's tracking events, newest first.
func (s *Postgres) ListTracking(ctx context.Context, shipmentID string) ([]TrackingEvent, error) {
	out := []TrackingEvent{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var one int
		if err := tx.QueryRow(ctx, `select 1 from shipment where id = $1`, shipmentID).Scan(&one); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NewProblem(http.StatusNotFound, CodeShipmentNotFound, "shipment not found")
			}
			return err
		}
		rows, err := tx.Query(ctx, `select provider, event_key, tracking_number, status, description, event_time
			from shipment_tracking_event where shipment_id = $1 order by event_time desc, event_key`, shipmentID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e TrackingEvent
			if err := rows.Scan(&e.Provider, &e.EventKey, &e.TrackingNumber, &e.Status, &e.Description, &e.EventTime); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}
