// Package mqtt is the connector's MQTT 3.1.1 client (PSG-25 D9, FR-25.9) and a small in-process broker
// for tests and the demo plant. The client does what the connector needs and no more: connect with a
// user name and TLS, subscribe with wildcards, publish at QoS 0 or 1 and wait for the acknowledgement,
// keep the connection alive. It has no sessions that survive a reconnect: the Session helper dials again
// and subscribes again.
package mqtt

import (
	"errors"
	"fmt"
	"io"
)

// Packet types (the high nibble of the first byte).
const (
	typeConnect     = 1
	typeConnack     = 2
	typePublish     = 3
	typePuback      = 4
	typeSubscribe   = 8
	typeSuback      = 9
	typeUnsubscribe = 10
	typeUnsuback    = 11
	typePingreq     = 12
	typePingresp    = 13
	typeDisconnect  = 14
)

// maxPacket bounds one packet's body; a larger one is a protocol error rather than an allocation.
const maxPacket = 1 << 20

var errMalformed = errors.New("mqtt: malformed packet")

// packet is one control packet: its type, the flag nibble and the body after the remaining length.
type packet struct {
	kind  byte
	flags byte
	body  []byte
}

func readPacket(r io.Reader) (packet, error) {
	var first [1]byte
	if _, err := io.ReadFull(r, first[:]); err != nil {
		return packet{}, err
	}
	n, mult := 0, 1
	for i := 0; ; i++ {
		if i == 4 {
			return packet{}, errMalformed
		}
		var b [1]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return packet{}, err
		}
		n += int(b[0]&0x7f) * mult
		if b[0]&0x80 == 0 {
			break
		}
		mult *= 128
	}
	if n > maxPacket {
		return packet{}, fmt.Errorf("%w: %d bytes", errMalformed, n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return packet{}, err
	}
	return packet{kind: first[0] >> 4, flags: first[0] & 0x0f, body: body}, nil
}

func (p packet) bytes() []byte {
	out := []byte{p.kind<<4 | p.flags&0x0f}
	n := len(p.body)
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			break
		}
	}
	return append(out, p.body...)
}

func putString(b []byte, s string) []byte {
	b = append(b, byte(len(s)>>8), byte(len(s)))
	return append(b, s...)
}

func getString(b []byte) (string, []byte, error) {
	if len(b) < 2 {
		return "", nil, errMalformed
	}
	n := int(b[0])<<8 | int(b[1])
	if len(b) < 2+n {
		return "", nil, errMalformed
	}
	return string(b[2 : 2+n]), b[2+n:], nil
}

func putUint16(b []byte, v uint16) []byte { return append(b, byte(v>>8), byte(v)) }

// matches reports whether topic matches the subscription filter (+ is one level, # the rest).
func matches(filter, topic string) bool {
	f, t := splitLevels(filter), splitLevels(topic)
	for i, lvl := range f {
		if lvl == "#" {
			return i == len(f)-1
		}
		if i >= len(t) {
			return false
		}
		if lvl != "+" && lvl != t[i] {
			return false
		}
	}
	return len(f) == len(t)
}

func splitLevels(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// ValidFilter reports whether s is a legal subscription filter.
func ValidFilter(s string) bool {
	if s == "" || len(s) > 65535 {
		return false
	}
	lv := splitLevels(s)
	for i, l := range lv {
		switch {
		case l == "#":
			if i != len(lv)-1 {
				return false
			}
		case l == "+":
		case containsAny(l, "#+"):
			return false
		}
	}
	return true
}

func containsAny(s, chars string) bool {
	for i := 0; i < len(s); i++ {
		for j := 0; j < len(chars); j++ {
			if s[i] == chars[j] {
				return true
			}
		}
	}
	return false
}
