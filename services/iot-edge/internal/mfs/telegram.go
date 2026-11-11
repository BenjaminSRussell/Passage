// Package mfs is the material flow system adapter of the edge connector (wave 32, FR-32.12): the
// telegrams a conveyor, sorter or AS/RS controller (PLC) exchanges with the warehouse. It has
// three parts: the telegram codec (this file), the stream framer and the link session (link.go),
// and the mapping to warehouse events and commands (events.go). No socket is opened here; the
// link runs over any net.Conn, so tests use net.Pipe and the plant runtime dials the PLC.
//
// Telegram profile v1 (Passage's own; a plant's PLC vendor profile is mapped onto it, see
// NOT-IMPLEMENTED.md):
//
//	frame    STX(0x02) LLLL body ETX(0x03)       LLLL: body length, 4 ASCII digits
//	body     TYPE(4) SEQ(5) SENDER(8) RECEIVER(8) payload
//	TYPE     MOVE ARRV DIVT ERRR ALIV ACKN NACK
//
// Every field is fixed width, ASCII, left-aligned and space padded; numbers are zero padded.
package mfs

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Frame bytes and sizes.
const (
	STX         = 0x02
	ETX         = 0x03
	lenDigits   = 4
	headerSize  = 4 + 5 + 8 + 8
	MaxBodySize = 512
	// MaxSeq is the last sequence number; the one after it is 1 again (0 is never sent).
	MaxSeq = 99999
)

// Telegram types.
const (
	TypeMove = "MOVE" // host to PLC: move a load unit from one location to another
	TypeArrv = "ARRV" // PLC to host: a load unit arrived at a location (scan point)
	TypeDivt = "DIVT" // PLC to host: a sorter diverted a load unit to a lane
	TypeErrr = "ERRR" // PLC to host: a fault, optionally for one load unit
	TypeAliv = "ALIV" // both ways: keep-alive
	TypeAckn = "ACKN" // both ways: the telegram with the referenced sequence was accepted
	TypeNack = "NACK" // both ways: the telegram with the referenced sequence was refused
)

// Negative acknowledgement codes.
const (
	NackFormat  = "0001" // the body could not be parsed
	NackUnknown = "0002" // unknown telegram type
	NackLU      = "0003" // the load unit id is not a valid SSCC
	NackBusy    = "0004" // the receiver cannot take the telegram now
)

// Telegram is one decoded message. Only the fields of its Type are used.
type Telegram struct {
	Type     string
	Seq      int
	Sender   string
	Receiver string

	LU       string // MOVE, ARRV, DIVT, ERRR: the SSCC (18 digits); ERRR may carry 18 zeros for "no unit"
	From     string // MOVE: source location
	To       string // MOVE: destination location
	Priority int    // MOVE: 1 (highest) to 9
	Location string // ARRV, DIVT: the scan point or sorter location
	Lane     string // DIVT: the chute or lane
	Code     string // ERRR, NACK: four-digit code
	Text     string // ERRR: up to 20 characters
	RefSeq   int    // ACKN, NACK: the sequence being answered
}

// NoLU is the load unit of a fault that belongs to no unit.
const NoLU = "000000000000000000"

var (
	// ErrFrame is returned for a frame that is not well formed.
	ErrFrame = errors.New("mfs: bad frame")
	// ErrTelegram is returned for a body that does not parse or validate.
	ErrTelegram = errors.New("mfs: bad telegram")
)

// ValidSSCC reports whether s is 18 digits with a correct GS1 mod-10 check digit.
func ValidSSCC(s string) bool {
	if len(s) != 18 {
		return false
	}
	sum := 0
	for i := 0; i < 17; i++ {
		d := int(s[i] - '0')
		if s[i] < '0' || s[i] > '9' {
			return false
		}
		if (17-i)%2 == 1 { // positions counted from the right of the 17 data digits: 1, 3, 5... weigh 3
			sum += d * 3
		} else {
			sum += d
		}
	}
	if s[17] < '0' || s[17] > '9' {
		return false
	}
	return (10-sum%10)%10 == int(s[17]-'0')
}

func pad(s string, n int) (string, error) {
	if len(s) > n {
		return "", fmt.Errorf("%w: %q is longer than %d", ErrTelegram, s, n)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return "", fmt.Errorf("%w: %q has a non-printable character", ErrTelegram, s)
		}
	}
	return s + strings.Repeat(" ", n-len(s)), nil
}

func num(v, n int) (string, error) {
	s := strconv.Itoa(v)
	if v < 0 || len(s) > n {
		return "", fmt.Errorf("%w: %d does not fit %d digits", ErrTelegram, v, n)
	}
	return strings.Repeat("0", n-len(s)) + s, nil
}

// Encode returns the framed bytes of t. It validates the telegram first.
func (t Telegram) Encode() ([]byte, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString(t.Type)
	seq, _ := num(t.Seq, 5)
	b.WriteString(seq)
	for _, f := range []struct {
		v string
		n int
	}{{t.Sender, 8}, {t.Receiver, 8}} {
		s, err := pad(f.v, f.n)
		if err != nil {
			return nil, err
		}
		b.WriteString(s)
	}
	add := func(v string, n int) error {
		s, err := pad(v, n)
		b.WriteString(s)
		return err
	}
	var err error
	switch t.Type {
	case TypeMove:
		if err = add(t.LU, 18); err == nil {
			if err = add(t.From, 12); err == nil {
				if err = add(t.To, 12); err == nil {
					b.WriteString(strconv.Itoa(t.Priority))
				}
			}
		}
	case TypeArrv:
		if err = add(t.LU, 18); err == nil {
			err = add(t.Location, 12)
		}
	case TypeDivt:
		if err = add(t.LU, 18); err == nil {
			if err = add(t.Location, 12); err == nil {
				err = add(t.Lane, 4)
			}
		}
	case TypeErrr:
		if err = add(t.LU, 18); err == nil {
			if err = add(t.Code, 4); err == nil {
				err = add(t.Text, 20)
			}
		}
	case TypeAckn:
		var s string
		if s, err = num(t.RefSeq, 5); err == nil {
			b.WriteString(s)
		}
	case TypeNack:
		var s string
		if s, err = num(t.RefSeq, 5); err == nil {
			b.WriteString(s)
			err = add(t.Code, 4)
		}
	}
	if err != nil {
		return nil, err
	}
	body := b.String()
	if len(body) > MaxBodySize {
		return nil, fmt.Errorf("%w: body is %d bytes", ErrTelegram, len(body))
	}
	l, _ := num(len(body), lenDigits)
	return []byte(string([]byte{STX}) + l + body + string([]byte{ETX})), nil
}

// Validate checks the telegram's fields for its type.
func (t Telegram) Validate() error {
	if t.Seq < 0 || t.Seq > MaxSeq {
		return fmt.Errorf("%w: seq %d out of range", ErrTelegram, t.Seq)
	}
	needLU := func() error {
		if t.LU == NoLU {
			if t.Type == TypeErrr {
				return nil
			}
			return fmt.Errorf("%w: %s needs a load unit", ErrTelegram, t.Type)
		}
		if !ValidSSCC(t.LU) {
			return fmt.Errorf("%w: %q is not a valid SSCC", ErrTelegram, t.LU)
		}
		return nil
	}
	switch t.Type {
	case TypeMove:
		if err := needLU(); err != nil {
			return err
		}
		if strings.TrimSpace(t.From) == "" || strings.TrimSpace(t.To) == "" || t.From == t.To {
			return fmt.Errorf("%w: MOVE needs two different locations", ErrTelegram)
		}
		if t.Priority < 1 || t.Priority > 9 {
			return fmt.Errorf("%w: priority %d is not 1 to 9", ErrTelegram, t.Priority)
		}
	case TypeArrv:
		if err := needLU(); err != nil {
			return err
		}
		if strings.TrimSpace(t.Location) == "" {
			return fmt.Errorf("%w: ARRV needs a location", ErrTelegram)
		}
	case TypeDivt:
		if err := needLU(); err != nil {
			return err
		}
		if strings.TrimSpace(t.Location) == "" || strings.TrimSpace(t.Lane) == "" {
			return fmt.Errorf("%w: DIVT needs a location and a lane", ErrTelegram)
		}
	case TypeErrr:
		if err := needLU(); err != nil {
			return err
		}
		if len(t.Code) != 4 || strings.Trim(t.Code, "0123456789") != "" {
			return fmt.Errorf("%w: ERRR code %q is not four digits", ErrTelegram, t.Code)
		}
	case TypeNack:
		if len(t.Code) != 4 || strings.Trim(t.Code, "0123456789") != "" {
			return fmt.Errorf("%w: NACK code %q is not four digits", ErrTelegram, t.Code)
		}
	case TypeAliv, TypeAckn:
	default:
		return fmt.Errorf("%w: unknown type %q", ErrTelegram, t.Type)
	}
	return nil
}

// Decode parses one body (the bytes between STX LLLL and ETX). A body with an unknown type
// returns the header fields with the error, so the link can answer NACK 0002 for its sequence.
func Decode(body []byte) (Telegram, error) {
	s := string(body)
	if len(s) < headerSize {
		return Telegram{}, fmt.Errorf("%w: %d bytes is shorter than the %d byte header", ErrTelegram, len(s), headerSize)
	}
	seq, err := strconv.Atoi(s[4:9])
	if err != nil || seq < 0 {
		return Telegram{}, fmt.Errorf("%w: bad sequence %q", ErrTelegram, s[4:9])
	}
	t := Telegram{Type: s[:4], Seq: seq, Sender: strings.TrimRight(s[9:17], " "), Receiver: strings.TrimRight(s[17:25], " ")}
	p := s[headerSize:]
	field := func(n int) (string, bool) {
		if len(p) < n {
			return "", false
		}
		v := p[:n]
		p = p[n:]
		return strings.TrimRight(v, " "), true
	}
	short := fmt.Errorf("%w: %s payload is too short", ErrTelegram, t.Type)
	var ok bool
	switch t.Type {
	case TypeMove:
		var prio string
		if t.LU, ok = field(18); !ok {
			return t, short
		}
		if t.From, ok = field(12); !ok {
			return t, short
		}
		if t.To, ok = field(12); !ok {
			return t, short
		}
		if prio, ok = field(1); !ok {
			return t, short
		}
		t.Priority, err = strconv.Atoi(prio)
		if err != nil {
			return t, fmt.Errorf("%w: priority %q", ErrTelegram, prio)
		}
	case TypeArrv:
		if t.LU, ok = field(18); !ok {
			return t, short
		}
		if t.Location, ok = field(12); !ok {
			return t, short
		}
	case TypeDivt:
		if t.LU, ok = field(18); !ok {
			return t, short
		}
		if t.Location, ok = field(12); !ok {
			return t, short
		}
		if t.Lane, ok = field(4); !ok {
			return t, short
		}
	case TypeErrr:
		if t.LU, ok = field(18); !ok {
			return t, short
		}
		if t.Code, ok = field(4); !ok {
			return t, short
		}
		if t.Text, ok = field(20); !ok {
			return t, short
		}
	case TypeAckn, TypeNack:
		var ref string
		if ref, ok = field(5); !ok {
			return t, short
		}
		if t.RefSeq, err = strconv.Atoi(ref); err != nil {
			return t, fmt.Errorf("%w: reference %q", ErrTelegram, ref)
		}
		if t.Type == TypeNack {
			if t.Code, ok = field(4); !ok {
				return t, short
			}
		}
	case TypeAliv:
	default:
		return t, fmt.Errorf("%w: unknown type %q", ErrTelegram, t.Type)
	}
	if p != "" {
		return t, fmt.Errorf("%w: %d unexpected trailing bytes in %s", ErrTelegram, len(p), t.Type)
	}
	if err := t.Validate(); err != nil {
		return t, err
	}
	return t, nil
}

// Framer cuts a byte stream into frames. Bytes before an STX are dropped (resynchronisation after
// noise), and a frame with a bad length or without its ETX is dropped up to the next STX, so a
// corrupted frame costs that frame and never the stream.
type Framer struct {
	buf []byte
	// Dropped counts bytes discarded while resynchronising.
	Dropped int
}

// Push adds received bytes and returns the bodies of every complete frame, in order.
func (f *Framer) Push(data []byte) [][]byte {
	f.buf = append(f.buf, data...)
	var out [][]byte
	for {
		i := indexByte(f.buf, STX)
		if i < 0 {
			f.Dropped += len(f.buf)
			f.buf = f.buf[:0]
			return out
		}
		f.Dropped += i
		f.buf = f.buf[i:]
		if len(f.buf) < 1+lenDigits {
			return out
		}
		n, err := strconv.Atoi(string(f.buf[1 : 1+lenDigits]))
		if err != nil || n < headerSize || n > MaxBodySize {
			f.skip()
			continue
		}
		end := 1 + lenDigits + n
		if len(f.buf) < end+1 {
			return out
		}
		if f.buf[end] != ETX {
			f.skip()
			continue
		}
		out = append(out, append([]byte(nil), f.buf[1+lenDigits:end]...))
		f.buf = f.buf[end+1:]
	}
}

// skip drops the current STX so the scan continues at the next one.
func (f *Framer) skip() {
	f.Dropped++
	f.buf = f.buf[1:]
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}
