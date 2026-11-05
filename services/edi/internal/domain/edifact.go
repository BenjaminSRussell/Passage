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
// are those between UNH and UNT. Group is the 0048 reference of the UNG..UNE
// group that carries the message, empty outside groups.
type EdifactMessage struct {
	Ref         string
	Type        string
	Version     string
	Release     string
	Agency      string
	Association string
	Group       string
	Segments    []EdifactSegment
}

// EdifactParty is a UNB S002/S003 identification: id and code qualifier
// (14 GLN, ZZZ mutually defined).
type EdifactParty struct {
	ID        string
	Qualifier string
}

// EdifactGroup is one UNG..UNE functional group header: ID is 0038 (the
// message type it carries), Sender and Recipient the S006/S007 application
// identifications, Date and Time S004, Ref the 0048 group reference, Agency
// 0051 and Version/Release S008.
type EdifactGroup struct {
	ID        string
	Sender    EdifactParty
	Recipient EdifactParty
	Date      string
	Time      string
	Ref       string
	Agency    string
	Version   string
	Release   string
}

// EdifactInterchange is one UNB..UNZ interchange. Syntax is S001 (UNOA..UNOC),
// SyntaxVersion 3 or 4; Date (YYMMDD) and Time (HHMM) are S004; Control is the
// UNB05 interchange reference; AckRequest true writes UNB09 = 1 (data element
// 0031: a CONTRL is requested); Test writes UNB11 = 1. WithUNA writes the UNA
// service string. Groups, when present, are the UNG..UNE functional groups
// and every message names its group; an interchange carries either groups or
// bare messages, never both.
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
	Groups        []EdifactGroup
	Messages      []EdifactMessage
}

// EdifactError is a syntax error found while reading an interchange, with
// what a CONTRL needs to report it: the ISO 9735 0085 code and where it
// lies. Group and Message are the UNG 0048 and UNH 0062 references when the
// error belongs to a group or a message (empty: the interchange); Segment is
// the position of the failing segment in its message (UNH is 1), Element and
// Component the position inside it.
type EdifactError struct {
	Code      string
	Group     string
	Message   string
	Segment   int
	Element   int
	Component int
	Detail    string
}

func (e *EdifactError) Error() string { return ErrEdifactSyntax.Error() + ": " + e.Detail }

// Unwrap makes errors.Is(err, ErrEdifactSyntax) hold.
func (e *EdifactError) Unwrap() error { return ErrEdifactSyntax }

func edifactErr(code, format string, args ...any) *EdifactError {
	return &EdifactError{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// ParseEdifact reads one interchange: optional UNA, UNB, optional UNG..UNE
// groups, messages, UNZ. It checks UNT01 (segment count including UNH and
// UNT), UNT02 against UNH01, UNE01/UNE02 against the group, UNZ01 (message or
// group count) and UNZ02 against UNB05. Every error is an *EdifactError.
func ParseEdifact(raw string) (EdifactInterchange, error) {
	ic, broken, err := ReadEdifact(raw)
	if err != nil {
		return ic, err
	}
	if len(broken) > 0 {
		return ic, broken[0]
	}
	return ic, nil
}

// ReadEdifact reads an interchange like ParseEdifact but tells the errors a
// receiver can still acknowledge message by message apart from those that
// refuse the whole interchange. A message whose UNT count or reference is
// wrong is kept in ic.Messages and listed in broken (an error naming the
// message and the UNT position), so the rest of the interchange can be
// processed and the CONTRL rejects that message alone. Any other error is
// returned as err (an *EdifactError); ic then carries the UNB fields when the
// UNB could be read (ic.Control is set), which is enough to answer with a
// CONTRL.
func ReadEdifact(raw string) (ic EdifactInterchange, broken []*EdifactError, err error) {
	raw = strings.TrimRight(strings.TrimLeft(raw, " \t\r\n\ufeff"), "\r\n")
	ic = EdifactInterchange{Delims: DefaultEdifactDelims}
	if strings.HasPrefix(raw, "UNA") {
		if len(raw) < 9 {
			return ic, nil, edifactErr(ContrlErrServiceChars, "UNA service string advice is shorter than 9 characters")
		}
		ic.WithUNA = true
		ic.Delims = EdifactDelims{Component: raw[3], Element: raw[4], Decimal: raw[5], Release: raw[6], Repetition: raw[7], Segment: raw[8]}
		raw = strings.TrimLeft(raw[9:], "\r\n")
	}
	if !ic.Delims.distinct() {
		return ic, nil, edifactErr(ContrlErrServiceChars, "UNA service characters are not distinct")
	}
	segs, splitErr := splitEdifact(raw, ic.Delims)
	if len(segs) == 0 || segs[0].Tag != "UNB" {
		if splitErr != nil {
			return ic, nil, splitErr
		}
		return ic, nil, edifactErr(ContrlErrMissing, "interchange must start with UNB")
	}
	unb := segs[0]
	ic.Syntax, ic.SyntaxVersion = unb.El(0, 0), unb.El(0, 1)
	ic.Sender = EdifactParty{ID: unb.El(1, 0), Qualifier: unb.El(1, 1)}
	ic.Recipient = EdifactParty{ID: unb.El(2, 0), Qualifier: unb.El(2, 1)}
	ic.Date, ic.Time, ic.Control = unb.El(3, 0), unb.El(3, 1), unb.El(4, 0)
	ic.AckRequest = unb.El(8, 0) == "1"
	ic.Test = unb.El(10, 0) == "1"
	if ic.Sender.ID == "" || ic.Recipient.ID == "" || ic.Control == "" {
		ic.Control = ""
		return ic, nil, edifactErr(ContrlErrMissing, "UNB needs a sender, recipient and interchange reference")
	}
	if splitErr != nil {
		return ic, nil, splitErr
	}
	if ic.SyntaxVersion != "3" && ic.SyntaxVersion != "4" {
		return ic, nil, edifactErr(ContrlErrSyntaxVersion, "UNB S001 syntax version %q is not 3 or 4", ic.SyntaxVersion)
	}
	if len(segs) < 2 || segs[len(segs)-1].Tag != "UNZ" {
		return ic, nil, edifactErr(ContrlErrMissing, "interchange must end with UNZ")
	}
	var (
		cur     *EdifactMessage
		group   = -1 // index of the open group in ic.Groups
		inGroup = 0  // messages closed in the open group
		bare    = 0  // messages outside groups
		start   = 0
	)
	groupErr := func(code, format string, args ...any) *EdifactError {
		e := edifactErr(code, format, args...)
		e.Group = ic.Groups[group].Ref
		return e
	}
	for i, s := range segs[1 : len(segs)-1] {
		switch s.Tag {
		case "UNG":
			switch {
			case cur != nil:
				return ic, broken, edifactErr(ContrlErrMissing, "UNG opens before UNT closes message %s", cur.Ref)
			case group >= 0:
				return ic, broken, groupErr(ContrlErrMissing, "UNG opens before UNE closes group %s", ic.Groups[group].Ref)
			case bare > 0:
				return ic, broken, edifactErr(ContrlErrMixed, "the interchange mixes functional groups and messages outside groups")
			}
			g := EdifactGroup{ID: s.El(0, 0), Sender: EdifactParty{ID: s.El(1, 0), Qualifier: s.El(1, 1)},
				Recipient: EdifactParty{ID: s.El(2, 0), Qualifier: s.El(2, 1)}, Date: s.El(3, 0), Time: s.El(3, 1), Ref: s.El(4, 0),
				Agency: s.El(5, 0), Version: s.El(6, 0), Release: s.El(6, 1)}
			if g.Ref == "" {
				return ic, broken, edifactErr(ContrlErrMissing, "UNG needs a group reference (0048)")
			}
			for _, prev := range ic.Groups {
				if prev.Ref == g.Ref {
					return ic, broken, edifactErr(ContrlErrDuplicate, "group reference %s occurs twice", g.Ref)
				}
			}
			ic.Groups = append(ic.Groups, g)
			group, inGroup = len(ic.Groups)-1, 0
		case "UNE":
			switch {
			case cur != nil:
				return ic, broken, edifactErr(ContrlErrMissing, "UNE closes a group before UNT closes message %s", cur.Ref)
			case group < 0:
				return ic, broken, edifactErr(ContrlErrOutside, "UNE outside a functional group")
			}
			if n, err := strconv.Atoi(s.El(0, 0)); err != nil || n != inGroup {
				return ic, broken, groupErr(ContrlErrCount, "UNE01 %s does not match the %d messages of group %s", s.El(0, 0), inGroup, ic.Groups[group].Ref)
			}
			if s.El(1, 0) != ic.Groups[group].Ref {
				return ic, broken, groupErr(ContrlErrReferences, "UNE02 %s does not match UNG 0048 %s", s.El(1, 0), ic.Groups[group].Ref)
			}
			group = -1
		case "UNH":
			switch {
			case cur != nil:
				return ic, broken, edifactErr(ContrlErrMissing, "UNH %s opens before UNT closes message %s", s.El(0, 0), cur.Ref)
			case group < 0 && len(ic.Groups) > 0:
				return ic, broken, edifactErr(ContrlErrMixed, "the interchange mixes functional groups and messages outside groups")
			}
			cur = &EdifactMessage{Ref: s.El(0, 0), Type: s.El(1, 0), Version: s.El(1, 1), Release: s.El(1, 2), Agency: s.El(1, 3), Association: s.El(1, 4)}
			if group >= 0 {
				cur.Group = ic.Groups[group].Ref
			}
			start = i
			if cur.Ref == "" || cur.Type == "" {
				return ic, broken, edifactErr(ContrlErrMissing, "UNH needs a message reference and type")
			}
		case "UNT":
			if cur == nil {
				return ic, broken, edifactErr(ContrlErrOutside, "UNT outside a message")
			}
			pos := i - start + 1
			if n, err := strconv.Atoi(s.El(0, 0)); err != nil || n != pos {
				broken = append(broken, &EdifactError{Code: ContrlErrCount, Group: cur.Group, Message: cur.Ref, Segment: pos, Element: 1, Component: 1,
					Detail: fmt.Sprintf("UNT01 %s does not match the %d segments of message %s", s.El(0, 0), pos, cur.Ref)})
			} else if s.El(1, 0) != cur.Ref {
				broken = append(broken, &EdifactError{Code: ContrlErrReferences, Group: cur.Group, Message: cur.Ref, Segment: pos, Element: 2, Component: 1,
					Detail: fmt.Sprintf("UNT02 %s does not match UNH01 %s", s.El(1, 0), cur.Ref)})
			}
			ic.Messages = append(ic.Messages, *cur)
			cur = nil
			if group >= 0 {
				inGroup++
			} else {
				bare++
			}
		default:
			if cur == nil {
				return ic, broken, edifactErr(ContrlErrOutside, "segment %s outside a message", s.Tag)
			}
			cur.Segments = append(cur.Segments, s)
		}
	}
	if cur != nil {
		return ic, broken, edifactErr(ContrlErrMissing, "message %s has no UNT", cur.Ref)
	}
	if group >= 0 {
		return ic, broken, groupErr(ContrlErrMissing, "group %s has no UNE", ic.Groups[group].Ref)
	}
	unz := segs[len(segs)-1]
	count, what := len(ic.Messages), "messages"
	if len(ic.Groups) > 0 {
		count, what = len(ic.Groups), "groups"
	}
	if n, err := strconv.Atoi(unz.El(0, 0)); err != nil || n != count {
		return ic, broken, edifactErr(ContrlErrCount, "UNZ01 %s does not match the %d %s", unz.El(0, 0), count, what)
	}
	if unz.El(1, 0) != ic.Control {
		return ic, broken, edifactErr(ContrlErrReferences, "UNZ02 %s does not match UNB05 %s", unz.El(1, 0), ic.Control)
	}
	return ic, broken, nil
}

// Syntaxes a payload can be in, told apart by DetectSyntax.
const (
	SyntaxX12     = "X12"
	SyntaxEdifact = "EDIFACT"
)

// DetectSyntax tells an X12 interchange (first segment ISA) from an EDIFACT
// one (UNA or UNB) after leading white space and a byte order mark; it
// returns "" for anything else. Nothing past the first segment tag is read.
func DetectSyntax(payload []byte) string {
	head := strings.TrimLeft(string(payload[:min(len(payload), 64)]), " \t\r\n\ufeff")
	switch {
	case strings.HasPrefix(head, "ISA"):
		return SyntaxX12
	case strings.HasPrefix(head, "UNA"), strings.HasPrefix(head, "UNB"):
		return SyntaxEdifact
	}
	return ""
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
// release character. Line breaks after a segment terminator are ignored. On
// an error it returns the segments read before it with an *EdifactError.
func splitEdifact(body string, d EdifactDelims) ([]EdifactSegment, *EdifactError) {
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
				return segs, edifactErr(ContrlErrUnspecified, "release character at the end of the interchange")
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
				return segs, edifactErr(ContrlErrUnspecified, "segment tag %q is not three characters", tag)
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
		return segs, edifactErr(ContrlErrMissing, "interchange does not end with a segment terminator")
	}
	return segs, nil
}

// SerializeEdifact writes the interchange with UNH/UNT, UNG/UNE and UNZ computed
// from its groups and messages. Values containing a service character are escaped with the
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
	writeMessage := func(m EdifactMessage) error {
		if m.Ref == "" || m.Type == "" {
			return edifactf("message needs a reference and type")
		}
		if err := write(EdifactSegment{Tag: "UNH", Elements: [][]string{{m.Ref}, {m.Type, m.Version, m.Release, m.Agency, m.Association}}}); err != nil {
			return err
		}
		for _, s := range m.Segments {
			if envelopeTags[s.Tag] {
				return edifactf("service segment %s inside message %s", s.Tag, m.Ref)
			}
			if err := write(s); err != nil {
				return err
			}
		}
		return write(Seg("UNT", strconv.Itoa(len(m.Segments)+2), m.Ref))
	}
	count := len(ic.Messages)
	if len(ic.Groups) == 0 {
		for _, m := range ic.Messages {
			if m.Group != "" {
				return "", edifactf("message %s names group %s but the interchange has no groups", m.Ref, m.Group)
			}
			if err := writeMessage(m); err != nil {
				return "", err
			}
		}
	} else {
		count = len(ic.Groups)
		written := 0
		for _, g := range ic.Groups {
			if g.Ref == "" {
				return "", edifactf("group needs a reference")
			}
			if err := write(EdifactSegment{Tag: "UNG", Elements: [][]string{{g.ID}, {g.Sender.ID, g.Sender.Qualifier},
				{g.Recipient.ID, g.Recipient.Qualifier}, {g.Date, g.Time}, {g.Ref}, {g.Agency}, {g.Version, g.Release}}}); err != nil {
				return "", err
			}
			n := 0
			for _, m := range ic.Messages {
				if m.Group != g.Ref {
					continue
				}
				if err := writeMessage(m); err != nil {
					return "", err
				}
				n++
			}
			if err := write(Seg("UNE", strconv.Itoa(n), g.Ref)); err != nil {
				return "", err
			}
			written += n
		}
		if written != len(ic.Messages) {
			return "", edifactf("%d messages name no group of the interchange", len(ic.Messages)-written)
		}
	}
	if err := write(Seg("UNZ", strconv.Itoa(count), ic.Control)); err != nil {
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
