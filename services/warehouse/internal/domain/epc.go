package domain

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// EPC decoding (wave 32, FR-32.9). A tag carries a GS1 EPC binary encoding (GS1 TDS); the warehouse decodes the
// schemes a warehouse reads: SGTIN-96 and SGTIN-198 (a trade item with serial), SSCC-96 (a logistic unit, the pallet),
// SGLN-96 and SGLN-195 (a location), GRAI-96, GRAI-170, GIAI-96 and GIAI-202 (returnable and individual assets),
// GSRN-96 and GSRNP-96 (a service relation), GDTI-96 and GDTI-174 (a document) and GID-96. Each gives the GS1 key the
// tag identifies with its check digit, so a read of a pallet's tag is the same SSCC the pallet's label and the receipt
// carry. The longer schemes hold their serial as 7-bit characters of the GS1 AI encodable set. CPI, SGCN, ITIP and
// ADI are not decoded.

// Codes of RFID reads.
const (
	CodeEPCInvalid = "WMS-EPC-INVALID" // the tag data is not a 96-bit EPC of a known scheme
)

// EPC is a decoded tag.
type EPC struct {
	Scheme        string `json:"scheme"`
	Filter        int    `json:"filter"`
	Partition     int    `json:"partition"`
	CompanyPrefix string `json:"companyPrefix"`
	// Key is the GS1 key the tag identifies: the GTIN-14 of an SGTIN, the SSCC, GLN-13, GRAI-14, GSRN-18 or GDTI-13,
	// each with its check digit; the GIAI has none. Serial is the SGTIN, GRAI or GDTI serial, and the location
	// extension of an SGLN, in decimal.
	Key    string `json:"key"`
	Serial string `json:"serial,omitempty"`
	URN    string `json:"urn"`
}

type epcPartition struct{ cpBits, cpDigits, refBits, refDigits int }

var (
	sgtinPartitions = []epcPartition{{40, 12, 4, 1}, {37, 11, 7, 2}, {34, 10, 10, 3}, {30, 9, 14, 4}, {27, 8, 17, 5}, {24, 7, 20, 6}, {20, 6, 24, 7}}
	ssccPartitions  = []epcPartition{{40, 12, 18, 5}, {37, 11, 21, 6}, {34, 10, 24, 7}, {30, 9, 28, 8}, {27, 8, 31, 9}, {24, 7, 34, 10}, {20, 6, 38, 11}}
	sglnPartitions  = []epcPartition{{40, 12, 1, 0}, {37, 11, 4, 1}, {34, 10, 7, 2}, {30, 9, 11, 3}, {27, 8, 14, 4}, {24, 7, 17, 5}, {20, 6, 21, 6}}
	graiPartitions  = []epcPartition{{40, 12, 4, 0}, {37, 11, 7, 1}, {34, 10, 10, 2}, {30, 9, 14, 3}, {27, 8, 17, 4}, {24, 7, 20, 5}, {20, 6, 24, 6}}
	giaiPartitions  = []epcPartition{{40, 12, 42, 13}, {37, 11, 45, 14}, {34, 10, 48, 15}, {30, 9, 52, 16}, {27, 8, 55, 17}, {24, 7, 58, 18}, {20, 6, 62, 19}}
	gdtiPartitions  = []epcPartition{{40, 12, 1, 0}, {37, 11, 4, 1}, {34, 10, 7, 2}, {30, 9, 11, 3}, {27, 8, 14, 4}, {24, 7, 17, 5}, {20, 6, 21, 6}}
)

type epcScheme struct {
	name      string
	bits      int // the scheme's length; the hexadecimal may pad it to a whole 16-bit word with zero bits
	table     []epcPartition
	tail      int // bits after the reference: the serial or extension
	urnPrefix string
	hasSerial bool
	// chars is the number of 7-bit characters of the serial (or, for GIAI-202, of the asset reference) of a scheme
	// that carries it as a string; 0 for a numeric one.
	chars int
}

var epcSchemes = map[int]epcScheme{
	0x30: {name: "SGTIN-96", bits: 96, table: sgtinPartitions, tail: 38, urnPrefix: "sgtin", hasSerial: true},
	0x31: {name: "SSCC-96", bits: 96, table: ssccPartitions, tail: 0, urnPrefix: "sscc"},
	0x32: {name: "SGLN-96", bits: 96, table: sglnPartitions, tail: 41, urnPrefix: "sgln", hasSerial: true},
	0x33: {name: "GRAI-96", bits: 96, table: graiPartitions, tail: 38, urnPrefix: "grai", hasSerial: true},
	0x34: {name: "GIAI-96", bits: 96, table: giaiPartitions, tail: 0, urnPrefix: "giai"},
	0x2D: {name: "GSRN-96", bits: 96, table: ssccPartitions, tail: 0, urnPrefix: "gsrn"},
	0x2E: {name: "GSRNP-96", bits: 96, table: ssccPartitions, tail: 0, urnPrefix: "gsrnp"},
	0x2C: {name: "GDTI-96", bits: 96, table: gdtiPartitions, tail: 41, urnPrefix: "gdti", hasSerial: true},
	// The schemes longer than 96 bits carry an alphanumeric serial (or asset reference) of 7-bit characters.
	0x36: {name: "SGTIN-198", bits: 198, table: sgtinPartitions, tail: 140, urnPrefix: "sgtin", hasSerial: true, chars: 20},
	0x39: {name: "SGLN-195", bits: 195, table: sglnPartitions, tail: 140, urnPrefix: "sgln", hasSerial: true, chars: 20},
	0x37: {name: "GRAI-170", bits: 170, table: graiPartitions, tail: 112, urnPrefix: "grai", hasSerial: true, chars: 16},
	0x3E: {name: "GDTI-174", bits: 174, table: gdtiPartitions, tail: 119, urnPrefix: "gdti", hasSerial: true, chars: 17},
	0x38: {name: "GIAI-202", bits: 202, table: giai202Partitions, tail: 0, urnPrefix: "giai"},
}

// GIAI-202 carries the individual asset reference as up to 18 to 24 characters, by partition.
var giai202Partitions = []epcPartition{{40, 12, 148, 18}, {37, 11, 151, 19}, {34, 10, 154, 20}, {30, 9, 158, 21}, {27, 8, 161, 22}, {24, 7, 164, 23}, {20, 6, 168, 24}}

// gidHeader is GID-96, the general identifier: a manager number, an object class and a serial, all numeric.
const gidHeader = 0x35

func epcErr(format string, args ...any) error {
	return errorf(CodeEPCInvalid, format, args...)
}

// epcBits reads fields of a tag of a given width, counted from its first (most significant) bit.
type epcBits struct {
	v     *big.Int
	width int
}

func (b epcBits) at(from, width int) *big.Int {
	shifted := new(big.Int).Rsh(b.v, uint(b.width-from-width))
	return shifted.And(shifted, new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(width)), big.NewInt(1)))
}

func zeroPad(v *big.Int, digits int) (string, bool) {
	s := v.String()
	if digits == 0 {
		return "", v.Sign() == 0
	}
	if len(s) > digits {
		return "", false
	}
	return strings.Repeat("0", digits-len(s)) + s, true
}

// epcChar reports whether c is in the GS1 AI encodable character set 82, the characters a string serial can hold.
func epcChar(c byte) bool {
	switch {
	case c >= '0' && c <= '9', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		return true
	}
	return strings.IndexByte(`!"%&'()*+,-./:;<=>?_`, c) >= 0
}

// decodeEPCString reads a string field of fieldBits bits holding at most n 7-bit characters: the string ends at the
// first zero character or after n characters, and every bit of the field after it must be zero.
func decodeEPCString(b epcBits, from, fieldBits, n int) (string, error) {
	var sb strings.Builder
	end := from + fieldBits
	pos := from
	for i := 0; i < n; i++ {
		c := byte(b.at(pos, 7).Int64())
		if c == 0 {
			break
		}
		if !epcChar(c) {
			return "", epcErr("character 0x%02X is not one a serial can hold", c)
		}
		sb.WriteByte(c)
		pos += 7
	}
	if end > pos && b.at(pos, end-pos).Sign() != 0 {
		return "", epcErr("the characters of the serial do not end in zero bits")
	}
	if sb.Len() == 0 {
		return "", epcErr("the serial is empty")
	}
	return sb.String(), nil
}

// urnEscape escapes the characters a pure identity URN cannot hold as they are (GS1 TDS, table A-1).
func urnEscape(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"', '%', '&', '/', '<', '>', '?':
			fmt.Fprintf(&sb, "%%%02X", c)
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

// DecodeEPC decodes the hexadecimal of an EPC binary encoding: the 96-bit schemes, SGTIN-198, SGLN-195, GRAI-170,
// GDTI-174, GIAI-202, GSRNP-96 and GID-96. A tag longer than 96 bits may be padded to a whole 16-bit word with zero
// bits, as readers report the EPC memory. Other lengths and schemes are WMS-EPC-INVALID.
func DecodeEPC(hex string) (EPC, error) {
	clean := strings.ToUpper(strings.TrimSpace(hex))
	if len(clean) < 2 {
		return EPC{}, epcErr("an EPC is at least 24 hexadecimal characters (96 bits), this has %d", len(clean))
	}
	v, ok := new(big.Int).SetString(clean, 16)
	if !ok {
		return EPC{}, epcErr("an EPC is hexadecimal")
	}
	b := epcBits{v: v, width: 4 * len(clean)}
	header := int(b.at(0, 8).Int64())
	bits := 96
	sc, known := epcSchemes[header]
	if known {
		bits = sc.bits
	} else if header != gidHeader {
		return EPC{}, epcErr("EPC header 0x%02X is not a scheme this warehouse decodes (SGTIN, SSCC, SGLN, GRAI, GIAI, GSRN, GSRNP, GDTI, GID)", header)
	}
	padded := (bits + 15) / 16 * 16
	if b.width < bits || b.width > padded {
		if bits == 96 {
			return EPC{}, epcErr("an EPC is 24 hexadecimal characters (96 bits), this has %d", len(clean))
		}
		return EPC{}, epcErr("a %d-bit EPC is %d to %d hexadecimal characters, this has %d", bits, (bits+3)/4, padded/4, len(clean))
	}
	if b.width > bits && b.at(bits, b.width-bits).Sign() != 0 {
		return EPC{}, epcErr("the bits after the %d-bit EPC are not zero", bits)
	}
	if header == gidHeader {
		manager, class, serial := b.at(8, 28), b.at(36, 24), b.at(60, 36)
		key := fmt.Sprintf("%s.%s.%s", manager, class, serial)
		return EPC{Scheme: "GID-96", Key: key, CompanyPrefix: manager.String(), Serial: serial.String(), URN: "urn:epc:id:gid:" + key}, nil
	}
	filter := int(b.at(8, 3).Int64())
	part := int(b.at(11, 3).Int64())
	if part >= len(sc.table) {
		return EPC{}, epcErr("EPC partition %d is not defined for %s", part, sc.name)
	}
	p := sc.table[part]
	cp, okc := zeroPad(b.at(14, p.cpBits), p.cpDigits)
	if !okc {
		return EPC{}, epcErr("the company prefix of the %s is out of range for its partition", sc.name)
	}
	out := EPC{Scheme: sc.name, Filter: filter, Partition: part, CompanyPrefix: cp}
	if sc.name == "GIAI-202" {
		asset, err := decodeEPCString(b, 14+p.cpBits, p.refBits, p.refDigits)
		if err != nil {
			return EPC{}, err
		}
		out.Key = cp + asset
		out.URN = fmt.Sprintf("urn:epc:id:giai:%s.%s", cp, urnEscape(asset))
		return out, nil
	}
	ref, okr := zeroPad(b.at(14+p.cpBits, p.refBits), p.refDigits)
	if !okr {
		return EPC{}, epcErr("the company prefix or reference of the %s is out of range for its partition", sc.name)
	}
	if sc.hasSerial {
		if sc.chars > 0 {
			serial, err := decodeEPCString(b, 14+p.cpBits+p.refBits, sc.tail, sc.chars)
			if err != nil {
				return EPC{}, err
			}
			out.Serial = serial
		} else {
			out.Serial = b.at(14+p.cpBits+p.refBits, sc.tail).String()
		}
	}
	check := func(body string) string { return body + strconv.Itoa(gs1Check(body)) }
	serial := urnEscape(out.Serial)
	switch sc.urnPrefix {
	case "sgtin":
		out.Key = check(ref[:1] + cp + ref[1:])
		out.URN = fmt.Sprintf("urn:epc:id:sgtin:%s.%s.%s", cp, ref, serial)
	case "sscc":
		out.Key = check(ref[:1] + cp + ref[1:])
		out.URN = fmt.Sprintf("urn:epc:id:sscc:%s.%s", cp, ref)
	case "gsrn", "gsrnp":
		out.Key = check(cp + ref)
		out.URN = fmt.Sprintf("urn:epc:id:%s:%s.%s", sc.urnPrefix, cp, ref)
	case "sgln":
		out.Key = check(cp + ref)
		out.URN = fmt.Sprintf("urn:epc:id:sgln:%s.%s.%s", cp, ref, serial)
	case "grai":
		out.Key = check("0" + cp + ref)
		out.URN = fmt.Sprintf("urn:epc:id:grai:%s.%s.%s", cp, ref, serial)
	case "gdti":
		out.Key = check(cp + ref)
		out.URN = fmt.Sprintf("urn:epc:id:gdti:%s.%s.%s", cp, ref, serial)
	case "giai":
		out.Key = cp + ref
		out.URN = fmt.Sprintf("urn:epc:id:giai:%s.%s", cp, ref)
	}
	return out, nil
}

// EncodeEPC96 builds the 96-bit hexadecimal of an EPC from its scheme (the header byte), filter, company prefix digits,
// reference digits and serial. It exists for tests and for printing tags; a company prefix of 6 to 12 digits picks the
// partition. The reference is the item reference (with the indicator digit first for an SGTIN), the serial reference of
// an SSCC, the location reference, asset type, asset reference or document type.
func EncodeEPC96(header, filter int, companyPrefix, reference string, serial *big.Int) (string, error) {
	sc, ok := epcSchemes[header]
	if !ok || sc.bits != 96 {
		return "", epcErr("header 0x%02X is not a known 96-bit scheme", header)
	}
	part := -1
	for i, p := range sc.table {
		if p.cpDigits == len(companyPrefix) {
			part = i
		}
	}
	if part < 0 {
		return "", epcErr("a company prefix of %d digits has no partition in %s", len(companyPrefix), sc.name)
	}
	p := sc.table[part]
	if len(reference) != p.refDigits || !allDigitsOrEmpty(reference) || !allDigits(companyPrefix) {
		return "", epcErr("the reference must be %d digits for a %d-digit company prefix", p.refDigits, p.cpDigits)
	}
	cp, _ := new(big.Int).SetString(companyPrefix, 10)
	ref := new(big.Int)
	if reference != "" {
		ref.SetString(reference, 10)
	}
	if ref.BitLen() > p.refBits {
		return "", epcErr("the reference does not fit %d bits", p.refBits)
	}
	v := big.NewInt(int64(header))
	push := func(x *big.Int, width int) {
		v.Lsh(v, uint(width))
		v.Or(v, x)
	}
	push(big.NewInt(int64(filter)), 3)
	push(big.NewInt(int64(part)), 3)
	push(cp, p.cpBits)
	push(ref, p.refBits)
	used := 14 + p.cpBits + p.refBits
	if sc.hasSerial {
		s := serial
		if s == nil {
			s = new(big.Int)
		}
		if s.Sign() < 0 || s.BitLen() > sc.tail {
			return "", epcErr("the serial does not fit %d bits", sc.tail)
		}
		push(s, sc.tail)
		used += sc.tail
	}
	v.Lsh(v, uint(96-used)) // reserved bits
	return fmt.Sprintf("%024X", v), nil
}

func allDigitsOrEmpty(s string) bool { return s == "" || allDigits(s) }

// EncodeEPCString builds the hexadecimal of a scheme longer than 96 bits (SGTIN-198, SGLN-195, GRAI-170, GDTI-174,
// GIAI-202), padded with zero bits to a whole 16-bit word as a reader reports it. serial is the alphanumeric serial,
// or the asset reference of a GIAI-202 (whose reference must then be empty).
func EncodeEPCString(header, filter int, companyPrefix, reference, serial string) (string, error) {
	sc, ok := epcSchemes[header]
	if !ok || sc.bits == 96 {
		return "", epcErr("header 0x%02X is not a known scheme longer than 96 bits", header)
	}
	part := -1
	for i, p := range sc.table {
		if p.cpDigits == len(companyPrefix) {
			part = i
		}
	}
	if part < 0 || !allDigits(companyPrefix) {
		return "", epcErr("a company prefix of %d digits has no partition in %s", len(companyPrefix), sc.name)
	}
	p := sc.table[part]
	chars, refBits, field := sc.chars, p.refBits, sc.tail
	if sc.name == "GIAI-202" {
		chars, refBits, field = p.refDigits, 0, p.refBits
		if reference != "" {
			return "", epcErr("a GIAI-202 carries its asset reference as the serial")
		}
	} else if len(reference) != p.refDigits || !allDigitsOrEmpty(reference) {
		return "", epcErr("the reference must be %d digits for a %d-digit company prefix", p.refDigits, p.cpDigits)
	}
	if serial == "" || len(serial) > chars {
		return "", epcErr("the serial must be 1 to %d characters", chars)
	}
	for i := 0; i < len(serial); i++ {
		if !epcChar(serial[i]) {
			return "", epcErr("character %q cannot be held in a serial", serial[i])
		}
	}
	cp, _ := new(big.Int).SetString(companyPrefix, 10)
	ref := new(big.Int)
	if reference != "" {
		ref.SetString(reference, 10)
	}
	if ref.BitLen() > refBits {
		return "", epcErr("the reference does not fit %d bits", refBits)
	}
	v := big.NewInt(int64(header))
	push := func(x *big.Int, width int) {
		v.Lsh(v, uint(width))
		v.Or(v, x)
	}
	push(big.NewInt(int64(filter)), 3)
	push(big.NewInt(int64(part)), 3)
	push(cp, p.cpBits)
	if refBits > 0 {
		push(ref, refBits)
	}
	for i := 0; i < chars; i++ {
		c := int64(0)
		if i < len(serial) {
			c = int64(serial[i])
		}
		push(big.NewInt(c), 7)
	}
	push(new(big.Int), field-7*chars)
	padded := (sc.bits + 15) / 16 * 16
	v.Lsh(v, uint(padded-sc.bits))
	return fmt.Sprintf("%0*X", padded/4, v), nil
}
