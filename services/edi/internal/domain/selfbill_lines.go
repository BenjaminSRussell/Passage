package domain

import (
	"sort"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

// SBShipmentLine is one shipped (delivery note, PO line, item) a partner may bill itself for, built from the fulfillment
// shipment and the sales order when its 856 or DESADV is generated. The delivery note is the shipment number the ASN carries
// (BSN02, BGM 1004), the PO line the sales order line number, the item the product id sent as the vendor part.
type SBShipmentLine struct {
	DeliveryNote string
	POLine       string
	Item         string
	Quantity     decimal.Decimal
	UnitPrice    decimal.Decimal
	ShippedOn    time.Time
	ShipmentID   string
}

// SelfBillingLines lists what a shipment shipped, one line per (PO line, item), from its shipped containers. A shipment that
// shipped nothing yields no lines.
func SelfBillingLines(p TradeProfile, sh SourceShipment, o SourceOrder) ([]SBShipmentLine, error) {
	return SelfBillingLinesForSlip(p, sh, o, nil)
}

// SelfBillingLinesForSlip lists what one packing slip shipped, under the delivery note its notice carries (nil: the whole shipment).
func SelfBillingLinesForSlip(p TradeProfile, sh SourceShipment, o SourceOrder, slip *SlipRef) ([]SBShipmentLine, error) {
	note := DeliveryNote(sh, slip)
	sh = OnlySlip(sh, slip)
	if err := validateShipmentSource(p, sh, slip.perSlip()); err != nil {
		return nil, err
	}
	shipLines := make(map[string]SourceShipLine, len(sh.Lines))
	for _, l := range sh.Lines {
		shipLines[l.ID] = l
	}
	poLines := orderLines(o)
	type key struct{ line, item string }
	byKey := map[key]*SBShipmentLine{}
	var order []key
	for _, c := range sh.Containers {
		if c.Status != "SHIPPED" {
			continue
		}
		for _, cl := range c.Lines {
			sl, ok := shipLines[cl.ShipmentLineID]
			po, okPO := poLines[sl.OrderLineID]
			if !ok || !okPO {
				return nil, outboundf("container %s packs a line that is not on the shipment or its sales order", c.SSCC)
			}
			k := key{strconv.Itoa(po.LineNo), sl.ProductID}
			row, seen := byKey[k]
			if !seen {
				row = &SBShipmentLine{DeliveryNote: note, POLine: k.line, Item: k.item, UnitPrice: po.UnitPrice,
					ShippedOn: sh.ActualShipDate.UTC(), ShipmentID: sh.ID}
				byKey[k] = row
				order = append(order, k)
			}
			row.Quantity = row.Quantity.Add(cl.Qty)
		}
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].line != order[j].line {
			return order[i].line < order[j].line
		}
		return order[i].item < order[j].item
	})
	out := make([]SBShipmentLine, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out, nil
}
