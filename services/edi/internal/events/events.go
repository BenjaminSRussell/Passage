// Package events holds the event types and payloads the edi service publishes.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

// Context is the bounded context segment of every event type.
const Context = "edi"

// MessageReceived is published once per inbound transaction after its
// interchange is recorded (FR-35.4 consumers: purchasing, sales, tms).
var MessageReceived = events.Type(Context, "message", "received", 1)

// MessageAcked is published once per outbound transaction when the partner's
// 997 answering it is matched (BR-35.5; consumers: analytics, the monitor).
var MessageAcked = events.Type(Context, "message", "acked", 1)

// MessageFailed is published when an inbound transaction fails mapping or a
// received 997 cannot be read or matched, at receipt and again when a
// reprocess leaves it failing with a different error (PRD §6.35.5; consumers:
// the monitor and the task queue).
var MessageFailed = events.Type(Context, "message", "failed", 1)

// All lists every event type this service publishes. posted and sent (PRD
// §6.35.5) are published once posting into waves 26/19 and the AS2/SFTP
// transports exist; see NOT-IMPLEMENTED.md.
func All() []string { return []string{MessageReceived, MessageAcked, MessageFailed} }

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
// (or AK9 when the 997 acknowledges the whole group without AK2 loops).
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
