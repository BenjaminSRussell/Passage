package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrEdifactSyntax marks an EDIFACT interchange that cannot be parsed or whose
// envelope counts and references are inconsistent (ISO 9735 syntax 3 and 4).
var ErrEdifactSyntax = errors.New("edifact: syntax error")

func edifactf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrEdifactSyntax, fmt.Sprintf(format, args...))
}

// EdifactDelims are the service characters of an interchange, declared by the
// UNA string advice or, without one, the ISO 9735 defaults. Repetition is used
// from syntax version 4; in syntax 3 UNA4 is reserved and written as a space.
type EdifactDelims struct {
	Component  byte
	Element    byte
	Decimal    byte
	Release    byte
	Repetition byte
	Segment    byte
}

// DefaultEdifactDelims are the ISO 9735 defaults ( : + . ? space ' ).
var DefaultEdifactDelims = EdifactDelims{Component: ':', Element: '+', Decimal: '.', Release: '?', Repetition: ' ', Segment: '\''}

// EdifactSegment is one segment: its tag and data elements, each a list of
// components (a simple element has one component).
type EdifactSegment struct {
	Tag      string
	Elements [][]string
}

// El returns component c of element e, or "" when absent.
func (s EdifactSegment) El(e, c int) string {
	if e < len(s.Elements) && c < len(s.Elements[e]) {
		return s.Elements[e][c]
	}
	return ""
}

// Seg builds a segment from elements whose components are separated by ':'
// in the argument; a value that itself contains ':' must be built directly.
func Seg(tag string, elements ...string) EdifactSegment {
	s := EdifactSegment{Tag: tag}
	for _, e := range elements {
		s.Elements = append(s.Elements, strings.Split(e, ":"))
	}
	return s
}

// EdifactMessage is one UNH..UNT message. Type is S009 0065 (ORDERS, DESADV,
// INVOIC, CONTRL), Version/Release/Agency are 0052/0054/0051 (D, 96A, UN) and
// Association is 0057 (an EANCOM subset such as EAN008, optional). Segments
// are those between UNH and UNT.
type EdifactMessage struct {
	Ref         string
	Type        string
	Version     string
	Release     string
	Agency      string
	Association string
	Segments    []EdifactSegment
}

// EdifactParty is a UNB S002/S003 identification: id and code qualifier
// (14 GLN, ZZZ mutually defined).
type EdifactParty struct {
	ID        string
	Qualifier string
}

// EdifactInterchange is one UNB..UNZ interchange. Syntax is S001 (UNOA..UNOC),
// SyntaxVersion 3 or 4; Date (YYMMDD) and Time (HHMM) are S004; Control is the
// UNB05 interchange reference; AckRequest true writes UNB09 = 1 (a CONTRL is
// requested); Test writes UNB11 = 1. WithUNA writes the UNA service string.
// Functional groups (UNG..UNE) are not supported.
type EdifactInterchange struct {
	Delims        EdifactDelims
	WithUNA       bool
	Syntax        string
	SyntaxVersion string
	Sender        EdifactParty
	Recipient     EdifactParty
	Date          string
	Time          string
	Control       string
	AckRequest    bool
	Test          bool
	Messages      []EdifactMessage
}

// ParseEdifact reads one interchange: optional UNA, UNB, messages, UNZ. It
// checks UNT01 (segment count including UNH and UNT), UNT02 against UNH01,
// UNZ01 (message count) and UNZ02 against UNB05.
func ParseEdifact(raw string) (EdifactInterchange, error) {
	raw = strings.TrimRight(strings.TrimLeft(raw, " \t\r\n\ufeff"), "\r\n")
	ic := EdifactInterchange{Delims: DefaultEdifactDelims}
	if strings.HasPrefix(raw, "UNA") {
		if len(raw) < 9 {
			return ic, edifactf("UNA service string advice is shorter than 9 characters")
		}
		ic.WithUNA = true
		ic.Delims = EdifactDelims{Component: raw[3], Element: raw[4], Decimal: raw[5], Release: raw[6], Repetition: raw[7], Segment: raw[8]}
		raw = strings.TrimLeft(raw[9:], "\r\n")
	}
	if !ic.Delims.distinct() {
		return ic, edifactf("UNA service characters are not distinct")
	}
	segs, err := splitEdifact(raw, ic.Delims)
	if err != nil {
		return ic, err
	}
	if len(segs) < 2 || segs[0].Tag != "UNB" || segs[len(segs)-1].Tag != "UNZ" {
		return ic, edifactf("interchange must start with UNB and end with UNZ")
	}
	unb := segs[0]
	ic.Syntax, ic.SyntaxVersion = unb.El(0, 0), unb.El(0, 1)
	if ic.SyntaxVersion != "3" && ic.SyntaxVersion != "4" {
		return ic, edifactf("UNB S001 syntax version %q is not 3 or 4", ic.SyntaxVersion)
	}
	ic.Sender = EdifactParty{ID: unb.El(1, 0), Qualifier: unb.El(1, 1)}
	ic.Recipient = EdifactParty{ID: unb.El(2, 0), Qualifier: unb.El(2, 1)}
	ic.Date, ic.Time, ic.Control = unb.El(3, 0), unb.El(3, 1), unb.El(4, 0)
	ic.AckRequest = unb.El(8, 0) == "1"
	ic.Test = unb.El(10, 0) == "1"
	if ic.Sender.ID == "" || ic.Recipient.ID == "" || ic.Control == "" {
		return ic, edifactf("UNB needs a sender, recipient and interchange reference")
	}
	var cur *EdifactMessage
	start := 0
	for i, s := range segs[1 : len(segs)-1] {
		switch s.Tag {
		case "UNG", "UNE":
			return ic, edifactf("functional groups (%s) are not supported", s.Tag)
		case "UNH":
			if cur != nil {
				return ic, edifactf("UNH %s opens before UNT closes message %s", s.El(0, 0), cur.Ref)
			}
			cur = &EdifactMessage{Ref: s.El(0, 0), Type: s.El(1, 0), Version: s.El(1, 1), Release: s.El(1, 2), Agency: s.El(1, 3), Association: s.El(1, 4)}
			start = i
			if cur.Ref == "" || cur.Type == "" {
				return ic, edifactf("UNH needs a message reference and type")
			}
		case "UNT":
			if cur == nil {
				return ic, edifactf("UNT outside a message")
			}
			if n, err := strconv.Atoi(s.El(0, 0)); err != nil || n != i-start+1 {
				return ic, edifactf("UNT01 %s does not match the %d segments of message %s", s.El(0, 0), i-start+1, cur.Ref)
			}
			if s.El(1, 0) != cur.Ref {
				return ic, edifactf("UNT02 %s does not match UNH01 %s", s.El(1, 0), cur.Ref)
			}
			ic.Messages = append(ic.Messages, *cur)
			cur = nil
		default:
			if cur == nil {
				return ic, edifactf("segment %s outside a message", s.Tag)
			}
			cur.Segments = append(cur.Segments, s)
		}
	}
	if cur != nil {
		return ic, edifactf("message %s has no UNT", cur.Ref)
	}
	unz := segs[len(segs)-1]
	if n, err := strconv.Atoi(unz.El(0, 0)); err != nil || n != len(ic.Messages) {
		return ic, edifactf("UNZ01 %s does not match the %d messages", unz.El(0, 0), len(ic.Messages))
	}
	if unz.El(1, 0) != ic.Control {
		return ic, edifactf("UNZ02 %s does not match UNB05 %s", unz.El(1, 0), ic.Control)
	}
	return ic, nil
}

func (d EdifactDelims) distinct() bool {
	set := []byte{d.Component, d.Element, d.Release, d.Segment}
	if d.Repetition != ' ' && d.Repetition != 0 {
		set = append(set, d.Repetition)
	}
	for i := range set {
		for j := i + 1; j < len(set); j++ {
			if set[i] == set[j] {
				return false
			}
		}
	}
	return true
}

// splitEdifact tokenizes segments, elements and components, honouring the
// release character. Line breaks after a segment terminator are ignored.
func splitEdifact(body string, d EdifactDelims) ([]EdifactSegment, error) {
	var (
		segs []EdifactSegment
		elem []string
		cur  []byte
		seg  [][]string
	)
	flushComp := func() { elem = append(elem, string(cur)); cur = cur[:0] }
	flushElem := func() { flushComp(); seg = append(seg, elem); elem = nil }
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch c {
		case d.Release:
			if i+1 >= len(body) {
				return nil, edifactf("release character at the end of the interchange")
			}
			i++
			cur = append(cur, body[i])
		case d.Component:
			flushComp()
		case d.Element:
			flushElem()
		case d.Segment:
			flushElem()
			tag := seg[0][0]
			if len(tag) != 3 || len(seg[0]) != 1 {
				return nil, edifactf("segment tag %q is not three characters", tag)
			}
			segs = append(segs, EdifactSegment{Tag: tag, Elements: seg[1:]})
			seg = nil
			for i+1 < len(body) && (body[i+1] == '\r' || body[i+1] == '\n') {
				i++
			}
		default:
			cur = append(cur, c)
		}
	}
	if len(cur) > 0 || len(seg) > 0 || len(elem) > 0 {
		return nil, edifactf("interchange does not end with a segment terminator")
	}
	return segs, nil
}

// SerializeEdifact writes the interchange with UNH/UNT and UNZ computed from
// its messages. Values containing a service character are escaped with the
// release character; trailing empty elements and components are dropped.
// A newline after each segment terminator is written when lineBreaks is set.
func SerializeEdifact(ic EdifactInterchange, lineBreaks bool) (string, error) {
	d := ic.Delims
	if !d.distinct() {
		return "", edifactf("service characters are not distinct")
	}
	if ic.SyntaxVersion != "3" && ic.SyntaxVersion != "4" {
		return "", edifactf("syntax version %q is not 3 or 4", ic.SyntaxVersion)
	}
	if !isDigits(ic.Date, 6) || !isDigits(ic.Time, 4) || ic.Control == "" || len(ic.Control) > 14 {
		return "", edifactf("UNB date, time or interchange reference is invalid")
	}
	if d != DefaultEdifactDelims && !ic.WithUNA {
		return "", edifactf("non-default service characters need a UNA")
	}
	special := string([]byte{d.Component, d.Element, d.Release, d.Segment})
	if d.Repetition != ' ' && d.Repetition != 0 {
		special += string(d.Repetition)
	}
	var out strings.Builder
	nl := ""
	if lineBreaks {
		nl = "\n"
	}
	escape := func(v string) string {
		var b strings.Builder
		for i := 0; i < len(v); i++ {
			if strings.IndexByte(special, v[i]) >= 0 {
				b.WriteByte(d.Release)
			}
			b.WriteByte(v[i])
		}
		return b.String()
	}
	write := func(s EdifactSegment) error {
		if len(s.Tag) != 3 {
			return edifactf("segment tag %q is not three characters", s.Tag)
		}
		if strings.ContainsAny(s.Tag, "\r\n") {
			return edifactf("segment tag contains a line break")
		}
		elems := make([]string, 0, len(s.Elements))
		for _, e := range s.Elements {
			comps := make([]string, len(e))
			for i, c := range e {
				comps[i] = escape(c)
			}
			for len(comps) > 0 && comps[len(comps)-1] == "" {
				comps = comps[:len(comps)-1]
			}
			elems = append(elems, strings.Join(comps, string(d.Component)))
		}
		for len(elems) > 0 && elems[len(elems)-1] == "" {
			elems = elems[:len(elems)-1]
		}
		out.WriteString(s.Tag)
		for _, e := range elems {
			out.WriteByte(d.Element)
			out.WriteString(e)
		}
		out.WriteByte(d.Segment)
		out.WriteString(nl)
		return nil
	}
	if ic.WithUNA {
		out.Write([]byte{'U', 'N', 'A', d.Component, d.Element, d.Decimal, d.Release, d.Repetition, d.Segment})
		out.WriteString(nl)
	}
	unb := EdifactSegment{Tag: "UNB", Elements: [][]string{
		{ic.Syntax, ic.SyntaxVersion}, {ic.Sender.ID, ic.Sender.Qualifier}, {ic.Recipient.ID, ic.Recipient.Qualifier},
		{ic.Date, ic.Time}, {ic.Control}, {}, {}, {}, {flag(ic.AckRequest)}, {}, {flag(ic.Test)}}}
	if err := write(unb); err != nil {
		return "", err
	}
	for _, m := range ic.Messages {
		if m.Ref == "" || m.Type == "" {
			return "", edifactf("message needs a reference and type")
		}
		if err := write(EdifactSegment{Tag: "UNH", Elements: [][]string{{m.Ref}, {m.Type, m.Version, m.Release, m.Agency, m.Association}}}); err != nil {
			return "", err
		}
		for _, s := range m.Segments {
			if envelopeTags[s.Tag] {
				return "", edifactf("service segment %s inside message %s", s.Tag, m.Ref)
			}
			if err := write(s); err != nil {
				return "", err
			}
		}
		if err := write(Seg("UNT", strconv.Itoa(len(m.Segments)+2), m.Ref)); err != nil {
			return "", err
		}
	}
	if err := write(Seg("UNZ", strconv.Itoa(len(ic.Messages)), ic.Control)); err != nil {
		return "", err
	}
	return out.String(), nil
}

// EdifactOutbound wraps messages in a UNOC syntax 3 interchange from us to the
// partner with the default service characters and a UNA. control is the
// gap-free outbound number (BR-35.5) written as UNB05; messages are numbered
// 1..n in UNH01. A CONTRL is requested (UNB09 = 1).
func EdifactOutbound(msgs []EdifactMessage, from, to EdifactParty, control int64, test bool, at time.Time) (EdifactInterchange, error) {
	if control < 1 || control > 99_999_999_999_999 {
		return EdifactInterchange{}, fmt.Errorf("edi: outbound control number %d is outside UNB05", control)
	}
	at = at.UTC()
	ic := EdifactInterchange{Delims: DefaultEdifactDelims, WithUNA: true, Syntax: "UNOC", SyntaxVersion: "3", Sender: from, Recipient: to,
		Date: at.Format("060102"), Time: at.Format("1504"), Control: strconv.FormatInt(control, 10), AckRequest: true, Test: test}
	for i, m := range msgs {
		m.Ref = strconv.Itoa(i + 1)
		ic.Messages = append(ic.Messages, m)
	}
	return ic, nil
}

// envelopeTags are the service segments that may not appear inside a message
// (UNS, the section control, may).
var envelopeTags = map[string]bool{"UNA": true, "UNB": true, "UNG": true, "UNH": true, "UNT": true, "UNE": true, "UNZ": true}

func flag(b bool) string {
	if b {
		return "1"
	}
	return ""
}
