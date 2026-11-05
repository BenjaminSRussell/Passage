package domain

import (
	"fmt"
	"time"
)

// CONTRL action codes (ISO 9735 data element 0083).
const (
	ContrlRejected     = "4" // this level and all lower levels rejected
	ContrlAcknowledged = "7" // this level acknowledged; lower levels acknowledged unless explicitly rejected
	ContrlReceived     = "8" // interchange received (receipt only, nothing checked)
)

// Common CONTRL syntax error codes (data element 0085).
const (
	ContrlErrInvalidValue = "12" // invalid value
	ContrlErrMissing      = "13" // missing
	ContrlErrNotSupported = "15" // not supported in this position
	ContrlErrUnspecified  = "18" // unspecified error
	ContrlErrDuplicate    = "26" // duplicate detected
	ContrlErrReferences   = "28" // references do not match
	ContrlErrCount        = "29" // control count does not match number of instances received
)

// EdifactOutcome is the result for one received message: Accepted, or the
// 0085 error code and, when known, the position of the failing segment in the
// message (UNH is 1) that becomes UCS.
type EdifactOutcome struct {
	Ref             string
	Accepted        bool
	ErrorCode       string
	SegmentPosition int
}

// contrlVersion is the S009 version of CONTRL for each syntax version.
func contrlVersion(syntax string) (string, string) {
	if syntax == "4" {
		return "4", "1"
	}
	return "2", "2"
}

// BuildCONTRL answers a received interchange with a CONTRL from its recipient
// back to its sender. UCI carries the received UNB05, sender and recipient and
// action 7 when at least one message is accepted (4 when every message is
// rejected or there were none). Each rejected message gets a UCM 4 with its
// error and, with a position, a UCS; accepted messages are acknowledged by
// UCI 7 and are not listed. ourControl is the outbound UNB05 for the CONTRL
// interchange. A CONTRL never requests a CONTRL (UNB09 is empty).
func BuildCONTRL(received EdifactInterchange, outcomes []EdifactOutcome, ourControl string, at time.Time) (EdifactInterchange, error) {
	byRef := map[string]EdifactMessage{}
	for _, m := range received.Messages {
		if m.Type == "CONTRL" {
			return EdifactInterchange{}, edifactf("a CONTRL is never acknowledged with a CONTRL")
		}
		byRef[m.Ref] = m
	}
	action := ContrlRejected
	var body []EdifactSegment
	for _, o := range outcomes {
		m, ok := byRef[o.Ref]
		if !ok {
			return EdifactInterchange{}, edifactf("outcome for message %s that the interchange does not carry", o.Ref)
		}
		if o.Accepted {
			action = ContrlAcknowledged
			continue
		}
		code := o.ErrorCode
		if code == "" {
			code = ContrlErrUnspecified
		}
		body = append(body, EdifactSegment{Tag: "UCM", Elements: [][]string{el(m.Ref), el(m.Type, m.Version, m.Release, m.Agency, m.Association), el(ContrlRejected), el(code)}})
		if o.SegmentPosition > 0 {
			body = append(body, Seg("UCS", fmt.Sprint(o.SegmentPosition), code))
		}
	}
	if len(outcomes) != len(received.Messages) {
		return EdifactInterchange{}, edifactf("%d outcomes for %d messages", len(outcomes), len(received.Messages))
	}
	uci := EdifactSegment{Tag: "UCI", Elements: [][]string{el(received.Control), el(received.Sender.ID, received.Sender.Qualifier),
		el(received.Recipient.ID, received.Recipient.Qualifier), el(action)}}
	v, r := contrlVersion(received.SyntaxVersion)
	at = at.UTC()
	return EdifactInterchange{
		Delims: received.Delims, WithUNA: received.WithUNA, Syntax: received.Syntax, SyntaxVersion: received.SyntaxVersion,
		Sender: received.Recipient, Recipient: received.Sender, Date: at.Format("060102"), Time: at.Format("1504"),
		Control: ourControl, Test: received.Test,
		Messages: []EdifactMessage{{Ref: "1", Type: "CONTRL", Version: v, Release: r, Agency: "UN", Segments: append([]EdifactSegment{uci}, body...)}},
	}, nil
}

// RejectInterchangeCONTRL answers an interchange whose UNB was readable but
// whose structure failed (counts, references, unsupported groups) with UCI 4
// and the 0085 code, without listing messages.
func RejectInterchangeCONTRL(received EdifactInterchange, errorCode, ourControl string, at time.Time) (EdifactInterchange, error) {
	ic, err := BuildCONTRL(EdifactInterchange{Delims: received.Delims, WithUNA: received.WithUNA, Syntax: received.Syntax, SyntaxVersion: received.SyntaxVersion,
		Sender: received.Sender, Recipient: received.Recipient, Control: received.Control, Test: received.Test}, nil, ourControl, at)
	if err != nil {
		return ic, err
	}
	ic.Messages[0].Segments[0].Elements = append(ic.Messages[0].Segments[0].Elements, el(errorCode))
	return ic, nil
}

// ContrlMessage is one UCM of a received CONTRL.
type ContrlMessage struct {
	Ref       string
	Type      string
	Action    string
	ErrorCode string
}

// ContrlAck is a received CONTRL: the acknowledged interchange reference, the
// UCI action and error, and the per-message UCMs.
type ContrlAck struct {
	InterchangeRef string
	Sender         EdifactParty
	Recipient      EdifactParty
	Action         string
	ErrorCode      string
	Messages       []ContrlMessage
}

// Status folds the CONTRL into the interchange ack status stored for the
// outbound interchange, like InterchangeAckStatus does for AK9.
func (c ContrlAck) Status(sentMessages int) AckStatus {
	rejected := 0
	for _, m := range c.Messages {
		if m.Action == ContrlRejected {
			rejected++
		}
	}
	switch {
	case c.Action == ContrlRejected, sentMessages > 0 && rejected >= sentMessages:
		return StatusRejected
	case rejected > 0:
		return StatusAcceptedWithErrors
	default:
		return StatusAccepted
	}
}

// ParseCONTRL reads a CONTRL message.
func ParseCONTRL(m EdifactMessage) (ContrlAck, error) {
	if m.Type != "CONTRL" {
		return ContrlAck{}, edifactf("message %s is %s, not CONTRL", m.Ref, m.Type)
	}
	var c ContrlAck
	sawUCI := false
	for _, s := range m.Segments {
		switch s.Tag {
		case "UCI":
			sawUCI = true
			c.InterchangeRef = s.El(0, 0)
			c.Sender = EdifactParty{ID: s.El(1, 0), Qualifier: s.El(1, 1)}
			c.Recipient = EdifactParty{ID: s.El(2, 0), Qualifier: s.El(2, 1)}
			c.Action, c.ErrorCode = s.El(3, 0), s.El(4, 0)
		case "UCM":
			c.Messages = append(c.Messages, ContrlMessage{Ref: s.El(0, 0), Type: s.El(1, 0), Action: s.El(2, 0), ErrorCode: s.El(3, 0)})
		}
	}
	if !sawUCI || c.InterchangeRef == "" {
		return c, edifactf("CONTRL needs a UCI with the interchange reference")
	}
	switch c.Action {
	case ContrlRejected, ContrlAcknowledged, ContrlReceived:
	default:
		return c, edifactf("UCI action %q is not 4, 7 or 8", c.Action)
	}
	return c, nil
}
