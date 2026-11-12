package store

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	evts "github.com/BenjaminSRussell/passage/services/warehouse/internal/events"
)

// ShipConfirmation is the warehouse's confirmation that a fulfillment shipment left the dock without a packing slip posted for
// it (#35, 35-1). SSCCs, when given, are the containers that left; otherwise every closed container of the shipment did.
type ShipConfirmation struct {
	ShipmentID    string    `json:"shipmentId"`
	LegalEntityID string    `json:"legalEntityId"`
	WarehouseID   string    `json:"warehouseId"`
	CarrierCode   string    `json:"carrierCode,omitempty"`
	ShippedAt     time.Time `json:"shippedAt"`
	SSCCs         []string  `json:"ssccs"`
	ConfirmedBy   string    `json:"confirmedBy"`
	CreatedAt     time.Time `json:"createdAt"`
}

// ConfirmShipped records the confirmation and enqueues shipment.shipped in the same transaction. A shipment is confirmed
// once: a second confirmation under another Idempotency-Key is ErrConflict, a repeat of the key replays the first answer.
func (s *Store) ConfirmShipped(ctx context.Context, idem Idem, c ShipConfirmation) (ShipConfirmation, bool, error) {
	var (
		out      ShipConfirmation
		replayed bool
	)
	if c.SSCCs == nil {
		c.SSCCs = []string{}
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = once(ctx, tx, idem, http.StatusCreated, func() (ShipConfirmation, error) {
			if err := tx.QueryRow(ctx, `insert into shipment_shipped (tenant_id, legal_entity_id, shipment_id, warehouse_id, carrier_code,
				shipped_at, ssccs, confirmed_by) values ($1, $2, $3, $4, $5, $6, $7, $8) returning created_at`,
				tenantID(ctx), c.LegalEntityID, c.ShipmentID, c.WarehouseID, c.CarrierCode, c.ShippedAt, c.SSCCs, c.ConfirmedBy).Scan(&c.CreatedAt); err != nil {
				return c, err
			}
			data := evts.ShipmentShippedData{ShipmentID: c.ShipmentID, LegalEntityID: c.LegalEntityID, WarehouseID: c.WarehouseID,
				CarrierCode: c.CarrierCode, ShippedAt: c.ShippedAt.UTC().Format(time.RFC3339), Containers: []evts.ShippedContainer{}, User: c.ConfirmedBy}
			for _, s := range c.SSCCs {
				data.Containers = append(data.Containers, evts.ShippedContainer{SSCC: s})
			}
			return c, enqueueFor(ctx, tx, "shipment", c.ShipmentID, evts.ShipmentShipped, data)
		})
		return err
	})
	return out, replayed, err
}

// GetShipConfirmation reads the confirmation of a shipment.
func (s *Store) GetShipConfirmation(ctx context.Context, shipmentID string) (ShipConfirmation, error) {
	var c ShipConfirmation
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select shipment_id, legal_entity_id, warehouse_id, carrier_code, shipped_at, ssccs, confirmed_by, created_at
			from shipment_shipped where tenant_id = $1 and shipment_id = $2`, tenantID(ctx), shipmentID).
			Scan(&c.ShipmentID, &c.LegalEntityID, &c.WarehouseID, &c.CarrierCode, &c.ShippedAt, &c.SSCCs, &c.ConfirmedBy, &c.CreatedAt)
	})
	return c, err
}
