package domain

import (
	"strconv"
	"time"
)

// TA1 acknowledgment codes (TA104, element I17).
const (
	TA1Accepted           = "A"
	TA1AcceptedWithErrors = "E"
	TA1Rejected           = "R"
)

// TA1 note codes (TA105, element I18) this service writes.
const (
	TA1NoError         = "000"
	TA1ControlMismatch = "001" // the interchange control number in the header and trailer do not match
	TA1GroupCount      = "021" // invalid number of included groups value
	TA1PrematureEnd    = "023" // improper (premature) end-of-file
	TA1InvalidContent  = "024" // invalid interchange content (no GS, or content after IEA)
	TA1Duplicate       = "025" // duplicate interchange control number
)

// InterchangeAck is one TA1 segment: the interchange it acknowledges (TA101
// control number, TA102 date, TA103 time, as in that interchange's ISA), the
// TA104 acknowledgment code and the TA105 note code.
type InterchangeAck struct {
	Control string `json:"control"`
	Date    string `json:"date"`
	Time    string `json:"time"`
	Code    string `json:"code"`
	Note    string `json:"note"`
}

func parseTA1(s Segment) (InterchangeAck, error) {
	a := InterchangeAck{Control: element(s, 0), Date: element(s, 1), Time: element(s, 2), Code: element(s, 3), Note: element(s, 4)}
	if !isDigits(a.Control, 9) {
		return a, syntaxf("TA101 %q is not a nine-digit interchange control number", a.Control)
	}
	switch a.Code {
	case TA1Accepted, TA1AcceptedWithErrors, TA1Rejected:
	default:
		return a, syntaxf("TA104 %q is not A, E or R", a.Code)
	}
	if !isDigits(a.Note, 3) {
		return a, syntaxf("TA105 %q is not a three-digit note code", a.Note)
	}
	return a, nil
}

// BuildTA1 answers a received interchange at the interchange level: an
// interchange of one TA1 and no groups, from its receiver back to its sender
// in its version, delimiters and usage, echoing its ISA13, ISA09 and ISA10.
// ourControl is our ISA13 for the TA1 interchange (from the gap-free counter
// the 997s use). code is TA104 (A, E, R) and note TA105.
func BuildTA1(received Interchange, code, note, ourControl string, at time.Time) (Interchange, error) {
	if !isDigits(received.Control, 9) || received.Sender.ID == "" || received.Receiver.ID == "" {
		return Interchange{}, syntaxf("a TA1 needs the ISA sender, receiver and a nine-digit ISA13")
	}
	ack := InterchangeAck{Control: received.Control, Date: received.Date, Time: received.Time, Code: code, Note: note}
	if code != TA1Accepted && code != TA1AcceptedWithErrors && code != TA1Rejected || !isDigits(note, 3) {
		return Interchange{}, syntaxf("TA104 %q or TA105 %q is invalid", code, note)
	}
	at = at.UTC()
	return Interchange{
		Delims:   received.Delims,
		Sender:   received.Receiver,
		Receiver: received.Sender,
		Date:     at.Format("060102"),
		Time:     at.Format("1504"),
		Version:  received.Version,
		Control:  ourControl,
		Usage:    received.Usage,
		TA1:      []InterchangeAck{ack},
	}, nil
}

// IsTA1Interchange reports whether raw is an interchange of TA1 segments
// only (no functional groups).
func IsTA1Interchange(raw string) bool {
	ic, err := Parse(raw)
	return err == nil && len(ic.TA1) > 0 && len(ic.Groups) == 0
}

// TA1Cause reads what a TA1 for a refused X12 interchange needs: its ISA
// header and the interchange-level error, if there is one: IEA missing (023),
// IEA02 not ISA13 (001), IEA01 not the number of GS groups (021), no group at
// all or content after IEA (024). ok is false when the ISA cannot be read,
// ISA13 is not nine digits, the error lies inside a group (a 997 reports
// it), or the interchange carries TA1s and no group: a TA1 is never answered.
func TA1Cause(raw string) (ic Interchange, note string, ok bool) {
	ic, body, err := readISA(raw)
	if err != nil || !isDigits(ic.Control, 9) {
		return Interchange{}, "", false
	}
	segs, err := splitSegments(body, ic.Delims)
	if err != nil {
		return ic, TA1InvalidContent, true
	}
	groups, ta1s, iea := 0, 0, -1
	for i, s := range segs {
		switch s.ID {
		case "TA1":
			ta1s++
		case "GS":
			groups++
		case "IEA":
			if iea < 0 {
				iea = i
			}
		}
	}
	switch {
	case ta1s > 0 && groups == 0:
		return ic, "", false
	case iea < 0:
		return ic, TA1PrematureEnd, true
	case element(segs[iea], 1) != ic.Control:
		return ic, TA1ControlMismatch, true
	case element(segs[iea], 0) != strconv.Itoa(groups):
		return ic, TA1GroupCount, true
	case groups == 0, iea != len(segs)-1:
		return ic, TA1InvalidContent, true
	}
	return ic, "", false
}
