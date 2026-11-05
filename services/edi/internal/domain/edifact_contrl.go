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
	ContrlErrSyntaxVersion = "2"  // syntax version or level not supported
	ContrlErrInvalidValue  = "12" // invalid value
	ContrlErrMissing       = "13" // missing
	ContrlErrNotSupported  = "15" // not supported in this position
	ContrlErrUnspecified   = "18" // unspecified error
	ContrlErrServiceChars  = "22" // invalid service character(s)
	ContrlErrUnknownSender = "23" // unknown interchange sender
	ContrlErrDuplicate     = "26" // duplicate detected
	ContrlErrReferences    = "28" // references do not match
	ContrlErrCount         = "29" // control count does not match number of instances received
	ContrlErrMixed         = "30" // functional groups and messages mixed
	ContrlErrEmpty         = "32" // lower level empty
	ContrlErrOutside       = "33" // invalid occurrence outside message or group
)

// EdifactOutcome is the result for one received message (Group and Ref name
// it): Accepted, or the 0085 error code and, when known, the position of the
// failing segment in the message (UNH is 1) that becomes UCS, and of the
// data element and component inside it that becomes UCD.
type EdifactOutcome struct {
	Group             string
	Ref               string
	Accepted          bool
	ErrorCode         string
	SegmentPosition   int
	ElementPosition   int
	ComponentPosition int
}

// contrlVersion is the S009 version of CONTRL for each syntax version.
func contrlVersion(syntax string) (string, string) {
	if syntax == "4" {
		return "4", "1"
	}
	return "2", "2"
}

// contrlEnvelope is the CONTRL interchange answering received: back from its
// recipient to its sender in the sender's service characters and syntax
// (syntax 3 when the received version is one we do not write), never
// requesting a CONTRL itself.
func contrlEnvelope(received EdifactInterchange, ourControl string, at time.Time, segs []EdifactSegment) EdifactInterchange {
	syntax, version := received.Syntax, received.SyntaxVersion
	if version != "3" && version != "4" {
		version = "3"
	}
	if syntax == "" {
		syntax = "UNOA"
	}
	v, r := contrlVersion(version)
	at = at.UTC()
	return EdifactInterchange{
		Delims: received.Delims, WithUNA: received.WithUNA || received.Delims != DefaultEdifactDelims, Syntax: syntax, SyntaxVersion: version,
		Sender: received.Recipient, Recipient: received.Sender, Date: at.Format("060102"), Time: at.Format("1504"),
		Control: ourControl, Test: received.Test,
		Messages: []EdifactMessage{{Ref: "1", Type: "CONTRL", Version: v, Release: r, Agency: "UN", Segments: segs}},
	}
}

func contrlKey(group, ref string) string { return group + "\x00" + ref }

// rejectedMessage is the UCM 4 of one rejected message with its UCS and UCD.
func rejectedMessage(m EdifactMessage, o EdifactOutcome) []EdifactSegment {
	code := o.ErrorCode
	if code == "" {
		code = ContrlErrUnspecified
	}
	out := []EdifactSegment{{Tag: "UCM", Elements: [][]string{el(m.Ref), el(m.Type, m.Version, m.Release, m.Agency, m.Association), el(ContrlRejected), el(code)}}}
	if o.SegmentPosition > 0 {
		out = append(out, Seg("UCS", fmt.Sprint(o.SegmentPosition), code))
		if o.ElementPosition > 0 {
			pos := fmt.Sprint(o.ElementPosition)
			if o.ComponentPosition > 0 {
				pos += ":" + fmt.Sprint(o.ComponentPosition)
			}
			out = append(out, Seg("UCD", code, pos))
		}
	}
	return out
}

// BuildCONTRL answers a received interchange with a CONTRL from its recipient
// back to its sender. UCI carries the received UNB05, sender and recipient and
// action 7 when at least one message is accepted (4 when every message is
// rejected or there were none). Each rejected message gets a UCM 4 with its
// error and, with a position, a UCS (and a UCD for the data element);
// accepted messages are acknowledged by UCI 7 and are not listed. In an
// interchange with functional groups each group holding a rejected message
// gets a UCF (7, or 4 when all its messages are rejected) before its UCMs.
// ourControl is the outbound UNB05 for the CONTRL interchange. A CONTRL never
// requests a CONTRL (UNB09 is empty).
func BuildCONTRL(received EdifactInterchange, outcomes []EdifactOutcome, ourControl string, at time.Time) (EdifactInterchange, error) {
	byKey := map[string]EdifactOutcome{}
	for _, o := range outcomes {
		byKey[contrlKey(o.Group, o.Ref)] = o
	}
	for _, m := range received.Messages {
		if m.Type == "CONTRL" {
			return EdifactInterchange{}, edifactf("a CONTRL is never acknowledged with a CONTRL")
		}
		if _, ok := byKey[contrlKey(m.Group, m.Ref)]; !ok {
			return EdifactInterchange{}, edifactf("message %s has no outcome", m.Ref)
		}
	}
	if len(outcomes) != len(received.Messages) {
		return EdifactInterchange{}, edifactf("%d outcomes for %d messages", len(outcomes), len(received.Messages))
	}
	action := ContrlRejected
	for _, o := range outcomes {
		if o.Accepted {
			action = ContrlAcknowledged
		}
	}
	var body []EdifactSegment
	if len(received.Groups) == 0 {
		for _, m := range received.Messages {
			if o := byKey[contrlKey(m.Group, m.Ref)]; !o.Accepted {
				body = append(body, rejectedMessage(m, o)...)
			}
		}
	}
	for _, g := range received.Groups {
		var rejected []EdifactSegment
		accepted, total := 0, 0
		for _, m := range received.Messages {
			if m.Group != g.Ref {
				continue
			}
			total++
			if o := byKey[contrlKey(m.Group, m.Ref)]; o.Accepted {
				accepted++
			} else {
				rejected = append(rejected, rejectedMessage(m, o)...)
			}
		}
		if accepted == total {
			continue
		}
		gAction := ContrlAcknowledged
		if accepted == 0 {
			gAction = ContrlRejected
		}
		body = append(body, EdifactSegment{Tag: "UCF", Elements: [][]string{el(g.Ref), el(g.Sender.ID, g.Sender.Qualifier),
			el(g.Recipient.ID, g.Recipient.Qualifier), el(gAction)}})
		body = append(body, rejected...)
	}
	uci := EdifactSegment{Tag: "UCI", Elements: [][]string{el(received.Control), el(received.Sender.ID, received.Sender.Qualifier),
		el(received.Recipient.ID, received.Recipient.Qualifier), el(action)}}
	return contrlEnvelope(received, ourControl, at, append([]EdifactSegment{uci}, body...)), nil
}

// RejectInterchangeCONTRL answers an interchange whose UNB was readable but
// whose structure failed (counts, references, unsupported groups) with UCI 4
// and the 0085 code, without listing messages.
func RejectInterchangeCONTRL(received EdifactInterchange, errorCode, ourControl string, at time.Time) (EdifactInterchange, error) {
	return RefusalCONTRL(received, &EdifactError{Code: errorCode}, ourControl, at)
}

// RefusalCONTRL answers an interchange refused as a whole (nothing of it is
// processed) with UCI 4. An interchange-level cause carries its 0085 code on
// the UCI; a cause in a functional group adds a UCF 4 for that group with the
// code, and a cause in a message a UCM 4 with its UCS and UCD. The UNB must
// have been read (received.Control set).
func RefusalCONTRL(received EdifactInterchange, cause *EdifactError, ourControl string, at time.Time) (EdifactInterchange, error) {
	if received.Control == "" || received.Sender.ID == "" || received.Recipient.ID == "" {
		return EdifactInterchange{}, edifactf("a CONTRL needs the UNB sender, recipient and interchange reference")
	}
	code := cause.Code
	if code == "" {
		code = ContrlErrUnspecified
	}
	uci := EdifactSegment{Tag: "UCI", Elements: [][]string{el(received.Control), el(received.Sender.ID, received.Sender.Qualifier),
		el(received.Recipient.ID, received.Recipient.Qualifier), el(ContrlRejected)}}
	segs := []EdifactSegment{uci}
	var group *EdifactGroup
	for i := range received.Groups {
		if received.Groups[i].Ref == cause.Group {
			group = &received.Groups[i]
		}
	}
	var msg *EdifactMessage
	for i := range received.Messages {
		if m := received.Messages[i]; m.Ref == cause.Message && m.Group == cause.Group {
			msg = &received.Messages[i]
		}
	}
	switch {
	case cause.Group != "" && group != nil:
		segs = append(segs, EdifactSegment{Tag: "UCF", Elements: [][]string{el(group.Ref), el(group.Sender.ID, group.Sender.Qualifier),
			el(group.Recipient.ID, group.Recipient.Qualifier), el(ContrlRejected), el(code)}})
		if msg != nil {
			segs = append(segs, rejectedMessage(*msg, EdifactOutcome{ErrorCode: code, SegmentPosition: cause.Segment,
				ElementPosition: cause.Element, ComponentPosition: cause.Component})...)
		}
	case cause.Message != "" && msg != nil:
		segs = append(segs, rejectedMessage(*msg, EdifactOutcome{ErrorCode: code, SegmentPosition: cause.Segment,
			ElementPosition: cause.Element, ComponentPosition: cause.Component})...)
	default:
		segs[0].Elements = append(segs[0].Elements, el(code))
	}
	return contrlEnvelope(received, ourControl, at, segs), nil
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
