package events

// ConsignmentClosed is emitted when a vendor replenishment order is closed
// short or cancelled (FR-16.17): the order takes no further receipt and the
// open quantity is no longer expected. Wave 18 and planning read it.
const ConsignmentClosed = "dev.passage.inventory.consignment.closed.v1"

// ConsignmentClosedLine is one order line at closing: the ordered and received
// quantities and the quantity given up.
type ConsignmentClosedLine struct {
	LineID      string `json:"lineId"`
	ProductID   string `json:"productId"`
	Unit        string `json:"unit"`
	Qty         string `json:"qty"`
	ReceivedQty string `json:"receivedQty"`
	GivenUpQty  string `json:"givenUpQty"`
}

// ConsignmentClosedData is the payload of consignment.closed. Disposition is
// CLOSED (a partly received order) or CANCELLED (nothing was received).
type ConsignmentClosedData struct {
	ConsignmentOrderID string                  `json:"consignmentOrderId"`
	LegalEntityID      string                  `json:"legalEntityId"`
	Number             string                  `json:"number"`
	VendorID           string                  `json:"vendorId"`
	Disposition        string                  `json:"disposition"`
	Reason             string                  `json:"reason"`
	Lines              []ConsignmentClosedLine `json:"lines"`
}
