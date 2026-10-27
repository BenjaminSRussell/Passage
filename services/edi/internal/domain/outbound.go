package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// OutboundVersion is the X12 version outbound documents are written in until
// the partner profile carries x12_version per transaction set (PRD §6.35.13).
const OutboundVersion = "00401"

// DefaultDelims are the PRD §6.35.13 default separators for 00401: element *,
// segment ~, component >.
var DefaultDelims = Delims{Element: '*', Component: '>', Segment: '~'}

// functionalIDs maps each outbound transaction set to its GS01 functional id.
var functionalIDs = map[string]string{"855": "PR", "856": "SH", "810": "IN"}

// FunctionalIDOf returns the GS01 functional id of an outbound transaction
// set, or "" for a set this service does not send.
func FunctionalIDOf(setID string) string { return functionalIDs[setID] }

// OutboundSets lists the transaction sets BuildOutbound writes.
var OutboundSets = []string{"855", "856", "810"}

// BuildOutbound decodes a canonical document for setID and builds its
// transaction. It returns the normalized document, re-encoded, so a caller can
// store and fingerprint it independent of JSON spacing or field order.
// Unknown fields are refused. Every failure wraps ErrOutbound.
func BuildOutbound(setID string, document json.RawMessage) (Transaction, []byte, error) {
	var (
		tr  Transaction
		doc any
		err error
	)
	switch setID {
	case "855":
		var a Ack855
		if err = strictDecode(document, &a); err == nil {
			tr, err = Build855(a)
		}
		doc = a
	case "856":
		var a ASN
		if err = strictDecode(document, &a); err == nil {
			tr, err = Build856(a)
		}
		doc = a
	case "810":
		var inv Invoice
		if err = strictDecode(document, &inv); err == nil {
			tr, err = Build810(inv)
		}
		doc = inv
	default:
		return Transaction{}, nil, outboundf("transaction set %q is not an outbound set (855, 856 or 810)", setID)
	}
	if err != nil {
		return Transaction{}, nil, err
	}
	normalized, err := json.Marshal(doc)
	if err != nil {
		return Transaction{}, nil, err
	}
	return tr, normalized, nil
}

func strictDecode(raw json.RawMessage, v any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return outboundf("document is required")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return outboundf("document is not valid: %v", err)
	}
	return nil
}

// OutboundEnvelope wraps one transaction in an 00401 interchange from us to the
// partner. control is the gap-free outbound number (BR-35.5): it is ISA13
// (zero-padded to nine digits) and GS06; ST02 is 0001 because each interchange
// carries one transaction. Usage is T for a TEST partner and P for PROD.
func OutboundEnvelope(t Transaction, from, to Party, usage string, control int64, at time.Time) (Interchange, error) {
	fid, ok := functionalIDs[t.SetID]
	if !ok {
		return Interchange{}, outboundf("transaction set %q has no functional group id", t.SetID)
	}
	if control < 1 || control > 999_999_999 {
		return Interchange{}, fmt.Errorf("edi: outbound control number %d is outside ISA13", control)
	}
	t.Control = "0001"
	at = at.UTC()
	return Interchange{
		Delims:   DefaultDelims,
		Sender:   from,
		Receiver: to,
		Date:     at.Format("060102"),
		Time:     at.Format("1504"),
		Version:  OutboundVersion,
		Control:  fmt.Sprintf("%09d", control),
		Usage:    usage,
		Groups: []Group{{
			FunctionalID: fid,
			Sender:       from.ID,
			Receiver:     to.ID,
			Date:         at.Format("20060102"),
			Time:         at.Format("1504"),
			Control:      strconv.FormatInt(control, 10),
			Version:      "004010",
			Transactions: []Transaction{t},
		}},
	}, nil
}
