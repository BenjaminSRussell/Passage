package domain

import "time"

// WarehouseShip is the warehouse's confirmation that a fulfillment shipment left the dock without a packing slip posted for it
// (dev.passage.wms.shipment.shipped.v1, #35 35-1). SSCCs, when given, are the containers that left; otherwise every closed
// container of the shipment did.
type WarehouseShip struct {
	ShippedAt   time.Time
	CarrierCode string
	SSCCs       []string
}

// ShippedByWarehouse returns the shipment as the warehouse shipped it, so the whole-shipment 856 or DESADV can be built from it:
// status SHIPPED, the warehouse's ship time as the actual ship date, its carrier when it named one, and only the containers that
// left, marked SHIPPED (a pallet named takes the cartons on it along). The shipment must be the partner's. A shipment fulfillment already shipped by a packing slip is
// refused: its notices come from that slip's asn.issued, and sending the whole shipment too would notify its containers twice. A
// container already on a slip, or one the shipment does not hold, is refused for the same reason.
func ShippedByWarehouse(p TradeProfile, sh SourceShipment, ws WarehouseShip) (SourceShipment, error) {
	if err := checkScope(p, sh.LegalEntityID, sh.CustomerID); err != nil {
		return sh, err
	}
	switch sh.Status {
	case "PICKED", "PACKED":
	case "SHIPPED", "PARTIALLY_SHIPPED", "DELIVERED":
		return sh, outboundf("shipment %s was shipped by a packing slip; its notice comes from that slip", sh.ShipmentNumber)
	default:
		return sh, outboundf("shipment %s is %s; only a picked or packed shipment can leave without a packing slip", sh.ShipmentNumber, sh.Status)
	}
	if ws.ShippedAt.IsZero() {
		return sh, outboundf("the warehouse confirmation of shipment %s has no ship time", sh.ShipmentNumber)
	}
	named := make(map[string]bool, len(ws.SSCCs))
	for _, s := range ws.SSCCs {
		named[s] = true
	}
	byID := make(map[string]SourceContainer, len(sh.Containers))
	for _, c := range sh.Containers {
		if c.ID != "" {
			byID[c.ID] = c
		}
	}
	// left reports whether c left: it is named, or every container left (none named), or the pallet it sits on left.
	var left func(c SourceContainer, depth int) bool
	left = func(c SourceContainer, depth int) bool {
		if len(named) == 0 || named[c.SSCC] {
			return true
		}
		if depth > 0 || c.ParentContainerID == nil {
			return false
		}
		parent, ok := byID[*c.ParentContainerID]
		return ok && left(parent, depth+1)
	}
	kept := make([]SourceContainer, 0, len(sh.Containers))
	found := map[string]bool{}
	for _, c := range sh.Containers {
		if !left(c, 0) {
			continue
		}
		found[c.SSCC] = true
		if c.PackingSlipID != nil && *c.PackingSlipID != "" {
			return sh, outboundf("container %s of shipment %s already shipped with a packing slip", c.SSCC, sh.ShipmentNumber)
		}
		if c.Status != "CLOSED" {
			if len(named) > 0 {
				return sh, outboundf("container %s of shipment %s is %s, not closed", c.SSCC, sh.ShipmentNumber, c.Status)
			}
			continue
		}
		c.Status = "SHIPPED"
		kept = append(kept, c)
	}
	for s := range named {
		if !found[s] {
			return sh, outboundf("the warehouse shipped container %s, which shipment %s does not hold", s, sh.ShipmentNumber)
		}
	}
	at := ws.ShippedAt.UTC()
	sh.Status, sh.ActualShipDate, sh.Containers = "SHIPPED", &at, kept
	if ws.CarrierCode != "" {
		code := ws.CarrierCode
		sh.CarrierCode = &code
	}
	return sh, nil
}
