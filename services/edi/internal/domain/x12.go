// Package domain holds the pure EDI rules: X12 syntax, functional
// acknowledgments, the 850 mapping, partner profiles and e-invoice arithmetic.
// Nothing here performs I/O.
package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// isaLen is the fixed length of the ISA header including its segment terminator.
const isaLen = 106

// ErrSyntax marks an interchange that cannot be parsed or is structurally inconsistent.
var ErrSyntax = errors.New("x12: syntax error")

// Delims are the envelope separators declared by the ISA header. Repetition is
// ISA11 in version 00501 and later; it is zero for 00401, where ISA11 is the
// standards identifier "U".
type Delims struct {
	Element    byte
	Component  byte
	Segment    byte
	Repetition byte
}

// SupportedVersions are the ISA12 interchange control versions the codec reads
// and writes (FR-35.2: 004010 and 005010).
var SupportedVersions = []string{"00401", "00501"}

func supportedVersion(v string) bool {
	for _, s := range SupportedVersions {
		if s == v {
			return true
		}
	}
	return false
}

// usesRepetition reports whether ISA11 carries the repetition separator.
func usesRepetition(version string) bool { return version >= "00501" }

func (d Delims) distinct() bool {
	set := []byte{d.Element, d.Component, d.Segment}
	if d.Repetition != 0 {
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

// Party is an ISA/GS identifier: qualifier plus id.
type Party struct {
	Qualifier string
	ID        string
}

// Segment is one X12 segment: its id and the data elements after it.
type Segment struct {
	ID       string
	Elements []string
}

// Transaction is one ST..SE set. Body holds the segments between ST and SE.
type Transaction struct {
	SetID   string
	Control string
	Body    []Segment
}

// Group is one GS..GE functional group.
type Group struct {
	FunctionalID string
	Sender       string
	Receiver     string
	Date         string
	Time         string
	Control      string
	Version      string
	Transactions []Transaction
}

// Interchange is one ISA..IEA envelope.
type Interchange struct {
	Delims   Delims
	Sender   Party
	Receiver Party
	Date     string
	Time     string
	Version  string
	Control  string
	Usage    string
	Groups   []Group
}

func syntaxf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSyntax, fmt.Sprintf(format, args...))
}

func element(s Segment, i int) string {
	if i < len(s.Elements) {
		return s.Elements[i]
	}
	return ""
}

// Parse reads one interchange. It checks the envelope counts (SE01, GE01, IEA01)
// and control numbers (SE02, GE02, IEA02) so a truncated or mis-numbered
// interchange never reaches the mapper.
func Parse(raw string) (Interchange, error) {
	ic, body, err := readISA(raw)
	if err != nil {
		return Interchange{}, err
	}
	segs, err := splitSegments(body, ic.Delims)
	if err != nil {
		return Interchange{}, err
	}
	groups, ieaCount, ieaControl, rest, err := parseGroups(segs)
	if err != nil {
		return Interchange{}, err
	}
	if rest != len(segs) {
		return Interchange{}, syntaxf("content after IEA")
	}
	if ieaCount != strconv.Itoa(len(groups)) {
		return Interchange{}, syntaxf("IEA01 says %s groups, found %d", ieaCount, len(groups))
	}
	if ieaControl != ic.Control {
		return Interchange{}, syntaxf("IEA02 %s does not match ISA13 %s", ieaControl, ic.Control)
	}
	ic.Groups = groups
	return ic, nil
}

// readISA reads the fixed-length ISA header and returns the interchange
// fields with the text after it.
func readISA(raw string) (Interchange, string, error) {
	raw = strings.TrimLeft(raw, " \t\r\n\ufeff")
	if len(raw) < isaLen || !strings.HasPrefix(raw, "ISA") {
		return Interchange{}, "", syntaxf("interchange does not start with a %d-character ISA header", isaLen)
	}
	d := Delims{Element: raw[3], Component: raw[104], Segment: raw[105]}
	isa := strings.Split(raw[:105], string(d.Element))
	if len(isa) != 17 {
		return Interchange{}, "", syntaxf("ISA header does not carry 16 elements")
	}
	if !supportedVersion(isa[12]) {
		return Interchange{}, "", syntaxf("ISA12 version %q is not supported (00401 or 00501)", isa[12])
	}
	if usesRepetition(isa[12]) {
		if len(isa[11]) != 1 {
			return Interchange{}, "", syntaxf("ISA11 repetition separator must be one character")
		}
		d.Repetition = isa[11][0]
	}
	if !d.distinct() {
		return Interchange{}, "", syntaxf("ISA delimiters are not distinct")
	}
	ic := Interchange{
		Delims:   d,
		Sender:   Party{Qualifier: isa[5], ID: strings.TrimRight(isa[6], " ")},
		Receiver: Party{Qualifier: isa[7], ID: strings.TrimRight(isa[8], " ")},
		Date:     isa[9],
		Time:     isa[10],
		Version:  isa[12],
		Control:  isa[13],
		Usage:    isa[15],
	}
	return ic, raw[isaLen:], nil
}

func splitSegments(body string, d Delims) ([]Segment, error) {
	var segs []Segment
	for _, piece := range strings.Split(body, string(d.Segment)) {
		piece = strings.Trim(piece, "\r\n\t")
		if piece == "" {
			continue
		}
		fields := strings.Split(piece, string(d.Element))
		if fields[0] == "" {
			return nil, syntaxf("segment without an id")
		}
		segs = append(segs, Segment{ID: fields[0], Elements: fields[1:]})
	}
	return segs, nil
}

// parseGroups walks GS..GE groups until IEA and returns the index of IEA plus one.
func parseGroups(segs []Segment) (groups []Group, ieaCount, ieaControl string, next int, err error) {
	i := 0
	for ; i < len(segs) && segs[i].ID != "IEA"; i++ {
		s := segs[i]
		if s.ID != "GS" {
			return nil, "", "", 0, syntaxf("expected GS or IEA, found %s", s.ID)
		}
		g := Group{FunctionalID: element(s, 0), Sender: element(s, 1), Receiver: element(s, 2),
			Date: element(s, 3), Time: element(s, 4), Control: element(s, 5), Version: element(s, 7)}
		i++
		for ; i < len(segs) && segs[i].ID != "GE"; i++ {
			if segs[i].ID != "ST" {
				return nil, "", "", 0, syntaxf("expected ST or GE, found %s", segs[i].ID)
			}
			st := segs[i]
			t := Transaction{SetID: element(st, 0), Control: element(st, 1)}
			i++
			for ; i < len(segs) && segs[i].ID != "SE"; i++ {
				t.Body = append(t.Body, segs[i])
			}
			if i >= len(segs) {
				return nil, "", "", 0, syntaxf("transaction %s has no SE", t.Control)
			}
			se := segs[i]
			if element(se, 1) != t.Control {
				return nil, "", "", 0, syntaxf("SE02 %s does not match ST02 %s", element(se, 1), t.Control)
			}
			if element(se, 0) != strconv.Itoa(len(t.Body)+2) {
				return nil, "", "", 0, syntaxf("SE01 %s does not count %d segments", element(se, 0), len(t.Body)+2)
			}
			g.Transactions = append(g.Transactions, t)
		}
		if i >= len(segs) {
			return nil, "", "", 0, syntaxf("group %s has no GE", g.Control)
		}
		ge := segs[i]
		if element(ge, 0) != strconv.Itoa(len(g.Transactions)) {
			return nil, "", "", 0, syntaxf("GE01 %s does not count %d transactions", element(ge, 0), len(g.Transactions))
		}
		if element(ge, 1) != g.Control {
			return nil, "", "", 0, syntaxf("GE02 %s does not match GS06 %s", element(ge, 1), g.Control)
		}
		groups = append(groups, g)
	}
	if i >= len(segs) {
		return nil, "", "", 0, syntaxf("interchange has no IEA")
	}
	ieaCount = element(segs[i], 0)
	ieaControl = element(segs[i], 1)
	return groups, ieaCount, ieaControl, i + 1, nil
}

// Serialize writes ic as X12 text with the interchange's delimiters. Values
// that contain a delimiter are refused so Serialize never produces text that
// Parse would read differently.
func Serialize(ic Interchange) (string, error) {
	d := ic.Delims
	if !d.distinct() {
		return "", syntaxf("ISA delimiters are not distinct")
	}
	if !supportedVersion(ic.Version) {
		return "", syntaxf("ISA12 version %q is not supported (00401 or 00501)", ic.Version)
	}
	isa11 := "U"
	if usesRepetition(ic.Version) {
		if d.Repetition == 0 {
			return "", syntaxf("version %s needs a repetition separator", ic.Version)
		}
		isa11 = string(d.Repetition)
	} else if d.Repetition != 0 {
		return "", syntaxf("version %s has no repetition separator", ic.Version)
	}
	if len(ic.Sender.ID) > 15 || len(ic.Receiver.ID) > 15 || len(ic.Sender.Qualifier) != 2 || len(ic.Receiver.Qualifier) != 2 {
		return "", syntaxf("ISA sender or receiver is outside its fixed width")
	}
	if len(ic.Date) != 6 || len(ic.Time) != 4 || len(ic.Control) > 9 || (ic.Usage != "T" && ic.Usage != "P") {
		return "", syntaxf("ISA date, time, control, version or usage is invalid")
	}
	var out strings.Builder
	el := string(d.Element)
	reserved := string([]byte{d.Element, d.Component, d.Segment, '\n', '\r'})
	if d.Repetition != 0 {
		reserved += string(d.Repetition)
	}
	seg := func(elems ...string) error {
		for _, v := range elems {
			if strings.ContainsAny(v, reserved) {
				return syntaxf("value %q contains a delimiter", v)
			}
		}
		out.WriteString(strings.Join(elems, el))
		out.WriteByte(d.Segment)
		return nil
	}
	isa := []string{"ISA", "00", strings.Repeat(" ", 10), "00", strings.Repeat(" ", 10),
		ic.Sender.Qualifier, padRight(ic.Sender.ID, 15), ic.Receiver.Qualifier, padRight(ic.Receiver.ID, 15),
		ic.Date, ic.Time, isa11, ic.Version, fmt.Sprintf("%09s", ic.Control), "0", ic.Usage}
	// ISA16 is the component separator itself; written raw so it is not treated as a value.
	out.WriteString(strings.Join(isa, el))
	out.WriteString(el + string(d.Component) + string(d.Segment))
	for _, g := range ic.Groups {
		if err := seg("GS", g.FunctionalID, g.Sender, g.Receiver, g.Date, g.Time, g.Control, "X", g.Version); err != nil {
			return "", err
		}
		for _, t := range g.Transactions {
			if err := seg("ST", t.SetID, t.Control); err != nil {
				return "", err
			}
			for _, s := range t.Body {
				if err := seg(append([]string{s.ID}, s.Elements...)...); err != nil {
					return "", err
				}
			}
			if err := seg("SE", strconv.Itoa(len(t.Body)+2), t.Control); err != nil {
				return "", err
			}
		}
		if err := seg("GE", strconv.Itoa(len(g.Transactions)), g.Control); err != nil {
			return "", err
		}
	}
	if err := seg("IEA", strconv.Itoa(len(ic.Groups)), fmt.Sprintf("%09s", ic.Control)); err != nil {
		return "", err
	}
	return out.String(), nil
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}
