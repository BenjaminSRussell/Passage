package events

// Transfer order event types (FR-16.15, section 3.16.5).
const (
	TransferShipped  = "dev.passage.inventory.transfer.shipped.v1"
	TransferReceived = "dev.passage.inventory.transfer.received.v1"
	// TransferChanged is published when a CREATED order is edited, confirmed from
	// a draft, deleted (status DELETED), or any order is cancelled; it carries
	// TransferData with every line of the order.
	TransferChanged = "dev.passage.inventory.transfer.changed.v1"
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

// TransferData is the payload of transfer.shipped, transfer.received and
// transfer.changed. Lines are the lines the event moved (on transfer.changed,
// every line of the order). ReceiptDate is the date of this receipt (on a
// one-step shipment, the ship date); it is null on a two-step shipment.
type TransferData struct {
	TransferOrderID  string  `json:"transferOrderId"`
	LegalEntityID    string  `json:"legalEntityId"`
	Number           string  `json:"number"`
	Status           string  `json:"status"`
	FromWarehouse    string  `json:"fromWarehouse"`
	ToWarehouse      string  `json:"toWarehouse"`
	TransitWarehouse *string `json:"transitWarehouse"`
	ShipDate         string  `json:"shipDate"`
	ReceiptDate      *string `json:"receiptDate"`
	// ToSiteID is the receiving site when it differs from the lines' site, and
	// PlannedReceiptDate the planned receipt date; both null when not set.
	ToSiteID           *string `json:"toSiteId"`
	PlannedReceiptDate *string `json:"plannedReceiptDate"`
	// DeliveryTerms and ModeOfDelivery are the order's delivery terms code and
	// transport mode code; null when not set.
	DeliveryTerms  *string            `json:"deliveryTerms"`
	ModeOfDelivery *string            `json:"modeOfDelivery"`
	Lines          []TransferLineData `json:"lines"`
}
