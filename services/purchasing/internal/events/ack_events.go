package events

const (
	POAckAccepted = "dev.passage.procurement.poack.accepted.v1"
	POAckRejected = "dev.passage.procurement.poack.rejected.v1"
	POAckProposed = "dev.passage.procurement.poack.proposed.v1"
	POAckDecided  = "dev.passage.procurement.poack.decided.v1"
)

type POAckChange struct {
	LineNo       int    `json:"line_no"`
	Qty          string `json:"qty,omitempty"`
	UnitPrice    string `json:"unit_price,omitempty"`
	DeliveryDate string `json:"delivery_date,omitempty"`
}

type POAckData struct {
	AckID              string        `json:"ack_id"`
	POID               string        `json:"po_id"`
	Number             string        `json:"number"`
	VersionNo          int           `json:"version_no"`
	VendorID           string        `json:"vendor_id"`
	LegalEntityID      string        `json:"legal_entity_id"`
	Outcome            string        `json:"outcome"`
	Reason             string        `json:"reason,omitempty"`
	Changes            []POAckChange `json:"changes,omitempty"`
	Decision           string        `json:"decision,omitempty"`
	DecidedBy          string        `json:"decided_by,omitempty"`
	Note               string        `json:"note,omitempty"`
	ResultingVersionNo int           `json:"resulting_version_no,omitempty"`
}
