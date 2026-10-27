package events

// Transfer order event types (FR-16.15, section 3.16.5).
const (
	TransferShipped  = "dev.passage.inventory.transfer.shipped.v1"
	TransferReceived = "dev.passage.inventory.transfer.received.v1"
)

// TransferLineData is one line of a transfer event. Qty is the quantity this
// event moved on the line (shipped, or received); ShippedQty and ReceivedQty
// are the line totals after it.
type TransferLineData struct {
	LineID       string            `json:"lineId"`
	ProductID    string            `json:"productId"`
	DimensionSet map[string]string `json:"dimensionSet"`
	Unit         string            `json:"unit"`
	Qty          string            `json:"qty"`
	ShippedQty   string            `json:"shippedQty"`
	ReceivedQty  string            `json:"receivedQty"`
	Closed       bool              `json:"closed"`
}

// TransferData is the payload of transfer.shipped and transfer.received. Lines
// are the lines the event moved. ReceiptDate is the date of this receipt (on a
// one-step shipment, the ship date); it is null on a two-step shipment.
type TransferData struct {
	TransferOrderID  string             `json:"transferOrderId"`
	LegalEntityID    string             `json:"legalEntityId"`
	Number           string             `json:"number"`
	Status           string             `json:"status"`
	FromWarehouse    string             `json:"fromWarehouse"`
	ToWarehouse      string             `json:"toWarehouse"`
	TransitWarehouse *string            `json:"transitWarehouse"`
	ShipDate         string             `json:"shipDate"`
	ReceiptDate      *string            `json:"receiptDate"`
	Lines            []TransferLineData `json:"lines"`
}
