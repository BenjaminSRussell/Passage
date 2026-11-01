package events

// DeliveryScheduleConfirmed is written when the vendor's confirmed date of a PO delivery schedule
// row is set or changed (FR-17.10). The subject is the PO id, so it stays ordered with the PO's
// other events.
const DeliveryScheduleConfirmed = "dev.passage.procurement.deliveryschedule.confirmed.v1"

// DeliveryScheduleConfirmedData is the data member of deliveryschedule.confirmed.v1.
type DeliveryScheduleConfirmedData struct {
	POID                  string `json:"po_id"`
	Number                string `json:"number"`
	LineNo                int    `json:"line_no"`
	Seq                   int    `json:"seq"`
	ProductID             string `json:"product_id,omitempty"`
	Qty                   string `json:"qty"`
	Unit                  string `json:"unit"`
	DeliveryDate          string `json:"delivery_date"`
	ConfirmedDate         string `json:"confirmed_date"`
	PreviousConfirmedDate string `json:"previous_confirmed_date,omitempty"`
	ConfirmedBy           string `json:"confirmed_by,omitempty"`
}
