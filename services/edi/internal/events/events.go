// Package events holds the event types and payloads the edi service publishes.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

// Context is the bounded context segment of every event type.
const Context = "edi"

// MessageReceived is published once per inbound transaction after its
// interchange is recorded (FR-35.4 consumers: purchasing, sales, tms).
var MessageReceived = events.Type(Context, "message", "received", 1)

// MessageAcked is published once per outbound transaction (or EDIFACT
// message) when the partner's 997 (or CONTRL) answering it is matched
// (BR-35.5; consumers: analytics, the monitor).
var MessageAcked = events.Type(Context, "message", "acked", 1)

// MessageFailed is published when an inbound transaction fails mapping or a
// received 997 cannot be read or matched, at receipt and again when a
// reprocess leaves it failing with a different error (PRD §6.35.5; consumers:
// the monitor and the task queue).
var MessageFailed = events.Type(Context, "message", "failed", 1)

// MessageSent is published once per outbound transaction when the partner's
// transport confirmed receipt of its interchange: a processed AS2 MDN
// (synchronous or asynchronous) or a completed SFTP upload (PRD §6.35.5;
// consumers: the monitor and analytics).
var MessageSent = events.Type(Context, "message", "sent", 1)

// DeliveryFailed is published once when a transport delivery (an outbound
// interchange or a 997 over AS2 or SFTP) gives up and needs an operator: its
// attempts ran out, its asynchronous MDN never came within the wait
// (MDNTimeout), or the partner's transport is gone. A resend queues a new
// delivery (consumers: the monitor and the task queue).
var DeliveryFailed = events.Type(Context, "delivery", "failed", 1)

// MessagePosted is published once when an inbound 850 or ORDERS became a sales
// order (PRD §6.35.5; consumers: the monitor and analytics).
var MessagePosted = events.Type(Context, "message", "posted", 1)

// The B2B batch events (#35): an e-invoice handed to its network and the network's answer, a punchout cart returned for
// requisition, a customer's self-billed document matched, and a self-billing dispute opened or decided.
var (
	EInvoiceSubmitted     = events.Type(Context, "einvoice", "submitted", 1)
	EInvoiceAccepted      = events.Type(Context, "einvoice", "accepted", 1)
	EInvoiceRejected      = events.Type(Context, "einvoice", "rejected", 1)
	PunchoutReturned      = events.Type(Context, "punchout", "returned", 1)
	SelfBillingReconciled = events.Type(Context, "selfbilling", "reconciled", 1)
	DisputeOpened         = events.Type(Context, "dispute", "opened", 1)
	DisputeResolved       = events.Type(Context, "dispute", "resolved", 1)
)

// All lists every event type this service publishes.
func All() []string {
	return []string{MessageReceived, MessageAcked, MessageFailed, MessageSent, MessagePosted, DeliveryFailed,
		EInvoiceSubmitted, EInvoiceAccepted, EInvoiceRejected, PunchoutReturned, SelfBillingReconciled, DisputeOpened, DisputeResolved}
}

// EInvoiceData is the payload of the e-invoice events. Reference is the network's submission reference, Reason the
// network's rejection text.
type EInvoiceData struct {
	EInvoiceID    string `json:"einvoice_id"`
	LegalEntityID string `json:"legal_entity_id"`
	InvoiceID     string `json:"invoice_id"`
	Number        string `json:"number"`
	Mandate       string `json:"mandate"`
	Reference     string `json:"reference,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// PunchoutLine is one requisition line a returned cart contributes (BR-35.13): catalog-sourced, so Catalog is always true.
type PunchoutLine struct {
	Line           int    `json:"line"`
	SupplierPartID string `json:"supplier_part_id"`
	Description    string `json:"description"`
	Quantity       string `json:"quantity"`
	UnitCode       string `json:"unit_code"`
	UnitPrice      string `json:"unit_price"`
	Currency       string `json:"currency"`
	UNSPSC         string `json:"unspsc,omitempty"`
	LeadTimeDays   int    `json:"lead_time_days,omitempty"`
	Catalog        bool   `json:"catalog"`
	NeedsApproval  bool   `json:"needs_approval"`
}

// PunchoutReturnedData is the payload of PunchoutReturned: the lines to put on a requisition for the requester.
type PunchoutReturnedData struct {
	SessionID     string         `json:"session_id"`
	PartnerID     string         `json:"partner_id"`
	LegalEntityID string         `json:"legal_entity_id"`
	RequesterID   string         `json:"requester_id"`
	CartRejected  bool           `json:"cart_rejected"`
	Lines         []PunchoutLine `json:"lines"`
}

// SelfBillingReconciledData is the payload of SelfBillingReconciled.
type SelfBillingReconciledData struct {
	DocumentID    string `json:"document_id"`
	PartnerID     string `json:"partner_id"`
	LegalEntityID string `json:"legal_entity_id"`
	Number        string `json:"number"`
	Status        string `json:"status"`
	MatchedLines  int    `json:"matched_lines"`
	DisputedLines int    `json:"disputed_lines"`
}

// DisputeData is the payload of DisputeOpened and DisputeResolved (Decision is ACCEPTED or REJECTED on the latter).
type DisputeData struct {
	DisputeID     string `json:"dispute_id"`
	PartnerID     string `json:"partner_id"`
	LegalEntityID string `json:"legal_entity_id"`
	Kind          string `json:"kind"`
	Code          string `json:"code"`
	DocumentID    string `json:"document_id,omitempty"`
	Line          string `json:"line,omitempty"`
	ShipmentID    string `json:"shipment_id,omitempty"`
	Detail        string `json:"detail"`
	Decision      string `json:"decision,omitempty"`
}

// MessagePostedData is the payload of MessagePosted.
type MessagePostedData struct {
	MessageID      string `json:"message_id"`
	InterchangeID  string `json:"interchange_id"`
	PartnerID      string `json:"partner_id"`
	TransactionSet string `json:"transaction_set"`
	ControlNumber  string `json:"control_number"`
	STControl      string `json:"st_control"`
	SalesOrderID   string `json:"sales_order_id"`
}

// DeliveryFailedData is the payload of DeliveryFailed. PayloadKind is
// DOCUMENT (an outbound interchange) or ACK (the 997 for an inbound one);
// Attempts counts the sends, MDNTimeouts the waits for an asynchronous MDN
// that ran out, and MDNTimeout says the last one did. Error is the last
// failure as recorded on the delivery (never secret material).
type DeliveryFailedData struct {
	DeliveryID         string `json:"delivery_id"`
	InterchangeID      string `json:"interchange_id"`
	PartnerID          string `json:"partner_id"`
	PayloadKind        string `json:"payload_kind"`
	Transport          string `json:"transport"`
	TransportMessageID string `json:"transport_message_id"`
	Attempts           int    `json:"attempts"`
	MDNTimeouts        int    `json:"mdn_timeouts"`
	MDNTimeout         bool   `json:"mdn_timeout"`
	Error              string `json:"error"`
}

// MessageSentData is the payload of MessageSent. Transport is AS2 or SFTP;
// TransportMessageID is the delivery's message id: the AS2 Message-ID
// without angle brackets (an SFTP delivery has one too; its file name is on
// the delivery).
type MessageSentData struct {
	MessageID          string `json:"message_id"`
	InterchangeID      string `json:"interchange_id"`
	PartnerID          string `json:"partner_id"`
	TransactionSet     string `json:"transaction_set"`
	ControlNumber      string `json:"control_number"`
	STControl          string `json:"st_control"`
	Transport          string `json:"transport"`
	DeliveryID         string `json:"delivery_id"`
	TransportMessageID string `json:"transport_message_id"`
}

// MessageReceivedData is the event payload. Canonical content is not included;
// consumers read it through the message API so PII stays out of the bus.
type MessageReceivedData struct {
	MessageID      string `json:"message_id"`
	InterchangeID  string `json:"interchange_id"`
	PartnerID      string `json:"partner_id"`
	Standard       string `json:"standard"`
	TransactionSet string `json:"transaction_set"`
	ControlNumber  string `json:"control_number"`
	STControl      string `json:"st_control"`
	Status         string `json:"status"`
	CanonicalRef   string `json:"canonical_ref,omitempty"`
}

// MessageAckedData is the payload of MessageAcked. AckStatus is ACCEPTED,
// ACCEPTED_WITH_ERRORS or REJECTED from the partner's AK5 for this transaction
// (or AK9 when the 997 acknowledges the whole group without AK2 loops); for
// an EDIFACT message, from the partner's CONTRL (REJECTED for UCI 4 or a UCM
// 4 naming the message, ACCEPTED otherwise).
type MessageAckedData struct {
	MessageID      string `json:"message_id"`
	InterchangeID  string `json:"interchange_id"`
	PartnerID      string `json:"partner_id"`
	TransactionSet string `json:"transaction_set"`
	ControlNumber  string `json:"control_number"`
	STControl      string `json:"st_control"`
	AckStatus      string `json:"ack_status"`
	AckMessageID   string `json:"ack_message_id"`
}

// MessageFailedData is the payload of MessageFailed. Error carries the
// catalog code and detail of the failing rule (for example EDI-0103 or
// EDI-0161). Reprocessed is true when a reprocess produced the failure.
type MessageFailedData struct {
	MessageID      string `json:"message_id"`
	InterchangeID  string `json:"interchange_id"`
	PartnerID      string `json:"partner_id"`
	TransactionSet string `json:"transaction_set"`
	ControlNumber  string `json:"control_number"`
	STControl      string `json:"st_control"`
	Error          string `json:"error"`
	Reprocessed    bool   `json:"reprocessed"`
}
