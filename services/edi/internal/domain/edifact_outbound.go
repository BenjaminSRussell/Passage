package domain

import "encoding/json"

// EdifactOutboundTypes lists the EDIFACT messages BuildEdifactOutbound
// writes for a partner: DESADV and INVOIC are the EDIFACT counterparts of the
// 856 and 810 we send, ORDERS is the purchase order (the canonical order the
// 850 maps to) for a partner we buy from.
var EdifactOutboundTypes = []string{"ORDERS", "DESADV", "INVOIC"}

// IsEdifactOutbound reports whether t is an EDIFACT message type
// BuildEdifactOutbound writes.
func IsEdifactOutbound(t string) bool {
	for _, x := range EdifactOutboundTypes {
		if x == t {
			return true
		}
	}
	return false
}

// EdifactOutboundVersion is the stored version of an outbound EDIFACT
// interchange: the UNB S001 syntax identifier and version EdifactOutbound
// writes.
const EdifactOutboundVersion = "UNOC:3"

// BuildEdifactOutbound decodes the canonical document for an EDIFACT message
// type and builds the message (UNH reference unset: EdifactOutbound numbers
// it). DESADV is held to the 856 rules and INVOIC to the 810 rules by their
// builders, so a document refused in X12 is refused in EDIFACT too. It returns
// the normalized document, re-encoded, like BuildOutbound. Unknown fields are
// refused; every failure wraps ErrOutbound.
func BuildEdifactOutbound(msgType string, document json.RawMessage) (EdifactMessage, []byte, error) {
	var (
		m   EdifactMessage
		doc any
		err error
	)
	switch msgType {
	case "ORDERS":
		var o Order
		if err = strictDecode(document, &o); err == nil {
			m, err = BuildORDERS(o)
		}
		doc = o
	case "DESADV":
		var a ASN
		if err = strictDecode(document, &a); err == nil {
			m, err = BuildDESADV(a)
		}
		doc = a
	case "INVOIC":
		var inv Invoice
		if err = strictDecode(document, &inv); err == nil {
			m, err = BuildINVOIC(inv)
		}
		doc = inv
	default:
		return EdifactMessage{}, nil, outboundf("message type %q is not an outbound EDIFACT message (ORDERS, DESADV or INVOIC)", msgType)
	}
	if err != nil {
		return EdifactMessage{}, nil, err
	}
	normalized, err := json.Marshal(doc)
	if err != nil {
		return EdifactMessage{}, nil, err
	}
	return m, normalized, nil
}

// ContrlAnswer is what a received CONTRL says about one message of the
// outbound interchange it answers: the message is accepted (ACKED) unless
// the UCI rejects the whole interchange or a UCM rejects the message.
type ContrlAnswer struct {
	Ref    string
	Type   string
	Status AckStatus
	Detail string
}

// AnswerMessages applies the CONTRL to the messages (UNH reference and type)
// of the outbound interchange it answers. UCI 4 rejects every message; UCI 7
// accepts every message no UCM 4 names; UCI 8 (received, nothing checked)
// accepts them as received. notes lists UCMs that name no message of the
// interchange or another message type.
func (c ContrlAck) AnswerMessages(sent []EdifactMessage) (answers []ContrlAnswer, notes []string) {
	rejected := map[string]ContrlMessage{}
	for _, um := range c.Messages {
		found := false
		for _, m := range sent {
			if m.Ref == um.Ref {
				found = true
				if um.Type != "" && um.Type != m.Type {
					notes = append(notes, "UCM "+um.Ref+" names "+um.Type+", the message is "+m.Type)
				}
			}
		}
		if !found {
			notes = append(notes, "UCM "+um.Ref+" names no message of the interchange")
			continue
		}
		if um.Action == ContrlRejected {
			rejected[um.Ref] = um
		}
	}
	for _, m := range sent {
		a := ContrlAnswer{Ref: m.Ref, Type: m.Type, Status: StatusAccepted}
		switch um, isRejected := rejected[m.Ref]; {
		case isRejected:
			a.Status, a.Detail = StatusRejected, "rejected by the partner CONTRL (UCM 4"+codeSuffix(um.ErrorCode)+")"
		case c.Action == ContrlRejected:
			a.Status, a.Detail = StatusRejected, "interchange rejected by the partner CONTRL (UCI 4"+codeSuffix(c.ErrorCode)+")"
		case c.Action == ContrlReceived:
			a.Detail = "received by the partner (UCI 8); not checked"
		}
		answers = append(answers, a)
	}
	return answers, notes
}

func codeSuffix(code string) string {
	if code == "" {
		return ""
	}
	return ", error " + code
}
